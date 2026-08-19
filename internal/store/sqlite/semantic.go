package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/semantic"
)

func (s *SQLite) UpsertMessageEmbeddings(ctx context.Context, items []semantic.MessageEmbedding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始写入消息向量索引事务失败：%w", err)
	}
	defer tx.Rollback()
	for _, item := range items {
		if len(item.Embedding) != item.EmbeddingDimensions || item.IndexVersion < 1 {
			return semantic.ErrEmbeddingMismatch
		}
		encoded, err := json.Marshal(item.Embedding)
		if err != nil {
			return fmt.Errorf("编码消息向量失败：%w", err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO message_embeddings(message_id, conversation_id, role, embedding_model, embedding_dimensions, index_version, embedding, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id) DO UPDATE SET
    conversation_id = excluded.conversation_id, role = excluded.role,
    embedding_model = excluded.embedding_model, embedding_dimensions = excluded.embedding_dimensions,
    index_version = excluded.index_version, embedding = excluded.embedding, updated_at = excluded.updated_at`,
			item.MessageID, item.ConversationID, item.Role, item.EmbeddingModel, item.EmbeddingDimensions,
			item.IndexVersion, string(encoded), formatTime(item.UpdatedAt))
		if err != nil {
			return fmt.Errorf("写入消息向量索引失败：%w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交消息向量索引事务失败：%w", err)
	}
	return nil
}

func (s *SQLite) UpsertMemoryEmbeddings(ctx context.Context, items []semantic.MemoryEmbedding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始写入长期记忆向量索引事务失败：%w", err)
	}
	defer tx.Rollback()
	for _, item := range items {
		if len(item.Embedding) != item.EmbeddingDimensions || item.IndexVersion < 1 {
			return semantic.ErrEmbeddingMismatch
		}
		encoded, err := json.Marshal(item.Embedding)
		if err != nil {
			return fmt.Errorf("编码长期记忆向量失败：%w", err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO memory_embeddings(memory_id, kind, embedding_model, embedding_dimensions, index_version, embedding, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(memory_id) DO UPDATE SET
    kind = excluded.kind, embedding_model = excluded.embedding_model,
    embedding_dimensions = excluded.embedding_dimensions, index_version = excluded.index_version,
    embedding = excluded.embedding, updated_at = excluded.updated_at`,
			item.MemoryID, item.Kind, item.EmbeddingModel, item.EmbeddingDimensions,
			item.IndexVersion, string(encoded), formatTime(item.UpdatedAt))
		if err != nil {
			return fmt.Errorf("写入长期记忆向量索引失败：%w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交长期记忆向量索引事务失败：%w", err)
	}
	return nil
}

func (s *SQLite) SearchMessageEmbeddings(ctx context.Context, request semantic.MessageSearchRequest) ([]semantic.MessageHit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.embedding, m.id, m.conversation_id, m.role, m.content, m.tool_name, m.tool_call_id, m.sequence, m.created_at
FROM message_embeddings e JOIN messages m ON m.id = e.message_id
WHERE e.embedding_model = ? AND e.embedding_dimensions = ? AND e.index_version = ?
  AND (? = '' OR e.conversation_id <> ?) AND (? = '' OR e.role = ?)
LIMIT 10000`, request.EmbeddingModel, request.EmbeddingDimensions, request.IndexVersion,
		request.ExcludeConversation, request.ExcludeConversation, request.Role, request.Role)
	if err != nil {
		return nil, fmt.Errorf("查询消息向量候选失败：%w", err)
	}
	defer rows.Close()
	hits := make([]semantic.MessageHit, 0)
	for rows.Next() {
		var encoded, createdAt string
		var item domain.Message
		if err := rows.Scan(&encoded, &item.ID, &item.ConversationID, &item.Role, &item.Content,
			&item.ToolName, &item.ToolCallID, &item.Sequence, &createdAt); err != nil {
			return nil, fmt.Errorf("读取消息向量候选失败：%w", err)
		}
		var vector []float64
		if err := json.Unmarshal([]byte(encoded), &vector); err != nil {
			return nil, fmt.Errorf("解析消息向量失败：%w", err)
		}
		if item.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		hits = append(hits, semantic.MessageHit{Message: item, Score: semantic.CosineSimilarity(request.QueryVector, vector)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	semantic.SortMessageHits(hits)
	if len(hits) > request.Limit {
		hits = hits[:request.Limit]
	}
	return hits, nil
}

func (s *SQLite) SearchMemoryEmbeddings(ctx context.Context, request semantic.MemorySearchRequest) ([]semantic.MemoryHit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.memory_id, e.embedding
FROM memory_embeddings e JOIN memories m ON m.id = e.memory_id
WHERE e.embedding_model = ? AND e.embedding_dimensions = ? AND e.index_version = ?
  AND (m.expires_at IS NULL OR m.expires_at > ?)
LIMIT 10000`, request.EmbeddingModel, request.EmbeddingDimensions, request.IndexVersion, formatTimeNow())
	if err != nil {
		return nil, fmt.Errorf("查询长期记忆向量候选失败：%w", err)
	}
	type candidate struct {
		id    string
		score float64
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var id, encoded string
		if err := rows.Scan(&id, &encoded); err != nil {
			rows.Close()
			return nil, err
		}
		var vector []float64
		if err := json.Unmarshal([]byte(encoded), &vector); err != nil {
			rows.Close()
			return nil, fmt.Errorf("解析长期记忆向量失败：%w", err)
		}
		candidates = append(candidates, candidate{id: id, score: semantic.CosineSimilarity(request.QueryVector, vector)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	hits := make([]semantic.MemoryHit, 0, len(candidates))
	for _, candidate := range candidates {
		item, err := s.GetMemory(ctx, candidate.id)
		if err != nil {
			return nil, err
		}
		hits = append(hits, semantic.MemoryHit{Memory: item, Score: candidate.score})
	}
	semantic.SortMemoryHits(hits)
	if len(hits) > request.Limit {
		hits = hits[:request.Limit]
	}
	return hits, nil
}

func (s *SQLite) ListMessagesForEmbedding(ctx context.Context, afterSequence int64, limit int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, conversation_id, role, content, tool_name, tool_call_id, sequence, created_at
FROM messages WHERE sequence > ? AND role IN ('user', 'assistant') ORDER BY sequence LIMIT ?`, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("读取待向量化消息失败：%w", err)
	}
	defer rows.Close()
	items := make([]domain.Message, 0)
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLite) ListMemoriesForEmbedding(ctx context.Context, afterID string, limit int) ([]memory.Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, memory_key, content, importance, user_edited, source_type, source_conversation_id,
       source_message_id, created_at, updated_at, expires_at
FROM memories WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取待向量化长期记忆失败：%w", err)
	}
	defer rows.Close()
	items := make([]memory.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
