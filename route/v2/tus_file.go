package v2

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/NimoTech/NimoOS/common"
	"github.com/tus/tusd/v2/pkg/handler"
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
func ingestToTarget(stagedPath, targetPath, relativePath string) (string, error) {
	dest := filepath.Join(targetPath, relativePath)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("mkdir target dir: %w", err)
	}
	dest = uniqueDestPath(dest)

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
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
