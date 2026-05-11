// Package pathlock provides per-path read/write locking so that concurrent
// file operations on the same path are serialised rather than interleaved.
//
// Usage:
//
//	unlock := pathlock.LockWrite("/DATA/foo/bar.txt")
//	defer unlock()
//	// ... perform write operation ...
package pathlock

import (
	"path/filepath"
	"sync"
)

type entry struct {
	rw      sync.RWMutex
	waiters int // number of goroutines holding or waiting for this lock
}

type manager struct {
	mu    sync.Mutex
	locks map[string]*entry
}

var global = &manager{locks: make(map[string]*entry)}

func (m *manager) acquire(path string) *entry {
	path = filepath.Clean(path)
	m.mu.Lock()
	e, ok := m.locks[path]
	if !ok {
		e = &entry{}
		m.locks[path] = e
	}
	e.waiters++
	m.mu.Unlock()
	return e
}

func (m *manager) release(path string, e *entry) {
	path = filepath.Clean(path)
	m.mu.Lock()
	e.waiters--
	if e.waiters == 0 {
		delete(m.locks, path)
	}
	m.mu.Unlock()
}

// LockWrite acquires an exclusive write lock on path and returns an unlock
// function. The caller must call the returned function (typically via defer).
func LockWrite(path string) func() {
	e := global.acquire(path)
	e.rw.Lock()
	return func() {
		e.rw.Unlock()
		global.release(path, e)
	}
}

// LockRead acquires a shared read lock on path and returns an unlock function.
func LockRead(path string) func() {
	e := global.acquire(path)
	e.rw.RLock()
	return func() {
		e.rw.RUnlock()
		global.release(path, e)
	}
}
