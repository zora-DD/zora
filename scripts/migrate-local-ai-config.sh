#!/usr/bin/env bash
set -euo pipefail

# 将旧 .env.local 中的 AI 元数据和直接 Key 迁移到被 Git 忽略的安全文件。
# 脚本只输出路径和状态，禁止打印配置正文或 Secret。
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${repo_dir}/.env.local"
runtime_dir="${repo_dir}/.runtime"
secret_dir="${repo_dir}/.secrets"
ai_config_file="${runtime_dir}/ai-config.json"

if [[ ! -f "${env_file}" ]]; then
  printf '缺少 %s，无法迁移。\n' "${env_file}" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  printf '迁移需要 jq 生成并校验 JSON。\n' >&2
  exit 1
fi

umask 077
install -d -m 0700 "${runtime_dir}" "${secret_dir}"
if [[ ! -f "${secret_dir}/env.local.before-ai-config" ]]; then
  install -m 0600 "${env_file}" "${secret_dir}/env.local.before-ai-config"
fi

# .env.local 本身就是 Makefile 的可信 Shell 配置来源；加载后只在当前进程内迁移，不回显值。
set -a
# shellcheck disable=SC1090
source "${env_file}"
set +a

llm_key="${DEEPSEEK_API_KEY:-${ZORA_LLM_API_KEY:-${ZORA_API_KEY:-}}}"
embedding_key="${ZORA_EMBEDDING_API_KEY:-}"
if [[ -z "${llm_key}" ]]; then
  printf '旧配置中没有可迁移的 LLM API Key。\n' >&2
  exit 1
fi
if [[ -z "${embedding_key}" ]]; then
  printf '旧配置中没有可迁移的 Embedding API Key。\n' >&2
  exit 1
fi

models_json="${ZORA_MODELS_JSON:-}"
if [[ -z "${models_json}" ]]; then
  models_json="$(jq -cn \
    --arg id "default" \
    --arg name "${ZORA_MODEL:-default}" \
    --arg provider "${ZORA_MODEL_PROVIDER:-mock}" \
    --arg model "${ZORA_MODEL:-}" \
    --arg baseURL "${ZORA_BASE_URL:-}" \
    '[{id:$id,name:$name,provider:$provider,model:$model,base_url:$baseURL}]')"
fi
if ! jq -e 'type == "array" and length > 0 and length <= 20' <<<"${models_json}" >/dev/null; then
  printf '旧 ZORA_MODELS_JSON 不是包含 1–20 项的 JSON 数组。\n' >&2
  exit 1
fi

embedding_provider="${ZORA_EMBEDDING_PROVIDER:-hash}"
embedding_model="${ZORA_EMBEDDING_MODEL:-hash-embedding-v1}"
embedding_base_url="${ZORA_EMBEDDING_BASE_URL:-}"
embedding_dimensions="${ZORA_EMBEDDING_DIMENSIONS:-384}"
if [[ ! "${embedding_dimensions}" =~ ^[0-9]+$ ]] || (( embedding_dimensions < 64 )); then
  printf '旧 ZORA_EMBEDDING_DIMENSIONS 必须是至少 64 的整数。\n' >&2
  exit 1
fi

# 所有 OpenAI-compatible LLM Profile 统一引用 ZORA_LLM_API_KEY；真实值单独写入 _FILE。
jq -n \
  --argjson models "${models_json}" \
  --arg defaultModelID "${ZORA_DEFAULT_MODEL_ID:-}" \
  --arg embeddingProvider "${embedding_provider}" \
  --arg embeddingModel "${embedding_model}" \
  --arg embeddingBaseURL "${embedding_base_url}" \
  --argjson embeddingDimensions "${embedding_dimensions}" \
  '{
    version: 1,
    default_model_id: (if $defaultModelID == "" then $models[0].id else $defaultModelID end),
    models: ($models | map(
      del(.api_key) |
      if ((.provider // "") | ascii_downcase) == "openai"
      then .api_key_env = "ZORA_LLM_API_KEY"
      else del(.api_key_env)
      end
    )),
    embedding: (
      {
        provider: $embeddingProvider,
        model: $embeddingModel,
        base_url: $embeddingBaseURL,
        dimensions: $embeddingDimensions
      } |
      if ($embeddingProvider | ascii_downcase) == "openai"
      then .api_key_env = "ZORA_EMBEDDING_API_KEY"
      else .
      end
    )
  }' > "${ai_config_file}"

printf '%s' "${llm_key}" > "${secret_dir}/llm_api_key"
printf '%s' "${embedding_key}" > "${secret_dir}/embedding_api_key"
chmod 0600 "${ai_config_file}" "${secret_dir}/llm_api_key" "${secret_dir}/embedding_api_key"

temp_env="$(mktemp "${repo_dir}/.env.local.migrate.XXXXXX")"
cleanup() {
  rm -f -- "${temp_env}"
}
trap cleanup EXIT

awk -v single_quote="'" '
  BEGIN {
    split("ZORA_AI_CONFIG_FILE ZORA_MODEL_PROVIDER ZORA_MODEL ZORA_API_KEY ZORA_API_KEY_FILE ZORA_BASE_URL DEEPSEEK_API_KEY DEEPSEEK_API_KEY_FILE ZORA_LLM_API_KEY ZORA_LLM_API_KEY_FILE ZORA_DEFAULT_MODEL_ID ZORA_MODELS_JSON ZORA_EMBEDDING_PROVIDER ZORA_EMBEDDING_MODEL ZORA_EMBEDDING_API_KEY ZORA_EMBEDDING_API_KEY_FILE ZORA_EMBEDDING_BASE_URL ZORA_EMBEDDING_DIMENSIONS", names, " ")
    for (i in names) removed[names[i]] = 1
  }
  skip_quote != "" {
    if (substr($0, length($0), 1) == skip_quote) skip_quote = ""
    next
  }
  /^[A-Za-z_][A-Za-z0-9_]*=/ {
    key = $0
    sub(/=.*/, "", key)
    if (removed[key]) {
      value = $0
      sub(/^[^=]*=/, "", value)
      first = substr(value, 1, 1)
      last = substr(value, length(value), 1)
      if ((first == "\"" || first == single_quote) && last != first) skip_quote = first
      next
    }
  }
  { print }
' "${env_file}" > "${temp_env}"

{
  printf '\n# AI 元数据与 Key 已迁移到 Git 忽略的安全文件。\n'
  printf 'ZORA_AI_CONFIG_FILE=.runtime/ai-config.json\n'
  printf 'ZORA_LLM_API_KEY_FILE=.secrets/llm_api_key\n'
  printf 'ZORA_EMBEDDING_API_KEY_FILE=.secrets/embedding_api_key\n'
} >> "${temp_env}"

install -m 0600 "${temp_env}" "${env_file}"

unset llm_key embedding_key models_json DEEPSEEK_API_KEY ZORA_LLM_API_KEY ZORA_API_KEY ZORA_EMBEDDING_API_KEY
printf '本地 AI 配置迁移完成：%s、%s。原配置安全备份位于 .secrets/env.local.before-ai-config。\n' \
  "${ai_config_file}" "${secret_dir}"
