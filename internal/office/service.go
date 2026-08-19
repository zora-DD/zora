package office

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/agentruntime"
	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/identity"
)

const (
	maxRecipients = 100
	maxSubject    = 200
	maxBody       = 20_000
	maxLocation   = 300
	maxReason     = 500
)

type Service struct {
	store         Store
	executor      Executor
	workerID      string
	leaseDuration time.Duration
	now           func() time.Time
}

type ServiceOption func(*Service)

// WithExecutor 注入真实外部写执行器。执行器不满足幂等重放契约时拒绝启动。
func WithExecutor(executor Executor) ServiceOption {
	return func(service *Service) { service.executor = executor }
}

func NewService(store Store, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("办公草稿存储不能为空")
	}
	service := &Service{
		store: store, workerID: id.New("office_worker"), leaseDuration: 2 * time.Minute, now: time.Now,
	}
	for _, option := range options {
		option(service)
	}
	if service.executor != nil {
		if strings.TrimSpace(service.executor.Name()) == "" {
			return nil, fmt.Errorf("办公执行器名称不能为空")
		}
		if !service.executor.IdempotencySafe() {
			return nil, fmt.Errorf("办公执行器 %q 不支持幂等重放，已拒绝启用", service.executor.Name())
		}
	}
	return service, nil
}

// CreateEmailDraft 创建内部邮件预览；该方法不会连接邮箱，也不会发送邮件。
func (s *Service) CreateEmailDraft(ctx context.Context, input EmailDraft) (Draft, bool, error) {
	identity, err := draftIdentity(ctx)
	if err != nil {
		return Draft{}, false, err
	}
	input.To, err = normalizeAddresses(input.To, "收件人", true)
	if err != nil {
		return Draft{}, false, err
	}
	input.CC, err = normalizeAddresses(input.CC, "抄送人", false)
	if err != nil {
		return Draft{}, false, err
	}
	if len(input.To)+len(input.CC) > maxRecipients {
		return Draft{}, false, fmt.Errorf("邮件收件人和抄送人合计不能超过 %d 个", maxRecipients)
	}
	input.Subject = strings.TrimSpace(input.Subject)
	input.Body = strings.TrimSpace(input.Body)
	if err := validateText("邮件主题", input.Subject, 1, maxSubject); err != nil {
		return Draft{}, false, err
	}
	if err := validateText("邮件正文", input.Body, 1, maxBody); err != nil {
		return Draft{}, false, err
	}
	return s.save(ctx, identity, KindEmail, input.Subject, input)
}

// CreateCalendarDraft 创建内部日程预览；该方法不会调用 Microsoft Graph 写接口。
func (s *Service) CreateCalendarDraft(ctx context.Context, input CalendarDraft) (Draft, bool, error) {
	identity, err := draftIdentity(ctx)
	if err != nil {
		return Draft{}, false, err
	}
	input.Attendees, err = normalizeAddresses(input.Attendees, "参与人", false)
	if err != nil {
		return Draft{}, false, err
	}
	if len(input.Attendees) > maxRecipients {
		return Draft{}, false, fmt.Errorf("日程参与人不能超过 %d 个", maxRecipients)
	}
	input.Subject = strings.TrimSpace(input.Subject)
	input.Body = strings.TrimSpace(input.Body)
	input.Location = strings.TrimSpace(input.Location)
	input.TimeZone = strings.TrimSpace(input.TimeZone)
	if err := validateText("日程主题", input.Subject, 1, maxSubject); err != nil {
		return Draft{}, false, err
	}
	if err := validateText("日程正文", input.Body, 0, maxBody); err != nil {
		return Draft{}, false, err
	}
	if err := validateText("日程地点", input.Location, 0, maxLocation); err != nil {
		return Draft{}, false, err
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(input.Start))
	if err != nil {
		return Draft{}, false, fmt.Errorf("日程开始时间必须是带时区的 RFC3339 时间：%w", err)
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(input.End))
	if err != nil {
		return Draft{}, false, fmt.Errorf("日程结束时间必须是带时区的 RFC3339 时间：%w", err)
	}
	if !end.After(start) {
		return Draft{}, false, fmt.Errorf("日程结束时间必须晚于开始时间")
	}
	if end.Sub(start) > 31*24*time.Hour {
		return Draft{}, false, fmt.Errorf("单个日程持续时间不能超过 31 天")
	}
	if input.TimeZone != "" {
		if _, err := time.LoadLocation(input.TimeZone); err != nil {
			return Draft{}, false, fmt.Errorf("日程时区必须是有效的 IANA 时区：%q", input.TimeZone)
		}
	}
	if input.IsAllDay {
		if input.TimeZone == "" {
			return Draft{}, false, fmt.Errorf("全天日程必须提供 IANA 时区")
		}
		if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 {
			return Draft{}, false, fmt.Errorf("全天日程的开始和结束时间必须是当地时间 00:00:00")
		}
	}
	input.Start = start.Format(time.RFC3339)
	input.End = end.Format(time.RFC3339)
	return s.save(ctx, identity, KindCalendar, input.Subject, input)
}

func (s *Service) Get(ctx context.Context, id string) (Draft, error) {
	if strings.TrimSpace(id) == "" {
		return Draft{}, fmt.Errorf("草稿 ID 不能为空")
	}
	return s.store.GetDraft(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) ([]Draft, error) {
	if filter.Kind != "" && filter.Kind != KindEmail && filter.Kind != KindCalendar {
		return nil, fmt.Errorf("草稿类型仅支持 email 或 calendar")
	}
	if filter.Status != "" && !isDraftStatus(filter.Status) {
		return nil, fmt.Errorf("不支持的草稿状态：%s", filter.Status)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 100
	}
	return s.store.ListDrafts(ctx, filter)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	draft, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if draft.Status != StatusDraft {
		return fmt.Errorf("只有 draft 状态的草稿可以删除")
	}
	return s.store.DeleteDraft(ctx, id)
}

// SubmitForConfirmation 冻结当前草稿内容并进入等待人工确认状态。
// 状态迁移和审计事件由 Store 在同一事务中写入，重复提交只会有一个请求成功。
func (s *Service) SubmitForConfirmation(ctx context.Context, draftID string) (Draft, error) {
	if strings.TrimSpace(draftID) == "" {
		return Draft{}, fmt.Errorf("草稿 ID 不能为空")
	}
	return s.transition(ctx, draftID, StatusDraft, StatusPendingConfirmation, "用户提交草稿，等待人工确认")
}

// Decide 只记录批准或拒绝决定，不会发送邮件或创建日程。
func (s *Service) Decide(ctx context.Context, draftID, decision, reason string) (Draft, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != StatusApproved && decision != StatusRejected {
		return Draft{}, fmt.Errorf("草稿确认决定仅支持 approved 或 rejected")
	}
	reason = strings.TrimSpace(reason)
	if err := validateText("确认原因", reason, 0, maxReason); err != nil {
		return Draft{}, err
	}
	return s.transition(ctx, draftID, StatusPendingConfirmation, decision, reason)
}

func (s *Service) ListEvents(ctx context.Context, draftID string) ([]DraftEvent, error) {
	if strings.TrimSpace(draftID) == "" {
		return nil, fmt.Errorf("草稿 ID 不能为空")
	}
	if _, err := s.store.GetDraft(ctx, draftID); err != nil {
		return nil, err
	}
	return s.store.ListDraftEvents(ctx, draftID)
}

func (s *Service) transition(ctx context.Context, draftID, from, to, reason string) (Draft, error) {
	now := s.now().UTC()
	event := DraftEvent{
		ID: id.New("draft_event"), DraftID: draftID, FromStatus: from, ToStatus: to,
		Actor: userActor(ctx), Reason: reason, CreatedAt: now,
	}
	return s.store.TransitionDraft(ctx, draftID, from, to, event)
}

// userActor 只信任认证中间件写入 Context 的主体；本地无认证模式保留兼容名称。
func userActor(ctx context.Context) string {
	if principal, ok := identity.FromContext(ctx); ok {
		return principal.ID
	}
	return "user"
}

func (s *Service) save(ctx context.Context, identity agentruntime.ExecutionIdentity, kind, title string, payload any) (Draft, bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, false, fmt.Errorf("序列化办公草稿失败：%w", err)
	}
	digest := sha256.Sum256(append([]byte(kind+"\x00"), encoded...))
	now := s.now().UTC()
	draft := Draft{
		ID: id.New("draft"), Kind: kind, Status: StatusDraft,
		ConversationID: identity.ConversationID, SourceRunID: identity.RunID,
		Title: title, Payload: encoded, ContentHash: hex.EncodeToString(digest[:]),
		CreatedAt: now, UpdatedAt: now,
	}
	saved, created, err := s.store.SaveDraft(ctx, draft)
	if err != nil {
		return Draft{}, false, err
	}
	return saved, created, nil
}

func draftIdentity(ctx context.Context) (agentruntime.ExecutionIdentity, error) {
	identity, ok := agentruntime.ExecutionIdentityFromContext(ctx)
	if !ok {
		return agentruntime.ExecutionIdentity{}, fmt.Errorf("办公草稿工具只能在已创建 Agent Run 的对话中使用")
	}
	return identity, nil
}

func normalizeAddresses(values []string, field string, required bool) ([]string, error) {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := mail.ParseAddress(raw)
		if err != nil {
			return nil, fmt.Errorf("%s邮箱地址无效：%q", field, raw)
		}
		address := strings.ToLower(strings.TrimSpace(parsed.Address))
		if !seen[address] {
			seen[address] = true
			result = append(result, address)
		}
	}
	if required && len(result) == 0 {
		return nil, fmt.Errorf("邮件至少需要一个收件人")
	}
	// 稳定顺序让相同内容在 Agent 重试时得到相同哈希。
	sort.Strings(result)
	return result, nil
}

func validateText(field, value string, minimum, maximum int) error {
	count := utf8.RuneCountInString(value)
	if count < minimum || count > maximum {
		if minimum == 0 {
			return fmt.Errorf("%s不能超过 %d 个字符", field, maximum)
		}
		return fmt.Errorf("%s必须包含 %d 到 %d 个字符", field, minimum, maximum)
	}
	return nil
}

func isDraftStatus(status string) bool {
	switch status {
	case StatusDraft, StatusPendingConfirmation, StatusApproved, StatusExecuting,
		StatusCompleted, StatusRejected, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}
