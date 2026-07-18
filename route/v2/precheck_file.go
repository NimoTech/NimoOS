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
	SizeMatch    bool   `json:"size_match"`
	IsDir        bool   `json:"is_dir"`
}

// precheckResponse is the JSON body returned by FileUploadPrecheck.
type precheckResponse struct {
	Results []precheckResultItem `json:"results"`
}

// statPrecheck 返回 join(targetPath, relativePath) 的存在性信息:
//
//	exists: 路径存在(常规文件或目录)—— 同名即视为冲突,由前端弹窗让用户决策;
//	sizeMatch: 是常规文件且大小与 size 相等(供前端提示「内容可能相同」);
//	isDir: 存在且是目录(同名目录无法被文件覆盖,前端应按 keep-both/skip 处理)。
//
// 路径穿越/受保护名/越界时一律按不存在返回(与原实现一致)。
func statPrecheck(targetPath, relativePath string, size int64) (exists, sizeMatch, isDir bool) {
	// Reject traversal indicators.
	if strings.Contains(relativePath, "..") || strings.HasPrefix(relativePath, "/") {
		return false, false, false
	}
	// Reject protected folder names in relativePath.
	if protected, _ := containsProtectedName(relativePath); protected {
		return false, false, false
	}
	// Resolve and verify the path stays within targetPath.
	cleanTarget := filepath.Clean(targetPath)
	final := filepath.Clean(filepath.Join(cleanTarget, relativePath))
	sep := string(filepath.Separator)
	if !strings.HasPrefix(final, cleanTarget+sep) && final != cleanTarget {
		return false, false, false
	}
	// Stat the resolved path.
	info, err := os.Stat(final)
	if err != nil {
		return false, false, false
	}
	exists = true
	if info.IsDir() {
		isDir = true
		return exists, false, isDir
	}
	sizeMatch = info.Mode().IsRegular() && info.Size() == size
	return exists, sizeMatch, isDir
}

// FileUploadPrecheck handles POST /v2/nimoos/file/upload-precheck.
// It checks, for each file in the request, whether a same-named path already
// exists on disk at join(targetPath, relativePath) — same name is treated as
// a conflict regardless of size, so the upload client can prompt the user
// (overwrite / keep both / skip) before enqueuing the upload. size_match and
// is_dir are reported alongside exists to help the client's dialog decide
// which actions to offer (e.g. a same-name directory can't be overwritten).
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
		exists, sizeMatch, isDir := statPrecheck(req.TargetPath, f.RelativePath, f.Size)
		results = append(results, precheckResultItem{
			RelativePath: f.RelativePath,
			Exists:       exists,
			SizeMatch:    sizeMatch,
			IsDir:        isDir,
		})
	}
	return c.JSON(http.StatusOK, precheckResponse{Results: results})
}
