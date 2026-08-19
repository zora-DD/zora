package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/zhiruo/zora/internal/knowledge"
)

// CreateDocument 在同一事务中保存文档、pgvector 向量和 FTS 词项。
func (p *Postgres) CreateDocument(ctx context.Context, document knowledge.Document, chunks []knowledge.Chunk) (knowledge.Document, error) {
	scope := requestScope(ctx)
	document.TenantID, document.OwnerID = scope.TenantID, scope.ID
	if document.EmbeddingDimensions != p.embeddingDimensions {
		return knowledge.Document{}, fmt.Errorf("文档向量维度为 %d，但 PostgreSQL 列维度为 %d", document.EmbeddingDimensions, p.embeddingDimensions)
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("开始保存知识库文档事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	versionLockKey := scope.TenantID + "\x00" + scope.ID + "\x00" + document.VersionGroupID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 4931))`, versionLockKey); err != nil {
		return knowledge.Document{}, fmt.Errorf("锁定知识库文档版本组失败：%w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM knowledge_documents WHERE version_group_id = $1 AND tenant_id = $2 AND owner_id = $3`, document.VersionGroupID, scope.TenantID, scope.ID).Scan(&document.Version); err != nil {
		return knowledge.Document{}, fmt.Errorf("计算知识库文档版本失败：%w", err)
	}
	document.IsLatest = true
	if _, err := tx.Exec(ctx, `UPDATE knowledge_documents SET is_latest = FALSE, updated_at = $1 WHERE version_group_id = $2 AND tenant_id = $3 AND owner_id = $4 AND is_latest = TRUE`, normalizeTime(document.UpdatedAt), document.VersionGroupID, scope.TenantID, scope.ID); err != nil {
		return knowledge.Document{}, fmt.Errorf("更新知识库旧版本状态失败：%w", err)
	}

	_, err = tx.Exec(ctx, `
INSERT INTO knowledge_documents(
    id, tenant_id, version_group_id, version, is_latest, name, source_type, mime_type, content_hash,
    owner_id, visibility, embedding_model, embedding_dimensions, chunk_count, created_at, updated_at
) VALUES($1, $2, $3, $4, TRUE, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		document.ID, scope.TenantID, document.VersionGroupID, document.Version, document.Name, document.SourceType,
		document.MIMEType, document.ContentHash, document.OwnerID, document.Visibility,
		document.EmbeddingModel, document.EmbeddingDimensions, document.ChunkCount,
		normalizeTime(document.CreatedAt), normalizeTime(document.UpdatedAt),
	)
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("保存知识库文档失败：%w", err)
	}

	for _, chunk := range chunks {
		if len(chunk.Embedding) != p.embeddingDimensions {
			return knowledge.Document{}, fmt.Errorf("第 %d 个分块的向量维度为 %d，但 PostgreSQL 列维度为 %d", chunk.Ordinal+1, len(chunk.Embedding), p.embeddingDimensions)
		}
		terms, err := json.Marshal(chunk.TermCounts)
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("编码第 %d 个分块词频失败：%w", chunk.Ordinal+1, err)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO knowledge_chunks(
    id, document_id, ordinal, content, start_rune, end_rune, embedding_model,
    embedding, term_counts, search_terms, token_count, created_at
) VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12)`,
			chunk.ID, chunk.DocumentID, chunk.Ordinal, chunk.Content,
			chunk.StartRune, chunk.EndRune, chunk.EmbeddingModel,
			pgvector.NewVector(toFloat32(chunk.Embedding)), string(terms), buildSearchTerms(chunk.TermCounts),
			chunk.TokenCount, normalizeTime(chunk.CreatedAt),
		)
		if err != nil {
			return knowledge.Document{}, fmt.Errorf("保存第 %d 个知识库分块失败：%w", chunk.Ordinal+1, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return knowledge.Document{}, fmt.Errorf("提交知识库文档事务失败：%w", err)
	}
	return document, nil
}

func (p *Postgres) GetDocumentByHash(ctx context.Context, contentHash string) (knowledge.Document, error) {
	scope := requestScope(ctx)
	document, err := scanKnowledgeDocument(p.pool.QueryRow(ctx, postgresKnowledgeDocumentSelect+` WHERE content_hash = $1 AND tenant_id = $2 AND owner_id = $3`, contentHash, scope.TenantID, scope.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.Document{}, knowledge.ErrNotFound
	}
	if err != nil {
		return knowledge.Document{}, fmt.Errorf("按哈希查询知识库文档失败：%w", err)
	}
	return document, nil
}

func (p *Postgres) ListDocuments(ctx context.Context, principalID string, includeHistory bool, limit int) ([]knowledge.Document, error) {
	scope := requestScope(ctx)
	historyFilter := ` AND is_latest = TRUE`
	if includeHistory {
		historyFilter = ""
	}
	rows, err := p.pool.Query(ctx, postgresKnowledgeDocumentSelect+` WHERE tenant_id = $1 AND (owner_id = $2 OR visibility = 'public')`+historyFilter+` ORDER BY updated_at DESC, version DESC LIMIT $3`, scope.TenantID, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档列表失败：%w", err)
	}
	defer rows.Close()

	documents := make([]knowledge.Document, 0)
	for rows.Next() {
		document, err := scanKnowledgeDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("读取知识库文档失败：%w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历知识库文档失败：%w", err)
	}
	return documents, nil
}

func (p *Postgres) ListDocumentVersions(ctx context.Context, documentID, principalID string) ([]knowledge.Document, error) {
	scope := requestScope(ctx)
	var groupID, ownerID string
	err := p.pool.QueryRow(ctx, `SELECT version_group_id, owner_id FROM knowledge_documents WHERE id = $1 AND tenant_id = $2 AND (owner_id = $3 OR visibility = 'public')`, documentID, scope.TenantID, principalID).Scan(&groupID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, knowledge.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档版本组失败：%w", err)
	}
	// 先从有权查看的文档确定版本组所有者，再固定 owner，避免同名组意外混入其他用户版本。
	rows, err := p.pool.Query(ctx, postgresKnowledgeDocumentSelect+` WHERE version_group_id = $1 AND tenant_id = $2 AND owner_id = $3 ORDER BY version DESC`, groupID, scope.TenantID, ownerID)
	if err != nil {
		return nil, fmt.Errorf("查询知识库文档版本列表失败：%w", err)
	}
	defer rows.Close()
	return scanKnowledgeDocuments(rows)
}

func (p *Postgres) DeleteDocument(ctx context.Context, id, principalID string) error {
	scope := requestScope(ctx)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始删除知识库文档事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	var ownerID, groupID string
	var latest bool
	err = tx.QueryRow(ctx, `SELECT owner_id, version_group_id, is_latest FROM knowledge_documents WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, scope.TenantID).Scan(&ownerID, &groupID, &latest)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("查询待删除知识库文档失败：%w", err)
	}
	if ownerID != principalID {
		return knowledge.ErrAccessDenied
	}
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_documents WHERE id = $1 AND tenant_id = $2 AND owner_id = $3`, id, scope.TenantID, principalID); err != nil {
		return fmt.Errorf("删除知识库文档失败：%w", err)
	}
	if latest {
		if _, err := tx.Exec(ctx, `UPDATE knowledge_documents SET is_latest = TRUE, updated_at = $1 WHERE id = (SELECT id FROM knowledge_documents WHERE version_group_id = $2 AND tenant_id = $3 AND owner_id = $4 ORDER BY version DESC LIMIT 1)`, normalizeTime(time.Now().UTC()), groupID, scope.TenantID, principalID); err != nil {
			return fmt.Errorf("恢复知识库上一版本失败：%w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交删除知识库文档事务失败：%w", err)
	}
	return nil
}

// ListChunks 保留完整 Store 契约，在线检索会优先使用 SearchCandidates 下推到数据库。
func (p *Postgres) ListChunks(ctx context.Context, principalID string, limit int) ([]knowledge.Chunk, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT c.id, c.document_id, d.name, c.ordinal, c.content, c.start_rune, c.end_rune,
       c.embedding_model, c.embedding, c.term_counts, c.token_count, c.created_at
FROM knowledge_chunks c
JOIN knowledge_documents d ON d.id = c.document_id
WHERE d.tenant_id = $1 AND d.is_latest = TRUE AND (d.owner_id = $2 OR d.visibility = 'public')
ORDER BY c.sequence ASC LIMIT $3`, scope.TenantID, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询知识库分块失败：%w", err)
	}
	defer rows.Close()

	chunks := make([]knowledge.Chunk, 0)
	for rows.Next() {
		var chunk knowledge.Chunk
		var vector pgvector.Vector
		var terms []byte
		if err := rows.Scan(
			&chunk.ID, &chunk.DocumentID, &chunk.DocumentName, &chunk.Ordinal, &chunk.Content,
			&chunk.StartRune, &chunk.EndRune, &chunk.EmbeddingModel, &vector, &terms,
			&chunk.TokenCount, &chunk.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取知识库分块失败：%w", err)
		}
		chunk.Embedding = toFloat64(vector.Slice())
		if err := json.Unmarshal(terms, &chunk.TermCounts); err != nil {
			return nil, fmt.Errorf("解析知识库分块词频失败：%w", err)
		}
		chunk.CreatedAt = normalizeTime(chunk.CreatedAt)
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历知识库分块失败：%w", err)
	}
	return chunks, nil
}

func (p *Postgres) SearchCandidates(ctx context.Context, request knowledge.CandidateRequest) ([]knowledge.Candidate, error) {
	if request.Limit < 1 || request.Limit > 200 {
		return nil, fmt.Errorf("候选召回数量必须在 1 到 200 之间")
	}
	if request.EmbeddingDimensions != p.embeddingDimensions {
		return nil, fmt.Errorf("检索向量维度为 %d，但 PostgreSQL 列维度为 %d", request.EmbeddingDimensions, p.embeddingDimensions)
	}
	merged := make(map[string]knowledge.Candidate, request.Limit*2)
	if request.Mode == knowledge.RetrievalVector || request.Mode == knowledge.RetrievalHybrid {
		if len(request.QueryVector) != p.embeddingDimensions {
			return nil, fmt.Errorf("检索向量维度无效")
		}
		candidates, err := p.searchVectorCandidates(ctx, request)
		if err != nil {
			return nil, err
		}
		for index, candidate := range candidates {
			candidate.VectorRank = index + 1
			merged[candidate.Chunk.ID] = candidate
		}
	}
	if request.Mode == knowledge.RetrievalKeyword || request.Mode == knowledge.RetrievalHybrid {
		candidates, err := p.searchKeywordCandidates(ctx, request.QueryTerms, request.PrincipalID, request.Limit)
		if err != nil {
			return nil, err
		}
		for index, candidate := range candidates {
			if existing, ok := merged[candidate.Chunk.ID]; ok {
				existing.KeywordScore = candidate.KeywordScore
				existing.KeywordRank = index + 1
				merged[candidate.Chunk.ID] = existing
				continue
			}
			candidate.KeywordRank = index + 1
			merged[candidate.Chunk.ID] = candidate
		}
	}
	result := make([]knowledge.Candidate, 0, len(merged))
	for _, candidate := range merged {
		result = append(result, candidate)
	}
	return result, nil
}

func (p *Postgres) searchVectorCandidates(ctx context.Context, request knowledge.CandidateRequest) ([]knowledge.Candidate, error) {
	scope := requestScope(ctx)
	rows, err := p.pool.Query(ctx, `
SELECT c.id, c.document_id, d.name, c.ordinal, c.content, c.start_rune, c.end_rune,
       c.embedding_model, c.token_count, c.created_at,
       1 - (c.embedding <=> $1) AS vector_score
FROM knowledge_chunks c
JOIN knowledge_documents d ON d.id = c.document_id
WHERE c.embedding_model = $2
  AND d.tenant_id = $3
  AND d.is_latest = TRUE
  AND (d.owner_id = $4 OR d.visibility = 'public')
ORDER BY c.embedding <=> $1
LIMIT $5`, pgvector.NewVector(toFloat32(request.QueryVector)), request.EmbeddingModel, scope.TenantID, request.PrincipalID, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("执行 pgvector 候选召回失败：%w", err)
	}
	defer rows.Close()
	return scanCandidates(rows, true)
}

func (p *Postgres) searchKeywordCandidates(ctx context.Context, terms []string, principalID string, limit int) ([]knowledge.Candidate, error) {
	scope := requestScope(ctx)
	if len(terms) == 0 {
		return []knowledge.Candidate{}, nil
	}
	// 词项来自统一 tokenizer，不直接拼入 SQL；OR 查询扩大候选集，再交给 RRF 融合。
	tsQuery := strings.Join(terms, " | ")
	rows, err := p.pool.Query(ctx, `
WITH query AS (SELECT to_tsquery('simple', $1) AS value)
SELECT c.id, c.document_id, d.name, c.ordinal, c.content, c.start_rune, c.end_rune,
       c.embedding_model, c.token_count, c.created_at,
       ts_rank_cd(c.search_vector, query.value, 32) AS keyword_score
FROM knowledge_chunks c
JOIN knowledge_documents d ON d.id = c.document_id
CROSS JOIN query
WHERE c.search_vector @@ query.value
  AND d.tenant_id = $2
  AND d.is_latest = TRUE
  AND (d.owner_id = $3 OR d.visibility = 'public')
ORDER BY keyword_score DESC
LIMIT $4`, tsQuery, scope.TenantID, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("执行 PostgreSQL 全文候选召回失败：%w", err)
	}
	defer rows.Close()
	return scanCandidates(rows, false)
}

type candidateRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanCandidates(rows candidateRows, vector bool) ([]knowledge.Candidate, error) {
	candidates := make([]knowledge.Candidate, 0)
	for rows.Next() {
		var candidate knowledge.Candidate
		score := &candidate.KeywordScore
		if vector {
			score = &candidate.VectorScore
		}
		if err := rows.Scan(
			&candidate.Chunk.ID, &candidate.Chunk.DocumentID, &candidate.Chunk.DocumentName,
			&candidate.Chunk.Ordinal, &candidate.Chunk.Content,
			&candidate.Chunk.StartRune, &candidate.Chunk.EndRune,
			&candidate.Chunk.EmbeddingModel, &candidate.Chunk.TokenCount,
			&candidate.Chunk.CreatedAt, score,
		); err != nil {
			return nil, fmt.Errorf("读取 PostgreSQL 检索候选失败：%w", err)
		}
		candidate.Chunk.CreatedAt = normalizeTime(candidate.Chunk.CreatedAt)
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 PostgreSQL 检索候选失败：%w", err)
	}
	return candidates, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

const postgresKnowledgeDocumentSelect = `
SELECT id, tenant_id, version_group_id, version, is_latest, name, source_type, mime_type, content_hash,
       owner_id, visibility, embedding_model, embedding_dimensions, chunk_count, created_at, updated_at
FROM knowledge_documents`

func scanKnowledgeDocument(row rowScanner) (knowledge.Document, error) {
	var document knowledge.Document
	if err := row.Scan(
		&document.ID, &document.TenantID, &document.VersionGroupID, &document.Version, &document.IsLatest,
		&document.Name, &document.SourceType, &document.MIMEType, &document.ContentHash,
		&document.OwnerID, &document.Visibility, &document.EmbeddingModel,
		&document.EmbeddingDimensions, &document.ChunkCount, &document.CreatedAt, &document.UpdatedAt,
	); err != nil {
		return knowledge.Document{}, err
	}
	document.CreatedAt = normalizeTime(document.CreatedAt)
	document.UpdatedAt = normalizeTime(document.UpdatedAt)
	return document, nil
}

type knowledgeDocumentRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanKnowledgeDocuments(rows knowledgeDocumentRows) ([]knowledge.Document, error) {
	documents := make([]knowledge.Document, 0)
	for rows.Next() {
		document, err := scanKnowledgeDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("读取知识库文档失败：%w", err)
		}
		documents = append(documents, document)
	}
	return documents, rows.Err()
}

func buildSearchTerms(counts map[string]int) string {
	terms := make([]string, 0, len(counts))
	for term := range counts {
		terms = append(terms, term)
	}
	slices.Sort(terms)
	var builder strings.Builder
	for _, term := range terms {
		for count := 0; count < counts[term]; count++ {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteString(term)
		}
	}
	return builder.String()
}

func toFloat32(values []float64) []float32 {
	result := make([]float32, len(values))
	for index, value := range values {
		result[index] = float32(value)
	}
	return result
}

func toFloat64(values []float32) []float64 {
	result := make([]float64, len(values))
	for index, value := range values {
		result[index] = float64(value)
	}
	return result
}
