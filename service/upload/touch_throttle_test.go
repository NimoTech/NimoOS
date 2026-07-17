package upload

import (
	"fmt"
	"testing"
)

func TestTouchThrottleAllowsOncePerInterval(t *testing.T) {
	th := NewTouchThrottle(5)
	if !th.Allow("k1", 1000) {
		t.Fatal("first call should be allowed")
	}
	if th.Allow("k1", 1004) {
		t.Fatal("within interval (4s < 5s) should be throttled")
	}
	if !th.Allow("k1", 1005) {
		t.Fatal("at exactly the interval boundary should be allowed")
	}
}

func TestTouchThrottlePerKeyIndependent(t *testing.T) {
	th := NewTouchThrottle(5)
	if !th.Allow("k1", 1000) {
		t.Fatal("k1 first call should be allowed")
	}
	if !th.Allow("k2", 1001) {
		t.Fatal("k2 first call should be allowed regardless of k1's state")
	}
	if th.Allow("k1", 1002) {
		t.Fatal("k1 still within its own interval should be throttled")
	}
}

// TestTouchThrottleCapResetsOnOverflow 防长期运行内存无上限增长:常驻 systemd 进程里
// offsetThrottle 按 tus 任务 id 记 key、只增不减,塞满 maxEntries 后必须整体重置,
// 而不是无限增长。重置后新 key 仍可放行,且 map 大小变小。
func TestTouchThrottleCapResetsOnOverflow(t *testing.T) {
	th := NewTouchThrottle(5)
	for i := 0; i < maxThrottleEntries; i++ {
		th.Allow(fmt.Sprintf("k%d", i), 1000)
	}
	if len(th.last) != maxThrottleEntries {
		t.Fatalf("want map filled to %d before overflow, got %d", maxThrottleEntries, len(th.last))
	}
	if !th.Allow("new-key", 1001) {
		t.Fatal("new key should be allowed even when map was at capacity")
	}
	if len(th.last) >= maxThrottleEntries {
		t.Fatalf("map should have been reset on overflow, got len=%d", len(th.last))
	}
	if len(th.last) != 1 {
		t.Fatalf("after reset, map should contain only the new key, got len=%d", len(th.last))
	}
}
