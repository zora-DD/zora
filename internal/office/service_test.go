package office

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/identity"
	"github.com/zhiruo/zora/internal/store"
)

func TestEmailDraftIsNormalizedPersistedAndIdempotent(t *testing.T) {
	t.Parallel()
	memoryStore := newMemoryDraftStore()
	service, err := NewService(memoryStore)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC) }
	ctx := agentruntime.WithExecutionIdentity(context.Background(), "conv-1", "run-1")
	input := EmailDraft{
		To: []string{"Dev@Example.com", "dev@example.com"}, CC: []string{"PM@example.com"},
		Subject: " 发布通知 ", Body: " 项目将在周五发布。 ",
	}
	first, created, err := service.CreateEmailDraft(ctx, input)
	if err != nil || !created {
		t.Fatalf("first draft = %+v, created=%v, err=%v", first, created, err)
	}
	second, created, err := service.CreateEmailDraft(ctx, input)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second draft = %+v, created=%v, err=%v", second, created, err)
	}
	var payload EmailDraft
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Join(payload.To, ",") != "dev@example.com" || strings.Join(payload.CC, ",") != "pm@example.com" {
		t.Fatalf("normalized recipients = %+v / %+v", payload.To, payload.CC)
	}
	if first.Status != StatusDraft || first.ConversationID != "conv-1" || first.SourceRunID != "run-1" || len(first.ContentHash) != 64 {
		t.Fatalf("unexpected persisted draft: %+v", first)
	}
}

func TestDraftValidationRejectsMissingIdentityInvalidAddressAndCalendarWindow(t *testing.T) {
	t.Parallel()
	service, err := NewService(newMemoryDraftStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CreateEmailDraft(context.Background(), EmailDraft{To: []string{"a@example.com"}, Subject: "主题", Body: "正文"}); err == nil || !strings.Contains(err.Error(), "Agent Run") {
		t.Fatalf("expected identity error, got %v", err)
	}
	ctx := agentruntime.WithExecutionIdentity(context.Background(), "conv-1", "run-1")
	if _, _, err := service.CreateEmailDraft(ctx, EmailDraft{To: []string{"not-an-email"}, Subject: "主题", Body: "正文"}); err == nil || !strings.Contains(err.Error(), "邮箱地址无效") {
		t.Fatalf("expected address error, got %v", err)
	}
	if _, _, err := service.CreateCalendarDraft(ctx, CalendarDraft{
		Subject: "评审会", Start: "2026-08-17T11:00:00+08:00", End: "2026-08-17T10:00:00+08:00",
	}); err == nil || !strings.Contains(err.Error(), "晚于开始时间") {
		t.Fatalf("expected calendar window error, got %v", err)
	}
	if _, _, err := service.CreateCalendarDraft(ctx, CalendarDraft{
		Subject: "全天发布窗口", Start: "2026-08-17T00:00:00+08:00", End: "2026-08-18T00:00:00+08:00", IsAllDay: true,
	}); err == nil || !strings.Contains(err.Error(), "全天日程必须提供") {
		t.Fatalf("expected all-day timezone error, got %v", err)
	}
}

func TestDraftToolReturnsExplicitNoExternalEffect(t *testing.T) {
	t.Parallel()
	service, err := NewService(newMemoryDraftStore())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDraftTools(service)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentruntime.WithExecutionIdentity(context.Background(), "conv-1", "run-1")
	var emailTool tool.InvokableTool
	for _, item := range tools {
		info, infoErr := item.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "preview_email_draft" {
			emailTool = item.(tool.InvokableTool)
		}
	}
	if emailTool == nil {
		t.Fatal("preview_email_draft tool not found")
	}
	result, err := emailTool.InvokableRun(ctx, `{"to":["dev@example.com"],"subject":"发布通知","body":"项目将在周五发布。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"external_effect":false`) || !strings.Contains(result, "尚未发送") || !strings.Contains(result, `"status":"draft"`) {
		t.Fatalf("unexpected tool result: %s", result)
	}
}

func TestDraftConfirmationStateMachineIsAuditableAndOneShot(t *testing.T) {
	t.Parallel()
	memoryStore := newMemoryDraftStore()
	service, err := NewService(memoryStore)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC) }
	ctx := agentruntime.WithExecutionIdentity(context.Background(), "conv-1", "run-confirm")
	ctx = identity.WithPrincipal(ctx, identity.Principal{ID: "github:12345", TenantID: "github-user:12345"})
	draft, _, err := service.CreateEmailDraft(ctx, EmailDraft{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "项目将在周五发布。",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := service.SubmitForConfirmation(ctx, draft.ID)
	if err != nil || pending.Status != StatusPendingConfirmation {
		t.Fatalf("pending draft = %+v, err=%v", pending, err)
	}
	if _, err := service.SubmitForConfirmation(ctx, draft.ID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected duplicate submit conflict, got %v", err)
	}
	approved, err := service.Decide(ctx, draft.ID, StatusApproved, "内容和收件人已核对")
	if err != nil || approved.Status != StatusApproved {
		t.Fatalf("approved draft = %+v, err=%v", approved, err)
	}
	if _, err := service.Decide(ctx, draft.ID, StatusRejected, "重复决定"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected duplicate decision conflict, got %v", err)
	}
	if err := service.Delete(ctx, draft.ID); err == nil || !strings.Contains(err.Error(), "draft 状态") {
		t.Fatalf("expected approved delete rejection, got %v", err)
	}
	events, err := service.ListEvents(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].FromStatus != StatusDraft || events[0].ToStatus != StatusPendingConfirmation ||
		events[1].ToStatus != StatusApproved || events[1].Actor != "github:12345" {
		t.Fatalf("unexpected draft events: %+v", events)
	}
}

func TestOperationExecutionRetriesWithStableIdempotencyKey(t *testing.T) {
	t.Parallel()
	memoryStore := newMemoryDraftStore()
	executor := &sequenceExecutor{errors: []error{errors.New("Graph 暂时不可用"), nil}}
	service, err := NewService(memoryStore, WithExecutor(executor))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC) }
	draft := createApprovedEmailDraft(t, service, "run-operation")
	operation, created, err := service.PrepareOperation(context.Background(), draft.ID)
	if err != nil || !created || operation.Status != OperationPending || len(operation.IdempotencyKey) != 64 {
		t.Fatalf("operation = %+v, created=%v, err=%v", operation, created, err)
	}
	same, created, err := service.PrepareOperation(context.Background(), draft.ID)
	if err != nil || created || same.ID != operation.ID || same.IdempotencyKey != operation.IdempotencyKey {
		t.Fatalf("idempotent prepare = %+v, created=%v, err=%v", same, created, err)
	}
	failed, err := service.ExecuteOperation(context.Background(), operation.ID)
	if err != nil || failed.Operation.Status != OperationFailed || failed.Draft.Status != StatusFailed || failed.ExternalEffect {
		t.Fatalf("failed outcome = %+v, err=%v", failed, err)
	}
	completed, err := service.ExecuteOperation(context.Background(), operation.ID)
	if err != nil || completed.Operation.Status != OperationCompleted || completed.Draft.Status != StatusCompleted || !completed.ExternalEffect {
		t.Fatalf("completed outcome = %+v, err=%v", completed, err)
	}
	if completed.Operation.Attempt != 2 || completed.Operation.ExternalReference != "remote-2" || len(executor.keys) != 2 || executor.keys[0] != executor.keys[1] {
		t.Fatalf("unexpected retry state: operation=%+v keys=%v", completed.Operation, executor.keys)
	}
	// 已完成任务重复请求直接返回持久化结果，不能再次调用外部执行器。
	if _, err := service.ExecuteOperation(context.Background(), operation.ID); err != nil || len(executor.keys) != 2 {
		t.Fatalf("completed retry err=%v calls=%d", err, len(executor.keys))
	}
	events, err := service.ListOperationEvents(context.Background(), operation.ID)
	if err != nil || len(events) != 5 || events[0].ToStatus != OperationPending || events[4].ToStatus != OperationCompleted {
		t.Fatalf("operation events = %+v, err=%v", events, err)
	}
}

func TestOperationExecutorSafetyGateAndUnavailableState(t *testing.T) {
	t.Parallel()
	if _, err := NewService(newMemoryDraftStore(), WithExecutor(&sequenceExecutor{unsafe: true})); err == nil || !strings.Contains(err.Error(), "不支持幂等重放") {
		t.Fatalf("expected unsafe executor rejection, got %v", err)
	}
	service, err := NewService(newMemoryDraftStore())
	if err != nil {
		t.Fatal(err)
	}
	draft := createApprovedEmailDraft(t, service, "run-no-executor")
	operation, _, err := service.PrepareOperation(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecuteOperation(context.Background(), operation.ID); !errors.Is(err, ErrExecutorUnavailable) {
		t.Fatalf("expected executor unavailable, got %v", err)
	}
	stored, err := service.GetOperation(context.Background(), operation.ID)
	if err != nil || stored.Status != OperationPending || stored.Attempt != 0 {
		t.Fatalf("unavailable executor mutated operation: %+v, err=%v", stored, err)
	}
}

func TestExpiredOperationIsRecoveredAsRetryableFailure(t *testing.T) {
	t.Parallel()
	memoryStore := newMemoryDraftStore()
	service, err := NewService(memoryStore, WithExecutor(&sequenceExecutor{}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	draft := createApprovedEmailDraft(t, service, "run-recovery")
	operation, _, err := service.PrepareOperation(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, err := memoryStore.ClaimOperation(
		context.Background(), operation.ID, "test-executor", service.workerID,
		now.Add(-2*time.Minute), now.Add(-time.Minute), OperationEvent{}, DraftEvent{},
	)
	if err != nil || claimed.Status != OperationExecuting {
		t.Fatalf("claimed = %+v, err=%v", claimed, err)
	}
	recovered, err := service.RecoverExpiredOperations(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d, err=%v", recovered, err)
	}
	stored, err := service.GetOperation(context.Background(), operation.ID)
	if err != nil || stored.Status != OperationFailed || stored.Attempt != 1 || !strings.Contains(stored.LastError, "租约超时") {
		t.Fatalf("recovered operation = %+v, err=%v", stored, err)
	}
}

func createApprovedEmailDraft(t *testing.T, service *Service, runID string) Draft {
	t.Helper()
	ctx := agentruntime.WithExecutionIdentity(context.Background(), "conv-1", runID)
	draft, _, err := service.CreateEmailDraft(ctx, EmailDraft{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "项目将在周五发布。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitForConfirmation(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	draft, err = service.Decide(ctx, draft.ID, StatusApproved, "测试批准")
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

type sequenceExecutor struct {
	unsafe bool
	errors []error
	keys   []string
}

func (e *sequenceExecutor) Name() string          { return "test-executor" }
func (e *sequenceExecutor) IdempotencySafe() bool { return !e.unsafe }
func (e *sequenceExecutor) Execute(_ context.Context, request ExecutionRequest) (ExecutionResult, error) {
	e.keys = append(e.keys, request.IdempotencyKey)
	index := len(e.keys) - 1
	if index < len(e.errors) && e.errors[index] != nil {
		return ExecutionResult{}, e.errors[index]
	}
	return ExecutionResult{ExternalEffect: true, ExternalReference: fmt.Sprintf("remote-%d", len(e.keys))}, nil
}

type memoryDraftStore struct {
	items            map[string]Draft
	keys             map[string]string
	events           map[string][]DraftEvent
	operations       map[string]Operation
	operationByDraft map[string]string
	operationEvents  map[string][]OperationEvent
}

func newMemoryDraftStore() *memoryDraftStore {
	return &memoryDraftStore{
		items: make(map[string]Draft), keys: make(map[string]string), events: make(map[string][]DraftEvent),
		operations: make(map[string]Operation), operationByDraft: make(map[string]string), operationEvents: make(map[string][]OperationEvent),
	}
}

func (s *memoryDraftStore) SaveDraft(_ context.Context, draft Draft) (Draft, bool, error) {
	key := draft.SourceRunID + "\x00" + draft.ContentHash
	if existingID := s.keys[key]; existingID != "" {
		return s.items[existingID], false, nil
	}
	s.items[draft.ID] = draft
	s.keys[key] = draft.ID
	return draft, true, nil
}

func (s *memoryDraftStore) GetDraft(_ context.Context, id string) (Draft, error) {
	item, ok := s.items[id]
	if !ok {
		return Draft{}, store.ErrNotFound
	}
	return item, nil
}

func (s *memoryDraftStore) ListDrafts(_ context.Context, filter ListFilter) ([]Draft, error) {
	result := make([]Draft, 0)
	for _, item := range s.items {
		if (filter.Kind == "" || item.Kind == filter.Kind) && (filter.Status == "" || item.Status == filter.Status) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *memoryDraftStore) DeleteDraft(_ context.Context, id string) error {
	item, ok := s.items[id]
	if !ok {
		return store.ErrNotFound
	}
	if item.Status != StatusDraft {
		return errors.New("草稿状态不允许删除")
	}
	delete(s.items, id)
	return nil
}

func (s *memoryDraftStore) TransitionDraft(_ context.Context, id, expectedStatus, nextStatus string, event DraftEvent) (Draft, error) {
	item, ok := s.items[id]
	if !ok {
		return Draft{}, store.ErrNotFound
	}
	if item.Status != expectedStatus {
		return Draft{}, fmt.Errorf("%w：当前为 %s", ErrStateConflict, item.Status)
	}
	item.Status = nextStatus
	item.UpdatedAt = event.CreatedAt
	s.items[id] = item
	s.events[id] = append(s.events[id], event)
	return item, nil
}

func (s *memoryDraftStore) ListDraftEvents(_ context.Context, draftID string) ([]DraftEvent, error) {
	return append([]DraftEvent(nil), s.events[draftID]...), nil
}

func (s *memoryDraftStore) CreateOperation(_ context.Context, operation Operation, event OperationEvent) (Operation, bool, error) {
	if existingID := s.operationByDraft[operation.DraftID]; existingID != "" {
		return s.operations[existingID], false, nil
	}
	draft, ok := s.items[operation.DraftID]
	if !ok {
		return Operation{}, false, store.ErrNotFound
	}
	if draft.Status != StatusApproved {
		return Operation{}, false, fmt.Errorf("%w：当前为 %s", ErrStateConflict, draft.Status)
	}
	s.operations[operation.ID] = operation
	s.operationByDraft[operation.DraftID] = operation.ID
	s.operationEvents[operation.ID] = append(s.operationEvents[operation.ID], event)
	return operation, true, nil
}

func (s *memoryDraftStore) GetOperation(_ context.Context, id string) (Operation, error) {
	item, ok := s.operations[id]
	if !ok {
		return Operation{}, store.ErrNotFound
	}
	return item, nil
}

func (s *memoryDraftStore) GetOperationByDraft(_ context.Context, draftID string) (Operation, error) {
	return s.GetOperation(context.Background(), s.operationByDraft[draftID])
}

func (s *memoryDraftStore) ListOperations(_ context.Context, filter OperationFilter) ([]Operation, error) {
	items := make([]Operation, 0)
	for _, item := range s.operations {
		if (filter.DraftID == "" || item.DraftID == filter.DraftID) && (filter.Status == "" || item.Status == filter.Status) {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *memoryDraftStore) ClaimOperation(_ context.Context, id, executorName, leaseOwner string, now, leaseUntil time.Time, operationEvent OperationEvent, draftEvent DraftEvent) (Operation, Draft, error) {
	item, ok := s.operations[id]
	if !ok {
		return Operation{}, Draft{}, store.ErrNotFound
	}
	if item.Status != OperationPending && item.Status != OperationFailed {
		return Operation{}, Draft{}, fmt.Errorf("%w：当前为 %s", ErrStateConflict, item.Status)
	}
	draft := s.items[item.DraftID]
	expectedDraftStatus := StatusApproved
	if item.Status == OperationFailed {
		expectedDraftStatus = StatusFailed
	}
	if draft.Status != expectedDraftStatus {
		return Operation{}, Draft{}, fmt.Errorf("%w：草稿当前为 %s", ErrStateConflict, draft.Status)
	}
	from := item.Status
	item.Status, item.ExecutorName, item.LeaseOwner = OperationExecuting, executorName, leaseOwner
	item.Attempt++
	item.LeaseUntil, item.UpdatedAt, item.LastError = &leaseUntil, now, ""
	draft.Status, draft.UpdatedAt = StatusExecuting, now
	s.operations[id], s.items[draft.ID] = item, draft
	operationEvent.FromStatus, operationEvent.ToStatus, operationEvent.Attempt = from, OperationExecuting, item.Attempt
	s.operationEvents[id] = append(s.operationEvents[id], operationEvent)
	s.events[draft.ID] = append(s.events[draft.ID], draftEvent)
	return item, draft, nil
}

func (s *memoryDraftStore) CheckpointOperation(_ context.Context, id, leaseOwner, externalReference string, now time.Time, event OperationEvent) (Operation, error) {
	item, ok := s.operations[id]
	if !ok {
		return Operation{}, store.ErrNotFound
	}
	if item.Status != OperationExecuting || item.LeaseOwner != leaseOwner {
		return Operation{}, fmt.Errorf("%w：租约已失效", ErrStateConflict)
	}
	item.ExternalReference, item.UpdatedAt = externalReference, now
	s.operations[id] = item
	event.FromStatus, event.ToStatus, event.Attempt = OperationExecuting, OperationExecuting, item.Attempt
	s.operationEvents[id] = append(s.operationEvents[id], event)
	return item, nil
}

func (s *memoryDraftStore) FinishOperation(_ context.Context, id, leaseOwner, nextStatus, externalReference, lastError string, now time.Time, operationEvent OperationEvent, draftEvent DraftEvent) (Operation, Draft, error) {
	item, ok := s.operations[id]
	if !ok {
		return Operation{}, Draft{}, store.ErrNotFound
	}
	if item.Status != OperationExecuting || item.LeaseOwner != leaseOwner {
		return Operation{}, Draft{}, fmt.Errorf("%w：租约已失效", ErrStateConflict)
	}
	item.Status, item.LeaseOwner, item.LeaseUntil = nextStatus, "", nil
	item.ExternalReference, item.LastError, item.UpdatedAt = externalReference, lastError, now
	if nextStatus == OperationCompleted {
		item.CompletedAt = &now
	}
	draft := s.items[item.DraftID]
	if draft.Status != StatusExecuting {
		return Operation{}, Draft{}, fmt.Errorf("%w：草稿当前为 %s", ErrStateConflict, draft.Status)
	}
	draft.Status = StatusFailed
	if nextStatus == OperationCompleted {
		draft.Status = StatusCompleted
	}
	draft.UpdatedAt = now
	s.operations[id], s.items[draft.ID] = item, draft
	operationEvent.FromStatus, operationEvent.ToStatus, operationEvent.Attempt = OperationExecuting, nextStatus, item.Attempt
	s.operationEvents[id] = append(s.operationEvents[id], operationEvent)
	s.events[draft.ID] = append(s.events[draft.ID], draftEvent)
	return item, draft, nil
}

func (s *memoryDraftStore) ListOperationEvents(_ context.Context, operationID string) ([]OperationEvent, error) {
	return append([]OperationEvent(nil), s.operationEvents[operationID]...), nil
}

func (s *memoryDraftStore) ListExpiredOperations(_ context.Context, now time.Time, limit int) ([]Operation, error) {
	items := make([]Operation, 0)
	for _, item := range s.operations {
		if item.Status == OperationExecuting && item.LeaseUntil != nil && !item.LeaseUntil.After(now) {
			items = append(items, item)
			if len(items) == limit {
				break
			}
		}
	}
	return items, nil
}
