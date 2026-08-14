package httpapi

import (
	"context"
	"net/http"
	"time"
)

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func contextDeadlineExceeded() error { return context.DeadlineExceeded }
func contextCanceled() error         { return context.Canceled }
