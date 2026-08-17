// Package mcpbridge 把官方 MCP Go SDK 暴露的工具适配为 Eino Tool。
package mcpbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig 是启动单个 MCP stdio Server 所需的最小配置。
type ServerConfig struct {
	Name         string
	Command      string
	Args         []string
	AllowedTools []string
	PassEnv      []string
}

// Options 统一限制 MCP 的启动时间、调用时间和输出大小。
type Options struct {
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
	MaxOutputRunes int
}

// Manager 持有 MCP 会话和已经适配的 Eino 工具，应用退出时必须关闭。
type Manager struct {
	mu       sync.Mutex
	sessions []*mcp.ClientSession
	tools    []tool.BaseTool
	closed   bool
}

// Connect 启动配置中的 MCP 子进程，完成握手和工具发现。
func Connect(ctx context.Context, servers []ServerConfig, options Options) (*Manager, error) {
	if options.ConnectTimeout <= 0 || options.CallTimeout <= 0 || options.MaxOutputRunes <= 0 {
		return nil, fmt.Errorf("MCP 超时和输出上限必须大于 0")
	}
	manager := &Manager{}
	exposedNames := make(map[string]string)
	for _, server := range servers {
		command := exec.Command(server.Command, server.Args...)
		// 显式设置非 nil Env，避免 MCP 子进程默认继承模型密钥、数据库连接串等全部环境变量。
		command.Env = selectedEnvironment(server.PassEnv)
		command.Stderr = os.Stderr
		transport := &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}
		tools, session, err := connectServer(ctx, server, options, transport)
		if err != nil {
			_ = manager.Close()
			return nil, err
		}
		for _, item := range tools {
			info, infoErr := item.Info(ctx)
			if infoErr != nil {
				_ = session.Close()
				_ = manager.Close()
				return nil, infoErr
			}
			if previous, exists := exposedNames[info.Name]; exists {
				_ = session.Close()
				_ = manager.Close()
				return nil, fmt.Errorf("MCP 工具公开名称冲突：%q 同时来自 %s 和 %s", info.Name, previous, server.Name)
			}
			exposedNames[info.Name] = server.Name
		}
		manager.sessions = append(manager.sessions, session)
		manager.tools = append(manager.tools, tools...)
	}
	return manager, nil
}

// Tools 返回副本，调用方可以安全追加到自己的 Tool allowlist。
func (m *Manager) Tools() []tool.BaseTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]tool.BaseTool(nil), m.tools...)
}

// Close 逆序关闭所有会话，确保 stdio 子进程能随 Zora 一起退出。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var joined error
	for index := len(m.sessions) - 1; index >= 0; index-- {
		if err := m.sessions[index].Close(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("关闭 MCP 会话失败：%w", err))
		}
	}
	return joined
}

func connectServer(ctx context.Context, server ServerConfig, options Options, transport mcp.Transport) ([]tool.BaseTool, *mcp.ClientSession, error) {
	connectCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "zora", Version: "0.5.0-dev"}, nil)
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 MCP Server %q 失败：%w", server.Name, err)
	}
	discovered, err := listAllTools(connectCtx, session)
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("发现 MCP Server %q 的工具失败：%w", server.Name, err)
	}
	allowed := make(map[string]struct{}, len(server.AllowedTools))
	for _, name := range server.AllowedTools {
		allowed[name] = struct{}{}
	}
	selected := make([]*mcp.Tool, 0, len(allowed))
	for _, item := range discovered {
		if _, ok := allowed[item.Name]; !ok {
			continue
		}
		// Annotation 只是提示而不是信任根；这里仍同时要求本地白名单，形成双重门禁。
		if item.Annotations == nil || !item.Annotations.ReadOnlyHint {
			_ = session.Close()
			return nil, nil, fmt.Errorf("MCP Server %q 的白名单工具 %q 未声明只读，已拒绝注册", server.Name, item.Name)
		}
		selected = append(selected, item)
	}
	if len(selected) != len(allowed) {
		found := make(map[string]struct{}, len(selected))
		for _, item := range selected {
			found[item.Name] = struct{}{}
		}
		missing := make([]string, 0)
		for name := range allowed {
			if _, ok := found[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		_ = session.Close()
		return nil, nil, fmt.Errorf("MCP Server %q 缺少白名单工具或工具未通过只读校验：%s", server.Name, strings.Join(missing, ", "))
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	result := make([]tool.BaseTool, 0, len(selected))
	for _, item := range selected {
		adapted, err := newTool(server.Name, session, item, options.CallTimeout, options.MaxOutputRunes)
		if err != nil {
			_ = session.Close()
			return nil, nil, err
		}
		result = append(result, adapted)
	}
	return result, session, nil
}

func listAllTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var result []*mcp.Tool
	cursor := ""
	for {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		result = append(result, page.Tools...)
		if page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

type mcpTool struct {
	info           *schema.ToolInfo
	session        *mcp.ClientSession
	originalName   string
	serverName     string
	callTimeout    time.Duration
	maxOutputRunes int
}

func newTool(serverName string, session *mcp.ClientSession, definition *mcp.Tool, callTimeout time.Duration, maxOutputRunes int) (*mcpTool, error) {
	rawSchema, err := json.Marshal(definition.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("序列化 MCP 工具 %q 的参数 Schema 失败：%w", definition.Name, err)
	}
	var inputSchema jsonschema.Schema
	if err := json.Unmarshal(rawSchema, &inputSchema); err != nil {
		return nil, fmt.Errorf("转换 MCP 工具 %q 的参数 Schema 失败：%w", definition.Name, err)
	}
	name := exposedToolName(serverName, definition.Name)
	description := strings.TrimSpace(definition.Description)
	if description == "" {
		description = fmt.Sprintf("调用 MCP Server %s 的只读工具 %s。", serverName, definition.Name)
	}
	return &mcpTool{
		info: &schema.ToolInfo{
			Name:        name,
			Desc:        description + " 此工具来自 MCP，只允许读取，不会执行写操作。",
			Extra:       map[string]any{"source": "mcp", "server": serverName, "original_name": definition.Name, "read_only": true},
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&inputSchema),
		},
		session: session, originalName: definition.Name, serverName: serverName,
		callTimeout: callTimeout, maxOutputRunes: maxOutputRunes,
	}, nil
}

func (t *mcpTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *mcpTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var arguments map[string]any
	if strings.TrimSpace(argumentsInJSON) == "" {
		arguments = map[string]any{}
	} else if err := json.Unmarshal([]byte(argumentsInJSON), &arguments); err != nil {
		return "", fmt.Errorf("MCP 工具 %s 的参数不是合法 JSON 对象：%w", t.info.Name, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, t.callTimeout)
	defer cancel()
	result, err := t.session.CallTool(callCtx, &mcp.CallToolParams{Name: t.originalName, Arguments: arguments})
	if err != nil {
		return "", fmt.Errorf("调用 MCP Server %q 的工具 %q 失败：%w", t.serverName, t.originalName, err)
	}
	output, err := formatResult(result)
	if err != nil {
		return "", fmt.Errorf("解析 MCP 工具 %q 的结果失败：%w", t.originalName, err)
	}
	output = truncateRunes(output, t.maxOutputRunes)
	if result.IsError {
		// MCP 业务错误应作为 ToolResult 交还模型，允许模型修正参数，而不是直接中断 ReAct。
		return "MCP 工具执行失败：" + output, nil
	}
	return output, nil
}

func formatResult(result *mcp.CallToolResult) (string, error) {
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, textContent.Text)
			continue
		}
		data, err := content.MarshalJSON()
		if err != nil {
			return "", err
		}
		parts = append(parts, string(data))
	}
	if len(parts) == 0 {
		return "{}", nil
	}
	return strings.Join(parts, "\n"), nil
}

func selectedEnvironment(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		if value, exists := os.LookupEnv(name); exists {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func exposedToolName(serverName, originalName string) string {
	raw := "mcp_" + normalizeName(serverName) + "_" + normalizeName(originalName)
	if len(raw) <= 64 {
		return raw
	}
	digest := sha256.Sum256([]byte(raw))
	return raw[:52] + "_" + hex.EncodeToString(digest[:5])
}

func normalizeName(value string) string {
	var builder strings.Builder
	previousUnderscore := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		valid := unicode.IsLetter(char) || unicode.IsDigit(char)
		if valid && char <= unicode.MaxASCII {
			builder.WriteRune(char)
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			builder.WriteByte('_')
			previousUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "tool"
	}
	return result
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + fmt.Sprintf("\n\n[结果过长，已截断；原始长度 %d 字符，上限 %d 字符]", len(runes), limit)
}
