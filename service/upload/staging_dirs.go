package upload

import (
	"os"
	"path/filepath"
)

// StagingDirs returns every tus staging directory currently in use: the
// legacy dir (always first) plus any per-volume staging dir that already
// exists one level under mediaRoot (no recursion — NimoOS mounts external
// volumes directly under /media/<label>, see route/v2/tus_routing_store.go
// and resolveStagingRoot).
//
// This exists because upload IDs may now be routed to a per-volume staging
// directory instead of always the legacy one; callers that need to find a
// staged file by task ID alone (batch sweeper, cancel endpoint) can no longer
// assume a single directory and must try every directory this returns.
func StagingDirs(legacyDir, mediaRoot string) []string {
	dirs := []string{legacyDir}

	entries, err := os.ReadDir(mediaRoot)
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cand := filepath.Join(mediaRoot, e.Name(), ".system_data", "file-tus-staging")
		if st, statErr := os.Stat(cand); statErr == nil && st.IsDir() {
			dirs = append(dirs, cand)
		}
	}
	return dirs
}
