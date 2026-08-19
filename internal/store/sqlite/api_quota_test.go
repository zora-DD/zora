package sqlite

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/security"
)

func TestConsumeAPIQuotaPersistsAndRejectsOverflow(t *testing.T) {
	database, err := Open(t.TempDir() + "/quota.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	usage, allowed, err := database.ConsumeAPIQuota(context.Background(), "2026-08-19", "user:127.0.0.1", security.ResourceChatRuns, 2, 3, now)
	if err != nil || !allowed || usage.Used != 2 {
		t.Fatalf("first usage=%+v allowed=%v err=%v", usage, allowed, err)
	}
	usage, allowed, err = database.ConsumeAPIQuota(context.Background(), "2026-08-19", "user:127.0.0.1", security.ResourceChatRuns, 2, 3, now)
	if err != nil || allowed || usage.Used != 2 {
		t.Fatalf("overflow usage=%+v allowed=%v err=%v", usage, allowed, err)
	}
}

func TestConsumeAPIQuotaIsAtomicUnderConcurrency(t *testing.T) {
	database, err := Open(t.TempDir() + "/quota-concurrent.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	var allowedCount atomic.Int64
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, allowed, consumeErr := database.ConsumeAPIQuota(context.Background(), "2026-08-19", "concurrent-user", security.ResourceRequests, 1, 5, now)
			if consumeErr != nil {
				t.Errorf("consume quota: %v", consumeErr)
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wait.Wait()
	if allowedCount.Load() != 5 {
		t.Fatalf("allowed count = %d, want 5", allowedCount.Load())
	}
}
