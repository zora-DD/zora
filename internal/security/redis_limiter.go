package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

var tokenBucketScript = redis.NewScript(`
local current = redis.call('TIME')
local now_ms = current[1] * 1000 + math.floor(current[2] / 1000)
local values = redis.call('HMGET', KEYS[1], 'tokens', 'updated_ms')
local tokens = tonumber(values[1])
local updated_ms = tonumber(values[2])
if tokens == nil then
  tokens = tonumber(ARGV[2])
  updated_ms = now_ms
end
local elapsed = math.max(0, now_ms - updated_ms)
tokens = math.min(tonumber(ARGV[2]), tokens + elapsed * tonumber(ARGV[1]) / 1000)
local allowed = 0
local retry_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_ms = math.ceil((1 - tokens) / tonumber(ARGV[1]) * 1000)
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated_ms', now_ms)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return {allowed, retry_ms}
`)

type RedisRateLimiter struct {
	client redis.Scripter
	rate   float64
	burst  int
	ttl    time.Duration
	prefix string
}

func NewRedisRateLimiter(client redis.Scripter, rate float64, burst int, prefix string) (*RedisRateLimiter, error) {
	if client == nil || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || burst < 1 {
		return nil, fmt.Errorf("Redis 限流器配置无效")
	}
	if prefix == "" {
		prefix = "zora:ratelimit:"
	}
	// 无访问后保留两个完整填桶周期，避免留下无过期的客户端状态。
	ttl := time.Duration(math.Ceil(float64(burst)/rate*2)) * time.Second
	if ttl < time.Minute {
		ttl = time.Minute
	}
	return &RedisRateLimiter{client: client, rate: rate, burst: burst, ttl: ttl, prefix: prefix}, nil
}

func (l *RedisRateLimiter) Allow(ctx context.Context, key string, _ time.Time) (bool, time.Duration, error) {
	digest := sha256.Sum256([]byte(key))
	result, err := tokenBucketScript.Run(ctx, l.client,
		[]string{l.prefix + hex.EncodeToString(digest[:])}, l.rate, l.burst, l.ttl.Milliseconds()).Slice()
	if err != nil {
		return false, 0, err
	}
	if len(result) != 2 {
		return false, 0, fmt.Errorf("Redis 限流脚本返回了无效结果")
	}
	allowed, ok := result[0].(int64)
	if !ok {
		return false, 0, fmt.Errorf("Redis 限流脚本 allowed 类型无效")
	}
	retryMS, ok := result[1].(int64)
	if !ok {
		return false, 0, fmt.Errorf("Redis 限流脚本 retry 类型无效")
	}
	return allowed == 1, time.Duration(retryMS) * time.Millisecond, nil
}
