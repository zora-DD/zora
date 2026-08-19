#!/usr/bin/env bash
set -euo pipefail

# 把仓库外编辑好的 AI 配置安装成仅 Zora 容器用户可读的运行时文件。
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_file="${1:-}"
target_dir="${script_dir}/.runtime"
target_file="${target_dir}/ai-config.json"

if [[ -z "${source_file}" || ! -f "${source_file}" ]]; then
  printf '用法：%s /仓库外/ai-config.json\n' "$0" >&2
  printf '可先复制 ai-config.template.json 到 /tmp，替换全部 CHANGE_ME 后再安装。\n' >&2
  exit 1
fi
if grep -q 'CHANGE_ME' "${source_file}"; then
  printf '源配置仍包含 CHANGE_ME，拒绝安装。\n' >&2
  exit 1
fi
if [[ "${EUID}" -ne 0 ]]; then
  exec sudo -- "$0" "${source_file}"
fi

install -d -m 0700 -o root -g root "${target_dir}"
install -m 0400 -o 10001 -g 10001 "${source_file}" "${target_file}"
printf 'AI 运行时配置已安全安装到 %s；修改后需要重启或滚动重启 Zora。\n' "${target_file}"
