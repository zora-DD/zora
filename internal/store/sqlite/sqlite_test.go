package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/approval"
	"github.com/zhiruo/zora/internal/domain"
	"github.com/zhiruo/zora/internal/memory"
	"github.com/zhiruo/zora/internal/office"
	"github.com/zhiruo/zora/internal/store"
	"github.com/zhiruo/zora/internal/summary"
)

func TestOpenMigratesV03MemoryColumns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy-memory.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE messages (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE, role TEXT NOT NULL, content TEXT NOT NULL, tool_name TEXT NOT NULL DEFAULT '', tool_call_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE memories (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, content TEXT NOT NULL, importance REAL NOT NULL,
  source_type TEXT NOT NULL, source_conversation_id TEXT, source_message_id TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, expires_at TEXT
);`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := memory.Memory{
		ID: "mem_migrated", Kind: memory.KindSemantic, MemoryKey: "profile:language",
		Content: "用户偏好 Go。", Importance: 0.8, UserEdited: true,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateMemory(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetMemory(context.Background(), item.ID)
	if err != nil || got.MemoryKey != item.MemoryKey || !got.UserEdited {
		t.Fatalf("migrated memory = %+v, %v", got, err)
	}
}

func TestConversationLifecycle(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	conversation := domain.Conversation{ID: "conv_1", Title: "test", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	message, err := database.AddMessage(ctx, domain.Message{
		ID: "msg_1", ConversationID: conversation.ID, Role: domain.RoleUser,
		Content: "hello", CreatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Sequence == 0 {
		t.Fatal("message sequence was not assigned")
	}

	got, err := database.GetConversation(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", got.MessageCount)
	}
	if err := database.DeleteConversation(ctx, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetConversation(ctx, conversation.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted conversation error = %v, want ErrNotFound", err)
	}
}

func TestAgentTaskRunLifecycle(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "agent-task-run.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	conversation := domain.Conversation{ID: "conv_task", Title: "父子 Run 测试", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	message, err := database.AddMessage(ctx, domain.Message{
		ID: "msg_task", ConversationID: conversation.ID, Role: domain.RoleUser,
		Content: "查询文档", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := domain.AgentRun{
		ID: "run_task", ConversationID: conversation.ID, UserMessageID: message.ID,
		Status: domain.RunRunning, Model: "zora-mock", StartedAt: now,
	}
	if err := database.CreateRun(ctx, root); err != nil {
		t.Fatal(err)
	}
	child := domain.AgentTaskRun{
		ID: "task_1", ParentRunID: root.ID, AgentName: "document_agent",
		ToolCallID: "call_1", Task: "查询发布日期", Status: domain.RunRunning,
		Attempt: 1, StartedAt: now.Add(time.Millisecond),
	}
	if err := database.CreateAgentTaskRun(ctx, child); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	if err := database.FinishAgentTaskRun(ctx, child.ID, domain.RunCompleted, "发布日期为 9 月 18 日", "", completedAt); err != nil {
		t.Fatal(err)
	}
	runs, err := database.ListAgentTaskRuns(ctx, root.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("child runs = %+v, %v", runs, err)
	}
	if runs[0].Status != domain.RunCompleted || runs[0].OutputPreview == "" || runs[0].CompletedAt == nil {
		t.Fatalf("unexpected child run: %+v", runs[0])
	}
	approvalItem := approval.Approval{
		ID: "approval_1", RunID: root.ID, ConversationID: conversation.ID,
		UserMessageID: message.ID, Status: approval.StatusPending,
		TriggerReason: "测试高影响操作", RequestedAt: now,
	}
	if err := database.CreateApproval(ctx, approvalItem); err != nil {
		t.Fatal(err)
	}
	resolved, err := database.ResolveApproval(ctx, approvalItem.ID, approval.StatusApproved, "已确认", completedAt)
	if err != nil || resolved.Status != approval.StatusApproved || resolved.DecidedAt == nil {
		t.Fatalf("resolved approval = %+v, %v", resolved, err)
	}
}

func TestOfficeDraftLifecycleAndRunIdempotency(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "office-draft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	conversation := domain.Conversation{ID: "conv_draft", Title: "草稿测试", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	message, err := database.AddMessage(ctx, domain.Message{
		ID: "msg_draft", ConversationID: conversation.ID, Role: domain.RoleUser, Content: "起草邮件", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := domain.AgentRun{
		ID: "run_draft", ConversationID: conversation.ID, UserMessageID: message.ID,
		Status: domain.RunRunning, Model: "zora-mock", StartedAt: now,
	}
	if err := database.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	service, err := office.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	executionCtx := agentruntime.WithExecutionIdentity(ctx, conversation.ID, run.ID)
	first, created, err := service.CreateEmailDraft(executionCtx, office.EmailDraft{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "项目将在周五发布。",
	})
	if err != nil || !created {
		t.Fatalf("first draft = %+v, created=%v, err=%v", first, created, err)
	}
	second, created, err := service.CreateEmailDraft(executionCtx, office.EmailDraft{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "项目将在周五发布。",
	})
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second draft = %+v, created=%v, err=%v", second, created, err)
	}
	items, err := service.List(ctx, office.ListFilter{Kind: office.KindEmail, Status: office.StatusDraft})
	if err != nil || len(items) != 1 || items[0].SourceRunID != run.ID {
		t.Fatalf("drafts = %+v, err=%v", items, err)
	}
	pending, err := service.SubmitForConfirmation(ctx, first.ID)
	if err != nil || pending.Status != office.StatusPendingConfirmation {
		t.Fatalf("pending draft = %+v, err=%v", pending, err)
	}
	if _, err := service.SubmitForConfirmation(ctx, first.ID); !errors.Is(err, office.ErrStateConflict) {
		t.Fatalf("duplicate submit error = %v", err)
	}
	type decisionResult struct {
		draft office.Draft
		err   error
	}
	decisions := make(chan decisionResult, 2)
	go func() {
		item, decideErr := service.Decide(ctx, first.ID, office.StatusApproved, "已核对")
		decisions <- decisionResult{draft: item, err: decideErr}
	}()
	go func() {
		item, decideErr := service.Decide(ctx, first.ID, office.StatusRejected, "不批准")
		decisions <- decisionResult{draft: item, err: decideErr}
	}()
	var winner office.Draft
	conflicts := 0
	for range 2 {
		result := <-decisions
		if result.err == nil {
			winner = result.draft
		} else if errors.Is(result.err, office.ErrStateConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected decision error: %v", result.err)
		}
	}
	if winner.ID != first.ID || conflicts != 1 || (winner.Status != office.StatusApproved && winner.Status != office.StatusRejected) {
		t.Fatalf("winner = %+v, conflicts=%d", winner, conflicts)
	}
	events, err := service.ListEvents(ctx, first.ID)
	if err != nil || len(events) != 2 || events[1].ToStatus != winner.Status {
		t.Fatalf("draft events = %+v, err=%v", events, err)
	}
	deletable, created, err := service.CreateEmailDraft(executionCtx, office.EmailDraft{
		To: []string{"dev@example.com"}, Subject: "临时草稿", Body: "仅用于验证删除。",
	})
	if err != nil || !created {
		t.Fatalf("deletable draft = %+v, created=%v, err=%v", deletable, created, err)
	}
	if err := service.Delete(ctx, deletable.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, deletable.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted draft error = %v", err)
	}
}

func TestMemoryLifecycleAndExpiryFilter(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := now.Add(-time.Hour)
	active := memory.Memory{
		ID: "mem_active", Kind: memory.KindSemantic, MemoryKey: "preference:language",
		Content: "偏好 Go", Importance: 0.8, UserEdited: true,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now,
	}
	expired := memory.Memory{
		ID: "mem_expired", Kind: memory.KindEpisodic, Content: "旧事件", Importance: 0.4,
		SourceType: memory.SourceManual, CreatedAt: now, UpdatedAt: now, ExpiresAt: &past,
	}
	for _, item := range []memory.Memory{active, expired} {
		if err := database.CreateMemory(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	items, err := database.ListMemories(ctx, memory.ListFilter{Limit: 20})
	if err != nil || len(items) != 1 || items[0].ID != active.ID {
		t.Fatalf("active memories = %+v, %v", items, err)
	}
	all, err := database.ListMemories(ctx, memory.ListFilter{IncludeExpired: true, Limit: 20})
	if err != nil || len(all) != 2 {
		t.Fatalf("all memories = %+v, %v", all, err)
	}

	active.Content = "偏好使用 Go 构建 Agent"
	active.UpdatedAt = now.Add(time.Minute)
	if err := database.UpdateMemory(ctx, active); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetMemory(ctx, active.ID)
	if err != nil || got.Content != active.Content || got.MemoryKey != active.MemoryKey || !got.UserEdited {
		t.Fatalf("updated memory = %+v, %v", got, err)
	}
	if err := database.DeleteMemory(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetMemory(ctx, active.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("get deleted memory error = %v", err)
	}
}

func TestConversationSummaryLifecycleAndMessageRange(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	conversation := domain.Conversation{ID: "conv_summary", Title: "摘要测试", CreatedAt: now, UpdatedAt: now}
	if err := database.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	var messages []domain.Message
	for index, content := range []string{"目标是开发 Agent", "收到", "主要使用 Go", "已记录"} {
		role := domain.RoleUser
		if index%2 == 1 {
			role = domain.RoleAssistant
		}
		message, err := database.AddMessage(ctx, domain.Message{
			ID: "msg_summary_" + content, ConversationID: conversation.ID,
			Role: role, Content: content, CreatedAt: now.Add(time.Duration(index) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}

	ranged, err := database.ListMessagesForSummary(ctx, conversation.ID, messages[0].Sequence, messages[2].Sequence, 10)
	if err != nil || len(ranged) != 2 || ranged[0].ID != messages[1].ID || ranged[1].ID != messages[2].ID {
		t.Fatalf("summary message range = %+v, %v", ranged, err)
	}
	item := summary.Summary{
		ConversationID: conversation.ID, Content: "用户要用 Go 开发 Agent。",
		ThroughSequence: messages[1].Sequence, MessageCount: 2, Model: "zora-mock", UpdatedAt: now,
	}
	if err := database.UpsertConversationSummary(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetConversationSummary(ctx, conversation.ID)
	if err != nil || got.Content != item.Content || got.ThroughSequence != item.ThroughSequence {
		t.Fatalf("summary = %+v, %v", got, err)
	}
	item.Content = "用户要用 Go 开发具备记忆的 Agent。"
	item.ThroughSequence = messages[2].Sequence
	item.MessageCount = 3
	if err := database.UpsertConversationSummary(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err = database.GetConversationSummary(ctx, conversation.ID)
	if err != nil || got.Content != item.Content || got.MessageCount != 3 {
		t.Fatalf("updated summary = %+v, %v", got, err)
	}

	if err := database.DeleteConversation(ctx, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetConversationSummary(ctx, conversation.ID); !errors.Is(err, summary.ErrNotFound) {
		t.Fatalf("summary should cascade delete, error = %v", err)
	}
}
