package utils

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// dataAllowedPrefixes are the mount-point families where non-admin users may
// browse.  Every other path — including the root filesystem and its partitions —
// is implicitly denied.
var dataAllowedPrefixes = []string{}

// systemDiskCache caches the set of system-disk mount points so we only pay
// the /proc/mounts parse cost once per minute instead of on every API call.
var systemDiskCache struct {
	sync.RWMutex
	paths     map[string]struct{} // mount points belonging to the system disk
	refreshAt time.Time
}

// IsPathAllowed returns true if the given path is accessible for the given role.
//
//   - Admin users can access any non-empty path.
//   - Regular (sub) users are restricted to /DATA, /mnt, /media and their
//     sub-directories, AND the path must not reside on the system disk (the
//     disk whose partition is mounted at "/").
func IsPathAllowed(path string, isAdmin bool) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	if isAdmin {
		return true
	}

	// 1. Must be under an allowed prefix.
	allowed := false
	for _, prefix := range dataAllowedPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}

	// 2. Must NOT be on the system disk (defense-in-depth — the Linux setfacl
	//    layer is the physical enforcement; this is the API-layer guard).
	if isOnSystemDisk(clean) {
		return false
	}

	return true
}

// SystemDiskMountPoints returns the current set of mount points that belong to
// the system disk.  Exposed so callers (e.g. the file-browser initial-path API)
// can filter the disk list shown to sub-users.
func SystemDiskMountPoints() map[string]struct{} {
	refreshSystemDiskCache()
	systemDiskCache.RLock()
	defer systemDiskCache.RUnlock()
	// return a shallow copy so the caller doesn't race on the map
	out := make(map[string]struct{}, len(systemDiskCache.paths))
	for k, v := range systemDiskCache.paths {
		out[k] = v
	}
	return out
}

// isOnSystemDisk reports whether the cleaned path is at or below a system-disk
// mount point.
func isOnSystemDisk(clean string) bool {
	refreshSystemDiskCache()
	systemDiskCache.RLock()
	defer systemDiskCache.RUnlock()
	for mp := range systemDiskCache.paths {
		if clean == mp || strings.HasPrefix(clean, mp+"/") {
			return true
		}
	}
	return false
}

// refreshSystemDiskCache updates the cache if it is stale (older than 1 minute).
func refreshSystemDiskCache() {
	systemDiskCache.RLock()
	fresh := time.Now().Before(systemDiskCache.refreshAt)
	systemDiskCache.RUnlock()
	if fresh {
		return
	}

	systemDiskCache.Lock()
	defer systemDiskCache.Unlock()
	// Double-check after acquiring write lock.
	if time.Now().Before(systemDiskCache.refreshAt) {
		return
	}
	systemDiskCache.paths = parseSystemDiskPaths()
	systemDiskCache.refreshAt = time.Now().Add(time.Minute)
}

// parseSystemDiskPaths reads /proc/mounts and returns all mount points that
// belong to the same physical disk as the root filesystem ("/").
func parseSystemDiskPaths() map[string]struct{} {
	result := map[string]struct{}{
		"/": {}, // root is always on the system disk
	}

	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return result
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")

	// Step 1 — find which /dev/* device is at "/".
	var rootDev string
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "/" && strings.HasPrefix(f[0], "/dev/") {
			rootDev = f[0]
			break
		}
	}
	if rootDev == "" {
		return result
	}

	// Step 2 — all partitions on the same physical disk share the same diskBase.
	sysDiskBase := aclDiskBase(rootDev)

	// Step 3 — collect mount points.
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], "/dev/") {
			continue
		}
		if aclDiskBase(f[0]) == sysDiskBase {
			result[f[1]] = struct{}{}
		}
	}
	return result
}

// aclDiskBase strips the partition suffix from a Linux block-device path:
//
//	/dev/sda1      → /dev/sda
//	/dev/nvme0n1p2 → /dev/nvme0n1
//	/dev/mmcblk0p1 → /dev/mmcblk0
func aclDiskBase(device string) string {
	dir := filepath.Dir(device)
	base := filepath.Base(device)
	// NVMe / eMMC: strip trailing p<digits>
	if re := regexp.MustCompile(`^(nvme\d+n\d+|mmcblk\d+)p\d+$`); re.MatchString(base) {
		return filepath.Join(dir, re.FindStringSubmatch(base)[1])
	}
	// SCSI / SATA / VirtIO: strip trailing digits
	if re := regexp.MustCompile(`^([a-zA-Z]+)\d+$`); re.MatchString(base) {
		return filepath.Join(dir, re.FindStringSubmatch(base)[1])
	}
	return device
}
