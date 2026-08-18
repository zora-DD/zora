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
		events[1].ToStatus != StatusApproved || events[1].Actor != "user" {
		t.Fatalf("unexpected draft events: %+v", events)
	}
}

type memoryDraftStore struct {
	items  map[string]Draft
	keys   map[string]string
	events map[string][]DraftEvent
}

func newMemoryDraftStore() *memoryDraftStore {
	return &memoryDraftStore{items: make(map[string]Draft), keys: make(map[string]string), events: make(map[string][]DraftEvent)}
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
