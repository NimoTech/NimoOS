package v2

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service"
	"github.com/NimoTech/NimoOS/service/upload"
	"github.com/tus/tusd/v2/pkg/handler"
	"go.uber.org/zap"
)

// freeBytesFn returns available bytes for a storage path; injectable for tests.
type freeBytesFn func() (uint64, error)

// statfsPath returns available bytes on the filesystem containing path via
// syscall.Statfs. Replaces the old hardcoded statfsDATA: the quota check must
// look at whichever volume the upload is actually staged on/headed to, not
// always /DATA (that mismatch is what caused spurious 413s for uploads to
// other volumes, e.g. a RAID array with plenty of free space).
func statfsPath(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return s.Bavail * uint64(s.Bsize), nil
}

// checkFileUploadQuota returns an error if uploadLength (with 5% margin) exceeds available space.
func checkFileUploadQuota(uploadLength int64, available freeBytesFn) error {
	avail, err := available()
	if err != nil {
		return fmt.Errorf("storage check failed: %w", err)
	}
	needed := uint64(float64(uploadLength) * 1.05)
	if needed > avail {
		return fmt.Errorf("insufficient storage: need %d available %d", needed, avail)
	}
	return nil
}

// validateFileUploadMetadataWithQuota validates metadata, protected folders, path traversal,
// file size, and available quota. available is an injectable free-bytes provider.
func validateFileUploadMetadataWithQuota(hook handler.HookEvent, available freeBytesFn) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	meta := hook.Upload.MetaData
	name := strings.TrimSpace(meta["filename"])
	targetPath := meta["targetPath"]
	relativePath := meta["relativePath"]

	// Client/validation errors must return 4xx. Key point: use tusd's handler.Error
	// (which carries a StatusCode) — otherwise tusd treats a plain error as 500,
	// and the tus-js-client frontend treats 5xx as retryable and retries forever,
	// stalling the upload at 0%. HTTPResponse also carries StatusCode for tests
	// to read directly.
	reject := func(sc int, code, msg string) (handler.HTTPResponse, handler.FileInfoChanges, error) {
		return handler.HTTPResponse{StatusCode: sc}, handler.FileInfoChanges{}, handler.NewError(code, msg, sc)
	}
	if name == "" {
		return reject(400, "ERR_FILENAME_REQUIRED", "filename metadata required")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return reject(400, "ERR_FILENAME_ILLEGAL", "filename contains illegal characters")
	}
	if targetPath == "" {
		return reject(400, "ERR_TARGETPATH_REQUIRED", "targetPath metadata required")
	}
	// The protected-folder check only applies to relativePath: it prevents a
	// "folder upload" from recreating a same-named system special folder
	// (Documents/Downloads/Gallery/Media/AppData) at the user's root.
	// Note: targetPath is NOT checked — uploading files "into" these user data
	// folders is normal usage (Gallery even gets indexed by Photos). A targetPath
	// check mistakenly added in c6eaced flagged normal uploads as protected and
	// returned 500, stalling the frontend; it has been removed.
	if protected, n := containsProtectedName(relativePath); protected {
		return reject(403, "ERR_PROTECTED_FOLDER", "protected folder: "+n)
	}
	// Path traversal: relativePath must not contain ".." or be absolute.
	if strings.Contains(relativePath, "..") || strings.HasPrefix(relativePath, "/") {
		return reject(400, "ERR_PATH_TRAVERSAL", "relativePath traversal rejected")
	}
	// Resolved path must stay within targetPath.
	final := filepath.Clean(filepath.Join(targetPath, relativePath))
	cleanTarget := filepath.Clean(targetPath)
	if !strings.HasPrefix(final, cleanTarget+string(filepath.Separator)) && final != cleanTarget {
		return reject(400, "ERR_PATH_ESCAPE", "resolved path escapes target")
	}
	if hook.Upload.Size <= 0 {
		return reject(400, "ERR_EMPTY_FILE", "empty file rejected")
	}
	// No artificial per-file size cap: allowed as long as the staging disk has
	// enough space (×1.05 margin).
	if err := checkFileUploadQuota(hook.Upload.Size, available); err != nil {
		return reject(413, "ERR_INSUFFICIENT_STORAGE", err.Error())
	}
	return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
}

// validateFileUploadMetadataForRoot resolves targetPath's staging root via
// mountsFn and checks quota against that root's filesystem (statfsPath),
// instead of always /DATA. mountsFn is injectable for tests.
func validateFileUploadMetadataForRoot(hook handler.HookEvent, mountsFn func() []MountEntry) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	targetPath := hook.Upload.MetaData["targetPath"]
	root, _ := resolveStagingRoot(targetPath, mountsFn())
	return validateFileUploadMetadataWithQuota(hook, func() (uint64, error) { return statfsPath(root) })
}

// validateFileUploadMetadata is the production entry point.
func validateFileUploadMetadata(hook handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	return validateFileUploadMetadataForRoot(hook, liveMounts)
}

// uniqueDestPath returns a path that does not conflict with an existing file.
// If dest already exists, it appends (1), (2), … before the extension
// (or at the end if there is no extension).
func uniqueDestPath(dest string) string {
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return dest
	}
	dir := filepath.Dir(dest)
	base := filepath.Base(dest)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", stem, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

// ingestToTarget moves a completed staging file to join(targetPath, relativePath).
// It creates intermediate directories, renames with a suffix on collision, falls back
// to copy+delete when os.Rename fails across devices, and removes the .info sidecar.
// It returns the final on-disk path.
//
// When resumed is true the upload is a re-send of a file the client already
// started (typically it finished server-side but the client refreshed before
// recording it as done, then re-queued it). In that case we overwrite the
// existing target instead of creating a "(1)" duplicate. Fresh uploads still get
// a unique name on collision.
func ingestToTarget(stagedPath, targetPath, relativePath string, resumed bool) (string, error) {
	policy := "rename"
	if resumed {
		policy = "overwrite"
	}
	dest, _, err := ingestToTargetWithPolicy(stagedPath, targetPath, relativePath, policy)
	return dest, err
}

// ingestToTargetWithPolicy moves a staging file to join(targetPath, relativePath)
// according to a conflict policy.
// policy: "overwrite" replaces / "rename" appends (n) / "skip" skips if it
// already exists / "" defaults to rename.
func ingestToTargetWithPolicy(stagedPath, targetPath, relativePath, policy string) (string, bool, error) {
	dest := filepath.Join(targetPath, relativePath)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", false, fmt.Errorf("mkdir target dir: %w", err)
	}
	_, statErr := os.Stat(dest)
	exists := statErr == nil

	switch policy {
	case "skip":
		if exists {
			os.Remove(stagedPath)          //nolint:errcheck
			os.Remove(stagedPath + ".info") //nolint:errcheck
			return dest, true, nil
		}
	case "overwrite":
		// Land directly on dest, overwriting it.
	default: // "rename" / ""
		if exists {
			dest = uniqueDestPath(dest)
		}
	}

	// If dest already exists and is about to be overwritten (overwrite policy, or
	// the rare case where a rename target happens to collide), its cache entry
	// must be purged here, before the change — afterward dest's mtime/size will
	// belong to the new file and the old entry's key can no longer be computed.
	// No-op if dest doesn't exist.
	file.PurgeThumbCacheEntry(dest)
	if err := os.Rename(stagedPath, dest); err != nil {
		if cerr := copyFileV2(stagedPath, dest); cerr != nil {
			return "", false, fmt.Errorf("rename and copy both failed: %w / %v", err, cerr)
		}
		os.Remove(stagedPath) //nolint:errcheck
	}
	os.Remove(stagedPath + ".info") //nolint:errcheck
	return dest, false, nil
}

func copyFileV2(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst) // clean up the partially-written, corrupt file
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst) // a Close failure also counts as a write failure; clean up
		return err
	}
	return nil
}

const fileTusBasePath = "/v2/nimoos/file/upload-tus"

// relativeLocationWriter rewrites the absolute Location header tusd generates
// into a path-only one, same reasoning as Photos (behind the Gateway proxy,
// Host would be an internal address).
type relativeLocationWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *relativeLocationWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		if loc := w.Header().Get("Location"); loc != "" {
			if u, err := url.Parse(loc); err == nil && u.Path != "" {
				w.Header().Set("Location", u.Path)
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *relativeLocationWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *relativeLocationWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func withRelativeLocation(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&relativeLocationWriter{ResponseWriter: w}, r)
	})
}

// NewFileTUSHandler creates the tusd handler for Files: staging storage +
// creation validation + moving to the user's target path on completion.
// Returns an http.Handler for use with echo.WrapHandler.
func NewFileTUSHandler(store *upload.TaskStore, batches *upload.BatchStore) (http.Handler, error) {
	if err := os.MkdirAll(common.FileUploadStagingDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}
	// routingStore stages fragments in-place under <volume mount root>/.system_data/file-tus-staging
	// based on the target volume, instead of hardcoding /DATA: quota checks and
	// disk writes follow the target volume, so ingest on completion becomes a
	// same-disk rename. When in-place staging isn't possible (FUSE/network mounts,
	// resolution failure), it falls back to the existing /DATA directory, behavior
	// unchanged.
	rs := newRoutingStore(common.FileUploadStagingDir, liveMounts)
	composer := handler.NewStoreComposer()
	composer.UseCore(rs)
	composer.UseTerminater(rs)
	composer.UseConcater(rs)
	composer.UseLengthDeferrer(rs)
	composer.UseContentServer(rs)

	tusH, err := handler.NewHandler(handler.Config{
		BasePath:                fileTusBasePath + "/",
		StoreComposer:           composer,
		NotifyCompleteUploads:   true,
		NotifyCreatedUploads:    true,
		NotifyUploadProgress:    true,
		NotifyTerminatedUploads: true,
		PreUploadCreateCallback: validateFileUploadMetadata,
	})
	if err != nil {
		return nil, err
	}

	// Create: write the task row.
	go func() {
		for ev := range tusH.CreatedUploads {
			task := upload.NewTaskFromHook(ev, time.Now())
			if cerr := store.Create(task); cerr != nil {
				logger.Error("upload task create failed", zap.String("id", ev.Upload.ID), zap.Error(cerr))
			}
		}
	}()

	// Progress: dual-throttled DB writes, to avoid tens of thousands of tiny
	// UPDATEs piling up across 7000 files and slowing the upload down. Both
	// throttle intervals are 5s, which leaves ample margin against both
	// downstream timeout thresholds and doesn't affect their outcome:
	//   - UpdateOffset also renews expires_at (idle timeout threshold 6h =
	//     21600s); the 5s throttle is essentially imperceptible to it.
	//   - TouchProgress feeds the batch timeout check (threshold 120s); the 5s
	//     throttle is still far smaller than that threshold, so it won't be
	//     misjudged as interrupted.
	go func() {
		offsetThrottle := upload.NewTouchThrottle(5)
		progressThrottle := upload.NewTouchThrottle(5)
		for ev := range tusH.UploadProgress {
			now := time.Now().Unix()
			if offsetThrottle.Allow(ev.Upload.ID, now) {
				_ = store.UpdateOffset(ev.Upload.ID, ev.Upload.Offset,
					now+common.UploadIdleTimeoutSeconds)
			}
			if bid := ev.Upload.MetaData["batch_id"]; bid != "" {
				if progressThrottle.Allow(bid, now) {
					_ = batches.TouchProgress(bid, now)
				}
			}
		}
	}()

	// Terminate (protocol DELETE): mark canceled.
	go func() {
		for ev := range tusH.TerminatedUploads {
			_ = store.SetStatus(ev.Upload.ID, commonUpload.UploadStatusCanceled,
				time.Now().Unix()+common.UploadCanceledTTLSeconds)
		}
	}()

	// Complete: ingest + set completed / failed.
	go func() {
		for event := range tusH.CompleteUploads {
			// stagedPath is taken from Storage["Path"] as written back by the tusd
			// filestore — this is the actual absolute path the fragments landed on,
			// which under routingStore may be on /DATA or on some other volume; it
			// can no longer be assumed to always be
			// common.FileUploadStagingDir/id. If missing (shouldn't happen in
			// theory), fall back to constructing it the old way.
			stagedPath := event.Upload.Storage["Path"]
			if stagedPath == "" {
				stagedPath = filepath.Join(common.FileUploadStagingDir, event.Upload.ID)
			}
			targetPath := event.Upload.MetaData["targetPath"]
			relativePath := event.Upload.MetaData["relativePath"]
			if relativePath == "" {
				relativePath = event.Upload.MetaData["filename"]
			}
			policy := event.Upload.MetaData["conflictPolicy"]
			if policy == "" && event.Upload.MetaData["resumed"] == "1" {
				policy = "overwrite"
			}
			dest, skipped, ierr := ingestToTargetWithPolicy(stagedPath, targetPath, relativePath, policy)
			if ierr != nil {
				logger.Error("Files tus ingest failed",
					zap.String("id", event.Upload.ID), zap.Error(ierr))
				_ = store.SetFailed(event.Upload.ID, ierr.Error(),
					time.Now().Unix(), time.Now().Unix()+common.UploadCanceledTTLSeconds)
				continue
			}
			_ = store.SetStatus(event.Upload.ID, commonUpload.UploadStatusCompleted, 0)
			now := time.Now().Unix()
			if bid := event.Upload.MetaData["batch_id"]; bid != "" {
				_ = batches.MarkItemDone(bid, relativePath, now)
			}
			// A plain re-upload (with or without batch_id) implicitly reconciles:
			// any other unfinished item with the same name in an
			// active/interrupted batch under the same targetPath is also
			// settled, so the old broken badge disappears automatically;
			// failures here are only logged, they don't block the main flow.
			if err := batches.MarkItemDoneAcrossBatches(targetPath, relativePath, now); err != nil {
				logger.Error("Files tus MarkItemDoneAcrossBatches failed",
					zap.String("targetPath", targetPath), zap.String("relativePath", relativePath), zap.Error(err))
			}
			logger.Info("Files tus upload complete", zap.String("dest", dest))
			if !skipped {
				go service.PublishMediaCreated([]string{dest})
			}
		}
	}()

	commonUpload.StartGC(store, upload.DefaultGCConfig())
	// Task IDs may now carry a volume prefix and land in different volumes'
	// staging directories; the sweeper can no longer assume a single directory:
	// each round re-enumerates the legacy /DATA directory plus every existing
	// /media/*/.system_data/file-tus-staging.
	upload.StartBatchSweeper(batches, store, func() []string {
		return upload.StagingDirs(common.FileUploadStagingDir, "/media")
	})
	return withRelativeLocation(http.StripPrefix(fileTusBasePath, tusH)), nil
}

