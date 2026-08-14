// Package knowledge 实现文档摄取、向量化和混合检索。
package knowledge

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("知识库文档不存在")
	ErrEmbeddingMismatch = errors.New("已存在分块使用了不同的 Embedding 模型或维度")
)

// Document 是一份完成摄取的知识库文档。
type Document struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	SourceType          string    `json:"source_type"`
	MIMEType            string    `json:"mime_type"`
	ContentHash         string    `json:"content_hash"`
	EmbeddingModel      string    `json:"embedding_model"`
	EmbeddingDimensions int       `json:"embedding_dimensions"`
	ChunkCount          int       `json:"chunk_count"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Chunk 保存可引用的原文范围及其检索特征。
type Chunk struct {
	ID             string         `json:"id"`
	DocumentID     string         `json:"document_id"`
	DocumentName   string         `json:"document_name,omitempty"`
	Ordinal        int            `json:"ordinal"`
	Content        string         `json:"content"`
	StartRune      int            `json:"start_rune"`
	EndRune        int            `json:"end_rune"`
	EmbeddingModel string         `json:"embedding_model"`
	Embedding      []float64      `json:"-"`
	TermCounts     map[string]int `json:"-"`
	TokenCount     int            `json:"-"`
	CreatedAt      time.Time      `json:"created_at"`
}

// SearchResult 包含答案引用所需的原文和可解释分数。
type SearchResult struct {
	ChunkID      string  `json:"chunk_id"`
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	Ordinal      int     `json:"ordinal"`
	Content      string  `json:"content"`
	StartRune    int     `json:"start_rune"`
	EndRune      int     `json:"end_rune"`
	Score        float64 `json:"score"`
	VectorScore  float64 `json:"vector_score"`
	KeywordScore float64 `json:"keyword_score"`
}

// IngestInput 是同步文档摄取请求。
type IngestInput struct {
	Name       string
	SourceType string
	MIMEType   string
	Content    []byte
}

// IngestResult 表明文档是新建还是命中了内容哈希去重。
type IngestResult struct {
	Document     Document `json:"document"`
	Deduplicated bool     `json:"deduplicated"`
}

// Store 是知识库持久化边界。当前 SQLite 实现采用精确扫描，
// 后续 pgvector 实现可以在不改变 Service 的情况下下推向量检索。
type Store interface {
	CreateDocument(ctx context.Context, document Document, chunks []Chunk) error
	GetDocumentByHash(ctx context.Context, contentHash string) (Document, error)
	ListDocuments(ctx context.Context, limit int) ([]Document, error)
	DeleteDocument(ctx context.Context, id string) error
	ListChunks(ctx context.Context, limit int) ([]Chunk, error)
}
