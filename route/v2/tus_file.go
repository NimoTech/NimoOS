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

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
	"go.uber.org/zap"
)

// FileUploadMaxSizeForTest exposes the upload size limit for tests.
func FileUploadMaxSizeForTest() int64 { return common.FileUploadMaxSize }

// freeBytesFn returns available bytes for a storage path; injectable for tests.
type freeBytesFn func() (uint64, error)

// statfsDATA returns available bytes on /DATA via syscall.Statfs.
func statfsDATA() (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs("/DATA", &s); err != nil {
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

	if name == "" {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("filename metadata required")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("filename contains illegal characters")
	}
	if targetPath == "" {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("targetPath metadata required")
	}
	// Protected folders: check targetPath and relativePath.
	if protected, n := containsProtectedName(targetPath); protected {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("protected folder: %s", n)
	}
	if protected, n := containsProtectedName(relativePath); protected {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("protected folder: %s", n)
	}
	// Path traversal: relativePath must not contain ".." or be absolute.
	if strings.Contains(relativePath, "..") || strings.HasPrefix(relativePath, "/") {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("relativePath traversal rejected")
	}
	// Resolved path must stay within targetPath.
	final := filepath.Clean(filepath.Join(targetPath, relativePath))
	cleanTarget := filepath.Clean(targetPath)
	if !strings.HasPrefix(final, cleanTarget+string(filepath.Separator)) && final != cleanTarget {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("resolved path escapes target")
	}
	if hook.Upload.Size <= 0 {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("empty file rejected")
	}
	if hook.Upload.Size > common.FileUploadMaxSize {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, fmt.Errorf("file exceeds %d byte limit", common.FileUploadMaxSize)
	}
	if err := checkFileUploadQuota(hook.Upload.Size, available); err != nil {
		return handler.HTTPResponse{StatusCode: 413}, handler.FileInfoChanges{}, err
	}
	return handler.HTTPResponse{}, handler.FileInfoChanges{}, nil
}

// validateFileUploadMetadata is the production entry point.
func validateFileUploadMetadata(hook handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	return validateFileUploadMetadataWithQuota(hook, statfsDATA)
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
	dest := filepath.Join(targetPath, relativePath)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("mkdir target dir: %w", err)
	}
	if !resumed {
		dest = uniqueDestPath(dest)
	}

	if err := os.Rename(stagedPath, dest); err != nil {
		if cerr := copyFileV2(stagedPath, dest); cerr != nil {
			return "", fmt.Errorf("rename and copy both failed: %w / %v", err, cerr)
		}
		os.Remove(stagedPath) //nolint:errcheck
	}
	os.Remove(stagedPath + ".info") //nolint:errcheck
	return dest, nil
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
		os.Remove(dst) // 清理半写损坏文件
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst) // Close 失败同样视为写入失败，清理
		return err
	}
	return nil
}

const fileTusBasePath = "/v2/nimoos/file/upload-tus"

// relativeLocationWriter 把 tusd 生成的绝对 Location header 改写成 path-only，
// 与 Photos 同理(Gateway 代理后 Host 为内部地址)。
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

// NewFileTUSHandler 创建 Files 用 tusd handler：staging 存储 + 创建校验 +
// 完成后移动到用户目标路径。返回 http.Handler 供 echo.WrapHandler 使用。
func NewFileTUSHandler() (http.Handler, error) {
	if err := os.MkdirAll(common.FileUploadStagingDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}
	store := filestore.New(common.FileUploadStagingDir)
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	tusH, err := handler.NewHandler(handler.Config{
		BasePath:                fileTusBasePath + "/",
		StoreComposer:           composer,
		NotifyCompleteUploads:   true,
		MaxSize:                 common.FileUploadMaxSize,
		PreUploadCreateCallback: validateFileUploadMetadata,
	})
	if err != nil {
		return nil, err
	}

	go func() {
		for event := range tusH.CompleteUploads {
			stagedPath := filepath.Join(common.FileUploadStagingDir, event.Upload.ID)
			targetPath := event.Upload.MetaData["targetPath"]
			relativePath := event.Upload.MetaData["relativePath"]
			if relativePath == "" {
				relativePath = event.Upload.MetaData["filename"]
			}
			resumed := event.Upload.MetaData["resumed"] == "1"
			dest, ierr := ingestToTarget(stagedPath, targetPath, relativePath, resumed)
			if ierr != nil {
				logger.Error("Files tus ingest failed",
					zap.String("id", event.Upload.ID), zap.Error(ierr))
				continue
			}
			logger.Info("Files tus upload complete", zap.String("dest", dest))
		}
	}()

	startStagingGC()
	return withRelativeLocation(http.StripPrefix(fileTusBasePath, tusH)), nil
}

// sweepStaging 删除 dir 中修改时间早于 ttlSeconds 的文件(连带 .info)。
// 返回删除的主文件数。.info sidecar 不单独计数。
func sweepStaging(dir string, ttlSeconds int64, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := now.Add(-time.Duration(ttlSeconds) * time.Second)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".info") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			p := filepath.Join(dir, e.Name())
			os.Remove(p)           //nolint:errcheck
			os.Remove(p + ".info") //nolint:errcheck
			removed++
		}
	}
	return removed
}

// startStagingGC 启动后台定时 GC(每 6 小时跑一次)。在 NewFileTUSHandler 里调用。
func startStagingGC() {
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sweepStaging(common.FileUploadStagingDir, common.FileUploadStagingTTLSeconds, time.Now())
		}
	}()
}
