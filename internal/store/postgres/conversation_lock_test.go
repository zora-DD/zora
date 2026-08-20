package postgres

import "testing"

func TestConversationLockKeyIsStableAndTenantScoped(t *testing.T) {
	first := conversationLockKey("tenant-a", "conversation-1")
	if first != conversationLockKey("tenant-a", "conversation-1") {
		t.Fatal("相同租户和会话必须生成稳定的执行锁键")
	}
	if first == conversationLockKey("tenant-b", "conversation-1") {
		t.Fatal("不同租户的同名会话不能共享执行锁键")
	}
	if conversationLockKey("ab", "c") == conversationLockKey("a", "bc") {
		t.Fatal("租户与会话边界不能产生相同执行锁键")
	}
}
