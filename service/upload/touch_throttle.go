package upload

// maxThrottleEntries 是 TouchThrottle.last 的容量上限。NimoOS 是常驻 systemd 进程,
// offsetThrottle 按 tus 任务 id 记 key、只增不减,不设上限会随长期运行无限增长。
// 超过上限时整体清空重建——粗暴但足够:最坏后果只是清空后每个仍活跃的 key 会
// "多放行一次"写库(下一次事件必然通过节流),不影响正确性,只是节流窗口重置了一次。
const maxThrottleEntries = 10000

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
	if _, ok := t.last[key]; !ok && len(t.last) >= maxThrottleEntries {
		t.last = map[string]int64{}
	}
	t.last[key] = now
	return true
}
