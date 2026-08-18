package office

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhiruo/zora/internal/id"
	"github.com/zhiruo/zora/internal/store"
)

const (
	maxOperationError = 2_000
	recoveryBatchSize = 100
)

func (s *Service) ExecutionEnabled() bool { return s.executor != nil }

func (s *Service) ExecutorName() string {
	if s.executor == nil {
		return ""
	}
	return s.executor.Name()
}

// PrepareOperation 为已批准草稿创建唯一执行任务；重复调用返回同一任务和幂等键。
func (s *Service) PrepareOperation(ctx context.Context, draftID string) (Operation, bool, error) {
	if strings.TrimSpace(draftID) == "" {
		return Operation{}, false, fmt.Errorf("草稿 ID 不能为空")
	}
	// 草稿进入 executing/completed 后，重复准备仍返回原任务，保持接口幂等。
	if existing, err := s.store.GetOperationByDraft(ctx, draftID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Operation{}, false, err
	}
	draft, err := s.Get(ctx, draftID)
	if err != nil {
		return Operation{}, false, err
	}
	if draft.Status != StatusApproved {
		return Operation{}, false, fmt.Errorf("%w：只有 approved 状态的草稿可以准备执行，当前为 %s", ErrStateConflict, draft.Status)
	}
	now := s.now().UTC()
	digest := sha256.Sum256([]byte(draft.ID + "\x00" + draft.ContentHash))
	operation := Operation{
		ID: id.New("office_operation"), DraftID: draft.ID, Kind: draft.Kind,
		Status: OperationPending, IdempotencyKey: hex.EncodeToString(digest[:]),
		CreatedAt: now, UpdatedAt: now,
	}
	event := OperationEvent{
		ID: id.New("operation_event"), OperationID: operation.ID,
		FromStatus: "", ToStatus: OperationPending, Actor: "user",
		Reason: "用户为已批准草稿准备执行任务", CreatedAt: now,
	}
	return s.store.CreateOperation(ctx, operation, event)
}

func (s *Service) GetOperation(ctx context.Context, operationID string) (Operation, error) {
	if strings.TrimSpace(operationID) == "" {
		return Operation{}, fmt.Errorf("执行任务 ID 不能为空")
	}
	return s.store.GetOperation(ctx, operationID)
}

func (s *Service) GetOperationByDraft(ctx context.Context, draftID string) (Operation, error) {
	if strings.TrimSpace(draftID) == "" {
		return Operation{}, fmt.Errorf("草稿 ID 不能为空")
	}
	return s.store.GetOperationByDraft(ctx, draftID)
}

func (s *Service) ListOperations(ctx context.Context, filter OperationFilter) ([]Operation, error) {
	if filter.Status != "" && !isOperationStatus(filter.Status) {
		return nil, fmt.Errorf("不支持的执行任务状态：%s", filter.Status)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 100
	}
	return s.store.ListOperations(ctx, filter)
}

func (s *Service) ListOperationEvents(ctx context.Context, operationID string) ([]OperationEvent, error) {
	if _, err := s.GetOperation(ctx, operationID); err != nil {
		return nil, err
	}
	return s.store.ListOperationEvents(ctx, operationID)
}

// ExecuteOperation 先领取数据库租约，再调用具备幂等能力的真实执行器。
// 外部调用失败属于可观察的业务结果：任务落为 failed，调用方可用同一幂等键重试。
func (s *Service) ExecuteOperation(ctx context.Context, operationID string) (ExecutionOutcome, error) {
	current, err := s.GetOperation(ctx, operationID)
	if err != nil {
		return ExecutionOutcome{}, err
	}
	if current.Status == OperationCompleted {
		draft, getErr := s.Get(ctx, current.DraftID)
		return ExecutionOutcome{Operation: current, Draft: draft, ExternalEffect: true}, getErr
	}
	if current.Status != OperationPending && current.Status != OperationFailed {
		return ExecutionOutcome{}, fmt.Errorf("%w：执行任务当前为 %s，不能执行", ErrStateConflict, current.Status)
	}
	if s.executor == nil {
		return ExecutionOutcome{}, fmt.Errorf("%w：请先配置具备最小写权限和幂等保障的连接器", ErrExecutorUnavailable)
	}
	now := s.now().UTC()
	operationEvent := OperationEvent{
		ID: id.New("operation_event"), OperationID: current.ID,
		FromStatus: current.Status, ToStatus: OperationExecuting, Actor: "system",
		Reason: "执行器领取任务租约", CreatedAt: now,
	}
	draftFrom := StatusApproved
	if current.Status == OperationFailed {
		draftFrom = StatusFailed
	}
	draftEvent := DraftEvent{
		ID: id.New("draft_event"), DraftID: current.DraftID,
		FromStatus: draftFrom, ToStatus: StatusExecuting, Actor: "system",
		Reason: "办公执行任务开始", CreatedAt: now,
	}
	claimed, draft, err := s.store.ClaimOperation(
		ctx, current.ID, s.executor.Name(), s.workerID, now, now.Add(s.leaseDuration), operationEvent, draftEvent,
	)
	if err != nil {
		return ExecutionOutcome{}, err
	}

	result, executeErr := s.executor.Execute(ctx, draft, claimed.IdempotencyKey)
	if executeErr == nil && (!result.ExternalEffect || strings.TrimSpace(result.ExternalReference) == "") {
		executeErr = errors.New("执行器未返回可核验的外部操作引用，不能标记为已完成")
	}
	if executeErr != nil {
		message := truncateRunes(executeErr.Error(), maxOperationError)
		finished, updatedDraft, finishErr := s.finishOperation(
			ctx, claimed, OperationFailed, "", message, "外部操作执行失败："+message,
		)
		if finishErr != nil {
			return ExecutionOutcome{}, fmt.Errorf("记录办公执行失败状态时出错：%w", finishErr)
		}
		return ExecutionOutcome{Operation: finished, Draft: updatedDraft, ExternalEffect: false}, nil
	}
	finished, updatedDraft, err := s.finishOperation(
		ctx, claimed, OperationCompleted, strings.TrimSpace(result.ExternalReference), "", "外部操作已完成并返回可核验引用",
	)
	if err != nil {
		return ExecutionOutcome{}, fmt.Errorf("外部操作已返回成功，但持久化完成状态失败；请使用原幂等键恢复：%w", err)
	}
	return ExecutionOutcome{Operation: finished, Draft: updatedDraft, ExternalEffect: true}, nil
}

func (s *Service) finishOperation(ctx context.Context, claimed Operation, nextStatus, externalReference, lastError, reason string) (Operation, Draft, error) {
	now := s.now().UTC()
	operationEvent := OperationEvent{
		ID: id.New("operation_event"), OperationID: claimed.ID,
		FromStatus: OperationExecuting, ToStatus: nextStatus, Attempt: claimed.Attempt,
		Actor: "system", Reason: reason, CreatedAt: now,
	}
	draftStatus := StatusFailed
	if nextStatus == OperationCompleted {
		draftStatus = StatusCompleted
	}
	draftEvent := DraftEvent{
		ID: id.New("draft_event"), DraftID: claimed.DraftID,
		FromStatus: StatusExecuting, ToStatus: draftStatus, Actor: "system",
		Reason: reason, CreatedAt: now,
	}
	// 即使 HTTP 客户端已经断开，也要尽力释放租约并持久化执行结果。
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.FinishOperation(
		finishCtx, claimed.ID, claimed.LeaseOwner, nextStatus, externalReference, lastError,
		now, operationEvent, draftEvent,
	)
}

// RecoverExpiredOperations 将进程异常退出遗留的 executing 任务恢复为 failed。
// 后续重试仍使用原幂等键，因此不会创建第二个业务任务。
func (s *Service) RecoverExpiredOperations(ctx context.Context) (int, error) {
	total := 0
	for {
		now := s.now().UTC()
		items, err := s.store.ListExpiredOperations(ctx, now, recoveryBatchSize)
		if err != nil {
			return total, err
		}
		if len(items) == 0 {
			return total, nil
		}
		batchRecovered := 0
		for _, item := range items {
			_, _, finishErr := s.finishOperation(
				ctx, item, OperationFailed, "", "上一次执行因进程退出或租约超时而中断",
				"执行租约已过期，任务已恢复为可重试状态",
			)
			if errors.Is(finishErr, ErrStateConflict) {
				continue
			}
			if finishErr != nil {
				return total, finishErr
			}
			batchRecovered++
			total++
		}
		if len(items) < recoveryBatchSize || batchRecovered == 0 {
			return total, nil
		}
	}
}

func isOperationStatus(status string) bool {
	switch status {
	case OperationPending, OperationExecuting, OperationCompleted, OperationFailed:
		return true
	default:
		return false
	}
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
