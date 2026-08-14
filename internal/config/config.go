// Package config 负责从环境变量加载并校验 Zora 的运行配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultInstruction = `你是 Zora，一个可靠、简洁的中文 AI 助手。
优先直接解决用户的问题。需要精确计算或当前时间时必须使用工具，不要猜测工具结果。
工具失败时要如实说明。不要声称已经执行未执行的操作。`

// Config 汇总服务启动所需的全部配置，避免业务代码直接读取环境变量。
type Config struct {
	Addr           string        // HTTP 监听地址，例如 :8080。
	DataDir        string        // SQLite 等本地持久化文件的根目录。
	Provider       string        // mock 或 openai；openai 兼容通义千问等接口。
	Model          string        // 发送给模型服务的模型名称。
	APIKey         string        // 仅从环境变量读取，禁止写入仓库。
	BaseURL        string        // OpenAI-compatible API 地址；留空时使用适配器默认值。
	Instruction    string        // Agent 的系统指令。
	RequestTimeout time.Duration // 单次模型请求和整条消息链路的超时上限。
	MaxIterations  int           // ReAct 最大循环次数，防止模型无限调用工具。
}

// Load 在启动阶段完成配置校验，让错误尽早暴露，而不是运行到模型调用时才失败。
func Load() (Config, error) {
	timeout, err := time.ParseDuration(env("ZORA_REQUEST_TIMEOUT", "90s"))
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("ZORA_REQUEST_TIMEOUT must be a positive duration")
	}

	maxIterations, err := strconv.Atoi(env("ZORA_MAX_ITERATIONS", "8"))
	if err != nil || maxIterations < 1 || maxIterations > 50 {
		return Config{}, fmt.Errorf("ZORA_MAX_ITERATIONS must be between 1 and 50")
	}

	cfg := Config{
		Addr:           env("ZORA_ADDR", ":8080"),
		DataDir:        env("ZORA_DATA_DIR", "./data"),
		Provider:       strings.ToLower(env("ZORA_MODEL_PROVIDER", "mock")),
		Model:          env("ZORA_MODEL", "qwen-plus"),
		APIKey:         strings.TrimSpace(os.Getenv("ZORA_API_KEY")),
		BaseURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("ZORA_BASE_URL")), "/"),
		Instruction:    env("ZORA_SYSTEM_PROMPT", defaultInstruction),
		RequestTimeout: timeout,
		MaxIterations:  maxIterations,
	}

	switch cfg.Provider {
	case "mock":
		// Mock 模式不依赖外部密钥，并固定模型名，便于测试结果可复现。
		cfg.Model = "zora-mock"
	case "openai":
		if cfg.APIKey == "" {
			return Config{}, fmt.Errorf("ZORA_API_KEY is required when ZORA_MODEL_PROVIDER=openai")
		}
	default:
		return Config{}, fmt.Errorf("unsupported ZORA_MODEL_PROVIDER %q; use mock or openai", cfg.Provider)
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
