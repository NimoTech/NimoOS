package v2

import (
	"path/filepath"
	"strings"

	"github.com/moby/sys/mountinfo"
)

// MountEntry is a (mountpoint, fstype) snapshot pair used to resolve which
// filesystem a target path lives on, without hitting /proc/self/mountinfo
// directly on every call — keeps resolveStagingRoot pure and unit-testable.
type MountEntry struct {
	Mountpoint string
	FSType     string
}

// localVolumeFSTypes are on-disk local block-device filesystems eligible for
// per-volume tus staging. Everything else (FUSE mounts such as fuse.rclone,
// network filesystems like cifs/nfs, the root/overlay fs, ...) falls back to
// the legacy /DATA staging root: writing tus upload fragments onto a
// cloud-mounted or network path would be a disaster, and the root overlay fs
// is not a real user data volume.
var localVolumeFSTypes = map[string]bool{
	"ext4":  true,
	"btrfs": true,
	"xfs":   true,
	"exfat": true,
	"vfat":  true,
	"ntfs":  true,
	"ntfs3": true,
}

// isLocalVolumeFSType reports whether fstype is safe for per-volume staging.
// mergerfs is a FUSE filesystem (commonly reported as "fuse.mergerfs") but is
// explicitly allowed: it fans out writes to real local disks underneath.
func isLocalVolumeFSType(fstype string) bool {
	if localVolumeFSTypes[fstype] {
		return true
	}
	return strings.Contains(fstype, "mergerfs")
}

// legacyStagingRoot is the pre-existing hardcoded staging root (common.FileUploadStagingDir's
// parent volume), used whenever per-volume staging cannot or should not be used.
const legacyStagingRoot = "/DATA"

// resolveStagingRoot returns the staging root (volume mount root) that
// targetPath should use, and whether it fell back. mounts is a snapshot list
// of (mountpoint, fstype), to keep this unit-testable. When falling back
// (fellBack=true), root is fixed to legacyStagingRoot ("/DATA"), matching the
// existing behavior.
func resolveStagingRoot(targetPath string, mounts []MountEntry) (root string, fellBack bool) {
	clean := filepath.Clean(targetPath)

	var bestMP, bestFS string
	found := false
	for _, m := range mounts {
		mp := filepath.Clean(m.Mountpoint)
		matches := mp == "/" || clean == mp || strings.HasPrefix(clean, mp+string(filepath.Separator))
		if !matches {
			continue
		}
		// Longest prefix wins: a deeper mountpoint match takes priority over a
		// shallower one (e.g. /media/RAID_0 wins over /).
		if !found || len(mp) > len(bestMP) {
			bestMP, bestFS, found = mp, m.FSType, true
		}
	}

	if !found || !isLocalVolumeFSType(bestFS) {
		return legacyStagingRoot, true
	}
	return bestMP, false
}

// liveMounts snapshots the current mount table for production use.
func liveMounts() []MountEntry {
	infos, err := mountinfo.GetMounts(nil)
	if err != nil {
		return nil
	}
	out := make([]MountEntry, 0, len(infos))
	for _, m := range infos {
		out = append(out, MountEntry{Mountpoint: m.Mountpoint, FSType: m.FSType})
	}
	return out
}

// stagingDirForRoot returns the per-volume tus staging directory for a
// resolved root, e.g. "/media/RAID_0" -> "/media/RAID_0/.system_data/file-tus-staging".
// For root == "/DATA" this is exactly common.FileUploadStagingDir.
func stagingDirForRoot(root string) string {
	return filepath.Join(root, ".system_data", "file-tus-staging")
}
