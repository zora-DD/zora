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
涉及用户上传的资料或私有知识时，优先使用 knowledge_search，并在回答中标注文档名和片段编号。
工具失败时要如实说明。不要声称已经执行未执行的操作。`

// Config 汇总服务启动所需的全部配置，避免业务代码直接读取环境变量。
type Config struct {
	Addr             string        // HTTP 监听地址，例如 :8088。
	DataDir          string        // SQLite 等本地持久化文件的根目录。
	StoreProvider    string        // sqlite 或 postgres；默认 sqlite 保持零依赖体验。
	PostgresDSN      string        // PostgreSQL 连接串，只允许通过环境变量注入。
	PostgresMaxConns int           // PostgreSQL 连接池最大连接数。
	Provider         string        // mock 或 openai；openai 兼容通义千问等接口。
	Model            string        // 发送给模型服务的模型名称。
	APIKey           string        // 仅从环境变量读取，禁止写入仓库。
	BaseURL          string        // OpenAI-compatible API 地址；留空时使用适配器默认值。
	Instruction      string        // Agent 的系统指令。
	RequestTimeout   time.Duration // 单次模型请求和整条消息链路的超时上限。
	MaxIterations    int           // ReAct 最大循环次数，防止模型无限调用工具。

	EmbeddingProvider   string // hash 用于本地开发，openai 用于真实语义向量。
	EmbeddingModel      string // Embedding 模型名，如 text-embedding-v4。
	EmbeddingAPIKey     string // 可单独配置；未配置时复用 ZORA_API_KEY。
	EmbeddingBaseURL    string // OpenAI-compatible Embedding API 的 v1 根地址。
	EmbeddingDimensions int    // 入库与查询必须使用相同的向量维度。
	KnowledgeChunkSize  int    // 按 Unicode 字符计算的分块上限。
	KnowledgeOverlap    int    // 相邻分块的重叠字符数，避免语义在边界断开。

	MemoryAutoCapture   bool // 是否在回答完成后自动提取长期记忆候选。
	MemoryMaxCandidates int  // 单轮最多接纳的候选数，限制额外成本和错误放大。
}

// Load 在启动阶段完成配置校验，让错误尽早暴露，而不是运行到模型调用时才失败。
func Load() (Config, error) {
	embeddingProvider := strings.ToLower(env("ZORA_EMBEDDING_PROVIDER", "hash"))
	embeddingDimensionFallback := "384"
	if embeddingProvider == "openai" {
		// text-embedding-v4 默认使用 1024 维；本地 Hash 模式用 384 维减少文件体积。
		embeddingDimensionFallback = "1024"
	}
	timeout, err := time.ParseDuration(env("ZORA_REQUEST_TIMEOUT", "90s"))
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("ZORA_REQUEST_TIMEOUT 必须是大于 0 的时间长度，例如 90s")
	}

	maxIterations, err := strconv.Atoi(env("ZORA_MAX_ITERATIONS", "8"))
	if err != nil || maxIterations < 1 || maxIterations > 50 {
		return Config{}, fmt.Errorf("ZORA_MAX_ITERATIONS 必须在 1 到 50 之间")
	}
	chunkSize, err := positiveInt("ZORA_KNOWLEDGE_CHUNK_SIZE", "800")
	if err != nil || chunkSize < 100 {
		return Config{}, fmt.Errorf("ZORA_KNOWLEDGE_CHUNK_SIZE 至少为 100")
	}
	chunkOverlap, err := nonNegativeInt("ZORA_KNOWLEDGE_CHUNK_OVERLAP", "120")
	if err != nil || chunkOverlap*2 >= chunkSize {
		return Config{}, fmt.Errorf("ZORA_KNOWLEDGE_CHUNK_OVERLAP 不能小于 0，且必须小于分块大小的一半")
	}
	embeddingDimensions, err := positiveInt("ZORA_EMBEDDING_DIMENSIONS", embeddingDimensionFallback)
	if err != nil || embeddingDimensions < 64 {
		return Config{}, fmt.Errorf("ZORA_EMBEDDING_DIMENSIONS 至少为 64")
	}
	postgresMaxConns, err := positiveInt("ZORA_POSTGRES_MAX_CONNS", "10")
	if err != nil || postgresMaxConns > 100 {
		return Config{}, fmt.Errorf("ZORA_POSTGRES_MAX_CONNS 必须在 1 到 100 之间")
	}
	memoryAutoCapture, err := strconv.ParseBool(env("ZORA_MEMORY_AUTO_CAPTURE", "true"))
	if err != nil {
		return Config{}, fmt.Errorf("ZORA_MEMORY_AUTO_CAPTURE 必须是 true 或 false")
	}
	memoryMaxCandidates, err := positiveInt("ZORA_MEMORY_MAX_CANDIDATES", "3")
	if err != nil || memoryMaxCandidates > 10 {
		return Config{}, fmt.Errorf("ZORA_MEMORY_MAX_CANDIDATES 必须在 1 到 10 之间")
	}

	cfg := Config{
		Addr:             env("ZORA_ADDR", ":8088"),
		DataDir:          env("ZORA_DATA_DIR", "./data"),
		StoreProvider:    strings.ToLower(env("ZORA_STORE_PROVIDER", "sqlite")),
		PostgresDSN:      strings.TrimSpace(os.Getenv("ZORA_POSTGRES_DSN")),
		PostgresMaxConns: postgresMaxConns,
		Provider:         strings.ToLower(env("ZORA_MODEL_PROVIDER", "mock")),
		Model:            env("ZORA_MODEL", "qwen-plus"),
		APIKey:           strings.TrimSpace(os.Getenv("ZORA_API_KEY")),
		BaseURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("ZORA_BASE_URL")), "/"),
		Instruction:      env("ZORA_SYSTEM_PROMPT", defaultInstruction),
		RequestTimeout:   timeout,
		MaxIterations:    maxIterations,

		EmbeddingProvider:   embeddingProvider,
		EmbeddingModel:      env("ZORA_EMBEDDING_MODEL", "text-embedding-v4"),
		EmbeddingAPIKey:     strings.TrimSpace(os.Getenv("ZORA_EMBEDDING_API_KEY")),
		EmbeddingBaseURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("ZORA_EMBEDDING_BASE_URL")), "/"),
		EmbeddingDimensions: embeddingDimensions,
		KnowledgeChunkSize:  chunkSize,
		KnowledgeOverlap:    chunkOverlap,

		MemoryAutoCapture:   memoryAutoCapture,
		MemoryMaxCandidates: memoryMaxCandidates,
	}

	switch cfg.StoreProvider {
	case "sqlite":
	case "postgres":
		if cfg.PostgresDSN == "" {
			return Config{}, fmt.Errorf("当 ZORA_STORE_PROVIDER=postgres 时，必须配置 ZORA_POSTGRES_DSN")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 ZORA_STORE_PROVIDER：%q，仅支持 sqlite 或 postgres", cfg.StoreProvider)
	}

	switch cfg.Provider {
	case "mock":
		// Mock 模式不依赖外部密钥，并固定模型名，便于测试结果可复现。
		cfg.Model = "zora-mock"
	case "openai":
		if cfg.APIKey == "" {
			return Config{}, fmt.Errorf("当 ZORA_MODEL_PROVIDER=openai 时，必须配置 ZORA_API_KEY")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 ZORA_MODEL_PROVIDER：%q，仅支持 mock 或 openai", cfg.Provider)
	}

	switch cfg.EmbeddingProvider {
	case "hash":
		// Hash Embedding 不需要密钥，适合本地打通 RAG 链路。
	case "openai":
		if cfg.EmbeddingAPIKey == "" {
			cfg.EmbeddingAPIKey = cfg.APIKey
		}
		if cfg.EmbeddingBaseURL == "" {
			cfg.EmbeddingBaseURL = cfg.BaseURL
		}
		if cfg.EmbeddingAPIKey == "" || cfg.EmbeddingBaseURL == "" {
			return Config{}, fmt.Errorf("当 ZORA_EMBEDDING_PROVIDER=openai 时，必须配置 Embedding API Key 和 BaseURL")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 ZORA_EMBEDDING_PROVIDER：%q，仅支持 hash 或 openai", cfg.EmbeddingProvider)
	}

	return cfg, nil
}

func positiveInt(key, fallback string) (int, error) {
	value, err := strconv.Atoi(env(key, fallback))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", key)
	}
	return value, nil
}

func nonNegativeInt(key, fallback string) (int, error) {
	value, err := strconv.Atoi(env(key, fallback))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s 不能小于 0", key)
	}
	return value, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
