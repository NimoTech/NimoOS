package upload

// maxThrottleEntries is the capacity cap for TouchThrottle.last. NimoOS is a
// long-running systemd process, and offsetThrottle keys entries by tus task id,
// only ever adding — without a cap it would grow unbounded over a long
// uptime. When the cap is exceeded, the whole map is wiped and rebuilt — crude
// but good enough: the worst consequence is that each still-active key gets
// "let through once extra" for a DB write right after the wipe (the next event
// is guaranteed to pass the throttle anyway), which doesn't affect correctness,
// it just resets the throttle window once.
const maxThrottleEntries = 10000

// TouchThrottle throttles by key: rejects if less than intervalSecs has passed
// since the same key was last let through.
// Only used within a single goroutine (the tus event loop); not locked.
type TouchThrottle struct {
	interval int64
	last     map[string]int64
}

func NewTouchThrottle(intervalSecs int64) *TouchThrottle {
	return &TouchThrottle{interval: intervalSecs, last: map[string]int64{}}
}

func (t *TouchThrottle) Allow(key string, now int64) bool {
	if last, ok := t.last[key]; ok && now-last < t.interval {
		return false
	}
	if _, ok := t.last[key]; !ok && len(t.last) >= maxThrottleEntries {
		t.last = map[string]int64{}
	}
	t.last[key] = now
	return true
}
