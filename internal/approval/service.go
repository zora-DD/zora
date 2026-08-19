// Package approval 为高影响 Agent 请求提供可审计的人工审批闸门。
package approval

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhiruo/zora/internal/id"
)

const (
	ModeOff   = "off"
	ModeRisky = "risky"
	ModeAll   = "all"

	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusExpired  = "expired"
)

type Approval struct {
	ID             string     `json:"id"`
	RunID          string     `json:"run_id"`
	ConversationID string     `json:"conversation_id"`
	UserMessageID  string     `json:"user_message_id"`
	Status         string     `json:"status"`
	TriggerReason  string     `json:"trigger_reason"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	RequestedAt    time.Time  `json:"requested_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
}

type RequestInput struct {
	RunID          string
	ConversationID string
	UserMessageID  string
	Content        string
}

type Store interface {
	CreateApproval(ctx context.Context, item Approval) error
	GetApproval(ctx context.Context, id string) (Approval, error)
	ListApprovals(ctx context.Context, status string, limit int) ([]Approval, error)
	ResolveApproval(ctx context.Context, id, status, decisionReason string, decidedAt time.Time) (Approval, error)
}

type Options struct {
	Mode         string
	Timeout      time.Duration
	PollInterval time.Duration
}

type Service struct {
	store   Store
	mode    string
	timeout time.Duration
	poll    time.Duration
	mu      sync.Mutex
	waiters map[string]chan Approval
}

func NewService(store Store, options Options) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("审批存储不能为空")
	}
	if options.Mode != ModeOff && options.Mode != ModeRisky && options.Mode != ModeAll {
		return nil, fmt.Errorf("审批模式仅支持 off、risky 或 all")
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("审批等待时间必须大于 0")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	return &Service{store: store, mode: options.Mode, timeout: options.Timeout, poll: options.PollInterval, waiters: make(map[string]chan Approval)}, nil
}

func (s *Service) Enabled() bool { return s != nil && s.mode != ModeOff }

// Request 只创建审批记录，不阻塞执行；调用方先把 approval_required 发给客户端，再调用 Wait。
func (s *Service) Request(ctx context.Context, input RequestInput) (*Approval, error) {
	reason, required := s.triggerReason(input.Content)
	if !required {
		return nil, nil
	}
	item := Approval{
		ID: id.New("approval"), RunID: input.RunID, ConversationID: input.ConversationID,
		UserMessageID: input.UserMessageID, Status: StatusPending,
		TriggerReason: reason, RequestedAt: time.Now().UTC(),
	}
	waiter := make(chan Approval, 1)
	s.mu.Lock()
	s.waiters[item.ID] = waiter
	s.mu.Unlock()
	if err := s.store.CreateApproval(ctx, item); err != nil {
		s.removeWaiter(item.ID)
		return nil, err
	}
	return &item, nil
}

func (s *Service) Wait(ctx context.Context, approvalID string) (Approval, error) {
	// 先查一次持久化状态，覆盖“其他副本在 Wait 开始前已经完成审批”的竞态。
	current, err := s.store.GetApproval(ctx, approvalID)
	if err != nil {
		return Approval{}, err
	}
	if current.Status != StatusPending {
		return current, nil
	}
	s.mu.Lock()
	waiter := s.waiters[approvalID]
	s.mu.Unlock()
	defer s.removeWaiter(approvalID)
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	poller := time.NewTicker(s.poll)
	defer poller.Stop()
	for {
		select {
		case item := <-waiter:
			return item, nil
		case <-poller.C:
			// PostgreSQL 是审批状态事实源；轮询让任意副本处理的 Decide 都能唤醒原 SSE。
			current, getErr := s.store.GetApproval(ctx, approvalID)
			if getErr == nil && current.Status != StatusPending {
				return current, nil
			}
		case <-timer.C:
			item, resolveErr := s.resolveWithoutCancel(ctx, approvalID, StatusExpired, "等待人工审批超时")
			if resolveErr != nil {
				return Approval{}, resolveErr
			}
			return item, nil
		case <-ctx.Done():
			_, _ = s.resolveWithoutCancel(ctx, approvalID, StatusExpired, "请求已取消，审批自动失效")
			return Approval{}, ctx.Err()
		}
	}
}

func (s *Service) Decide(ctx context.Context, approvalID, decision, reason string) (Approval, error) {
	if decision != StatusApproved && decision != StatusRejected {
		return Approval{}, fmt.Errorf("审批决定仅支持 approved 或 rejected")
	}
	item, err := s.store.ResolveApproval(ctx, approvalID, decision, strings.TrimSpace(reason), time.Now().UTC())
	if err != nil {
		return Approval{}, err
	}
	s.mu.Lock()
	waiter := s.waiters[approvalID]
	s.mu.Unlock()
	if waiter != nil {
		select {
		case waiter <- item:
		default:
		}
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, id string) (Approval, error) {
	return s.store.GetApproval(ctx, id)
}

func (s *Service) List(ctx context.Context, status string, limit int) ([]Approval, error) {
	if status != "" && status != StatusPending && status != StatusApproved && status != StatusRejected && status != StatusExpired {
		return nil, fmt.Errorf("不支持的审批状态：%s", status)
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.store.ListApprovals(ctx, status, limit)
}

func (s *Service) resolveWithoutCancel(ctx context.Context, id, status, reason string) (Approval, error) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	item, err := s.store.ResolveApproval(persistCtx, id, status, reason, time.Now().UTC())
	if err == nil {
		return item, nil
	}
	// 决策请求可能与超时同时到达。若另一方已经成功写入终态，就使用数据库中的胜出结果，
	// 避免把一次合法批准误报为 Agent Run 失败。
	current, getErr := s.store.GetApproval(persistCtx, id)
	if getErr == nil && current.Status != StatusPending {
		return current, nil
	}
	return Approval{}, err
}

func (s *Service) removeWaiter(id string) {
	s.mu.Lock()
	delete(s.waiters, id)
	s.mu.Unlock()
}

func (s *Service) triggerReason(content string) (string, bool) {
	if s.mode == ModeOff {
		return "", false
	}
	if s.mode == ModeAll {
		return "当前配置要求所有多 Agent 请求在执行前人工确认", true
	}
	lower := strings.ToLower(content)
	for _, keyword := range []string{
		"发布到", "正式发布", "发送给", "发邮件", "提交到", "提交审批", "删除", "执行命令",
		"写入", "更新生产", "修改生产", "同步到", "部署到", "上线", "付款", "转账",
	} {
		if strings.Contains(lower, keyword) {
			return fmt.Sprintf("请求包含可能产生外部影响的操作“%s”", keyword), true
		}
	}
	return "", false
}
