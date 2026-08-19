package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhiruo/zora/internal/security"
)

func (p *Postgres) ConsumeAPIQuota(ctx context.Context, date, principal, resource string, amount, limit int64, now time.Time) (security.QuotaUsage, bool, error) {
	var used int64
	err := p.pool.QueryRow(ctx, `
INSERT INTO api_usage_daily(usage_date,principal_id,resource,used,updated_at)
SELECT $1::date,$2,$3,$4,$5 WHERE $4<=$6
ON CONFLICT(usage_date,principal_id,resource) DO UPDATE SET
    used=api_usage_daily.used+EXCLUDED.used,updated_at=EXCLUDED.updated_at
WHERE api_usage_daily.used+EXCLUDED.used<=$6
RETURNING used`, date, principal, resource, amount, now, limit).Scan(&used)
	allowed := true
	if errors.Is(err, pgx.ErrNoRows) {
		allowed = false
		err = p.pool.QueryRow(ctx, `SELECT used FROM api_usage_daily WHERE usage_date=$1::date AND principal_id=$2 AND resource=$3`, date, principal, resource).Scan(&used)
		if errors.Is(err, pgx.ErrNoRows) {
			used, err = 0, nil
		}
	}
	if err != nil {
		return security.QuotaUsage{}, false, fmt.Errorf("扣减 API 配额失败：%w", err)
	}
	now = now.UTC()
	resetAt := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return security.QuotaUsage{Resource: resource, Used: used, Limit: limit, ResetAt: resetAt}, allowed, nil
}
