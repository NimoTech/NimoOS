package upload

import "testing"

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
