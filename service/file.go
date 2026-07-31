/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-12-20 14:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-04 16:18:23
 * @FilePath: /NimoOS/service/file.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
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

// beginRunHook, if non-nil, is called by FileOperate immediately before it
// calls beginRun(k) — i.e. at the exact TOCTOU window between a task being
// admitted/dispatched and its beginRun call actually claiming `running`
// and reading its context. Tests use this to deterministically land a
// CancelOp call inside that window (see
// TestCancelOp_CancelDuringDispatchRace_StillHonored) instead of racing
// goroutine scheduling with a sleep. nil (its default) in production —
// zero overhead, zero behavior change.
var beginRunHook func(id string)

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
// EnqueueOp gets its own cancelable context. Unlike ids/fingerprintOf,
// ctxOf/cancelOf are released ONLY by DequeueOp — never by
// releaseQueueSlotLocked or ClearOps, which touch ids/fingerprintOf but
// leave ctxOf/cancelOf alone on purpose (see releaseQueueSlotLocked's doc
// comment for why: a FileOperate goroutine racing toward its own beginRun
// call for a just-cancelled id must still find its context there). running
// names the id FileOperate is currently executing (the single-worker
// model: ExecOpFile only ever starts PeekOps()[0]) so CancelOp can tell
// "queued, never started" (remove it from ids/fingerprintOf right away,
// current pre-A3 behavior) apart from "executing right now" (cancel its
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

// releaseQueueSlotLocked removes id from the queue slice and releases the
// fingerprint it was admitted under. The caller must hold opQueue's lock.
//
// It deliberately leaves ctxOf/cancelOf untouched — this is the fix for a
// TOCTOU CancelOp used to have: a task's context/cancel func is created at
// EnqueueOp time, but the corresponding "go FileOperate(id)" dispatch and
// that goroutine's own beginRun call happen later, asynchronously. If
// CancelOp observed "not currently running" (opQueue.running != id) and
// responded by deleting ctxOf[id]/cancelOf[id] right then, a FileOperate
// goroutine already racing toward beginRun for that exact id would find
// nothing there and silently fall back to a fresh, never-cancelled
// context.Background() — running the task to completion, uncancellable,
// the exact production bug A3 exists to fix, reintroduced through this
// race window. Leaving ctxOf/cancelOf in place means that racing beginRun
// call still finds the (by then already cancelled, via the cancel() call
// in CancelOp) context — so the task aborts via FileOperate's itemsLoop
// ctx.Err() check instead of running unchecked.
//
// ctxOf/cancelOf for this id are released later by DequeueOp: either when
// that racing beginRun call happens and FileOperate runs its own
// cancelled-terminal sequence (which calls DequeueOp), or — the ordinary
// case of a task that really was still queued behind others and is never
// dispatched again once removed from opQueue.ids — never, which is a
// small bounded leak (one context+cancelFunc per task cancelled while
// queued) traded deliberately for correctness, not a growing-per-request
// leak: the executing-cancel and natural-completion paths still release
// ctxOf/cancelOf via DequeueOp exactly as before.
func releaseQueueSlotLocked(id string) {
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
}

// DequeueOp removes id from the queue and releases the fingerprint it was
// admitted under (if any), so a subsequent identical submission is no
// longer rejected. Called from both task-completion cleanup
// (service/notify.go's SendFileOperateNotify) and FileOperate's own
// cancelled-terminal sequence (service/file.go) — the two paths that
// retire a single task once its outcome (natural completion or
// cancellation) is fully settled.
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

// ClearOps empties the queue and releases every fingerprint. Used by
// CancelAllOps (DELETE /file/operate/0) and directly by tests that need to
// reset queue/fingerprint state between cases.
//
// Deliberately does NOT touch ctxOf/cancelOf, for the same TOCTOU reason
// documented on releaseQueueSlotLocked: CancelAllOps cancels every
// registered context before calling this, but a task whose FileOperate
// goroutine is concurrently racing toward its own beginRun call must still
// be able to find its (by then already cancelled) context afterward,
// rather than being handed a fresh context.Background() because this
// wiped the map out from under it.
func ClearOps() {
	opQueue.Lock()
	defer opQueue.Unlock()
	opQueue.ids = nil
	opQueue.fingerprintOf = nil
	opQueue.activeFingerprints = nil
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
//     destination to clean up. This branch is decided by the SAME lock
//     acquisition that reads opQueue.running (see below) — closing the
//     TOCTOU window between EnqueueOp/dispatch and that task's own
//     beginRun call: even if a FileOperate goroutine for this exact id is
//     concurrently racing toward beginRun right now, releaseQueueSlotLocked
//     leaves ctxOf/cancelOf in place, so that racing call still finds this
//     (by then already cancelled) context instead of minting a fresh one.
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
	if known && !running {
		// Decided under the same lock acquisition that read `running`, so
		// there is no window in which a concurrent beginRun call could
		// have claimed `running` right after this check without either
		// (a) having already run — in which case `running` above would
		// have been true — or (b) running later and finding ctxOf/cancelOf
		// still intact, per releaseQueueSlotLocked's contract.
		releaseQueueSlotLocked(id)
	}
	opQueue.Unlock()
	if !known {
		return
	}

	cancel()
	if running {
		return
	}

	FileQueue.Delete(id)
}

// CancelAllOps cancels every in-flight task's context — including the one
// currently executing, whose FileOperate goroutine will notice via ctx.Err()
// and run its own terminal-notify + retire sequence — and then clears the
// queue, matching the pre-A3 "clear all" semantics (DELETE
// /file/operate/0) for every task that has not started yet.
//
// Uses FileQueue.Clear() rather than reassigning the FileQueue package
// variable (FileQueue = sync.Map{}): reassignment is a plain, unsynchronized
// write to a package-level variable that every FileOperate goroutine reads
// and calls methods on concurrently (FileQueue.Store at the end of
// FileOperate, FileQueue.Load/Delete elsewhere) — racing a goroutine's read
// of the FileQueue variable itself against this write is a genuine data
// race (flagged by `go test -race`), independent of sync.Map's own internal
// synchronization of its methods. Clear() (Go 1.23+) mutates the existing
// map in place, so every concurrent Store/Load/Delete/Range on it remains
// safe.
func CancelAllOps() {
	opQueue.Lock()
	for _, cancel := range opQueue.cancelOf {
		cancel()
	}
	opQueue.Unlock()

	FileQueue.Clear()
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

// moveItem moves from into dst (the exact final destination path — for the
// ordinary conflict-free/overwrite flows this is opDestPath(from, to); for
// the "rename" (keep-both) style it is whatever de-conflicted name the
// caller already resolved, which need not share from's basename). It tries
// an atomic os.Rename first — the fast, same-filesystem path, instantaneous
// even for huge directories. Only when that fails with EXDEV does it fall
// back to the pre-existing copy -> verify size -> delete source path, which
// is the correct shape for a genuine cross-device move. Any other rename
// failure is returned untouched: callers must not delete dst and retry.
//
// The copy fallback uses file.CopyDirContents (not file.CopyDir) precisely
// because it copies to dst exactly as given, without re-deriving it from a
// parent dir + from's basename — CopyDir would recompute
// filepath.Join(to, filepath.Base(from)), which only coincides with dst in
// the no-rename case and would silently land a rename-style copy at the
// wrong (conflicting) path.
func moveItem(ctx context.Context, from, dst, style string) (usedRename bool, err error) {
	// 必须在 from 真正消失之前调用:无论走哪条分支(下面的原子 rename,还是
	// 跨设备时的 copy->校验->删源),from 最终都会不再存在于原路径上,缓存
	// key 含 mtime/size,事后无法再算出。moveItem 只在 move 类型任务里调用
	// (copy 分支源文件保留,不在这里),所以在函数入口统一 purge 一次就够。
	file.PurgeThumbCacheEntry(from)

	if rErr := renameFn(from, dst); rErr == nil {
		return true, nil
	} else if !isCrossDevice(rErr) {
		return false, rErr
	}

	// Cross-device: fall back to copy -> verify -> delete source.
	if err := file.CopyDirContents(ctx, from, dst, style); err != nil {
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

// maxRenameCandidateAttempts bounds resolveRenameTarget's TOCTOU-guard retry
// loop. Pathological external churn that keeps re-occupying every
// freshly-picked candidate name is not something worth retrying forever;
// past this many attempts we give up and let the caller skip the item
// rather than spin indefinitely. A var (not a const) so tests can shrink it
// instead of having to create hundreds of files to exercise exhaustion.
var maxRenameCandidateAttempts = 1000

// afterRenameCandidateScan is a test seam, mirroring renameFn above: it runs
// after file.GetNoDuplicateFileName picks a candidate but before the final
// existence re-check below, letting tests deterministically simulate the
// probe-to-execution race (something else claiming the exact candidate name
// in that window) instead of depending on real goroutine-scheduling luck.
// A no-op in production.
var afterRenameCandidateScan = func(candidate string) {}

// resolveRenameTarget implements the "rename" (keep-both) conflict style:
// it picks a destination path that does not collide with anything already
// on disk, using file.GetNoDuplicateFileName's existing "name(n).ext"
// numbering (e.g. "report.docx" -> "report(1).docx", or "(2)", "(3)", ...
// skipping past whichever numbers are already taken — this is the same
// convention the 2026-07 recovery-link flow already produces, so this
// keeps naming consistent across the codebase). It works identically for
// files and directories: file.Exists (which GetNoDuplicateFileName's loop
// is built on) is a plain os.Stat check with no type distinction, and
// splitting on a directory's last path segment for a "suffix" is harmless
// when that segment happens to contain no dot.
//
// GetNoDuplicateFileName itself only guarantees the name it returns was
// free at the instant it scanned — between that scan and the caller acting
// on the result, something else could claim the same name. This function
// closes that race down (it cannot eliminate it — no rename-based approach
// can without abandoning the "reuse the R1 fast path" requirement) by
// re-verifying the candidate with a fresh existence check immediately
// before handing it back, and retrying with a freshly recomputed candidate
// — always rescanning from the ORIGINAL dst, never from the stale
// candidate, so GetNoDuplicateFileName's stateless scan naturally skips
// past whatever just got claimed and lands on the next actually-free name —
// if the race is lost. This leaves the same residual check-then-act gap
// between "verified free" and "caller's rename/copy call" that the rest of
// FileOperate's conflict handling already carries (e.g. the no-conflict
// fast path's CheckNotExist(dst) followed later by the actual move/copy);
// it never widens that gap, and it never overwrites — if every attempt
// loses the race, the caller gets an error instead of a silent clobber.
func resolveRenameTarget(dst string) (string, error) {
	for i := 0; i < maxRenameCandidateAttempts; i++ {
		candidate := file.GetNoDuplicateFileName(dst)
		afterRenameCandidateScan(candidate)
		if file.CheckNotExist(candidate) {
			return candidate, nil
		}
		// Raced: something claimed `candidate` between the scan inside
		// GetNoDuplicateFileName and the check above. Loop and let a fresh
		// call (from the original dst, not the stale candidate) rescan and
		// skip past it.
	}
	return "", fmt.Errorf("could not find a non-conflicting rename target for %q after %d attempts", dst, maxRenameCandidateAttempts)
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

	if beginRunHook != nil {
		beginRunHook(k)
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

	// movedPairs records (from, actual-landed-path) for every move item that
	// truly landed on disk this run — used after the loop to keep any Samba
	// share hanging off that item's old path from becoming a dead entry in
	// the "Shared" tab. This deliberately mirrors createdPaths' append sites
	// for the move branches (NOT copy: the source still exists there, so no
	// share is left dangling) rather than reusing createdPaths itself,
	// because the two diverge on the "rename" (keep-both) conflict style:
	// the landed path there is renameDst, not dst.
	var movedPairs [][2]string

	// cancelled records whether cancellation actually prevented or
	// interrupted work — set only at the specific break sites below, all
	// of which are themselves gated on ctx.Err() != nil. It is
	// deliberately NOT derived from a single ctx.Err() sample taken after
	// the loop: if every item finishes successfully and the loop simply
	// runs out of items (no break was ever needed), a cancel() that lands
	// after the last item's work but before this function gets around to
	// checking must not retroactively relabel a fully-successful task as
	// Cancelled — the data landed regardless of that late signal.
	var cancelled bool

itemsLoop:
	for i := 0; i < len(temp.Item); i++ {
		// Checked before starting each item: a task cancelled while item i-1
		// was in flight must never begin item i (or any item after it).
		if ctx.Err() != nil {
			cancelled = true
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
						usedRename, opErr = moveItem(ctx, v.From, dst, temp.Style)
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
							cancelled = true
							break itemsLoop
						}
						continue
					}
					if usedRename {
						temp.Item[i].Finished = true
						temp.Item[i].ProcessedSize = v.Size
					}
					createdPaths = append(createdPaths, dst)
					movedPairs = append(movedPairs, [2]string{v.From, dst})
					continue
				case "rename":
					// Keep-both: land at a de-conflicted sibling name instead
					// of touching the existing dst at all.
					renameDst, resolveErr := resolveRenameTarget(dst)
					if resolveErr != nil {
						logger.Error("move: could not resolve a non-conflicting rename target, skipping item",
							zap.String("from", v.From), zap.String("dst", dst), zap.Error(resolveErr))
						continue
					}
					usedRename, err := moveItem(ctx, v.From, renameDst, temp.Style)
					if err != nil {
						logger.Error("move: rename (keep-both) failed", zap.String("from", v.From), zap.String("dst", renameDst), zap.Error(err))
						// moveItem already removed any half-written renameDst
						// if this failure was caused by cancellation (see
						// moveItem) — renameDst is a freshly-resolved,
						// previously-nonexistent path, same shape as the
						// no-conflict fast path below.
						if ctx.Err() != nil {
							cancelled = true
							break itemsLoop
						}
						continue
					}
					if usedRename {
						temp.Item[i].Finished = true
						temp.Item[i].ProcessedSize = v.Size
					}
					createdPaths = append(createdPaths, renameDst)
					movedPairs = append(movedPairs, [2]string{v.From, renameDst})
					logger.Info("move: conflict resolved via rename (keep-both)",
						zap.String("from", v.From), zap.String("original_dst", dst), zap.String("final_dst", renameDst))
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

			usedRename, err := moveItem(ctx, v.From, dst, temp.Style)
			if err != nil {
				logger.Error("move: failed", zap.String("from", v.From), zap.String("dst", dst), zap.Error(err))
				// moveItem already removed any half-written dst if this
				// failure was caused by cancellation (see moveItem).
				if ctx.Err() != nil {
					cancelled = true
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
			movedPairs = append(movedPairs, [2]string{v.From, dst})

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
							cancelled = true
							break itemsLoop
						}
						continue
					}
					createdPaths = append(createdPaths, dst)
					continue
				case "rename":
					// Keep-both: land at a de-conflicted sibling name instead
					// of touching the existing dst at all. file.CopyDirContents
					// (not file.CopyDir) is used because it copies to the
					// exact path given, rather than re-deriving the target
					// from temp.To + v.From's basename — which would put the
					// copy right back at the conflicting dst.
					renameDst, resolveErr := resolveRenameTarget(dst)
					if resolveErr != nil {
						logger.Error("copy: could not resolve a non-conflicting rename target, skipping item",
							zap.String("from", v.From), zap.String("dst", dst), zap.Error(resolveErr))
						continue
					}
					if err := file.CopyDirContents(ctx, v.From, renameDst, temp.Style); err != nil {
						logger.Error("copy: rename (keep-both) failed", zap.String("from", v.From), zap.String("dst", renameDst), zap.Error(err))
						if ctx.Err() != nil {
							// renameDst is a freshly-resolved, previously-
							// nonexistent path — whatever got written there
							// is wholly this attempt's own fragment, same as
							// the no-conflict fast path below.
							os.RemoveAll(renameDst)
							cancelled = true
							break itemsLoop
						}
						continue
					}
					createdPaths = append(createdPaths, renameDst)
					logger.Info("copy: conflict resolved via rename (keep-both)",
						zap.String("from", v.From), zap.String("original_dst", dst), zap.String("final_dst", renameDst))
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
					cancelled = true
					break itemsLoop
				}
				continue
			}
			createdPaths = append(createdPaths, dst)
		} else {
			continue
		}
	}

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

	// Keep any Samba share hanging off a moved item's OLD path from becoming
	// a dead "Shared" tab entry: rewrite it to follow the item to its actual
	// landed path. Done synchronously (unlike the PublishMediaCreated
	// fire-and-forget above) and still before the deferred unlocks release —
	// the data has already landed on disk by this point, so there is nothing
	// left to race against.
	for _, p := range movedPairs {
		if MyService == nil {
			continue
		}
		if shares := MyService.Shares(); shares != nil {
			shares.RewriteSharePathPrefix(p[0], p[1])
		}
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
