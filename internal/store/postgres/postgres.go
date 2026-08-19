// Package postgres 使用 PostgreSQL、pgvector 和全文索引实现应用持久化。
package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"

	"github.com/zhiruo/zora/internal/knowledge"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/semantic"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/summary"
)

type Config struct {
	DSN                 string
	MaxConns            int32
	EmbeddingDimensions int
}

type Postgres struct {
	pool                *pgxpool.Pool
	embeddingDimensions int
}

var (
	_ store.Store              = (*Postgres)(nil)
	_ knowledge.Store          = (*Postgres)(nil)
	_ knowledge.CandidateStore = (*Postgres)(nil)
	_ memory.Store             = (*Postgres)(nil)
	_ memory.CaptureJobStore   = (*Postgres)(nil)
	_ semantic.Store           = (*Postgres)(nil)
	_ office.Store             = (*Postgres)(nil)
	_ summary.Store            = (*Postgres)(nil)
)

// Open 创建连接池并执行幂等迁移。每条连接先确认 vector 扩展存在，再注册其二进制类型。
func Open(ctx context.Context, config Config) (*Postgres, error) {
	if strings.TrimSpace(config.DSN) == "" {
		return nil, fmt.Errorf("PostgreSQL 连接串不能为空")
	}
	if config.EmbeddingDimensions < 1 {
		return nil, fmt.Errorf("PostgreSQL 向量维度必须为正整数")
	}
	poolConfig, err := pgxpool.ParseConfig(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("解析 PostgreSQL 连接配置失败：%w", err)
	}
	if config.MaxConns > 0 {
		poolConfig.MaxConns = config.MaxConns
	}
	poolConfig.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		var extensionExists bool
		if err := connection.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'vector')`,
		).Scan(&extensionExists); err != nil {
			return fmt.Errorf("检查 pgvector 扩展失败：%w", err)
		}
		if !extensionExists {
			if _, err := connection.Exec(ctx, `CREATE EXTENSION vector`); err != nil {
				return fmt.Errorf("启用 pgvector 扩展失败：%w", err)
			}
		}
		if err := pgxvector.RegisterTypes(ctx, connection); err != nil {
			return fmt.Errorf("注册 pgvector 类型失败：%w", err)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("创建 PostgreSQL 连接池失败：%w", err)
	}
	database := &Postgres{pool: pool, embeddingDimensions: config.EmbeddingDimensions}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败：%w", err)
	}
	if err := database.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return database, nil
}

func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开始 PostgreSQL 迁移事务失败：%w", err)
	}
	defer tx.Rollback(ctx)
	// 多实例同时启动时只允许一个实例执行 DDL，避免 CREATE INDEX 竞争。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(908276451)`); err != nil {
		return fmt.Errorf("获取 PostgreSQL 迁移锁失败：%w", err)
	}
	schema := fmt.Sprintf(postgresSchema, p.embeddingDimensions, p.embeddingDimensions, p.embeddingDimensions)
	if _, err := tx.Exec(ctx, schema); err != nil {
		return fmt.Errorf("执行 PostgreSQL 表结构迁移失败：%w", err)
	}
	var vectorType string
	expected := fmt.Sprintf("vector(%d)", p.embeddingDimensions)
	for _, table := range []string{"knowledge_chunks", "message_embeddings", "memory_embeddings"} {
		if err := tx.QueryRow(ctx, `
SELECT format_type(attribute.atttypid, attribute.atttypmod)
FROM pg_attribute attribute
JOIN pg_class relation ON relation.oid = attribute.attrelid
		WHERE relation.relname = $1 AND attribute.attname = 'embedding'`, table).Scan(&vectorType); err != nil {
			return fmt.Errorf("检查 PostgreSQL %s 向量维度失败：%w", table, err)
		}
		if vectorType != expected {
			return fmt.Errorf("PostgreSQL %s 现有向量列类型为 %s，但当前配置需要 %s；请迁移或重建对应索引", table, vectorType, expected)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交 PostgreSQL 迁移事务失败：%w", err)
	}
	return nil
}

func affected(operation string, tag pgconnCommandTag, err error) error {
	if err != nil {
		return fmt.Errorf("%s失败：%w", operation, err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// pgconnCommandTag 只保留测试与业务代码需要的最小接口。
type pgconnCommandTag interface {
	RowsAffected() int64
}

func normalizeTime(value time.Time) time.Time { return value.UTC() }
