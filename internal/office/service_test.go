package office

import (
	"context"
	"encoding/json"
	"errors"
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

type memoryDraftStore struct {
	items map[string]Draft
	keys  map[string]string
}

func newMemoryDraftStore() *memoryDraftStore {
	return &memoryDraftStore{items: make(map[string]Draft), keys: make(map[string]string)}
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
