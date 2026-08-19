package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// LockConversation 使用 PostgreSQL Session Advisory Lock 串行化跨副本的同会话 Agent Run。
// 专用连接在解锁前不会归还连接池；进程异常退出时数据库会自动释放该连接持有的锁。
func (p *Postgres) LockConversation(ctx context.Context, key string) (func(), error) {
	scope := requestScope(ctx)
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("从连接池获取会话锁连接失败：%w", err)
	}
	lockKey := scope.TenantID + "\x00" + key
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 4927))`, lockKey); err != nil {
		connection.Release()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 4927))`, lockKey)
			connection.Release()
		})
	}, nil
}
