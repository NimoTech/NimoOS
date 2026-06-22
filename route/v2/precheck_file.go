package v2

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// precheckFileItem is a single file entry in the precheck request.
type precheckFileItem struct {
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
}

// precheckRequest is the JSON body for POST /v2/nimoos/file/upload-precheck.
type precheckRequest struct {
	TargetPath string             `json:"targetPath"`
	Files      []precheckFileItem `json:"files"`
}

// precheckResultItem is a single result entry in the precheck response.
type precheckResultItem struct {
	RelativePath string `json:"relativePath"`
	Exists       bool   `json:"exists"`
}

// precheckResponse is the JSON body returned by FileUploadPrecheck.
type precheckResponse struct {
	Results []precheckResultItem `json:"results"`
}

// fileExistsWithSize reports whether filepath.Join(targetPath, relativePath)
// is a regular file whose on-disk size equals size.
//
// Returns false (without calling os.Stat) when:
//   - relativePath contains ".." or starts with "/"
//   - relativePath contains a protected folder name
//   - the resolved path would escape targetPath
//   - the path does not exist, is a directory, or has a different size
func fileExistsWithSize(targetPath, relativePath string, size int64) bool {
	// Reject traversal indicators.
	if strings.Contains(relativePath, "..") || strings.HasPrefix(relativePath, "/") {
		return false
	}
	// Reject protected folder names in relativePath.
	if protected, _ := containsProtectedName(relativePath); protected {
		return false
	}
	// Resolve and verify the path stays within targetPath.
	cleanTarget := filepath.Clean(targetPath)
	final := filepath.Clean(filepath.Join(cleanTarget, relativePath))
	sep := string(filepath.Separator)
	if !strings.HasPrefix(final, cleanTarget+sep) && final != cleanTarget {
		return false
	}
	// Stat the resolved path.
	info, err := os.Stat(final)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() == size
}

// FileUploadPrecheck handles POST /v2/nimoos/file/upload-precheck.
// It checks, for each file in the request, whether the file already exists
// on disk at join(targetPath, relativePath) with the exact same size.
// This allows the upload client to skip re-uploading files that are already present.
func FileUploadPrecheck(c echo.Context) error {
	var req precheckRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.TargetPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "targetPath is required")
	}
	// 不再检查 targetPath 是否为受保护文件夹:上传文件进入 Documents/Downloads/
	// Gallery/Media 等用户数据文件夹是正常用途。受保护语义只用于防止删/改/重建这些
	// 文件夹本身(见 file.go 的 rename/mkdir/create),不适用于「上传进入」。

	results := make([]precheckResultItem, 0, len(req.Files))
	for _, f := range req.Files {
		results = append(results, precheckResultItem{
			RelativePath: f.RelativePath,
			Exists:       fileExistsWithSize(req.TargetPath, f.RelativePath, f.Size),
		})
	}
	return c.JSON(http.StatusOK, precheckResponse{Results: results})
}
