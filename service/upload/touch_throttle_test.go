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

// TestTouchThrottleCapResetsOnOverflow guards against unbounded memory growth
// over a long uptime: in a long-running systemd process, offsetThrottle keys
// entries by tus task id and only ever adds them, so once maxEntries fills up
// it must reset entirely rather than growing without bound. After the reset,
// new keys can still be let through, and the map size shrinks.
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
