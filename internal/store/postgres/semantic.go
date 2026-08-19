package postgres

import (
	"context"
	"fmt"

	"github.com/pgvector/pgvector-go"

	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/semantic"
)

func (p *Postgres) UpsertMessageEmbeddings(ctx context.Context, items []semantic.MessageEmbedding) error {
	scope := requestScope(ctx)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始写入消息向量索引事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		if len(item.Embedding) != p.embeddingDimensions || item.EmbeddingDimensions != p.embeddingDimensions {
			return semantic.ErrEmbeddingMismatch
		}
		_, err = tx.Exec(ctx, `
INSERT INTO message_embeddings(message_id, conversation_id, role, embedding_model, embedding_dimensions, index_version, embedding, updated_at)
SELECT $1,$2,$3,$4,$5,$6,$7,$8
WHERE EXISTS(SELECT 1 FROM conversations WHERE id=$2 AND tenant_id=$9 AND principal_id=$10)
ON CONFLICT(message_id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id, role=EXCLUDED.role,
embedding_model=EXCLUDED.embedding_model, embedding_dimensions=EXCLUDED.embedding_dimensions,
index_version=EXCLUDED.index_version, embedding=EXCLUDED.embedding, updated_at=EXCLUDED.updated_at`,
			item.MessageID, item.ConversationID, item.Role, item.EmbeddingModel, item.EmbeddingDimensions,
			item.IndexVersion, pgvector.NewVector(toFloat32(item.Embedding)), normalizeTime(item.UpdatedAt), scope.TenantID, scope.ID)
		if err != nil {
			return fmt.Errorf("写入消息向量索引失败：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交消息向量索引事务失败：%w", err)
	}
	return nil
}

func (p *Postgres) UpsertMemoryEmbeddings(ctx context.Context, items []semantic.MemoryEmbedding) error {
	scope := requestScope(ctx)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始写入长期记忆向量索引事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		if len(item.Embedding) != p.embeddingDimensions || item.EmbeddingDimensions != p.embeddingDimensions {
			return semantic.ErrEmbeddingMismatch
		}
		_, err = tx.Exec(ctx, `
INSERT INTO memory_embeddings(memory_id, kind, embedding_model, embedding_dimensions, index_version, embedding, updated_at)
SELECT $1,$2,$3,$4,$5,$6,$7
WHERE EXISTS(SELECT 1 FROM memories WHERE id=$1 AND tenant_id=$8 AND principal_id=$9)
ON CONFLICT(memory_id) DO UPDATE SET kind=EXCLUDED.kind, embedding_model=EXCLUDED.embedding_model,
embedding_dimensions=EXCLUDED.embedding_dimensions, index_version=EXCLUDED.index_version,
embedding=EXCLUDED.embedding, updated_at=EXCLUDED.updated_at`, item.MemoryID, item.Kind, item.EmbeddingModel,
			item.EmbeddingDimensions, item.IndexVersion, pgvector.NewVector(toFloat32(item.Embedding)), normalizeTime(item.UpdatedAt), scope.TenantID, scope.ID)
		if err != nil {
			return fmt.Errorf("写入长期记忆向量索引失败：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交长期记忆向量索引事务失败：%w", err)
	}
	return nil
}

func (p *Postgres) SearchMessageEmbeddings(ctx context.Context, request semantic.MessageSearchRequest) ([]semantic.MessageHit, error) {
	scope := requestScope(ctx)
	if len(request.QueryVector) != p.embeddingDimensions {
		return nil, semantic.ErrEmbeddingMismatch
	}
	rows, err := p.pool.Query(ctx, `
SELECT m.id,m.conversation_id,m.role,m.content,m.tool_name,m.tool_call_id,m.sequence,m.created_at,
       1-(e.embedding <=> $1) AS score
FROM message_embeddings e JOIN messages m ON m.id=e.message_id
JOIN conversations c ON c.id=m.conversation_id
WHERE e.embedding_model=$2 AND e.embedding_dimensions=$3 AND e.index_version=$4
  AND ($5='' OR e.conversation_id<>$5) AND ($6='' OR e.role=$6)
  AND c.tenant_id=$7 AND c.principal_id=$8
ORDER BY e.embedding <=> $1 LIMIT $9`, pgvector.NewVector(toFloat32(request.QueryVector)), request.EmbeddingModel,
		request.EmbeddingDimensions, request.IndexVersion, request.ExcludeConversation, request.Role, scope.TenantID, scope.ID, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("查询消息向量索引失败：%w", err)
	}
	defer rows.Close()
	hits := make([]semantic.MessageHit, 0)
	for rows.Next() {
		var hit semantic.MessageHit
		if err := rows.Scan(&hit.Message.ID, &hit.Message.ConversationID, &hit.Message.Role, &hit.Message.Content,
			&hit.Message.ToolName, &hit.Message.ToolCallID, &hit.Message.Sequence, &hit.Message.CreatedAt, &hit.Score); err != nil {
			return nil, err
		}
		hit.Message.CreatedAt = normalizeTime(hit.Message.CreatedAt)
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func (p *Postgres) SearchMemoryEmbeddings(ctx context.Context, request semantic.MemorySearchRequest) ([]semantic.MemoryHit, error) {
	scope := requestScope(ctx)
	if len(request.QueryVector) != p.embeddingDimensions {
		return nil, semantic.ErrEmbeddingMismatch
	}
	rows, err := p.pool.Query(ctx, `
SELECT e.memory_id, 1-(e.embedding <=> $1) AS score
FROM memory_embeddings e JOIN memories m ON m.id=e.memory_id
WHERE e.embedding_model=$2 AND e.embedding_dimensions=$3 AND e.index_version=$4
  AND (m.expires_at IS NULL OR m.expires_at>NOW())
  AND m.tenant_id=$5 AND m.principal_id=$6
ORDER BY e.embedding <=> $1 LIMIT $7`, pgvector.NewVector(toFloat32(request.QueryVector)), request.EmbeddingModel,
		request.EmbeddingDimensions, request.IndexVersion, scope.TenantID, scope.ID, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("查询长期记忆向量索引失败：%w", err)
	}
	type candidate struct {
		id    string
		score float64
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.score); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hits := make([]semantic.MemoryHit, 0, len(candidates))
	for _, candidate := range candidates {
		item, err := p.GetMemory(ctx, candidate.id)
		if err != nil {
			return nil, err
		}
		hits = append(hits, semantic.MemoryHit{Memory: item, Score: candidate.score})
	}
	return hits, nil
}

func (p *Postgres) ListMessagesForEmbedding(ctx context.Context, afterSequence int64, limit int) ([]domain.Message, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `SELECT m.id,m.conversation_id,m.role,m.content,m.tool_name,m.tool_call_id,m.sequence,m.created_at
FROM messages m JOIN conversations c ON c.id=m.conversation_id
WHERE m.sequence>$1 AND m.role IN ('user','assistant') AND c.tenant_id=$2 AND c.principal_id=$3
ORDER BY m.sequence LIMIT $4`, afterSequence, scope.TenantID, scope.ID, limit)
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

func (p *Postgres) ListMemoriesForEmbedding(ctx context.Context, afterID string, limit int) ([]memory.Memory, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `SELECT id,kind,memory_key,content,importance,user_edited,source_type,source_conversation_id,
source_message_id,created_at,updated_at,expires_at FROM memories
WHERE id>$1 AND tenant_id=$2 AND principal_id=$3 ORDER BY id LIMIT $4`, afterID, scope.TenantID, scope.ID, limit)
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
