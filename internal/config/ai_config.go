package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhiruo/zora/internal/secrets"
)

const aiConfigVersion = 1

// aiConfigFile 保存可在部署时替换的 AI 运行参数。真实 Key 只能通过 api_key_env 引用，
// ResolveEnvironment 会继续优先读取同名的 _FILE Secret 文件。
type aiConfigFile struct {
	Version        int                  `json:"version"`
	DefaultModelID string               `json:"default_model_id"`
	Models         []modelProfileInput  `json:"models"`
	Embedding      embeddingConfigInput `json:"embedding"`
}

type embeddingConfigInput struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	APIKey     string `json:"api_key"`
	APIKeyEnv  string `json:"api_key_env"`
	BaseURL    string `json:"base_url"`
	Dimensions int    `json:"dimensions"`
}

// applyAIConfigFile 从仓库外的安全文件加载模型与 Embedding 配置。
// 不做进程内热更新：LLM Runtime 在启动时完成工具装配，而 Embedding 变更还涉及索引一致性。
func applyAIConfigFile(cfg *Config, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("ZORA_AI_CONFIG_FILE 不能为空")
	}
	if err := rejectAIConfigEnvironmentConflicts(); err != nil {
		return err
	}
	raw, err := secrets.ReadSecureFile(path, "ZORA_AI_CONFIG_FILE")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input aiConfigFile
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("ZORA_AI_CONFIG_FILE 必须是合法且字段受支持的 JSON：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("ZORA_AI_CONFIG_FILE 只能包含一个 JSON 对象")
	}
	if input.Version != aiConfigVersion {
		return fmt.Errorf("ZORA_AI_CONFIG_FILE version=%d 不受支持，当前仅支持 %d", input.Version, aiConfigVersion)
	}
	modelsJSON, err := json.Marshal(input.Models)
	if err != nil {
		return fmt.Errorf("序列化 AI 模型配置失败：%w", err)
	}
	if err := configureModels(cfg, string(modelsJSON), input.DefaultModelID); err != nil {
		return fmt.Errorf("AI 配置中的 models 无效：%w", err)
	}
	return applyEmbeddingConfig(cfg, input.Embedding)
}

func applyEmbeddingConfig(cfg *Config, input embeddingConfigInput) error {
	if strings.TrimSpace(input.APIKey) != "" {
		return fmt.Errorf("AI 配置中的 embedding 禁止写 api_key，请改用 api_key_env")
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	model := strings.TrimSpace(input.Model)
	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	dimensions := input.Dimensions
	switch provider {
	case "hash":
		if dimensions == 0 {
			dimensions = 384
		}
		if dimensions < 64 {
			return fmt.Errorf("AI 配置中的 embedding.dimensions 至少为 64")
		}
		if model == "" {
			model = "hash-embedding-v1"
		}
		cfg.EmbeddingAPIKey = ""
	case "openai":
		if model == "" || baseURL == "" || dimensions < 64 {
			return fmt.Errorf("AI 配置中的 openai embedding 必须填写 model、base_url 和不小于 64 的 dimensions")
		}
		apiKeyEnv := strings.TrimSpace(input.APIKeyEnv)
		if !validEnvironmentName(apiKeyEnv) {
			return fmt.Errorf("AI 配置中的 embedding.api_key_env=%q 无效", apiKeyEnv)
		}
		apiKey, err := secrets.ResolveEnvironment(apiKeyEnv)
		if err != nil {
			return fmt.Errorf("AI 配置中的 Embedding API Key 加载失败：%w", err)
		}
		if apiKey == "" {
			return fmt.Errorf("AI 配置中的 embedding 需要通过环境变量 %s 或 %s_FILE 提供 API Key", apiKeyEnv, apiKeyEnv)
		}
		cfg.EmbeddingAPIKey = apiKey
	default:
		return fmt.Errorf("AI 配置中的 embedding.provider=%q 不受支持，仅支持 hash 或 openai", provider)
	}
	cfg.EmbeddingProvider = provider
	cfg.EmbeddingModel = model
	cfg.EmbeddingBaseURL = baseURL
	cfg.EmbeddingDimensions = dimensions
	return nil
}

func rejectAIConfigEnvironmentConflicts() error {
	// 只允许一种配置来源，避免文件和环境变量组合后得到无法审计的最终配置。
	for _, key := range []string{
		"ZORA_MODEL_PROVIDER", "ZORA_MODEL", "ZORA_BASE_URL", "ZORA_MODELS_JSON", "ZORA_DEFAULT_MODEL_ID",
		"ZORA_EMBEDDING_PROVIDER", "ZORA_EMBEDDING_MODEL", "ZORA_EMBEDDING_BASE_URL", "ZORA_EMBEDDING_DIMENSIONS",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Errorf("配置 ZORA_AI_CONFIG_FILE 时禁止同时设置 %s", key)
		}
	}
	return nil
}
