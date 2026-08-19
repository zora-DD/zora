#!/usr/bin/env bash
set -euo pipefail

# 该脚本在轻量应用服务器上运行，为不同容器创建最小权限 Secret 文件。
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
secret_dir="${script_dir}/.secrets"

if [[ "${EUID}" -ne 0 ]]; then
  printf '需要 root 权限设置 Secret 文件属主，正在请求 sudo。\n'
  exec sudo -- "$0" "$@"
fi

install -d -m 0700 -o root -g root "${secret_dir}"

random_hex() {
  openssl rand -hex "$1"
}

write_root_secret() {
  local name="$1"
  local value="$2"
  local path="${secret_dir}/${name}"
  install -m 0400 -o root -g root /dev/null "${path}"
  printf '%s' "${value}" > "${path}"
}

write_zora_secret() {
  local name="$1"
  local value="$2"
  local path="${secret_dir}/${name}"
  # Zora 镜像固定使用 UID/GID 10001；仅应用进程可读取这些文件。
  install -m 0400 -o 10001 -g 10001 /dev/null "${path}"
  printf '%s' "${value}" > "${path}"
}

read_secret() {
  local prompt="$1"
  local value
  read -r -s -p "${prompt}: " value
  printf '\n' >&2
  if [[ -z "${value}" ]]; then
    printf '输入不能为空。\n' >&2
    exit 1
  fi
  printf '%s' "${value}"
}

postgres_password="$(random_hex 24)"
redis_password="$(random_hex 24)"
metrics_token="$(random_hex 32)"
github_secret="$(read_secret '请输入 staging GitHub OAuth Client Secret')"
llm_key="$(read_secret '请输入 LLM API Key')"
embedding_key="$(read_secret '请输入 Embedding API Key')"

write_root_secret postgres_password "${postgres_password}"
write_root_secret redis_password "${redis_password}"
write_zora_secret postgres_dsn "postgres://zora:${postgres_password}@postgres:5432/zora?sslmode=disable"
write_zora_secret redis_url "redis://:${redis_password}@redis:6379/0"
write_zora_secret metrics_token "${metrics_token}"
write_zora_secret github_oauth_client_secret "${github_secret}"
write_zora_secret llm_api_key "${llm_key}"
write_zora_secret embedding_api_key "${embedding_key}"

unset postgres_password redis_password metrics_token github_secret llm_key embedding_key
printf 'Secret 文件已写入 %s；不要复制、打印或提交该目录。\n' "${secret_dir}"
