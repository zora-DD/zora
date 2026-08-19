package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhiruo/zora/internal/security"
)

func (s *SQLite) ConsumeAPIQuota(ctx context.Context, date, principal, resource string, amount, limit int64, now time.Time) (security.QuotaUsage, bool, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO api_usage_daily(usage_date,principal_id,resource,used,updated_at)
SELECT ?,?,?,?,? WHERE ?<=?
ON CONFLICT(usage_date,principal_id,resource) DO UPDATE SET
    used=api_usage_daily.used+excluded.used,updated_at=excluded.updated_at
WHERE api_usage_daily.used+excluded.used<=?`, date, principal, resource, amount, formatTime(now), amount, limit, limit)
	if err != nil {
		return security.QuotaUsage{}, false, fmt.Errorf("扣减 API 配额失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return security.QuotaUsage{}, false, err
	}
	used := int64(0)
	err = s.db.QueryRowContext(ctx, `SELECT used FROM api_usage_daily WHERE usage_date=? AND principal_id=? AND resource=?`, date, principal, resource).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return security.QuotaUsage{}, false, fmt.Errorf("读取 API 配额失败：%w", err)
	}
	return security.QuotaUsage{Resource: resource, Used: used, Limit: limit, ResetAt: nextUTCMidnight(now)}, affected == 1, nil
}

func nextUTCMidnight(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}
