#!/usr/bin/env bash
set -euo pipefail

# 用法：BASE_URL=https://zora.example.com ./scripts/verify-staging.sh
# 登录态验证可额外传入 AUTH_COOKIE_JAR=/安全临时目录/cookies.txt。
base_url="${BASE_URL:?必须设置 BASE_URL，例如 https://zora.example.com}"
base_url="${base_url%/}"
cookie_jar="${AUTH_COOKIE_JAR:-}"
metrics_token="${METRICS_TOKEN:-}"
bad_origin="${DISALLOWED_ORIGIN:-https://evil.invalid}"

status() {
  curl -sS -o /dev/null -w '%{http_code}' "$@"
}

expect() {
  local expected="$1"
  local actual="$2"
  local label="$3"
  if [[ "$actual" != "$expected" ]]; then
    printf '失败：%s，期望 HTTP %s，实际 HTTP %s\n' "$label" "$expected" "$actual" >&2
    exit 1
  fi
  printf '通过：%s（HTTP %s）\n' "$label" "$actual"
}

expect 200 "$(status "$base_url/api/health")" "liveness"
expect 200 "$(status "$base_url/api/ready")" "PostgreSQL/Redis readiness"
expect 401 "$(status "$base_url/api/conversations")" "未登录访问业务 API"
expect 403 "$(status -H "Origin: $bad_origin" "$base_url/api/auth/login")" "非法 Origin/CORS"
expect 401 "$(status "$base_url/metrics")" "无监控凭据访问 /metrics"

if [[ -n "$metrics_token" ]]; then
  expect 200 "$(status -H "Authorization: Bearer $metrics_token" "$base_url/metrics")" "有效监控凭据"
fi

if [[ -n "$cookie_jar" ]]; then
  if [[ ! -f "$cookie_jar" ]]; then
    printf '失败：AUTH_COOKIE_JAR 不存在：%s\n' "$cookie_jar" >&2
    exit 1
  fi
  expect 403 "$(status -b "$cookie_jar" -H 'Content-Type: application/json' -d '{"title":"无 CSRF"}' "$base_url/api/conversations")" "登录态缺少 CSRF Token"

  csrf_json="$(curl -sS -b "$cookie_jar" -c "$cookie_jar" "$base_url/api/security/csrf")"
  csrf_token="$(printf '%s' "$csrf_json" | jq -er '.token')"
  expect 201 "$(status -b "$cookie_jar" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf_token" -d '{"title":"staging 上线验证"}' "$base_url/api/conversations")" "登录态携带有效 CSRF Token"
else
  printf '跳过：未设置 AUTH_COOKIE_JAR，未执行登录态 CSRF 写请求。\n'
fi

printf '基础 staging 负向验证全部通过。429、UTC 配额重置、Secret 轮换和 Pod 删除验证请按部署文档执行。\n'
