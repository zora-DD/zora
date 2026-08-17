package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSummaryBatchMessages = 1_000

type Service struct {
	store      Store
	summarizer Summarizer
	options    Options
	now        func() time.Time
}

func NewService(store Store, summarizer Summarizer, options Options) (*Service, error) {
	if store == nil || summarizer == nil {
		return nil, fmt.Errorf("会话摘要存储和摘要器不能为空")
	}
	if options.TriggerMessages < 4 || options.TriggerMessages > 500 {
		return nil, fmt.Errorf("摘要触发消息数必须在 4 到 500 之间")
	}
	if options.KeepRecent < 2 || options.KeepRecent >= options.TriggerMessages {
		return nil, fmt.Errorf("摘要保留消息数必须至少为 2，且小于触发消息数")
	}
	if options.MaxRunes < 500 || options.MaxRunes > 20_000 {
		return nil, fmt.Errorf("会话摘要长度必须在 500 到 20000 个字符之间")
	}
	if strings.TrimSpace(options.Model) == "" {
		return nil, fmt.Errorf("会话摘要模型名称不能为空")
	}
	return &Service{
		store: store, summarizer: summarizer, options: options,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Get(ctx context.Context, conversationID string) (Summary, error) {
	if strings.TrimSpace(conversationID) == "" {
		return Summary{}, fmt.Errorf("对话 ID 不能为空")
	}
	return s.store.GetConversationSummary(ctx, conversationID)
}

// HistoryLimit 告诉 Chat 在摘要触发前至少要保留多少条原始消息，避免阈值大于固定窗口时漏上下文。
func (s *Service) HistoryLimit() int { return s.options.TriggerMessages }

// Update 在“尚未摘要的消息数量达到阈值”时，把较早消息合并进已有摘要，
// 并始终保留最近一段原始消息供模型理解细节。
func (s *Service) Update(ctx context.Context, conversationID string, latestSequence int64) (UpdateResult, error) {
	if strings.TrimSpace(conversationID) == "" || latestSequence <= 0 {
		return UpdateResult{}, fmt.Errorf("更新会话摘要需要有效的对话 ID 和消息序号")
	}
	current, err := s.store.GetConversationSummary(ctx, conversationID)
	if errors.Is(err, ErrNotFound) {
		current = Summary{ConversationID: conversationID}
	} else if err != nil {
		return UpdateResult{}, fmt.Errorf("读取已有会话摘要失败：%w", err)
	}
	messages, err := s.store.ListMessagesForSummary(
		ctx, conversationID, current.ThroughSequence, latestSequence, maxSummaryBatchMessages,
	)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("读取待摘要消息失败：%w", err)
	}
	// sequence 是全库自增值，不同会话之间可能存在间隔，因此必须按实际消息条数判断阈值。
	if len(messages) < s.options.TriggerMessages {
		return UpdateResult{ThroughSequence: current.ThroughSequence, MessageCount: current.MessageCount}, nil
	}
	// 只压缩较早部分，最近窗口继续以原文进入模型，保留代词、代码和即时上下文细节。
	messages = messages[:len(messages)-s.options.KeepRecent]
	content, err := s.summarizer.Summarize(ctx, SummarizeInput{
		PreviousSummary: current.Content, Messages: messages,
	})
	if err != nil {
		return UpdateResult{}, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return UpdateResult{}, fmt.Errorf("会话摘要器返回了空内容")
	}
	if containsSensitiveSummaryLabel(content) {
		return UpdateResult{}, fmt.Errorf("会话摘要包含疑似敏感凭据，已拒绝保存")
	}
	if utf8.RuneCountInString(content) > s.options.MaxRunes {
		return UpdateResult{}, fmt.Errorf("会话摘要超过 %d 个字符，请调整摘要提示词或长度配置", s.options.MaxRunes)
	}
	current.Content = content
	current.ThroughSequence = messages[len(messages)-1].Sequence
	current.MessageCount += len(messages)
	current.Model = s.options.Model
	current.UpdatedAt = s.now()
	if err := s.store.UpsertConversationSummary(ctx, current); err != nil {
		return UpdateResult{}, fmt.Errorf("保存会话摘要失败：%w", err)
	}
	return UpdateResult{
		Updated: true, ThroughSequence: current.ThroughSequence,
		MessageCount: current.MessageCount, Characters: utf8.RuneCountInString(content),
	}, nil
}
