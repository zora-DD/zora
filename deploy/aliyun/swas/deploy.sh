#!/usr/bin/env bash
set -euo pipefail

# 从固定目录执行，避免调用方当前目录不同导致挂载路径错位。
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}"

# AI 配置与 Secret 由固定容器 UID 持有，部署前需要 root 校验并交给 Docker 挂载。
if [[ "${EUID}" -ne 0 ]]; then
  exec sudo -- "$0" "$@"
fi

env_file="${script_dir}/.env.staging"
secret_dir="${script_dir}/.secrets"
ai_config_file="${script_dir}/.runtime/ai-config.json"

if [[ ! -f "${env_file}" ]]; then
  printf '缺少 %s，请先复制 .env.staging.example 并替换 CHANGE_ME。\n' "${env_file}" >&2
  exit 1
fi

if grep -q 'CHANGE_ME' "${env_file}"; then
  printf '.env.staging 中仍有 CHANGE_ME，拒绝部署。\n' >&2
  exit 1
fi

if [[ ! -s "${ai_config_file}" ]]; then
  printf '缺少 %s，请先根据 ai-config.template.json 创建临时配置，再运行 install-ai-config.sh。\n' "${ai_config_file}" >&2
  exit 1
fi
if grep -q 'CHANGE_ME' "${ai_config_file}"; then
  printf '.runtime/ai-config.json 中仍有 CHANGE_ME，拒绝部署。\n' >&2
  exit 1
fi
if [[ "$(stat -c '%a:%u:%g' "${ai_config_file}")" != "400:10001:10001" ]]; then
  printf 'AI 配置权限或属主不正确，请重新运行 install-ai-config.sh。\n' >&2
  exit 1
fi

required_secrets=(
  postgres_password postgres_dsn redis_password redis_url metrics_token
  github_oauth_client_secret llm_api_key embedding_api_key
)
for name in "${required_secrets[@]}"; do
  if [[ ! -s "${secret_dir}/${name}" ]]; then
    printf '缺少 Secret：%s，请先运行 prepare-secrets.sh。\n' "${name}" >&2
    exit 1
  fi
done

if [[ "$(stat -c '%a' "${env_file}")" != "600" ]]; then
  printf '.env.staging 权限必须是 0600，请执行 chmod 600 %s。\n' "${env_file}" >&2
  exit 1
fi

docker compose --env-file "${env_file}" -f compose.staging.yaml config --quiet
docker compose --env-file "${env_file}" -f compose.staging.yaml pull
docker compose --env-file "${env_file}" -f compose.staging.yaml up -d --remove-orphans
docker compose --env-file "${env_file}" -f compose.staging.yaml ps

printf '容器已启动。DNS 生效后执行：BASE_URL=https://你的域名 ../../../scripts/verify-staging.sh\n'
