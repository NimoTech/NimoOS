package upload

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"go.uber.org/zap"
)

const (
	// BatchIdleInterruptSeconds: how long an active batch can go without
	// progress before it's auto-judged interrupted (a fallback for a lost
	// window-close signal).
	BatchIdleInterruptSeconds = int64(120)
	// BatchStagingGraceSeconds: how long to wait after a timeout-interrupt
	// before clearing staging — during this window, a tus session's automatic
	// reconnect (up to ~4 minutes) can still resume from the offset; clearing
	// too early would break the whole file transfer.
	BatchStagingGraceSeconds = int64(600)
	// BatchSweepIntervalSeconds: the sweep interval.
	BatchSweepIntervalSeconds = int64(30)
)

// listMountPoints enumerates current mount points (reads /proc/self/mounts); a
// package-level var to make it easy to inject in tests.
var listMountPoints = func() []string {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			// Spaces in mounts are escaped as \040 (e.g. volume labels containing spaces).
			out = append(out, strings.ReplaceAll(f[1], `\040`, " "))
		}
	}
	return out
}

// targetOrphaned determines whether a batch's target directory is
// "definitively missing": the target can't be stat'd, and it's covered by some
// non-root mount point (i.e. the volume it's on is actually mounted). When the
// volume isn't mounted (USB unplugged / RAID not yet assembled / slow cold-boot
// enumeration), the target also can't be stat'd, but the mount point that would
// cover it is also absent — so it's not judged dead; once the volume comes
// back, the badge comes back too. Root "/" doesn't count as coverage: when the
// volume is absent, the path falls back to the root filesystem's view (e.g. a
// leftover empty dir at /media/X), and there's no way to tell "deleted" from
// "not mounted" based on the root mount alone. The cost is that data placed
// directly on the root filesystem doesn't get the benefit of this orphan
// fallback (NimoOS data volumes are all independently mounted, so this isn't
// actually affected in practice).
func targetOrphaned(target string, mountPoints []string) bool {
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return false // exists, or some other error like a permission issue: never judged orphaned
	}
	clean := filepath.Clean(target)
	for _, m := range mountPoints {
		mm := filepath.Clean(m)
		if mm == "/" {
			continue
		}
		if clean == mm || strings.HasPrefix(clean, mm+"/") {
			return true
		}
	}
	return false
}

// cancelUnfinished terminates a batch's unfinished tus tasks and clears leftover files from each staging dir.
func cancelUnfinished(tasks *TaskStore, batchID string, stagingDirs []string, now int64) error {
	list, err := tasks.ListUnfinishedByBatch(batchID)
	if err != nil {
		return err
	}
	expires := now + common.UploadCanceledTTLSeconds
	for _, t := range list {
		_, _ = commonUpload.Cancel(tasks, t.ID, expires)
		for _, dir := range stagingDirs {
			os.Remove(filepath.Join(dir, t.ID))         //nolint:errcheck
			os.Remove(filepath.Join(dir, t.ID+".info")) //nolint:errcheck
		}
	}
	return nil
}

// SweepBatches performs one round of batch scanning:
//  1. active and (now - last_progress_at) > 120s → interrupted (badge appears)
//  2. interrupted, staging not yet cleared, and (now - interrupted_at) > 600s → terminate tasks + clear staging
//  3. orphan fallback: an interrupted batch whose target directory has been
//     deleted (targetOrphaned) → auto-abandoned
//  4. terminal state (completed/abandoned) → delete the batch and its items
//
// interrupted batches don't auto-expire: the badge stays up until the user
// manually abandons it or the resumed upload completes. The only exception is
// the orphan case in step 3 — the badge only ever attaches to entries that
// actually exist in a listing, so once the target directory is gone, the badge
// never shows and the user has no manual entry point to clear it; leaving it
// unhandled would strand it forever (real incident: after /media/RAID_0/homer
// was deleted, 4 batches with 7k+ items were left with no way to clear them).
//
// stagingDirs is the set of all currently in-use staging directories (see
// StagingDirs): task IDs may now carry a volume prefix and live in staging
// directories on different volumes, so a single directory can no longer be
// assumed — cleanup tries each one in turn (os.Remove fails silently if it
// doesn't exist, which is fine to ignore).
func SweepBatches(batches *BatchStore, tasks *TaskStore, stagingDirs []string, now int64) error {
	actives, err := batches.ListByStatus(BatchStatusActive)
	if err != nil {
		return err
	}
	for _, b := range actives {
		last := b.LastProgressAt
		if last == 0 {
			last = b.CreatedAt
		}
		if now-last > BatchIdleInterruptSeconds {
			if err := batches.SetInterrupted(b.ID, now); err != nil {
				return err
			}
		}
	}
	interrupted, err := batches.ListByStatus(BatchStatusInterrupted)
	if err != nil {
		return err
	}
	for _, b := range interrupted {
		if b.StagingCleaned || now-b.InterruptedAt <= BatchStagingGraceSeconds {
			continue
		}
		if err := cancelUnfinished(tasks, b.ID, stagingDirs, now); err != nil {
			return err
		}
		if err := batches.MarkStagingCleaned(b.ID); err != nil {
			return err
		}
	}
	mounts := listMountPoints()
	for _, b := range interrupted {
		if !targetOrphaned(b.TargetPath, mounts) {
			continue
		}
		if err := cancelUnfinished(tasks, b.ID, stagingDirs, now); err != nil {
			return err
		}
		if err := batches.SetStatus(b.ID, BatchStatusAbandoned); err != nil {
			return err
		}
	}
	if _, err := batches.DeleteTerminal(); err != nil {
		return err
	}
	return nil
}

// StartBatchSweeper scans at a fixed interval in its own goroutine (coexists
// with task GC, with a different responsibility: task GC manages the
// o_upload_tasks lifecycle, this sweeper manages the batch state machine and
// delayed cleanup).
// stagingDirsFn re-enumerates the currently in-use staging directories every
// round (see StagingDirs), rather than being passed in once and fixed — this
// way, once a volume mounted during runtime produces a staging directory, the
// next sweep round will pick it up.
func StartBatchSweeper(batches *BatchStore, tasks *TaskStore, stagingDirsFn func() []string) {
	go func() {
		ticker := time.NewTicker(time.Duration(BatchSweepIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := SweepBatches(batches, tasks, stagingDirsFn(), time.Now().Unix()); err != nil {
				logger.Error("upload batch sweep failed", zap.Error(err))
			}
		}
	}()
}
