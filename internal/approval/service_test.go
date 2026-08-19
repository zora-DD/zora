package approval

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu    sync.Mutex
	items map[string]Approval
}

func newMemoryStore() *memoryStore { return &memoryStore{items: make(map[string]Approval)} }

func (s *memoryStore) CreateApproval(_ context.Context, item Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[item.ID] = item
	return nil
}

func (s *memoryStore) GetApproval(_ context.Context, id string) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return Approval{}, fmt.Errorf("not found")
	}
	return item, nil
}

func (s *memoryStore) ListApprovals(_ context.Context, status string, _ int) ([]Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Approval, 0)
	for _, item := range s.items {
		if status == "" || item.Status == status {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *memoryStore) ResolveApproval(_ context.Context, id, status, reason string, decidedAt time.Time) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return Approval{}, fmt.Errorf("not found")
	}
	if item.Status != StatusPending {
		return Approval{}, fmt.Errorf("already resolved")
	}
	item.Status, item.DecisionReason, item.DecidedAt = status, reason, &decidedAt
	s.items[id] = item
	return item, nil
}

func TestRiskyApprovalCanResumeWaitingRun(t *testing.T) {
	service, err := NewService(newMemoryStore(), Options{Mode: ModeRisky, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Request(context.Background(), RequestInput{
		RunID: "run_1", ConversationID: "conv_1", UserMessageID: "msg_1",
		Content: "请把发布通知发送给项目成员",
	})
	if err != nil || item == nil {
		t.Fatalf("approval = %+v, %v", item, err)
	}
	decided := make(chan Approval, 1)
	go func() {
		result, waitErr := service.Wait(context.Background(), item.ID)
		if waitErr != nil {
			decided <- Approval{DecisionReason: waitErr.Error()}
			return
		}
		decided <- result
	}()
	if _, err := service.Decide(context.Background(), item.ID, StatusApproved, "确认发送"); err != nil {
		t.Fatal(err)
	}
	result := <-decided
	if result.Status != StatusApproved || result.DecisionReason != "确认发送" {
		t.Fatalf("decision = %+v", result)
	}
}

func TestApprovalDecisionFromAnotherReplicaResumesWaitingRun(t *testing.T) {
	store := newMemoryStore()
	waitingReplica, err := NewService(store, Options{Mode: ModeAll, Timeout: time.Second, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	decidingReplica, err := NewService(store, Options{Mode: ModeAll, Timeout: time.Second, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	item, err := waitingReplica.Request(context.Background(), RequestInput{
		RunID: "run_cross_replica", ConversationID: "conv_cross_replica", UserMessageID: "msg_cross_replica", Content: "发布到生产环境",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultChannel := make(chan Approval, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, waitErr := waitingReplica.Wait(context.Background(), item.ID)
		if waitErr != nil {
			errorChannel <- waitErr
			return
		}
		resultChannel <- result
	}()
	if _, err := decidingReplica.Decide(context.Background(), item.ID, StatusApproved, "由另一个 Pod 确认"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errorChannel:
		t.Fatal(err)
	case result := <-resultChannel:
		if result.Status != StatusApproved || result.DecisionReason != "由另一个 Pod 确认" {
			t.Fatalf("cross replica decision = %+v", result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("另一个副本的审批决定没有唤醒等待中的 Run")
	}
}

func TestRiskyApprovalSkipsReadOnlyQuestion(t *testing.T) {
	service, err := NewService(newMemoryStore(), Options{Mode: ModeRisky, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Request(context.Background(), RequestInput{Content: "根据文档查询发布日期"})
	if err != nil || item != nil {
		t.Fatalf("approval = %+v, %v", item, err)
	}
}

func TestApprovalExpires(t *testing.T) {
	service, err := NewService(newMemoryStore(), Options{Mode: ModeAll, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.Request(context.Background(), RequestInput{Content: "普通问题"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Wait(context.Background(), item.ID)
	if err != nil || result.Status != StatusExpired {
		t.Fatalf("approval = %+v, %v", result, err)
	}
}
