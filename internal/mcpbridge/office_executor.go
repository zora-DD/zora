package mcpbridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhiruo/zora/internal/office"
)

const (
	officeToolCreateEmail    = "create_email_draft"
	officeToolEmailState     = "get_email_delivery_state"
	officeToolSendEmail      = "send_email_draft"
	officeToolCreateCalendar = "create_calendar_event"
)

// OfficeExecutorConfig 只描述隔离子进程；令牌值不会进入 Zora Config 或 Agent 上下文。
type OfficeExecutorConfig struct {
	Command string
	Args    []string
	PassEnv []string
}

// OfficeExecutor 通过专用 MCP 会话调用固定的 Graph 写协议。
// 它不会把写工具适配成 Eino Tool，因此模型无法发现或调用这些能力。
type OfficeExecutor struct {
	mu          sync.Mutex
	session     *mcp.ClientSession
	callTimeout time.Duration
	closed      bool
}

// ConnectOfficeExecutor 启动凭据隔离子进程，并严格核对写工具名称与安全声明。
func ConnectOfficeExecutor(ctx context.Context, config OfficeExecutorConfig, options Options) (*OfficeExecutor, error) {
	if strings.TrimSpace(config.Command) == "" {
		return nil, fmt.Errorf("Microsoft 办公执行器命令不能为空")
	}
	if options.ConnectTimeout <= 0 || options.CallTimeout <= 0 {
		return nil, fmt.Errorf("Microsoft 办公执行器超时必须大于 0")
	}
	command := exec.Command(config.Command, config.Args...)
	// 非 nil Env 是安全边界：子进程只能得到白名单中的 Graph 凭据，不能继承模型和数据库密钥。
	command.Env = selectedEnvironment(config.PassEnv)
	command.Stderr = os.Stderr
	transport := &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}
	connectCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "zora-office-executor", Version: "0.9.0-dev"}, nil)
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 Microsoft 办公执行器失败：%w", err)
	}
	if err := validateOfficeTools(connectCtx, session); err != nil {
		_ = session.Close()
		return nil, err
	}
	return &OfficeExecutor{session: session, callTimeout: options.CallTimeout}, nil
}

func (e *OfficeExecutor) Name() string          { return "microsoft_graph_mcp" }
func (e *OfficeExecutor) IdempotencySafe() bool { return true }

// Execute 只接受 Service 交付的已批准快照。
// 邮件必须先创建远端草稿并持久化检查点，再允许发送；日历使用固定 transactionId。
func (e *OfficeExecutor) Execute(ctx context.Context, request office.ExecutionRequest) (office.ExecutionResult, error) {
	if request.Checkpoint == nil {
		return office.ExecutionResult{}, fmt.Errorf("Microsoft 办公执行器缺少检查点回调")
	}
	switch request.Draft.Kind {
	case office.KindEmail:
		return e.executeEmail(ctx, request)
	case office.KindCalendar:
		return e.executeCalendar(ctx, request)
	default:
		return office.ExecutionResult{}, fmt.Errorf("Microsoft 办公执行器不支持草稿类型：%s", request.Draft.Kind)
	}
}

func (e *OfficeExecutor) executeEmail(ctx context.Context, request office.ExecutionRequest) (office.ExecutionResult, error) {
	var draft office.EmailDraft
	if err := json.Unmarshal(request.Draft.Payload, &draft); err != nil {
		return office.ExecutionResult{}, fmt.Errorf("解析已批准邮件草稿失败：%w", err)
	}
	reference := strings.TrimSpace(request.ExternalReference)
	messageID := ""
	if reference == "" {
		var created struct {
			ID string `json:"id"`
		}
		if err := e.call(ctx, officeToolCreateEmail, draft, &created); err != nil {
			return office.ExecutionResult{}, err
		}
		messageID = strings.TrimSpace(created.ID)
		if messageID == "" {
			return office.ExecutionResult{}, fmt.Errorf("Microsoft Graph 未返回远端邮件草稿 ID")
		}
		reference = encodeGraphReference("message", messageID)
		// 只有检查点事务成功后，才允许调用真正发送邮件的工具。
		if err := request.Checkpoint(ctx, reference); err != nil {
			return office.ExecutionResult{}, fmt.Errorf("保存远端邮件草稿检查点失败，邮件尚未发送：%w", err)
		}
	} else {
		var err error
		messageID, err = decodeGraphReference(reference, "message")
		if err != nil {
			return office.ExecutionResult{}, err
		}
		var state struct {
			ID      string `json:"id"`
			IsDraft bool   `json:"is_draft"`
		}
		if err := e.call(ctx, officeToolEmailState, map[string]string{"id": messageID}, &state); err != nil {
			return office.ExecutionResult{}, fmt.Errorf("核对远端邮件恢复状态失败：%w", err)
		}
		if !state.IsDraft {
			// 上一次可能已发送，只是主进程在落完成状态前退出；此时不得重复发送。
			return office.ExecutionResult{ExternalEffect: true, ExternalReference: reference}, nil
		}
	}
	var sent struct {
		Sent bool `json:"sent"`
	}
	if err := e.call(ctx, officeToolSendEmail, map[string]string{"id": messageID}, &sent); err != nil {
		return office.ExecutionResult{}, fmt.Errorf("发送 Microsoft 邮件草稿失败：%w", err)
	}
	if !sent.Sent {
		return office.ExecutionResult{}, fmt.Errorf("Microsoft Graph 未确认邮件发送请求")
	}
	return office.ExecutionResult{ExternalEffect: true, ExternalReference: reference}, nil
}

func (e *OfficeExecutor) executeCalendar(ctx context.Context, request office.ExecutionRequest) (office.ExecutionResult, error) {
	if reference := strings.TrimSpace(request.ExternalReference); reference != "" {
		if _, err := decodeGraphReference(reference, "event"); err != nil {
			return office.ExecutionResult{}, err
		}
		return office.ExecutionResult{ExternalEffect: true, ExternalReference: reference}, nil
	}
	var draft office.CalendarDraft
	if err := json.Unmarshal(request.Draft.Payload, &draft); err != nil {
		return office.ExecutionResult{}, fmt.Errorf("解析已批准日程草稿失败：%w", err)
	}
	arguments := map[string]any{
		"attendees": draft.Attendees, "subject": draft.Subject,
		"start": draft.Start, "end": draft.End, "timezone": draft.TimeZone,
		"location": draft.Location, "body": draft.Body, "is_all_day": draft.IsAllDay,
		"transaction_id": transactionID(request.IdempotencyKey),
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := e.call(ctx, officeToolCreateCalendar, arguments, &created); err != nil {
		return office.ExecutionResult{}, err
	}
	if strings.TrimSpace(created.ID) == "" {
		return office.ExecutionResult{}, fmt.Errorf("Microsoft Graph 未返回远端日程 ID")
	}
	reference := encodeGraphReference("event", created.ID)
	if err := request.Checkpoint(ctx, reference); err != nil {
		return office.ExecutionResult{}, fmt.Errorf("保存远端日程检查点失败；重试时将复用相同 transactionId：%w", err)
	}
	return office.ExecutionResult{ExternalEffect: true, ExternalReference: reference}, nil
}

func (e *OfficeExecutor) call(ctx context.Context, name string, arguments, output any) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return fmt.Errorf("Microsoft 办公执行器已关闭")
	}
	session := e.session
	e.mu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("调用 Microsoft 办公工具 %q 失败：%w", name, err)
	}
	if result.IsError {
		message, formatErr := formatResult(result)
		if formatErr != nil {
			return fmt.Errorf("Microsoft 办公工具 %q 执行失败", name)
		}
		return fmt.Errorf("Microsoft 办公工具 %q 执行失败：%s", name, message)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("序列化 Microsoft 办公工具 %q 的结果失败：%w", name, err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("解析 Microsoft 办公工具 %q 的结果失败：%w", name, err)
	}
	return nil
}

// Close 关闭 MCP 会话并终止凭据隔离子进程。
func (e *OfficeExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if err := e.session.Close(); err != nil {
		return fmt.Errorf("关闭 Microsoft 办公执行器失败：%w", err)
	}
	return nil
}

func validateOfficeTools(ctx context.Context, session *mcp.ClientSession) error {
	listed, err := listAllTools(ctx, session)
	if err != nil {
		return fmt.Errorf("发现 Microsoft 办公执行工具失败：%w", err)
	}
	byName := make(map[string]*mcp.Tool, len(listed))
	for _, item := range listed {
		byName[item.Name] = item
	}
	required := map[string]bool{
		officeToolCreateEmail: false, officeToolEmailState: true,
		officeToolSendEmail: false, officeToolCreateCalendar: false,
	}
	for name, readOnly := range required {
		item := byName[name]
		if item == nil || item.Annotations == nil || item.Annotations.ReadOnlyHint != readOnly {
			return fmt.Errorf("Microsoft 办公执行器缺少工具 %q 或安全声明不匹配", name)
		}
	}
	if item := byName[officeToolSendEmail]; item.Annotations.DestructiveHint == nil || !*item.Annotations.DestructiveHint {
		return fmt.Errorf("Microsoft 办公执行器的发送工具未声明破坏性写操作")
	}
	return nil
}

func encodeGraphReference(kind, id string) string {
	return "microsoft-graph:" + kind + ":" + base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeGraphReference(reference, expectedKind string) (string, error) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] != "microsoft-graph" || parts[1] != expectedKind {
		return "", fmt.Errorf("外部操作引用与 Microsoft Graph %s 类型不匹配", expectedKind)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(decoded) == 0 {
		return "", fmt.Errorf("Microsoft Graph 外部操作引用无效")
	}
	if len(decoded) > 2048 || strings.ContainsAny(string(decoded), "\r\n") {
		return "", fmt.Errorf("Microsoft Graph 外部对象 ID 无效")
	}
	return string(decoded), nil
}

func transactionID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	// 固定设置 RFC 4122 version/variant 位，得到 Graph 文档示例使用的 UUID 形态。
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	value := hex.EncodeToString(digest[:16])
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:32]
}
