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
	"github.com/NimoTech/NimoOS/service/upload"
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

	// 客户端/校验类错误必须返回 4xx。关键:要用 tusd 的 handler.Error(携带 StatusCode),
	// 否则 tusd 对普通 error 一律按 500 处理,前端 tus-js-client 会把 5xx 当可重试错误
	// 无限重试,导致上传卡在 0%。HTTPResponse 里也带上 StatusCode 供单测直接读取。
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
	// 受保护文件夹检查只针对 relativePath:防止「文件夹上传」在用户根部重建出同名的
	// 系统特殊文件夹(Documents/Downloads/Gallery/Media/AppData)。
	// 注意:不检查 targetPath——上传文件「进入」这些用户数据文件夹本就是正常用途
	//（Gallery 还会被 Photos 索引)。c6eaced 误加的 targetPath 检查会把正常上传判为
	// 受保护并返回 500,导致前端卡死,已移除。
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
	if hook.Upload.Size > common.FileUploadMaxSize {
		return reject(413, "ERR_FILE_TOO_LARGE", fmt.Sprintf("file exceeds %d byte limit", common.FileUploadMaxSize))
	}
	if err := checkFileUploadQuota(hook.Upload.Size, available); err != nil {
		return reject(413, "ERR_INSUFFICIENT_STORAGE", err.Error())
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
	policy := "rename"
	if resumed {
		policy = "overwrite"
	}
	dest, _, err := ingestToTargetWithPolicy(stagedPath, targetPath, relativePath, policy)
	return dest, err
}

// ingestToTargetWithPolicy 按冲突策略把 staging 文件落到 join(targetPath, relativePath)。
// policy: "overwrite" 覆盖 / "rename" 加(n) / "skip" 已存在则跳过 / ""=rename。
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
		// 直接落到 dest,覆盖。
	default: // "rename" / ""
		if exists {
			dest = uniqueDestPath(dest)
		}
	}

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
func NewFileTUSHandler(store *upload.TaskStore) (http.Handler, error) {
	if err := os.MkdirAll(common.FileUploadStagingDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}
	st := filestore.New(common.FileUploadStagingDir)
	composer := handler.NewStoreComposer()
	st.UseIn(composer)

	tusH, err := handler.NewHandler(handler.Config{
		BasePath:                 fileTusBasePath + "/",
		StoreComposer:            composer,
		NotifyCompleteUploads:    true,
		NotifyCreatedUploads:     true,
		NotifyUploadProgress:     true,
		NotifyTerminatedUploads:  true,
		MaxSize:                  common.FileUploadMaxSize,
		PreUploadCreateCallback:  validateFileUploadMetadata,
	})
	if err != nil {
		return nil, err
	}

	// 创建:写任务行。
	go func() {
		for ev := range tusH.CreatedUploads {
			task := upload.NewTaskFromHook(ev, time.Now())
			if cerr := store.Create(task); cerr != nil {
				logger.Error("upload task create failed", zap.String("id", ev.Upload.ID), zap.Error(cerr))
			}
		}
	}()

	// 进度:节流更新 offset(每次事件即更新 offset 与 expires;tusd 已按 chunk 粒度发,足够稀疏)。
	go func() {
		for ev := range tusH.UploadProgress {
			_ = store.UpdateOffset(ev.Upload.ID, ev.Upload.Offset,
				time.Now().Unix()+common.UploadIdleTimeoutSeconds)
		}
	}()

	// 终止(协议 DELETE):标记 canceled。
	go func() {
		for ev := range tusH.TerminatedUploads {
			_ = store.SetStatus(ev.Upload.ID, commonUpload.UploadStatusCanceled,
				time.Now().Unix()+common.UploadCanceledTTLSeconds)
		}
	}()

	// 完成:ingest + 置 completed / failed。
	go func() {
		for event := range tusH.CompleteUploads {
			stagedPath := filepath.Join(common.FileUploadStagingDir, event.Upload.ID)
			targetPath := event.Upload.MetaData["targetPath"]
			relativePath := event.Upload.MetaData["relativePath"]
			if relativePath == "" {
				relativePath = event.Upload.MetaData["filename"]
			}
			policy := event.Upload.MetaData["conflictPolicy"]
			if policy == "" && event.Upload.MetaData["resumed"] == "1" {
				policy = "overwrite"
			}
			dest, _, ierr := ingestToTargetWithPolicy(stagedPath, targetPath, relativePath, policy)
			if ierr != nil {
				logger.Error("Files tus ingest failed",
					zap.String("id", event.Upload.ID), zap.Error(ierr))
				_ = store.SetFailed(event.Upload.ID, ierr.Error(),
					time.Now().Unix(), time.Now().Unix()+common.UploadCanceledTTLSeconds)
				continue
			}
			_ = store.SetStatus(event.Upload.ID, commonUpload.UploadStatusCompleted, 0)
			logger.Info("Files tus upload complete", zap.String("dest", dest))
		}
	}()

	commonUpload.StartGC(store, upload.DefaultGCConfig())
	return withRelativeLocation(http.StripPrefix(fileTusBasePath, tusH)), nil
}

