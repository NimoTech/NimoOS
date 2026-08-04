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

// statPrecheck returns existence info for join(targetPath, relativePath):
//
//	exists: the path exists (regular file or directory) — same name is treated
//	  as a conflict, letting the frontend dialog decide with the user;
//	sizeMatch: it's a regular file and its size equals size (used by the
//	  frontend to hint that "the content may be the same");
//	isDir: it exists and is a directory (a same-name directory can't be
//	  overwritten by a file, the frontend should handle it as keep-both/skip).
//
// Path traversal / protected names / escaping the target are all reported as
// not-existing (consistent with the original implementation).
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
	// No longer checks whether targetPath is a protected folder: uploading files
	// into user data folders like Documents/Downloads/Gallery/Media is normal
	// usage. The protected-folder semantics only exist to prevent
	// deleting/renaming/recreating these folders themselves (see the
	// rename/mkdir/create handlers in file.go), and don't apply to "uploading into".

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
