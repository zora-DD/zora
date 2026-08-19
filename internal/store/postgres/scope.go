package postgres

import (
	"context"

	"github.com/zhiruo/zora/internal/identity"
)

func requestScope(ctx context.Context) identity.Principal {
	return identity.ScopeOrLocal(ctx)
}
