package security

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisRateLimiterUsesHashedKeyAndParsesAtomicResult(t *testing.T) {
	client := &fakeScripter{result: []any{int64(1), int64(0)}}
	limiter, err := NewRedisRateLimiter(client, 2, 3, "test:limit:")
	if err != nil {
		t.Fatal(err)
	}
	allowed, retry, err := limiter.Allow(context.Background(), "203.0.113.8", time.Now())
	if err != nil || !allowed || retry != 0 {
		t.Fatalf("allow = %v, retry = %s, err = %v", allowed, retry, err)
	}
	if len(client.keys) != 1 || !strings.HasPrefix(client.keys[0], "test:limit:") || strings.Contains(client.keys[0], "203.0.113.8") {
		t.Fatalf("Redis 限流 Key 未正确哈希：%v", client.keys)
	}
	if len(client.args) != 3 || client.args[0] != float64(2) || client.args[1] != 3 {
		t.Fatalf("Redis Lua 参数错误：%#v", client.args)
	}

	client.result = []any{int64(0), int64(500)}
	allowed, retry, err = limiter.Allow(context.Background(), "203.0.113.8", time.Now())
	if err != nil || allowed || retry != 500*time.Millisecond {
		t.Fatalf("limited = %v, retry = %s, err = %v", allowed, retry, err)
	}
}

type fakeScripter struct {
	result []any
	keys   []string
	args   []any
}

func (f *fakeScripter) command(ctx context.Context, keys []string, args ...any) *redis.Cmd {
	f.keys = append([]string(nil), keys...)
	f.args = append([]any(nil), args...)
	command := redis.NewCmd(ctx)
	command.SetVal(f.result)
	return command
}

func (f *fakeScripter) Eval(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.command(ctx, keys, args...)
}
func (f *fakeScripter) EvalSha(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.command(ctx, keys, args...)
}
func (f *fakeScripter) EvalRO(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.command(ctx, keys, args...)
}
func (f *fakeScripter) EvalShaRO(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.command(ctx, keys, args...)
}
func (f *fakeScripter) ScriptExists(ctx context.Context, _ ...string) *redis.BoolSliceCmd {
	command := redis.NewBoolSliceCmd(ctx)
	command.SetVal([]bool{true})
	return command
}
func (f *fakeScripter) ScriptLoad(ctx context.Context, _ string) *redis.StringCmd {
	command := redis.NewStringCmd(ctx)
	command.SetVal("sha")
	return command
}
