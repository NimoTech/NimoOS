package common

// Files constants related to the file manager's tus uploads. Kept separate
// from Photos' photos-tus-staging so the two never interfere with each other.
const (
	// FileUploadStagingDir is the directory where tusd lands temporary
	// chunks. Every in-progress upload's partial data lives here — never
	// leave .part/.tmp files in the user's own directories.
	FileUploadStagingDir = "/DATA/.system_data/file-tus-staging"
)

// Tiered cache-cleanup TTLs (seconds). May become configurable later; for
// now these constants provide the default values.
const (
	// UploadIdleTimeoutSeconds: how long an uploading task can go without
	// progress before it's downgraded to paused (6 hours).
	UploadIdleTimeoutSeconds = int64(6 * 60 * 60)
	// UploadPausedTTLSeconds: retention window for paused (awaiting resume)
	// staging data (3 days).
	UploadPausedTTLSeconds = int64(3 * 24 * 60 * 60)
	// UploadCanceledTTLSeconds: how long until canceled/failed uploads are
	// cleaned up (1 hour).
	UploadCanceledTTLSeconds = int64(60 * 60)
	// UploadGCIntervalSeconds: interval between GC goroutine runs (1 hour).
	UploadGCIntervalSeconds = int64(60 * 60)
)
