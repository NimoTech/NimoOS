package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	localfile "github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service/pathlock"
	"go.uber.org/zap"
)

const (
	PathConfigFile   = "/var/lib/nimoos/path_config.json"
	MigrationLockFile = "/var/run/nimoos/migration.lock"

	MigrateTypeAppData  = "app_data"
	MigrateTypeImages   = "images"
	MigrateTypeUserData = "database"

	DefaultAppDataPath  = "/DATA/AppData"
	DefaultImagesPath   = "/DATA/.system_data/.docker"
	DefaultUserDataPath = "/DATA"
)

// PathConfig stores current configured paths for the three data locations.
type PathConfig struct {
	AppData  string `json:"app_data"`
	Images   string `json:"images"`
	UserData string `json:"database"`
}

// MigrateStatus describes a running or finished migration job.
type MigrateStatus struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Status        string `json:"status"`       // "running" | "done" | "error"
	Phase         string `json:"phase"`        // "stopping_services" | "copying" | "starting_services"
	StoppingApps  int    `json:"stopping_apps"` // number of containers being gracefully stopped
	Progress      int    `json:"progress"`
	ProcessedSize int64  `json:"processed_size"`
	TotalSize     int64  `json:"total_size"`
	NewPath       string `json:"new_path,omitempty"`
	Error         string `json:"error,omitempty"`
}

// MigrateJobs holds all active/completed migration jobs.
var MigrateJobs sync.Map // map[string]MigrateStatus

// migrationCleanup describes one destination that must be removed when SIGTERM fires.
// For system-restore units that have already been promoted from staging to anchor,
// restoreAnchor/restoreSrc are set so that CleanupActiveMigration can re-create the
// original anchor symlink after removing the partially-restored data.
type migrationCleanup struct {
	path          string // path passed to os.RemoveAll
	restoreAnchor string // if non-empty: re-create symlink restoreAnchor → restoreSrc after removal
	restoreSrc    string
}

var activeMigrationCleanups []migrationCleanup
var activeMigrationMu sync.Mutex

func CleanupActiveMigration() {
	activeMigrationMu.Lock()
	cleanups := append([]migrationCleanup(nil), activeMigrationCleanups...)
	activeMigrationMu.Unlock()
	for _, c := range cleanups {
		logger.Info("removing partial migration destination on shutdown", zap.String("path", c.path))
		_ = os.RemoveAll(c.path)
		if c.restoreAnchor != "" {
			logger.Info("restoring anchor symlink on shutdown",
				zap.String("anchor", c.restoreAnchor), zap.String("src", c.restoreSrc))
			_ = os.Symlink(c.restoreSrc, c.restoreAnchor)
		}
	}
}

// ResolveActivePaths detects where the data is actually living by checking config and standard symlinks.
// It auto-corrects the config if it finds reality differs from the stored JSON.
func ResolveActivePaths() PathConfig {
	cfg := LoadPathConfig()
	changed := false

	resolveReal := func(configuredPath, legacyPath string) string {
		// 1. If configured path exists, trust it
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		}

		// 2. If not, check if there's a legacy entry point (like /DATA/AppData) and where it leads
		if resolved, err := filepath.EvalSymlinks(legacyPath); err == nil {
			if _, serr := os.Stat(resolved); serr == nil {
				changed = true
				return resolved
			}
		}

		return configuredPath
	}

	cfg.AppData = resolveReal(cfg.AppData, DefaultAppDataPath)
	
	// Fast track for Images: anchor is /DATA/.system_data/.docker; follow it to find the real location
	if resolvedDocker, err := filepath.EvalSymlinks(DefaultImagesPath); err == nil {
		if cfg.Images != resolvedDocker {
			cfg.Images = resolvedDocker
			changed = true
		}
	} else {
		cfg.Images = DefaultImagesPath
	}
	
	// Special handling for UserData which usually contains subfolders
	if _, err := os.Stat(cfg.UserData); err != nil {
		if resolved, err := filepath.EvalSymlinks(DefaultUserDataPath); err == nil {
			// Check if it's a real directory (not just a broken link)
			if info, serr := os.Stat(resolved); serr == nil && info.IsDir() {
				cfg.UserData = resolved
				changed = true
			}
		}
	}

	if changed {
		logger.Info("auto-corrected path config based on system symlinks", zap.Any("new_cfg", cfg))
		_ = SavePathConfig(cfg)
	}

	return cfg
}

func LoadPathConfig() PathConfig {
	cfg := PathConfig{
		AppData:  DefaultAppDataPath,
		Images:   DefaultImagesPath,
		UserData: DefaultUserDataPath,
	}
	data, err := os.ReadFile(PathConfigFile)
	if err != nil {
		return cfg
	}
	var stored PathConfig
	if err := json.Unmarshal(data, &stored); err != nil {
		return cfg
	}
	if stored.AppData != "" {
		cfg.AppData = stored.AppData
	}
	if stored.Images != "" {
		cfg.Images = stored.Images
	}
	if stored.UserData != "" {
		cfg.UserData = stored.UserData
	}
	return cfg
}

func SavePathConfig(cfg PathConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PathConfigFile, data, 0644)
}

func StartMigration(jobID, migrationType, targetMountPoint string) {
	status := MigrateStatus{
		ID:     jobID,
		Type:   migrationType,
		Status: "running",
	}
	MigrateJobs.Store(jobID, status)

	go func() {
		newPath, err := executeMigration(jobID, migrationType, targetMountPoint)
		if v, ok := MigrateJobs.Load(jobID); ok {
			s := v.(MigrateStatus)
			if err != nil {
				s.Status = "error"
				s.Error = err.Error()
				logger.Error("migration failed", zap.String("type", migrationType), zap.Error(err))
			} else {
				s.Status = "done"
				s.Progress = 100
				s.NewPath = newPath
			}
			MigrateJobs.Store(jobID, s)
		}
	}()
}

func GetMigrationStatus(jobID string) (MigrateStatus, bool) {
	v, ok := MigrateJobs.Load(jobID)
	if !ok {
		return MigrateStatus{}, false
	}
	return v.(MigrateStatus), true
}

type migrateUnit struct {
	anchor string
	src    string
	dst    string
}

func executeMigration(jobID, migrationType, targetMountPoint string) (string, error) {
	cfg := ResolveActivePaths()

	var units []migrateUnit
	var newPath string
	var runningContainerIDs []string

	// "/DATA" is the user-facing root of the system disk (folder browser never exposes "/").
	isSystemRestore := targetMountPoint == "/" || targetMountPoint == "/DATA"

	switch migrationType {
	case MigrateTypeAppData:
		anchor := "/DATA/AppData"
		src, _ := filepath.EvalSymlinks(anchor)
		if src == "" {
			src = anchor
		}
		dst := filepath.Join(targetMountPoint, "AppData")
		if isSystemRestore {
			dst = anchor
		}
		newPath = anchor
		units = append(units, migrateUnit{anchor, src, dst})

	case MigrateTypeImages:
		for _, name := range []string{".docker", ".containerd"} {
			anchor := "/DATA/.system_data/" + name
			src, _ := filepath.EvalSymlinks(anchor)
			if src == "" {
				src = anchor
			}
			dst := filepath.Join(targetMountPoint, name)
			if isSystemRestore {
				dst = anchor
			}
			units = append(units, migrateUnit{anchor, src, dst})
		}
		newPath = "/DATA/.system_data/.docker"

	case MigrateTypeUserData:
		newPath = targetMountPoint
		if isSystemRestore {
			newPath = "/DATA"
		}
		for _, name := range []string{"Documents", "Downloads", "Gallery", "Media"} {
			anchor := "/DATA/" + name
			src, _ := filepath.EvalSymlinks(anchor)
			if src == "" {
				src = anchor
			}
			dst := filepath.Join(targetMountPoint, name)
			if isSystemRestore {
				dst = anchor
			}
			units = append(units, migrateUnit{anchor, src, dst})
		}

	default:
		return "", fmt.Errorf("unknown migration type: %s", migrationType)
	}

	// For system-restore: verify every unit that needs to move data can actually reach its
	// source. When EvalSymlinks fails (broken symlink), src falls back to the anchor path
	// itself, making u.src == u.dst — the copy loop treats this as "already done" and skips
	// it, but the data is still on the inaccessible external disk.  Catch this here so we
	// never reach Phase 5 and update the path config with a stale value.
	if isSystemRestore {
		for _, u := range units {
			if u.src != u.anchor {
				// src resolved to a different path via EvalSymlinks — the external disk is
				// accessible, or will be checked by os.Stat in the copy loop.
				continue
			}
			// src == anchor: either the anchor is already a real directory on the system
			// disk (fine — nothing to move), or EvalSymlinks failed because the anchor is a
			// broken symlink (external disk not mounted — must abort).
			linfo, lerr := os.Lstat(u.anchor)
			if lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf(
					"cannot restore %s to system disk: symlink target is not accessible (external disk may not be mounted)",
					u.anchor,
				)
			}
		}
	}

	// Hold write locks on every source path for the entire migration so that
	// concurrent file deletions or writes from the Files UI cannot modify data
	// that rsync is in the process of reading.
	for _, u := range units {
		if u.src != u.dst {
			unlock := pathlock.LockWrite(u.src)
			defer unlock()
		}
	}

	// Create a lock file so other services (e.g. AppManagement) can detect
	// that a migration is in progress and reject mutating requests with a
	// clear error instead of silently failing.
	if f, err := os.Create(MigrationLockFile); err == nil {
		f.Close()
	}
	defer os.Remove(MigrationLockFile)

	// Register all destination directories for SIGTERM cleanup.
	// All units (both isSystemRestore and non-isSystemRestore) rsync into a staging path
	// (dst + ".migrating") before the data is promoted to its final location.  Tracking
	// the staging path means a SIGTERM during rsync only removes the partial staging
	// directory and never touches a pre-existing u.dst that may contain user data.
	// After staging is promoted the entry is updated in-place (under the lock) to track
	// the promoted path instead, so a late signal still cleans up correctly.
	var sigCleanups []migrationCleanup
	for _, u := range units {
		if u.src == u.dst || strings.HasPrefix(u.dst, strings.TrimRight(u.src, "/")+"/") {
			continue
		}
		sigCleanups = append(sigCleanups, migrationCleanup{path: u.dst + ".migrating"})
	}
	activeMigrationMu.Lock()
	activeMigrationCleanups = sigCleanups
	activeMigrationMu.Unlock()
	defer func() {
		activeMigrationMu.Lock()
		activeMigrationCleanups = nil
		activeMigrationMu.Unlock()
	}()

	// 1. Stop services before migrating images, app data, or user data.
	// Images: overlay mounts must be gone before measuring size.
	// AppData/UserData: containers may write to these dirs during rsync, causing "vanished source files" (exit 24).
	if migrationType == MigrateTypeImages || migrationType == MigrateTypeAppData || migrationType == MigrateTypeUserData {
		setPhase(jobID, "stopping_services")
		logger.Info("stopping containers and docker for migration")

		// Publish the number of running containers so the frontend can show
		// "Stopping N apps..." instead of a silent spinner. No extra wait is
		// added here — systemctl stop docker handles graceful shutdown itself.
		// Save IDs so we can explicitly restart them after migration, rather than
		// relying solely on Docker's restart-policy mechanism (which can be disrupted
		// by the pkill -9 containerd-shim below).
		if out, err := exec.Command("docker", "ps", "-q").Output(); err == nil {
			runningContainerIDs = strings.Fields(string(out))
			if len(runningContainerIDs) > 0 {
				setStoppingApps(jobID, len(runningContainerIDs))
				logger.Info("stopping docker with running containers", zap.Int("count", len(runningContainerIDs)))
			}
		}

		_ = exec.Command("systemctl", "stop", "docker.socket", "docker", "containerd").Run()
		setStoppingApps(jobID, 0)
		// Kill any lingering shim processes that hold overlay mounts after service stop.
		_ = exec.Command("pkill", "-9", "-f", "containerd-shim").Run()
		time.Sleep(500 * time.Millisecond)
	}

	// 2. Size tracking — done after service stop so overlay mounts are gone and size is accurate
	var totalSize int64
	for _, u := range units {
		if u.src == u.dst || strings.HasPrefix(u.dst, strings.TrimRight(u.src, "/")+"/") {
			continue
		}
		sz, _ := localfile.GetFileOrDirSize(u.src)
		totalSize += sz
	}
	setPhase(jobID, "copying")
	updateProgress(jobID, 1, 0, totalSize)

	done := make(chan struct{})
	go trackProgress(jobID, units, totalSize, done)

	// 3. Perform Copies & Cleanup Anchor
	// processedUnits records every unit for which data has been fully written to dst,
	// so that all of them can be cleaned up if a later unit fails.
	var processedUnits []migrateUnit

	// rollbackCopies removes all successfully written destinations in reverse order.
	// For non-isSystemRestore units it also restores any pre-existing directory that was
	// moved aside to u.dst+".old" during promotion. For isSystemRestore units it restores
	// the anchor symlink to its original source.
	rollbackCopies := func() {
		for i := len(processedUnits) - 1; i >= 0; i-- {
			ru := processedUnits[i]
			if rerr := os.RemoveAll(ru.dst); rerr != nil {
				logger.Error("rollback cleanup failed", zap.String("dst", ru.dst), zap.Error(rerr))
				// RemoveAll failed: anchor still contains data. Skip symlink restore — attempting
				// Symlink on an existing path would fail with EEXIST and leave a confusing second
				// error. The operator must manually remove the anchor and recreate the symlink.
				continue
			}
			logger.Info("rollback: removed destination", zap.String("dst", ru.dst))
			if !isSystemRestore {
				dstOld := ru.dst + ".old"
				if _, serr := os.Stat(dstOld); serr == nil {
					if rerr := os.Rename(dstOld, ru.dst); rerr != nil {
						logger.Error("rollback: failed to restore pre-existing destination",
							zap.String("path", ru.dst), zap.Error(rerr))
					} else {
						logger.Info("rollback: restored pre-existing destination", zap.String("dst", ru.dst))
					}
				}
			}
			if isSystemRestore && ru.dst == ru.anchor {
				if serr := os.Symlink(ru.src, ru.anchor); serr != nil {
					logger.Error("rollback: failed to restore anchor symlink",
						zap.String("anchor", ru.anchor), zap.String("src", ru.src), zap.Error(serr))
				} else {
					logger.Info("rollback: restored anchor symlink",
						zap.String("anchor", ru.anchor), zap.String("src", ru.src))
				}
			}
		}
	}

	failCopy := func(err error) (string, error) {
		close(done)
		rollbackCopies()
		if migrationType == MigrateTypeImages || migrationType == MigrateTypeAppData || migrationType == MigrateTypeUserData {
			_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
		}
		return "", err
	}

	for _, u := range units {
		if u.src == u.dst || strings.HasPrefix(u.dst, strings.TrimRight(u.src, "/")+"/") {
			continue
		}

		// Skip: anchor is already a symlink pointing to dst — data is already in place, nothing to copy
		if resolved, err := filepath.EvalSymlinks(u.anchor); err == nil && resolved == u.dst {
			logger.Info("skipping unit — anchor already points to dst", zap.String("anchor", u.anchor), zap.String("dst", u.dst))
			continue
		}

		if _, err := os.Stat(u.src); os.IsNotExist(err) {
			// Source not accessible (external disk not mounted or data missing).
			// For a system-restore this means we cannot move the data back — treat it as
			// an error so Phase 5 never updates the path config with a stale value.
			if isSystemRestore {
				close(done)
				if migrationType == MigrateTypeImages {
					_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
				}
				return "", fmt.Errorf("source data at %s is not accessible; external disk may not be mounted", u.src)
			}
			continue
		}

		srcWithSlash := strings.TrimRight(u.src, "/") + "/"

		rsyncArgs := []string{"-a", "--one-file-system"}
		if migrationType == MigrateTypeImages {
			// Exclude runtime state directories that docker recreates on start
			rsyncArgs = append(rsyncArgs, "--exclude=rootfs/overlayfs", "--exclude=rootfs/mounts")
		}

		if isSystemRestore && u.dst == u.anchor {
			// Rsync into a staging directory first so the anchor symlink stays valid until
			// the copy is confirmed complete. Only then do we atomically swap staging → anchor.
			// This prevents a broken-anchor state when rsync fails partway through.
			staging := u.dst + ".migrating"
			_ = os.RemoveAll(staging) // clean up any leftover from a previous failed attempt
			if err := os.MkdirAll(staging, 0755); err != nil {
				return failCopy(fmt.Errorf("failed to create staging dir %s: %w", staging, err))
			}

			if err := exec.Command("rsync", append(rsyncArgs, srcWithSlash, staging+"/")...).Run(); err != nil {
				_ = os.RemoveAll(staging)
				return failCopy(fmt.Errorf("copy failed during rsync of %s: %w", u.anchor, err))
			}

			// Rsync complete: atomically promote staging to the anchor location.
			if err := os.Remove(u.anchor); err != nil {
				_ = os.RemoveAll(staging)
				return failCopy(fmt.Errorf("failed to sever anchor symlink %s: %w", u.anchor, err))
			}
			if err := os.Rename(staging, u.anchor); err != nil {
				// Rename failed after the symlink was already removed — restore the symlink
				// so the system can still reach the data on the external disk.
				if serr := os.Symlink(u.src, u.anchor); serr != nil {
					logger.Error("critical: failed to restore anchor after rename failure",
						zap.String("anchor", u.anchor), zap.String("src", u.src), zap.Error(serr))
				}
				_ = os.RemoveAll(staging)
				return failCopy(fmt.Errorf("failed to promote staging to anchor %s: %w", u.anchor, err))
			}
			// Staging promoted: update SIGTERM cleanup entry from the (now-gone) staging path
			// to the anchor so that a late signal removes the restored data and re-creates the
			// original symlink, leaving the system consistent.
			activeMigrationMu.Lock()
			for i, c := range activeMigrationCleanups {
				if c.path == staging {
					activeMigrationCleanups[i] = migrationCleanup{
						path:          u.anchor,
						restoreAnchor: u.anchor,
						restoreSrc:    u.src,
					}
					break
				}
			}
			activeMigrationMu.Unlock()
			processedUnits = append(processedUnits, u)
		} else {
			// Like isSystemRestore, rsync into a staging directory first so that u.dst is
			// never touched on failure — it may contain pre-existing user data.
			staging := u.dst + ".migrating"
			_ = os.RemoveAll(staging) // clean up any leftover from a previous failed attempt
			if err := os.MkdirAll(staging, 0755); err != nil {
				return failCopy(fmt.Errorf("failed to create staging dir %s: %w", staging, err))
			}
			if err := exec.Command("rsync", append(rsyncArgs, srcWithSlash, staging+"/")...).Run(); err != nil {
				_ = os.RemoveAll(staging) // clean up partial staging; u.dst is untouched
				return failCopy(fmt.Errorf("copy failed during rsync of %s: %w", u.anchor, err))
			}
			// Rsync complete: promote staging → u.dst.
			// If u.dst already exists (e.g. from a previous partial migration), move it aside
			// so os.Rename can place staging atomically into the slot.
			dstOld := u.dst + ".old"
			_ = os.RemoveAll(dstOld)
			if _, err := os.Stat(u.dst); err == nil {
				if rerr := os.Rename(u.dst, dstOld); rerr != nil {
					_ = os.RemoveAll(staging)
					return failCopy(fmt.Errorf("failed to move aside existing destination %s: %w", u.dst, rerr))
				}
			}
			if err := os.Rename(staging, u.dst); err != nil {
				_ = os.Rename(dstOld, u.dst) // best-effort restore of pre-existing directory
				_ = os.RemoveAll(staging)
				return failCopy(fmt.Errorf("failed to promote staging to %s: %w", u.dst, err))
			}
			// dstOld is kept alive until migration fully succeeds so that rollbackCopies can
			// restore it if a later unit fails. It is cleaned up in Phase 4 after all swaps succeed.
			// Update SIGTERM cleanup entry from staging to the promoted destination.
			activeMigrationMu.Lock()
			for i, c := range activeMigrationCleanups {
				if c.path == staging {
					activeMigrationCleanups[i] = migrationCleanup{path: u.dst}
					break
				}
			}
			activeMigrationMu.Unlock()
			processedUnits = append(processedUnits, u)
		}
	}
	close(done)
	// Rsync complete: jump to 95% and signal that we're restarting services.
	setPhase(jobID, "starting_services")
	updateProgress(jobID, 95, totalSize, totalSize)

	// 4. Anchor cleanup and symlink management
	// Three cases for all migration types:
	//   External → System : remove old external data, no symlink (data lives at anchor)
	//   System → External : remove physical data at anchor, create symlink anchor → dst
	//   External → External: remove old external data, update symlink anchor → new dst
	//
	// failAnchor is a local helper for step-4 failures: docker was stopped for images/app_data/user_data
	// migrations and must be restarted even on partial failure.
	failAnchor := func(err error) (string, error) {
		if migrationType == MigrateTypeImages || migrationType == MigrateTypeAppData || migrationType == MigrateTypeUserData {
			_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
		}
		return newPath, err
	}

	// needsAnchorWork reports whether unit u requires symlink management in step 4.
	needsAnchorWork := func(u migrateUnit) bool {
		if u.src == u.dst || strings.HasPrefix(u.dst, strings.TrimRight(u.src, "/")+"/") {
			return false
		}
		resolved, err := filepath.EvalSymlinks(u.anchor)
		return !(err == nil && resolved == u.dst)
	}

	if isSystemRestore {
		// External → System: data is now physically at anchor. Remove old external sources.
		// Clear SIGTERM cleanups before touching u.src: from this point a late signal must
		// not remove the restored anchor or attempt to recreate the external symlink, since
		// u.src is about to be deleted and restoring a symlink to a deleted path would leave
		// the system with a broken anchor.
		activeMigrationMu.Lock()
		activeMigrationCleanups = nil
		activeMigrationMu.Unlock()

		for _, u := range units {
			if u.src != u.anchor {
				if err := os.RemoveAll(u.src); err != nil {
					logger.Error("failed to remove old external source after system-restore; stale data remains",
						zap.String("src", u.src), zap.Error(err))
				}
			}
		}
	} else {
		// System → External OR External → External: two-phase symlink swap.
		//
		// Phase 1 — atomically swap every anchor to point at its new destination.
		//   For each unit: rename anchor → anchor.bak (frees the slot), then create symlink.
		//   If any step fails, roll back ALL symlinks already created (remove symlink,
		//   rename .bak back) so the system is left fully intact with all anchors pointing
		//   at the original locations. No old data is deleted until every swap succeeds.
		//
		// Phase 2 — only after all swaps succeed, delete the now-superseded old data.
		//   This prevents the "brain-split" state where some units are migrated and others
		//   are not, which would leave old data from completed units permanently deleted
		//   while the partially-migrated system is in an inconsistent state.
		type anchorSwap struct {
			u            migrateUnit
			backupAnchor string
		}
		var swapped []anchorSwap

		// Clear SIGTERM cleanups before entering the symlink swap loop. From this point
		// rollbackSwaps + rollbackCopies own failure recovery; CleanupActiveMigration must
		// not race with the loop and delete a u.dst that may already be the live anchor target.
		activeMigrationMu.Lock()
		activeMigrationCleanups = nil
		activeMigrationMu.Unlock()

		rollbackSwaps := func() {
			for i := len(swapped) - 1; i >= 0; i-- {
				sw := swapped[i]
				if rerr := os.Remove(sw.u.anchor); rerr != nil {
					logger.Error("rollback: failed to remove symlink",
						zap.String("anchor", sw.u.anchor), zap.Error(rerr))
				}
				if rerr := os.Rename(sw.backupAnchor, sw.u.anchor); rerr != nil {
					logger.Error("CRITICAL: rollback rename failed — manual intervention required",
						zap.String("backup", sw.backupAnchor), zap.String("anchor", sw.u.anchor), zap.Error(rerr))
				}
			}
		}

		for _, u := range units {
			if !needsAnchorWork(u) {
				continue
			}
			backupAnchor := u.anchor + ".bak"
			_ = os.RemoveAll(backupAnchor) // clean up any leftover from a previous failed attempt
			if err := os.Rename(u.anchor, backupAnchor); err != nil {
				logger.Error("failed to backup anchor; rolling back all swaps and copies",
					zap.String("anchor", u.anchor), zap.Error(err))
				rollbackSwaps()  // restore anchor symlinks first so no anchor is left dangling
				rollbackCopies() // then safely remove destination data that is no longer referenced
				return failAnchor(fmt.Errorf("anchor backup failed for %s: %w", u.anchor, err))
			}
			if err := os.Symlink(u.dst, u.anchor); err != nil {
				logger.Error("failed to create symlink; rolling back all swaps and copies",
					zap.String("anchor", u.anchor), zap.String("dst", u.dst), zap.Error(err))
				_ = os.Rename(backupAnchor, u.anchor) // restore this unit before rolling back others
				rollbackSwaps()                        // restore anchor symlinks first so no anchor is left dangling
				rollbackCopies()                       // then safely remove destination data that is no longer referenced
				return failAnchor(fmt.Errorf("symlink %s → %s creation failed: %w", u.anchor, u.dst, err))
			}
			swapped = append(swapped, anchorSwap{u, backupAnchor})
		}

		// Phase 2: all symlinks live — safe to delete old data.
		for _, sw := range swapped {
			linfo, lerr := os.Lstat(sw.backupAnchor)
			wasSymlink := lerr == nil && linfo.Mode()&os.ModeSymlink != 0

			if wasSymlink {
				// External → External (or symlink to elsewhere): remove old source.
				if err := os.RemoveAll(sw.u.src); err != nil {
					logger.Error("failed to remove old external source; stale data remains",
						zap.String("src", sw.u.src), zap.Error(err))
				}
			}
			// Remove backup (old system-disk dir for System→External, old symlink for Ext→Ext).
			if err := os.RemoveAll(sw.backupAnchor); err != nil {
				logger.Error("failed to remove anchor backup; stale data remains",
					zap.String("backup", sw.backupAnchor), zap.Error(err))
			}
			// Remove pre-existing destination backup preserved for rollback safety in Phase 3.
			_ = os.RemoveAll(sw.u.dst + ".old")
		}
	}

	// 5. Update Config & finalize
	switch migrationType {
	case MigrateTypeAppData:
		cfg.AppData = newPath
		logger.Info("starting docker and containerd after app data migration")
		_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
		waitForDockerReady(90 * time.Second)
		startContainersAfterMigration(runningContainerIDs)
	case MigrateTypeImages:
		cfg.Images = newPath
		// Update config files so Docker and Containerd natively know the data path.
		// This avoids the fragility of /var/lib/* symlinks.
		dockerDataRoot := "/DATA/.system_data/.docker"
		containerdRoot := "/DATA/.system_data/.containerd"
		if !isSystemRestore {
			// Migrating to external: update configs to the new target paths
			for _, u := range units {
				if strings.HasSuffix(u.anchor, "/.docker") {
					dockerDataRoot = u.dst
				}
				if strings.HasSuffix(u.anchor, "/.containerd") {
					containerdRoot = u.dst
				}
			}
		}
		// Always clean up any legacy /var/lib symlinks that may exist from old migrations.
		for _, name := range []string{"docker", "containerd"} {
			varLibPath := "/var/lib/" + name
			if linfo, lerr := os.Lstat(varLibPath); lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
				logger.Info("removing legacy /var/lib symlink", zap.String("path", varLibPath))
				_ = os.Remove(varLibPath)
				// Re-create as a real directory so the daemons can start even without config
				_ = os.MkdirAll(varLibPath, 0711)
			}
		}
		if err := updateDockerConfig(dockerDataRoot); err != nil {
			logger.Error("failed to update docker daemon.json", zap.Error(err))
		}
		if err := updateContainerdConfig(containerdRoot); err != nil {
			logger.Error("failed to update containerd config.toml", zap.Error(err))
		}
		logger.Info("starting docker and containerd after migration")
		_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
		waitForDockerReady(90 * time.Second)
		startContainersAfterMigration(runningContainerIDs)
	case MigrateTypeUserData:
		cfg.UserData = newPath
		logger.Info("starting docker and containerd after user data migration")
		_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
		waitForDockerReady(90 * time.Second)
		startContainersAfterMigration(runningContainerIDs)
	}

	if err := SavePathConfig(cfg); err != nil {
		logger.Error("failed to save path config", zap.Error(err))
	}

	return newPath, nil
}

func trackProgress(jobID string, units []migrateUnit, totalSize int64, done <-chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// unitStarted tracks whether a unit's staging directory has been observed at least once.
	// We only count u.dst (post-promotion) for units that have already had a staging dir —
	// measuring u.dst for a not-yet-started unit would count stale data from a previous
	// failed migration attempt and cause a visible progress regression when the unit finally
	// starts and the stale directory is moved aside.
	unitStarted := make(map[string]bool, len(units))

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			var processed int64
			for _, u := range units {
				if u.src == u.dst || strings.HasPrefix(u.dst, strings.TrimRight(u.src, "/")+"/") {
					continue
				}
				staging := u.dst + ".migrating"
				if _, err := os.Stat(staging); err == nil {
					// Staging exists: unit is actively being rsynced.
					unitStarted[u.dst] = true
					p, _ := localfile.GetFileOrDirSize(staging)
					processed += p
				} else if unitStarted[u.dst] {
					// Staging was promoted to u.dst; measure the final destination.
					p, _ := localfile.GetFileOrDirSize(u.dst)
					processed += p
				}
				// Unit not yet started: skip entirely so stale data in u.dst is not counted.
			}

			pct := 0
			if totalSize > 0 {
				pct = int(processed * 95 / totalSize)
			}
			updateProgress(jobID, pct, processed, totalSize)
		}
	}
}

func updateProgress(jobID string, pct int, processed, total int64) {
	if v, ok := MigrateJobs.Load(jobID); ok {
		s := v.(MigrateStatus)
		s.Progress = pct
		s.ProcessedSize = processed
		s.TotalSize = total
		MigrateJobs.Store(jobID, s)
	}
}

func setPhase(jobID, phase string) {
	if v, ok := MigrateJobs.Load(jobID); ok {
		s := v.(MigrateStatus)
		s.Phase = phase
		MigrateJobs.Store(jobID, s)
	}
}

func setStoppingApps(jobID string, count int) {
	if v, ok := MigrateJobs.Load(jobID); ok {
		s := v.(MigrateStatus)
		s.StoppingApps = count
		MigrateJobs.Store(jobID, s)
	}
}

// updateDockerConfig writes or updates /etc/docker/daemon.json with the given data-root.
func updateDockerConfig(dataRoot string) error {
	const configPath = "/etc/docker/daemon.json"
	var cfg map[string]interface{}

	data, err := os.ReadFile(configPath)
	if err != nil {
		cfg = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = make(map[string]interface{})
		}
	}

	cfg["data-root"] = dataRoot

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal docker daemon.json: %w", err)
	}
	if err := os.MkdirAll("/etc/docker", 0755); err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0644)
}

// waitForDockerReady polls until the Docker daemon accepts connections or timeout elapses.
// It is called after systemctl start so that startContainersAfterMigration does not race
// against Docker's own initialization.
func waitForDockerReady(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if exec.Command("docker", "info").Run() == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	logger.Error("docker did not become ready within timeout", zap.Duration("timeout", timeout))
}

// startContainersAfterMigration explicitly starts the containers that were running before
// migration began. This is a safety net: Docker's restart-policy mechanism may not fire
// reliably after the data-root changes or after containerd-shim processes are force-killed.
func startContainersAfterMigration(ids []string) {
	if len(ids) == 0 {
		return
	}
	args := append([]string{"start"}, ids...)
	if err := exec.Command("docker", args...).Run(); err != nil {
		logger.Error("failed to restart containers after migration", zap.Strings("ids", ids), zap.Error(err))
		return
	}
	logger.Info("restarted containers after migration", zap.Int("count", len(ids)))
}

// updateContainerdConfig writes or updates /etc/containerd/config.toml with the given root path.
func updateContainerdConfig(rootPath string) error {
	const configPath = "/etc/containerd/config.toml"

	data, err := os.ReadFile(configPath)
	if err != nil {
		content := fmt.Sprintf("root = %q\nstate = \"/run/containerd\"\n", rootPath)
		return os.WriteFile(configPath, []byte(content), 0644)
	}

	content := string(data)
	rootRegex := regexp.MustCompile(`(?m)^#?\s*root\s*=\s*"[^"]*"`)
	newRootLine := fmt.Sprintf(`root = %q`, rootPath)

	if rootRegex.MatchString(content) {
		content = rootRegex.ReplaceAllString(content, newRootLine)
	} else {
		content = newRootLine + "\n" + content
	}

	return os.WriteFile(configPath, []byte(content), 0644)
}

