#!/bin/sh
set -eu

# Redis 密码来自只读文件，不写入 Compose 文件或容器环境变量。
password="$(cat /run/zora-secrets/redis_password)"
umask 077
cat > /tmp/zora-redis.conf <<EOF
bind 0.0.0.0
protected-mode yes
appendonly yes
dir /data
maxmemory 64mb
maxmemory-policy allkeys-lru
requirepass ${password}
EOF
unset password

# 复用官方入口，让它在启动 Redis 前降权到 redis 用户。
exec /usr/local/bin/docker-entrypoint.sh redis-server /tmp/zora-redis.conf
