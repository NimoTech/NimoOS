package upload

// TouchThrottle 按 key 节流：同一 key 距上次放行不足 intervalSecs 则拒绝。
// 仅在单 goroutine（tus 事件循环）内使用，不加锁。
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
	t.last[key] = now
	return true
}
