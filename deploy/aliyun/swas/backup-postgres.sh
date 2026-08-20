#!/bin/sh
set -eu

# 单机 staging 的最低成本备份方案：每天生成 PostgreSQL 自包含归档，并保留 7 天。
# 生产环境应把归档同步到 OSS，或改用 RDS 自动备份与时间点恢复（PITR）。
project_dir="${ZORA_PROJECT_DIR:-/opt/zora/app/deploy/aliyun/swas}"
env_file="${ZORA_ENV_FILE:-/home/zora-deploy/.zora/env.staging}"
backup_dir="${ZORA_BACKUP_DIR:-/home/zora-deploy/backups/postgres}"
retention_days="${ZORA_BACKUP_RETENTION_DAYS:-7}"

umask 077
mkdir -p "$backup_dir"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
temporary_file="$backup_dir/.zora-$timestamp.dump.tmp"
backup_file="$backup_dir/zora-$timestamp.dump"

cd "$project_dir"
docker compose --env-file "$env_file" -f compose.staging.yaml exec -T postgres \
  pg_dump -U zora -d zora --format=custom --compress=6 >"$temporary_file"

# 写入最终文件前先验证归档目录，避免把截断或无效文件标记成可恢复备份。
docker compose --env-file "$env_file" -f compose.staging.yaml exec -T postgres \
  pg_restore --list <"$temporary_file" >/dev/null

mv "$temporary_file" "$backup_file"
sha256sum "$backup_file" >"$backup_file.sha256"

# 清理仅限固定备份目录和固定文件名前缀，避免误删其他数据。
find "$backup_dir" -type f -name 'zora-*.dump' -mtime "+$retention_days" -delete
find "$backup_dir" -type f -name 'zora-*.dump.sha256' -mtime "+$retention_days" -delete

echo "PostgreSQL 备份完成：$backup_file"
