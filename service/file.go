/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-12-20 14:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-04 16:18:23
 * @FilePath: /NimoOS/service/file.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service/pathlock"
	"github.com/google/uuid"
	"github.com/moby/sys/mountinfo"
	"go.uber.org/zap"
)

var FileQueue sync.Map

// renameFn is os.Rename, indirected so tests can inject an EXDEV failure
// without needing a real cross-device filesystem boundary.
var renameFn = os.Rename

// terminalNotifyFn publishes a single task's terminal notification
// immediately (see service/notify.go's pushSingleFileNotify). Indirected so
// tests can substitute a capturing stub instead of requiring a live
// MyService/MessageBus (nil in the test binary — the same reason renameFn
// above is indirected rather than called directly), and so a test can
// assert the push happens before FileQueue/opQueue are touched by
// inspecting them from inside the stub itself.
var terminalNotifyFn = pushSingleFileNotify

// opQueue replaces the former bare []string OpStrArr.
// All access goes through EnqueueOp / DequeueOp / PeekOps / ClearOps so
// concurrent handler goroutines can never race on the slice.
//
// fingerprintOf/activeFingerprints implement R3 (task dedup): they are
// updated in the same critical section as ids, so add/remove of a
// fingerprint can never be forgotten by a caller — every path that retires a
// task (service/notify.go's completion loop, route/v1/file.go's DELETE
// cleanup) goes through DequeueOp or ClearOps, and every path that admits a
// task goes through EnqueueOp.
//
// ctxOf/cancelOf implement A3 (real cancellation): every task admitted via
// EnqueueOp gets its own cancelable context, released (like the fingerprint)
// through DequeueOp/ClearOps. running names the id FileOperate is currently
// executing (the single-worker model: ExecOpFile only ever starts
// PeekOps()[0]) so CancelOp can tell "queued, never started" (just remove
// it, current pre-A3 behavior) apart from "executing right now" (cancel its
// context and let FileOperate's own goroutine perform the cleanup ->
// terminal-notify -> retire sequence; CancelOp must not race it by touching
// FileQueue/opQueue itself in that case).
var opQueue struct {
	sync.Mutex
	ids []string
	// fingerprintOf maps an in-flight task id to the fingerprint string it
	// was admitted under.
	fingerprintOf map[string]string
	// activeFingerprints is the set of fingerprints currently occupied by an
	// unfinished (queued or executing) task.
	activeFingerprints map[string]bool
	// ctxOf/cancelOf map an in-flight task id to its cancelable context.
	ctxOf    map[string]context.Context
	cancelOf map[string]context.CancelFunc
	// running is the id FileOperate is currently executing, or "" if none.
	running string
}

// fileOperateFingerprint identifies a file-operation request by its
// semantic content — type, destination, and source set — independent of
// item order, per R3's spec: type + "\x00" + to + "\x00" + sorted froms
// joined by "\x00". Two requests submitted for the exact same batch (the
// production incident's trigger: the same move resubmitted after the UI
// gave no progress feedback) fingerprint identically regardless of any
// reordering of Item.
func fileOperateFingerprint(t model.FileOperate) string {
	froms := make([]string, len(t.Item))
	for i, item := range t.Item {
		froms[i] = item.From
	}
	sort.Strings(froms)
	return t.Type + "\x00" + t.To + "\x00" + strings.Join(froms, "\x00")
}

// EnqueueOp admits a new file-operation task: it computes t's fingerprint
// and, if no other in-flight task shares it, stores t in FileQueue and
// appends id to the queue as a single atomic step (check-and-insert under
// one lock, so two concurrent identical submissions cannot both pass the
// check). If an identical fingerprint is already active, nothing is stored
// or enqueued and duplicate=true is returned — there is nothing left for the
// caller to clean up in FileQueue on rejection.
//
// isFirst reports whether the caller should kick off
// ExecOpFile/CheckFileStatus (i.e. this is the only entry in the queue); it
// is meaningless when duplicate is true.
func EnqueueOp(id string, t model.FileOperate) (isFirst bool, duplicate bool) {
	fp := fileOperateFingerprint(t)

	opQueue.Lock()
	defer opQueue.Unlock()

	if opQueue.activeFingerprints[fp] {
		return false, true
	}

	FileQueue.Store(id, t)
	opQueue.ids = append(opQueue.ids, id)
	if opQueue.fingerprintOf == nil {
		opQueue.fingerprintOf = make(map[string]string)
	}
	if opQueue.activeFingerprints == nil {
		opQueue.activeFingerprints = make(map[string]bool)
	}
	opQueue.fingerprintOf[id] = fp
	opQueue.activeFingerprints[fp] = true

	if opQueue.ctxOf == nil {
		opQueue.ctxOf = make(map[string]context.Context)
	}
	if opQueue.cancelOf == nil {
		opQueue.cancelOf = make(map[string]context.CancelFunc)
	}
	taskCtx, taskCancel := context.WithCancel(context.Background())
	opQueue.ctxOf[id] = taskCtx
	opQueue.cancelOf[id] = taskCancel

	return len(opQueue.ids) == 1, false
}

// DequeueOp removes id from the queue and releases the fingerprint it was
// admitted under (if any), so a subsequent identical submission is no
// longer rejected. Called from both task-completion cleanup
// (service/notify.go's SendFileOperateNotify) and the
// DELETE /file/operate/:id cleanup route (route/v1/file.go) — the two
// paths, per R3, that retire a single task.
func DequeueOp(id string) {
	opQueue.Lock()
	defer opQueue.Unlock()
	out := opQueue.ids[:0]
	for _, v := range opQueue.ids {
		if v != id {
			out = append(out, v)
		}
	}
	opQueue.ids = out

	if fp, ok := opQueue.fingerprintOf[id]; ok {
		delete(opQueue.fingerprintOf, id)
		delete(opQueue.activeFingerprints, fp)
	}

	delete(opQueue.ctxOf, id)
	delete(opQueue.cancelOf, id)
	if opQueue.running == id {
		opQueue.running = ""
	}
}

// PeekOps returns a snapshot copy of the current queue.
func PeekOps() []string {
	opQueue.Lock()
	defer opQueue.Unlock()
	cp := make([]string, len(opQueue.ids))
	copy(cp, opQueue.ids)
	return cp
}

// ClearOps empties the queue and releases every fingerprint. Used by the
// DELETE /file/operate/0 "clear all" route.
func ClearOps() {
	opQueue.Lock()
	defer opQueue.Unlock()
	opQueue.ids = nil
	opQueue.fingerprintOf = nil
	opQueue.activeFingerprints = nil
	opQueue.ctxOf = nil
	opQueue.cancelOf = nil
	opQueue.running = ""
}

// beginRun marks id as the task FileOperate is currently executing and
// returns its cancelable context (or context.Background() if id was never
// admitted via EnqueueOp — e.g. tests that seed FileQueue directly, or any
// caller bypassing the normal enqueue path — so cancellation simply never
// fires for it, matching pre-A3 behavior exactly).
//
// alreadyRunning guards against FileOperate somehow being invoked twice
// concurrently for the same id; the current single-worker ExecOpFile model
// shouldn't produce that, but it costs nothing to refuse it outright rather
// than double-process a task.
func beginRun(id string) (ctx context.Context, alreadyRunning bool) {
	opQueue.Lock()
	defer opQueue.Unlock()
	if opQueue.running == id {
		return nil, true
	}
	opQueue.running = id
	if c, ok := opQueue.ctxOf[id]; ok {
		return c, false
	}
	return context.Background(), false
}

// endRun clears id's running marker (set by beginRun) once FileOperate
// returns, whatever the outcome.
func endRun(id string) {
	opQueue.Lock()
	defer opQueue.Unlock()
	if opQueue.running == id {
		opQueue.running = ""
	}
}

// CancelOp requests cancellation of the in-flight (queued or executing)
// task id — the DELETE /file/operate/:id route.
//
//   - Unknown id, or a task whose Finished flag is already true: a no-op.
//     "Already true" covers both a task that has fully retired (FileQueue no
//     longer has it — DequeueOp already ran) and one that finished but is
//     still awaiting the periodic notify poller's next sweep; either way its
//     terminal notification is already sent or owned by that existing path,
//     and CancelOp must not race it or send a second one.
//   - Queued (admitted but FileOperate has not started it yet): behavior is
//     unchanged from before A3 — remove it outright. Nothing is executing,
//     so there is no cp subprocess to interrupt and no half-written
//     destination to clean up.
//   - Executing right now: cancel its context and return. CancelOp does not
//     touch FileQueue/opQueue itself in this case — FileOperate's own
//     goroutine owns that task's in-memory item state (which item is
//     mid-copy, what still needs cleaning up) and performs the mark-terminal
//     -> push-notify -> dequeue/delete/fingerprint-release sequence once it
//     observes ctx.Err() (see FileOperate).
func CancelOp(id string) {
	item, ok := FileQueue.Load(id)
	if !ok {
		return
	}
	if op, ok2 := item.(model.FileOperate); ok2 && op.Finished {
		return
	}

	opQueue.Lock()
	cancel, known := opQueue.cancelOf[id]
	running := opQueue.running == id
	opQueue.Unlock()
	if !known {
		return
	}

	cancel()
	if running {
		return
	}

	FileQueue.Delete(id)
	DequeueOp(id)
}

// CancelAllOps cancels every in-flight task's context — including the one
// currently executing, whose FileOperate goroutine will notice via ctx.Err()
// and run its own terminal-notify + retire sequence — and then clears the
// queue, matching the pre-A3 "clear all" semantics (DELETE
// /file/operate/0) for every task that has not started yet.
func CancelAllOps() {
	opQueue.Lock()
	for _, cancel := range opQueue.cancelOf {
		cancel()
	}
	opQueue.Unlock()

	FileQueue = sync.Map{}
	ClearOps()
}

type reader struct {
	ctx context.Context
	r   io.Reader
}

// NewReader wraps an io.Reader to handle context cancellation.
//
// Context state is checked BEFORE every Read.
func NewReader(ctx context.Context, r io.Reader) io.Reader {
	if r, ok := r.(*reader); ok && ctx == r.ctx {
		return r
	}
	return &reader{ctx: ctx, r: r}
}

func (r *reader) Read(p []byte) (n int, err error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

type writer struct {
	ctx context.Context
	w   io.Writer
}

type copier struct {
	writer
}

func NewWriter(ctx context.Context, w io.Writer) io.Writer {
	if w, ok := w.(*copier); ok && ctx == w.ctx {
		return w
	}
	return &copier{writer{ctx: ctx, w: w}}
}

// Write implements io.Writer, but with context awareness.
func (w *writer) Write(p []byte) (n int, err error) {
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	default:
		return w.w.Write(p)
	}
}

// opDestPath returns the destination CopyDir/move will actually create.
// filepath.Base 会剥离尾部斜杠;此前手写的 strings.LastIndex 切分在 from 以
// "/" 结尾时得到空文件名,dst 退化为 to 本身(必然存在),skip 判断永真,
// 整个复制会被静默跳过(CopyDir 都不会被调用)。
func opDestPath(from, to string) string {
	return filepath.Join(to, filepath.Base(from))
}

// isCrossDevice reports whether err is a rename failure caused by src and
// dst living on different filesystems (EXDEV). That is the ONLY rename
// failure that should fall back to a copy; any other error (permission,
// ENOENT, dst conflict, ...) must be surfaced as-is.
func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err == syscall.EXDEV
	}
	return false
}

// moveItem moves from into dst (dst is the full destination path, i.e.
// opDestPath(from, to)). It tries an atomic os.Rename first — the fast,
// same-filesystem path, instantaneous even for huge directories. Only when
// that fails with EXDEV does it fall back to the pre-existing
// copy -> verify size -> delete source path, which is the correct shape for
// a genuine cross-device move. Any other rename failure is returned
// untouched: callers must not delete dst and retry.
func moveItem(ctx context.Context, from, to, dst, style string) (usedRename bool, err error) {
	if rErr := renameFn(from, dst); rErr == nil {
		return true, nil
	} else if !isCrossDevice(rErr) {
		return false, rErr
	}

	// Cross-device: fall back to copy -> verify -> delete source.
	if err := file.CopyDir(ctx, from, to, style); err != nil {
		if ctx.Err() != nil {
			// Cancelled mid-copy. dst did not exist before this attempt (the
			// caller only reaches this cross-device branch when there was no
			// pre-existing conflict), so whatever cp/the manual fallback
			// managed to write here is wholly this attempt's own half-written
			// fragment — removing it destroys nothing the user had before.
			// The source is untouched: it is only ever removed below, after a
			// successful, size-verified copy, which did not happen.
			os.RemoveAll(dst)
		}
		return false, fmt.Errorf("copy phase failed: %w", err)
	}
	srcSize, err := file.GetFileOrDirSize(from)
	if err != nil {
		os.RemoveAll(dst)
		return false, fmt.Errorf("stat source failed: %w", err)
	}
	dstSize, err := file.GetFileOrDirSize(dst)
	if err != nil || dstSize != srcSize {
		os.RemoveAll(dst)
		return false, fmt.Errorf("destination size mismatch (src=%d dst=%d, err=%v)", srcSize, dstSize, err)
	}
	if err := os.RemoveAll(from); err != nil {
		logger.Error("move: remove source failed", zap.String("from", from), zap.Error(err))
		// Destination is a verified, complete copy; source removal failing
		// is a cleanup nuisance, not data loss — log only.
	}
	return false, nil
}

// replaceConflict implements R2's "replace" style without ever deleting
// existing data up front: the current dst is staged aside under a sibling
// temp name first, then doOp runs (expected to (re)create dst from
// scratch). The staged-aside copy is only removed once doOp has succeeded;
// on failure it is renamed straight back over dst, so a failed replace
// never loses data — the caller sees an error but both the original
// destination and (if untouched) the source remain intact.
//
// If the rollback rename (tmp -> dst) itself fails, the original data is
// left sitting at tmp instead of being lost — that path is returned as
// parkedPath (empty in every other case, including the happy path and a
// doOp failure that rolled back cleanly) so the caller can record it in the
// task state instead of it being observable only via the log line below.
func replaceConflict(dst string, doOp func() error) (parkedPath string, err error) {
	tmp := dst + ".nimoos-replacing-" + uuid.NewString()[:8]
	if err := os.Rename(dst, tmp); err != nil {
		return "", fmt.Errorf("could not stage existing destination aside: %w", err)
	}
	if err := doOp(); err != nil {
		// Roll back: clear whatever doOp may have partially written at dst,
		// then restore the original data.
		os.RemoveAll(dst)
		if rerr := os.Rename(tmp, dst); rerr != nil {
			logger.Error("replace: rollback failed, original data stuck at temp path",
				zap.String("temp", tmp), zap.String("dst", dst), zap.Error(rerr))
			return tmp, err
		}
		return "", err
	}
	os.RemoveAll(tmp)
	return "", nil
}

func FileOperate(k string) {
	list, ok := FileQueue.Load(k)
	if !ok {
		return
	}

	temp := list.(model.FileOperate)
	if temp.ProcessedSize > 0 {
		return
	}

	ctx, alreadyRunning := beginRun(k)
	if alreadyRunning {
		return
	}
	defer endRun(k)

	// Hold write locks on every source path and the destination for the
	// duration of this operation batch.
	var unlocks []func()
	for _, item := range temp.Item {
		unlocks = append(unlocks, pathlock.LockWrite(item.From))
	}
	if temp.To != "" {
		unlocks = append(unlocks, pathlock.LockWrite(temp.To))
	}
	defer func() {
		for _, u := range unlocks {
			u()
		}
	}()

	createdPaths := make([]string, 0, len(temp.Item))

itemsLoop:
	for i := 0; i < len(temp.Item); i++ {
		// Checked before starting each item: a task cancelled while item i-1
		// was in flight must never begin item i (or any item after it).
		if ctx.Err() != nil {
			break itemsLoop
		}

		v := temp.Item[i]
		if temp.Type == "move" {
			dst := opDestPath(v.From, temp.To)

			if !file.CheckNotExist(dst) {
				// Destination conflict: resolve per Style. Never delete dst
				// up front (D1) — os.Rename onto a non-empty existing
				// directory already fails with ENOTEMPTY/EEXIST on Linux,
				// which is exactly the signal that routes us here.
				switch temp.Style {
				case "skip":
					temp.Item[i].Finished = true
					continue
				case "replace", "overwrite":
					// "overwrite" is what the current NimoOS-UI actually sends
					// (FilePanel.vue's paste() defaults to style="overwrite");
					// "replace" is treated identically as the forward-looking name.
					var usedRename bool
					parkedPath, replaceErr := replaceConflict(dst, func() error {
						var opErr error
						usedRename, opErr = moveItem(ctx, v.From, temp.To, dst, temp.Style)
						return opErr
					})
					if replaceErr != nil {
						if parkedPath != "" {
							temp.Item[i].ParkedPath = parkedPath
						}
						logger.Error("move: replace failed, rolled back to original destination",
							zap.String("from", v.From), zap.String("dst", dst), zap.Error(replaceErr))
						// replaceConflict already rolled the staged-aside dst
						// back (or, if that rollback itself failed, recorded
						// ParkedPath above) regardless of why doOp failed —
						// cancellation included. Nothing further to clean up
						// here; just stop admitting new items.
						if ctx.Err() != nil {
							break itemsLoop
						}
						continue
					}
					if usedRename {
						temp.Item[i].Finished = true
						temp.Item[i].ProcessedSize = v.Size
					}
					createdPaths = append(createdPaths, dst)
					continue
				default:
					// Conservative default: an unknown/empty Style must never
					// re-introduce a deleting behavior. Treat exactly like skip.
					logger.Info("move: unrecognized conflict style, treating as skip",
						zap.String("style", temp.Style), zap.String("dst", dst))
					temp.Item[i].Finished = true
					continue
				}
			}

			usedRename, err := moveItem(ctx, v.From, temp.To, dst, temp.Style)
			if err != nil {
				logger.Error("move: failed", zap.String("from", v.From), zap.String("dst", dst), zap.Error(err))
				// moveItem already removed any half-written dst if this
				// failure was caused by cancellation (see moveItem).
				if ctx.Err() != nil {
					break itemsLoop
				}
				continue
			}
			if usedRename {
				// Rename is instantaneous — mark done now, otherwise the 3s
				// size-polling progress loop (CheckFileStatus) never sees
				// this item transition and it looks stuck.
				temp.Item[i].Finished = true
				temp.Item[i].ProcessedSize = v.Size
			}
			createdPaths = append(createdPaths, dst)

		} else if temp.Type == "copy" {
			dst := opDestPath(v.From, temp.To)

			if !file.CheckNotExist(dst) {
				switch temp.Style {
				case "skip":
					// 目的地已存在且策略为 skip:没有真正落盘,不发 media:created
					continue
				case "replace", "overwrite":
					parkedPath, replaceErr := replaceConflict(dst, func() error {
						return file.CopyDir(ctx, v.From, temp.To, temp.Style)
					})
					if replaceErr != nil {
						if parkedPath != "" {
							temp.Item[i].ParkedPath = parkedPath
						}
						logger.Error("copy: replace failed, rolled back to original destination",
							zap.String("from", v.From), zap.String("dst", dst), zap.Error(replaceErr))
						if ctx.Err() != nil {
							break itemsLoop
						}
						continue
					}
					createdPaths = append(createdPaths, dst)
					continue
				default:
					logger.Info("copy: unrecognized conflict style, treating as skip",
						zap.String("style", temp.Style), zap.String("dst", dst))
					continue
				}
			}

			if err := file.CopyDir(ctx, v.From, temp.To, temp.Style); err != nil {
				if ctx.Err() != nil {
					// dst did not exist before this attempt (no conflict was
					// found above), so it's wholly this attempt's own
					// half-written fragment — safe to remove outright.
					os.RemoveAll(dst)
					break itemsLoop
				}
				continue
			}
			createdPaths = append(createdPaths, dst)
		} else {
			continue
		}
	}

	cancelled := ctx.Err() != nil
	temp.Finished = true
	if cancelled {
		temp.Cancelled = true
	}
	FileQueue.Store(k, temp)

	if cancelled {
		// Cancellation's terminal sequence: mark terminal state (above) ->
		// push the notification -> only then retire the task (dequeue /
		// delete / release the fingerprint). This ordering is the fix for
		// the pre-A3 bug where DELETE removed the task from the queue while
		// FileOperate was still running, so the periodic notify poller
		// (service/notify.go) — which only ever looks at PeekOps() — could
		// never find it again and silently never sent a terminal
		// notification. Natural (non-cancelled) completion is unchanged: it
		// continues to rely on that same poller noticing temp.Finished.
		task, _ := buildFileNotifyTask(k, temp)
		terminalNotifyFn(task)
		FileQueue.Delete(k)
		DequeueOp(k)
		go ExecOpFile()
	}

	if len(createdPaths) > 0 {
		go PublishMediaCreated(createdPaths)
	}
}

func ExecOpFile() {
	ids := PeekOps()
	if len(ids) == 0 {
		return
	}
	go FileOperate(ids[0])
}

// file move or copy and send notify
func CheckFileStatus() {
	for {
		ids := PeekOps()
		if len(ids) == 0 {
			return
		}
		for _, v := range ids {
			var total int64 = 0
			item, ok := FileQueue.Load(v)
			if !ok {
				continue
			}
			temp := item.(model.FileOperate)
			for i := 0; i < len(temp.Item); i++ {
				if !temp.Item[i].Finished {
					size, err := file.GetFileOrDirSize(temp.To + "/" + filepath.Base(temp.Item[i].From))
					if err != nil {
						continue
					}
					temp.Item[i].ProcessedSize = size
					if size == temp.Item[i].Size {
						temp.Item[i].Finished = true
					}
					total += size
				} else {
					total += temp.Item[i].ProcessedSize
				}
			}
			temp.ProcessedSize = total
			FileQueue.Store(v, temp)
		}
		time.Sleep(time.Second * 3)
	}
}
func IsMounted(path string) bool {
	mounted, _ := mountinfo.Mounted(path)
	if mounted {
		return true
	}
	connections := MyService.Connections().GetConnectionsList()
	for _, v := range connections {
		if v.MountPoint == path {
			return true
		}
	}
	return false
}

// HasChildMounts returns true if any mount point exists at or below path.
// Docker bind-mounts AppData subdirectories into running containers; those
// active mounts make os.RemoveAll fail with EBUSY.  Call this before
// attempting to delete a directory tree to surface the problem early.
func HasChildMounts(path string) bool {
	mounts, err := mountinfo.GetMounts(func(info *mountinfo.Info) (bool, bool) {
		// skip=false means include; stop=false means keep scanning
		return false, false
	})
	if err != nil {
		return false
	}
	prefix := filepath.Clean(path)
	for _, m := range mounts {
		mp := filepath.Clean(m.Mountpoint)
		if mp == prefix || strings.HasPrefix(mp, prefix+"/") {
			return true
		}
	}
	return false
}
