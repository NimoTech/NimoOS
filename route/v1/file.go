package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/robfig/cron/v3"
	"github.com/tidwall/gjson"

	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/sqlite"
	"github.com/NimoTech/NimoOS/pkg/utils"
	"github.com/NimoTech/NimoOS/pkg/utils/common_err"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service"
	model2 "github.com/NimoTech/NimoOS/service/model"
	"github.com/NimoTech/NimoOS/service/pathlock"
	uploadsvc "github.com/NimoTech/NimoOS/service/upload"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ListReq struct {
	model.PageReq
	Path string `json:"path" form:"path"`
	// Refresh bool   `json:"refresh"`
}

type ObjResp struct {
	Name       string                 `json:"name"`
	Size       int64                  `json:"size"`
	IsDir      bool                   `json:"is_dir"`
	IsSymlink  bool                   `json:"is_symlink"`
	Modified   time.Time              `json:"modified"`
	Sign       string                 `json:"sign"`
	Thumb      string                 `json:"thumb"`
	Type       int                    `json:"type"`
	Path       string                 `json:"path"`
	Date       time.Time              `json:"date"`
	Extensions map[string]interface{} `json:"extensions"`
}
type FsListResp struct {
	Content  []ObjResp `json:"content"`
	Total    int64     `json:"total"`
	Readme   string    `json:"readme,omitempty"`
	Write    bool      `json:"write,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Index    int       `json:"index"`
	Size     int       `json:"size"`
}

var (
	// Upgrade to WebSocket protocol
	upgraderFile = websocket.Upgrader{
		// Allow cross-origin (CORS) requests
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	conn *websocket.Conn
	err  error

	// uploadBatchStore is lazily initialized: reuses the global gorm singleton (same construction as route/v2.go).
	uploadBatchStore *uploadsvc.BatchStore
)

func getUploadBatchStore() *uploadsvc.BatchStore {
	if uploadBatchStore == nil {
		uploadBatchStore = uploadsvc.NewBatchStore(sqlite.GetDb(config.AppInfo.DBPath + "/db"))
	}
	return uploadBatchStore
}

// checkPathAccess returns an error response if the authenticated user is not
// allowed to access the given path. Returns nil if access is permitted.
// For localhost requests (JWT middleware skipped), both user_id and user_role
// are empty — these are internal service calls and are always permitted.
func checkPathAccess(ctx echo.Context, path string) error {
	role := ctx.Request().Header.Get("user_role")
	userID := ctx.Request().Header.Get("user_id")

	logger.Info("Checking path access",
		zap.String("path", path),
		zap.String("userID", userID),
		zap.String("role", role),
		zap.String("remoteIP", ctx.RealIP()))

	// 1. Internal calls / localhost loopback are exempt from permission checks.
	// Localhost bypass: JWT middleware skipped, no headers set.
	if role == "" && userID == "" {
		return nil
	}
	// 2. Super-admin privilege check.
	// Only User ID 1 (Root Admin) gets the "Skeleton Key" to all paths.
	// Other admins (like admin1) must still follow explicit folder grants for security isolation.
	isSuperAdmin := userID == "1"
	cleanPath := filepath.Clean(path)

	// Fast path: Super-admin gets everything.
	if isSuperAdmin {
		return nil
	}

	// 3. Base allow-rule check.
	// Base safety check (empty for users now that prefixes are removed).
	if utils.IsPathAllowed(cleanPath, false) {
		return nil
	}

	// 4. Explicit folder grant check.
	// Slow path: check whether the user has an explicit folder grant.
	if userID != "" {
		uid, err := strconv.Atoi(userID)
		if err == nil && service.MyService.User().IsPathGranted(uid, cleanPath) {
			return nil
		}
	}

	logger.Info("Access DENIED", zap.String("path", cleanPath), zap.String("userID", userID))
	ctx.JSON(http.StatusForbidden, model.Result{ //nolint:errcheck
		Success: common_err.INSUFFICIENT_PERMISSIONS,
		Message: common_err.GetMsg(common_err.INSUFFICIENT_PERMISSIONS),
	})
	return echo.ErrForbidden
}

// @Summary Read file
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string true "path"
// @Success 200 {string} string "ok"
// @Router /file/read [get]
func GetFilerContent(ctx echo.Context) error {
	filePath := ctx.QueryParam("path")
	if len(filePath) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}
	if err := checkPathAccess(ctx, filePath); err != nil {
		return err
	}
	if !file.Exists(filePath) {
		return ctx.JSON(http.StatusNotFound, model.Result{
			Success: common_err.FILE_DOES_NOT_EXIST,
			Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		})
	}
	// The file read task reads the file content into memory.
	info, err := ioutil.ReadFile(filePath)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.FILE_READ_ERROR,
			Message: common_err.GetMsg(common_err.FILE_READ_ERROR),
			Data:    err.Error(),
		})
	}
	result := string(info)

	return ctx.JSON(common_err.SUCCESS, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    result,
	})
}

func GetLocalFile(ctx echo.Context) error {
	path := ctx.QueryParam("path")
	if len(path) == 0 {
		return ctx.JSON(http.StatusOK, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}
	if !file.Exists(path) {
		return ctx.JSON(http.StatusOK, model.Result{
			Success: common_err.FILE_DOES_NOT_EXIST,
			Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		})
	}
	return ctx.File(path)
}

// @Summary download
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param format query string false "Compression format" Enums(zip,tar,targz)
// @Param files query string true "file list eg: filename1,filename2,filename3 "
// @Success 200 {string} string "ok"
// @Router /file/download [get]
func GetDownloadFile(ctx echo.Context) error {
	t := ctx.QueryParam("format")

	files := ctx.QueryParam("files")

	if len(files) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}
	list := strings.Split(files, ",")
	for _, v := range list {
		if err := checkPathAccess(ctx, v); err != nil {
			return err
		}
	}
	// Acquire read locks on all requested paths before existence checks so a
	// concurrent delete cannot race between the Exists() call and ctx.File().
	var dlUnlocks []func()
	for _, v := range list {
		dlUnlocks = append(dlUnlocks, pathlock.LockRead(v))
	}
	defer func() {
		for _, u := range dlUnlocks {
			u()
		}
	}()

	for _, v := range list {
		if !file.Exists(v) {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
				Success: common_err.FILE_DOES_NOT_EXIST,
				Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
			})
		}
	}
	// handles only single files not folders and multiple files
	if len(list) == 1 {

		filePath := list[0]
		info, err := os.Stat(filePath)
		if err != nil {
			return ctx.JSON(http.StatusOK, model.Result{
				Success: common_err.FILE_DOES_NOT_EXIST,
				Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
			})
		}
		if !info.IsDir() {

			// Open the file
			fileTmp, _ := os.Open(filePath)
			defer fileTmp.Close()

			// Get the file name
			fileName := path.Base(filePath)
			ctx.Response().Header().Add("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(fileName))
			ctx.File(filePath)
			return nil
		}
	}

	extension, ar, err := file.GetCompressionAlgorithm(t)
	if err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}

	// These three headers only apply to the archive branch; the single-file
	// branch above already returned, so reaching here means an archive response,
	// which doesn't affect ctx.File's own Content-Type. Content-Type is only
	// explicitly set for zip; other formats (tar/targz…, not currently sent by
	// the frontend) are left to net/http's content sniffing on first write, to
	// avoid mislabeling.
	if extension == ".zip" {
		ctx.Response().Header().Set("Content-Type", "application/zip")
	}
	ctx.Response().Header().Set("Content-Transfer-Encoding", "binary")
	ctx.Response().Header().Set("Cache-Control", "no-cache")

	name := downloadArchiveName(list, extension)
	// Must be set before ar.Create (which writes the response body); once the body is written, headers are locked.
	ctx.Response().Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))

	err = ar.Create(ctx.Response().Writer)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: common_err.GetMsg(common_err.SERVICE_ERROR),
			Data:    err.Error(),
		})
	}
	defer ar.Close()
	commonDir := file.CommonPrefix(filepath.Separator, list...)

	for _, fname := range list {
		err = file.AddFile(ar, fname, commonDir)
		if err != nil {
			log.Printf("Failed to archive %s: %v", fname, err)
		}
	}
	return nil
}

// downloadArchiveName decides the outward-facing filename for a batch download
// archive: a single folder → folder name + extension; multiple selections →
// common parent directory name + extension (Synology-style: picking 5 files in
// the photos directory → photos.zip); falls back to NimoOS when the common
// parent is root ("/" or empty).
// extension comes from GetCompressionAlgorithm (e.g. ".zip"/".tar.gz"), following the format param.
func downloadArchiveName(list []string, extension string) string {
	var base string
	if len(list) == 1 {
		base = filepath.Base(path.Clean(list[0]))
	} else {
		commonDir := file.CommonPrefix(filepath.Separator, list...)
		base = filepath.Base(commonDir)
	}
	if base == "/" || base == "." || base == "" {
		base = "NimoOS"
	}
	if extension == "" {
		extension = ".zip"
	}
	return base + extension
}

func GetDownloadSingleFile(ctx echo.Context) error {
	filePath := ctx.QueryParam("path")
	if len(filePath) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: common_err.GetMsg(common_err.INVALID_PARAMS),
		})
	}
	if err := checkPathAccess(ctx, filePath); err != nil {
		return err
	}
	fileName := path.Base(filePath)
	// c.Header("Content-Disposition", "inline")
	ctx.Response().Header().Add("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(fileName))

	fi, err := os.Open(filePath)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.FILE_DOES_NOT_EXIST,
			Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		})
	}
	defer fi.Close()

	node, err := os.Stat(filePath)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.FILE_DOES_NOT_EXIST,
			Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST),
		})
	}
	// Content-Type/Last-Modified/Content-Length are set by ServeContent itself
	// (based on filename extension/content sniffing + modtime + seeker length).
	// This used to manually set these three headers on ctx.Request().Header —
	// wrong object, a pure no-op — now removed.
	http.ServeContent(ctx.Response().Writer, ctx.Request(), fileName, node.ModTime(), fi)
	return nil
}

// @Summary Get directory listing
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string false "path"
// @Success 200 {string} string "ok"
// @Router /file/dirpath [get]
func DirPath(ctx echo.Context) error {
	var req ListReq
	path := ctx.QueryParam("path")
	req.Path = path
	req.Validate()
	if err := checkPathAccess(ctx, req.Path); err != nil {
		return err
	}
	info, err := service.MyService.System().GetDirPath(req.Path)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	shares := service.MyService.Shares().GetSharesList()
	sharesMap := make(map[string]string)
	for _, v := range shares {
		sharesMap[v.Path] = fmt.Sprint(v.ID)
	}
	// if len(info) <= (req.Page-1)*req.Size {
	// 	return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: "page out of range"})
	// 	return
	// }
	forEnd := req.Index * req.Size
	if forEnd > len(info) {
		forEnd = len(info)
	}
	for i := (req.Index - 1) * req.Size; i < forEnd; i++ {
		if v, ok := sharesMap[info[i].Path]; ok {
			ex := make(map[string]interface{})
			shareEx := make(map[string]string)
			shareEx["shared"] = "true"
			shareEx["id"] = v
			ex["share"] = shareEx
			ex["mounted"] = false
			info[i].Extensions = ex
		}
	}
	// Upload-interrupted badge: for any of the current user's own interrupted
	// batches, if a missing file's path falls under a given child entry, inject
	// extensions.upload on that entry so the frontend can overlay a "broken"
	// badge. Query failure only degrades gracefully, never errors — the badge
	// is informational and must not derail the main list-directory flow.
	if broken, berr := getUploadBatchStore().BrokenChildren(req.Path, ctx.Request().Header.Get("user_id")); berr == nil && len(broken) > 0 {
		for i := (req.Index - 1) * req.Size; i < forEnd; i++ {
			bid, ok := broken[info[i].Name]
			if !ok {
				continue
			}
			ex := info[i].Extensions
			if ex == nil {
				ex = make(map[string]interface{})
			}
			ex["upload"] = map[string]interface{}{"broken": true, "batchId": bid}
			info[i].Extensions = ex
		}
	}
	if strings.HasPrefix(req.Path, "/mnt") || strings.HasPrefix(req.Path, "/media") {
		for i := (req.Index - 1) * req.Size; i < forEnd; i++ {
			ex := info[i].Extensions
			if ex == nil {
				ex = make(map[string]interface{})
			}
			mounted := service.IsMounted(info[i].Path)
			ex["mounted"] = mounted
			info[i].Extensions = ex
		}
	}
	// Hide the files or folders in operation
	fileQueue := make(map[string]string)
	for _, v := range service.PeekOps() {
		item, ok := service.FileQueue.Load(v)
		if !ok {
			continue
		}
		vt := item.(model.FileOperate)
		for _, i := range vt.Item {
			lastPath := i.From[strings.LastIndex(i.From, "/")+1:]
			fileQueue[vt.To+"/"+lastPath] = i.From
		}
	}

	pathList := []ObjResp{}
	for i := (req.Index - 1) * req.Size; i < forEnd; i++ {
		if info[i].Name == ".temp" && info[i].IsDir {
			continue
		}

		if _, ok := fileQueue[info[i].Path]; !ok {
			t := ObjResp{}
			t.IsDir = info[i].IsDir
			t.IsSymlink = info[i].IsSymlink
			t.Name = info[i].Name
			t.Modified = info[i].Date
			t.Date = info[i].Date
			t.Size = info[i].Size
			t.Path = info[i].Path
			t.Extensions = info[i].Extensions
			pathList = append(pathList, t)

		}
	}
	flist := FsListResp{
		Content: pathList,
		Total:   int64(len(info)),
		// Readme:   "",
		// Write:    true,
		// Provider: "local",
		Index: req.Index,
		Size:  req.Size,
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: flist})
}

// @Summary rename file or dir
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param oldpath body string true "path of old"
// @Param newpath body string true "path of new"
// @Success 200 {string} string "ok"
// @Router /file/rename [put]
func RenamePath(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	op := json["old_path"]
	np := json["new_path"]
	if len(op) == 0 || len(np) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	if isProtectedName(filepath.Base(op)) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(op))})
	}
	if isProtectedName(filepath.Base(np)) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(np))})
	}
	if err := checkPathAccess(ctx, op); err != nil {
		return err
	}
	if err := checkPathAccess(ctx, np); err != nil {
		return err
	}
	mounted := service.IsMounted(op)
	if mounted {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.MOUNTED_DIRECTIORIES, Message: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES), Data: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES)})
	}

	// Must be called before the actual rename: the cache key includes the old
	// path's mtime/size, and after rename the old path no longer exists, so
	// the original key can never be recomputed.
	file.PurgeThumbCacheEntry(op)
	success, err := service.MyService.System().RenameFile(op, np)
	if success == common_err.SUCCESS {
		// After a successful rename/move, share records hanging off op itself or
		// its subtree still point their path at the old location, which turns
		// into a permanently unopenable dangling entry in the "Shared" tab —
		// the correct semantics is to rewrite the path, not delete it (mirrors
		// DeleteShareByPath used when a directory is deleted).
		if shares := service.MyService.Shares(); shares != nil {
			shares.RewriteSharePathPrefix(op, np)
		}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: success, Message: common_err.GetMsg(success), Data: err})
}

// @Summary create folder
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path body string true "path of folder"
// @Success 200 {string} string "ok"
// @Router /file/mkdir [post]
func MkdirAll(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	path := json["path"]
	var code int
	if len(path) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	if isProtectedName(filepath.Base(path)) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(path))})
	}
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	// decodedPath, err := url.QueryUnescape(path)
	// if err != nil {
	// 	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	// 	return
	// }
	code, _ = service.MyService.System().MkdirAll(path)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: code, Message: common_err.GetMsg(code)})
}

// @Summary create file
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path body string true "path of folder (path need to url encode)"
// @Success 200 {string} string "ok"
// @Router /file/create [post]
func PostCreateFile(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	path := json["path"]
	var code int
	if len(path) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	if isProtectedName(filepath.Base(path)) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(path))})
	}
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	// decodedPath, err := url.QueryUnescape(path)
	// if err != nil {
	// 	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	// 	return
	// }
	code, _ = service.MyService.System().CreateFile(path)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: code, Message: common_err.GetMsg(code)})
}

// @Summary upload file
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path formData string false "file path"
// @Param file formData file true "file"
// @Success 200 {string} string "ok"
// @Router /file/upload [get]
func GetFileUpload(ctx echo.Context) error {
	relative := ctx.QueryParam("relativePath")
	fileName := ctx.QueryParam("filename")
	chunkNumber := ctx.QueryParam("chunkNumber")
	totalChunks, _ := strconv.Atoi(utils.DefaultQuery(ctx, "totalChunks", "0"))
	path := ctx.QueryParam("path")
	if protected, name := containsProtectedName(relative); protected {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' in path '%s' is protected", name, relative)})
	}
	dirPath := ""
	hash := file.GetHashByContent([]byte(fileName))
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	if file.Exists(path + "/" + relative) {
		return ctx.JSON(http.StatusConflict, model.Result{Success: http.StatusConflict, Message: common_err.GetMsg(common_err.FILE_ALREADY_EXISTS)})
	}
	tempDir := filepath.Join(path, ".temp", hash+strconv.Itoa(totalChunks)) + "/"
	if fileName != relative {
		dirPath = strings.TrimSuffix(relative, fileName)
		tempDir += dirPath
		file.MkDir(path + "/" + dirPath)
	}
	tempDir += chunkNumber
	if !file.CheckNotExist(tempDir) {
		return ctx.JSON(200, model.Result{Success: 200, Message: common_err.GetMsg(common_err.FILE_ALREADY_EXISTS)})
	}

	return ctx.JSON(204, model.Result{Success: 204, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary upload file
// @Produce  application/json
// @Accept  multipart/form-data
// @Tags file
// @Security ApiKeyAuth
// @Param path formData string false "file path"
// @Param file formData file true "file"
// @Success 200 {string} string "ok"
// @Router /file/upload [post]
func PostFileUpload(ctx echo.Context) error {
	f, _, err := ctx.Request().FormFile("file")
	if err != nil {
		logger.Error("failed to read uploaded file", zap.Error(err))
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: "failed to read uploaded file: " + err.Error()})
	}
	relative := ctx.FormValue("relativePath")
	fileName := ctx.FormValue("filename")
	totalChunks, _ := strconv.Atoi(utils.DefaultPostForm(ctx, "totalChunks", "0"))
	chunkNumber := ctx.FormValue("chunkNumber")
	if protected, name := containsProtectedName(relative); protected {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' in path '%s' is protected", name, relative)})
	}
	dirPath := ""
	path := ctx.FormValue("path")

	hash := file.GetHashByContent([]byte(fileName))

	if len(path) == 0 {
		logger.Error("path should not be empty")
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	tempDir := filepath.Join(path, ".temp", hash+strconv.Itoa(totalChunks)) + "/"

	if fileName != relative {
		dirPath = strings.TrimSuffix(relative, fileName)
		tempDir += dirPath
		if err := file.MkDir(path + "/" + dirPath); err != nil {
			logger.Error("error when trying to create `"+path+"/"+dirPath+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	path += "/" + relative

	if !file.CheckNotExist(tempDir + chunkNumber) {
		if err := file.RMDir(tempDir + chunkNumber); err != nil {
			logger.Error("error when trying to remove existing `"+tempDir+chunkNumber+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	if totalChunks > 1 {
		if err := file.IsNotExistMkDir(tempDir); err != nil {
			logger.Error("error when trying to create `"+tempDir+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		// O_EXCL: if this chunk was already written by a duplicate request, treat
		// it as success (idempotent) rather than overwriting in-flight data.
		out, err := os.OpenFile(tempDir+chunkNumber, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				// Chunk already present — idempotent success.
				return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
			}
			logger.Error("error when trying to open `"+tempDir+chunkNumber+"` for creation", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		if _, err := io.Copy(out, f); err != nil {
			out.Close()
			os.Remove(tempDir + chunkNumber) // clean up partial chunk
			logger.Error("error when trying to write to `"+tempDir+chunkNumber+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
		out.Close()

		fileNum, err := ioutil.ReadDir(tempDir)
		if err != nil {
			logger.Error("error when trying to read number of files under `"+tempDir+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		if totalChunks == len(fileNum) {
			// All chunks present — merge synchronously so any failure is returned
			// to the client instead of being silently lost in a goroutine.
			unlock := pathlock.LockWrite(path)
			defer unlock()
			if err := file.SpliceFiles(tempDir, path, totalChunks, 1); err != nil {
				logger.Error("error when trying to splice files under `"+tempDir+"`", zap.Error(err))
				return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
			}
			if err := file.RMDir(tempDir); err != nil {
				logger.Error("error when trying to remove `"+tempDir+"`", zap.Error(err))
			}
			// Final merged file has landed at `path` — notify Photos.
			go service.PublishMediaCreated([]string{path})
		}
	} else {
		unlock := pathlock.LockWrite(path)
		defer unlock()
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			logger.Error("error when trying to open `"+path+"` for creation", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		defer out.Close()

		if _, err := io.Copy(out, f); err != nil { // recommend to use https://github.com/iceber/iouring-go for faster copy
			logger.Error("error when trying to write to `"+path+"`", zap.Error(err))
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
		}
		// Single-shot direct write landed at `path` — notify Photos.
		go service.PublishMediaCreated([]string{path})
	}
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

func PostFileOctet(ctx echo.Context) error {
	content_length := ctx.Request().ContentLength
	if content_length <= 0 || content_length > 1024*1024*1024*2*1024 {
		log.Printf("content_length error\n")
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.CLIENT_ERROR), Data: "content_length error"})
	}
	content_type_, has_key := ctx.Request().Header["Content-Type"]
	if !has_key {
		log.Printf("Content-Type error\n")
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.CLIENT_ERROR), Data: "Content-Type error"})
	}
	if len(content_type_) != 1 {
		log.Printf("Content-Type count error\n")
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.CLIENT_ERROR), Data: "Content-Type count error"})
	}
	content_type := content_type_[0]
	const BOUNDARY string = "; boundary="
	loc := strings.Index(content_type, BOUNDARY)
	if loc == -1 {
		log.Printf("Content-Type error, no boundary\n")
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.CLIENT_ERROR), Data: "Content-Type error, no boundary"})
	}
	boundary := []byte(content_type[(loc + len(BOUNDARY)):])
	log.Printf("[%s]\n\n", boundary)
	read_data := make([]byte, 1024*24)
	var read_total int = 0
	for {
		file_header, file_data, err := file.ParseFromHead(read_data, read_total, append(boundary, []byte("\r\n")...), ctx.Request().Body)
		if err != nil {
			log.Printf("%v", err)
		}
		log.Printf("file :%s\n", file_header)
		//
		//os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
		f, err := os.OpenFile(file_header["path"]+"/"+file_header["filename"], os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			log.Printf("create file fail:%v\n", err)
		}
		f.Write(file_data)
		file_data = nil

		temp_data, reach_end, err := file.ReadToBoundary(boundary, ctx.Request().Body, f)
		f.Close()
		if err != nil {
			log.Printf("%v\n", err)
		}
		if reach_end {
			break
		} else {
			copy(read_data[0:], temp_data)
			read_total = len(temp_data)
			continue
		}
	}
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary copy or move file
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param body body model.FileOperate true "type:move,copy"
// @Success 200 {string} string "ok"
// @Failure 409 {object} model.Result "identical file operation already in progress"
// @Router /file/operate [post]
func PostOperateFileOrDir(ctx echo.Context) error {
	list := model.FileOperate{}
	ctx.Bind(&list)

	if len(list.Item) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	if err := checkPathAccess(ctx, list.To); err != nil {
		return err
	}
	for _, item := range list.Item {
		if err := checkPathAccess(ctx, item.From); err != nil {
			return err
		}
		if isProtectedName(filepath.Base(item.From)) {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(item.From))})
		}
		if isAncestorOfSystemPath(item.From) {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("Folder '%s' contains a system-critical directory and cannot be moved", filepath.Base(item.From))})
		}
	}
	if list.To == list.Item[0].From[:strings.LastIndex(list.Item[0].From, "/")] {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SOURCE_DES_SAME, Message: common_err.GetMsg(common_err.SOURCE_DES_SAME)})
	}

	var total int64 = 0
	for i := 0; i < len(list.Item); i++ {

		size, err := file.GetFileOrDirSize(list.Item[i].From)
		if err != nil {
			continue
		}
		list.Item[i].Size = size
		total += size
		if list.Type == "move" {
			mounted := service.IsMounted(list.Item[i].From)
			if mounted {
				return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.MOUNTED_DIRECTIORIES, Message: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES), Data: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES)})
			}
		}
	}

	list.TotalSize = total
	list.ProcessedSize = 0

	uid := uuid.NewString()
	isFirst, duplicate := service.EnqueueOp(uid, list)
	if duplicate {
		return ctx.JSON(common_err.CONFLICT, model.Result{Success: common_err.DUPLICATE_FILE_OPERATION, Message: common_err.GetMsg(common_err.DUPLICATE_FILE_OPERATION)})
	}
	if isFirst {
		go service.ExecOpFile()
		go service.CheckFileStatus()
		go service.MyService.Notify().SendFileOperateNotify(false)
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary delete file
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param body body string true "paths eg ["/a/b/c","/d/e/f"]"
// @Success 200 {string} string "ok"
// @Router /file/delete [delete]
// deletedMediaExts mirrors NimoOS-Photos' supportedExts: the image/video types
// it indexes. Used to scope the nimoos:media:deleted event to deletions Photos
// actually cares about. Keep the two lists in sync when adding formats.
var deletedMediaExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".webp": true,
	".gif": true, ".bmp": true, ".tiff": true, ".tif": true, ".avif": true,
	".mp4": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true,
	".m4v": true, ".3gp": true,
}

// jsonMangledName reproduces encoding/json's handling of invalid UTF-8: each
// invalid byte is replaced with U+FFFD, while valid multi-byte runes are kept
// as-is. Used to map the real on-disk directory name to "the name the frontend
// sees via the list API", so deletion can match it back.
//
// Doesn't use strings.ToValidUTF8 — it collapses a run of consecutive invalid
// bytes into a single U+FFFD, which doesn't match JSON's byte-by-byte
// replacement semantics (encoding/json's string encoder also advances via
// utf8.DecodeRuneInString rune by rune; on an invalid byte that function
// returns (RuneError, 1), i.e. each invalid byte produces its own U+FFFD — the
// loop here matches that exactly).
func jsonMangledName(name string) string {
	if utf8.ValidString(name) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		b.WriteRune(r) // on an invalid byte DecodeRuneInString returns (RuneError, 1), giving byte-by-byte replacement
		i += size
	}
	return b.String()
}

// resolveDeletePath resolves a client-supplied delete path to the real on-disk
// path. If the path exists → returned as-is. If it doesn't (Lstat ENOENT) →
// search the parent directory for a real entry whose "JSON-mangled name
// exactly equals the requested name": a single match returns the real path;
// zero matches returns os.ErrNotExist; multiple matches returns an ambiguity
// error (never guess a delete). Any other Lstat error is returned as-is.
func resolveDeletePath(p string) (string, error) {
	if _, err := os.Lstat(p); err == nil {
		return p, nil
	} else if !os.IsNotExist(err) {
		return p, err
	}

	dir := filepath.Dir(p)
	base := filepath.Base(p)

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Can't even read the parent directory — no room for rescue.
		return p, os.ErrNotExist
	}

	var matches []string
	for _, entry := range entries {
		if jsonMangledName(entry.Name()) == base {
			matches = append(matches, filepath.Join(dir, entry.Name()))
		}
	}

	switch len(matches) {
	case 0:
		return p, os.ErrNotExist
	case 1:
		return matches[0], nil
	default:
		return p, fmt.Errorf("multiple entries match the requested name %q; delete it via terminal", base)
	}
}

func DeleteFile(ctx echo.Context) error {
	paths := []string{}
	ctx.Bind(&paths)
	if len(paths) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	for _, p := range paths {
		if err := checkPathAccess(ctx, p); err != nil {
			return err
		}
		if isProtectedName(filepath.Base(p)) {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(p))})
		}
		if isAncestorOfSystemPath(p) {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("Folder '%s' contains a system-critical directory and cannot be deleted", filepath.Base(p))})
		}
	}

	// Resolve each requested path to the real on-disk path. When it doesn't
	// exist, eliminate the "false success": return FILE_DOES_NOT_EXIST directly
	// instead of letting the later os.RemoveAll silently return nil against a
	// nonexistent path. A rescued real path may have a different name from the
	// requested one, so re-run the protected-name/system-path-ancestor checks
	// (checkPathAccess is semantically equivalent based on the parent directory
	// path, no need to rerun it).
	for i, p := range paths {
		resolved, err := resolveDeletePath(p)
		if err != nil {
			if os.IsNotExist(err) {
				return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DOES_NOT_EXIST, Message: common_err.GetMsg(common_err.FILE_DOES_NOT_EXIST)})
			}
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
		if resolved != p {
			logger.Info("delete path rescued via mangled-name match", zap.String("requested", p), zap.String("resolved", resolved))
			if isProtectedName(filepath.Base(resolved)) {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("System default folder name '%s' is protected", filepath.Base(resolved))})
			}
			if isAncestorOfSystemPath(resolved) {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: fmt.Sprintf("Folder '%s' contains a system-critical directory and cannot be deleted", filepath.Base(resolved))})
			}
		}
		paths[i] = resolved
	}

	for _, v := range paths {
		mounted := service.IsMounted(v)
		if mounted {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.MOUNTED_DIRECTIORIES, Message: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES), Data: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES)})
		}
	}

	for _, v := range paths {
		unlock := pathlock.LockWrite(v)
		// Must be called before RemoveAll: the cache key includes mtime/size, and
		// once the file is gone the original key can never be recomputed. For a
		// directory, its children aren't walked to clear their cache immediately
		// (cost consideration) — left to the LRU as a fallback.
		file.PurgeThumbCacheEntry(v)
		err := os.RemoveAll(v)
		unlock()
		if err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: err})
		}
		// Once a directory is deleted, Samba share records hanging off it or its
		// subtree become permanently unopenable dangling entries in the "Shared"
		// tab (verified: after deleting a parent directory, its already-shared
		// child folders still show up in the tab). Clear them and rewrite the smb
		// config in sync; DeleteShareByPath has its own "/" boundary, so it
		// doesn't harm sibling directories with the same prefix.
		if shares := service.MyService.Shares(); shares != nil {
			shares.DeleteShareByPath(v)
		}
	}

	// Publish the deleted media paths so Photos can clean up its index/CLIP
	// vectors in real time. Scoped to media on purpose (hence the event name):
	// only paths that are image/video files — or extensionless paths that may
	// be directories whose contents can't be inspected after RemoveAll — are
	// included; a delete batch with no such path publishes nothing.
	// properties["paths"] is a JSON array of absolute paths. Fire-and-forget:
	// failure to publish must never fail the delete itself.
	go func(deleted []string) {
		media := deleted[:0]
		for _, p := range deleted {
			ext := strings.ToLower(filepath.Ext(filepath.Base(p)))
			if ext == "" || deletedMediaExts[ext] {
				media = append(media, p)
			}
		}
		service.PublishMediaPathsEvent("nimoos:media:deleted", media)
	}(append([]string(nil), paths...))

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary update file
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path body string true "path"
// @Param content body string true "content"
// @Success 200 {string} string "ok"
// @Router /file/update [put]
func PutFileContent(ctx echo.Context) error {
	fi := model.FileUpdate{}
	ctx.Bind(&fi)

	// path := ctx.FormValue("path")
	// content := ctx.FormValue("content")
	if err := checkPathAccess(ctx, fi.FilePath); err != nil {
		return err
	}
	if !file.Exists(fi.FilePath) {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.FILE_ALREADY_EXISTS)})
	}
	// err := os.Remove(path)
	f, err := os.Stat(fi.FilePath)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.FILE_ALREADY_EXISTS)})
	}
	fm := f.Mode()
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: err})
	}
	os.OpenFile(fi.FilePath, os.O_CREATE, fm)
	err = file.WriteToFullPath([]byte(fi.FileContent), fi.FilePath, fm)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary image thumbnail/original image
// @Produce  application/json
// @Accept  application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string true "path"
// @Param type query string false "original,thumbnail" Enums(original,thumbnail)
// @Success 200 {string} string "ok"
// @Router /file/image [get]
func GetFileImage(ctx echo.Context) error {
	t := ctx.QueryParam("type")
	path := ctx.QueryParam("path")
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	if !file.Exists(path) {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.FILE_ALREADY_EXISTS)})
	}
	if t == "thumbnail" {
		// BF23: previously this branch wrote the (small) thumbnail bytes
		// and then fell through — with no `return` — into the code below,
		// which re-opened and wrote the *full original file* right after
		// it. The response was thumbnail-bytes + full-original-bytes
		// concatenated, which is why a 723KB photo came back as ~729KB
		// instead of a real ~20-50KB thumbnail: it was never actually
		// short-circuiting.
		//
		// Cache hit/miss + generation now lives in file.GetOrCreateThumbnailCached
		// (disk cache under file.ThumbCacheDir, keyed by path+mtime+size,
		// singleflight-guarded, with a short-TTL negative cache for
		// formats/files that fail to decode e.g. HEIC).
		if cachedPath, ok := file.GetOrCreateThumbnailCached(path); ok {
			ctx.Response().Header().Set("Cache-Control", "public, max-age=86400")
			return ctx.File(cachedPath)
		}
		// Thumbnail generation failed (unsupported format such as HEIC, or
		// a corrupt file) — fall back to serving the original below.
	}
	f, err := os.Open(path)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	defer f.Close()
	data, err := ioutil.ReadAll(f)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	ctx.Response().Header().Set("Cache-Control", "public, max-age=86400")
	ctx.Response().Writer.Write(data)
	return nil
}

// DeleteOperateFileOrDir cancels a file move/copy task. id == "0" cancels
// every in-flight task (queued and currently executing) and clears the
// queue; any other id cancels just that task. A queued task that has not
// started yet is removed outright (unchanged from before); a task that is
// currently executing has its context cancelled — the running FileOperate
// goroutine observes that and performs its own cleanup, terminal
// notification, and retirement (see service.CancelOp/CancelAllOps and
// service.FileOperate). Cancelling an unknown id or an already-completed
// task is a no-op; the response shape is unchanged either way.
func DeleteOperateFileOrDir(ctx echo.Context) error {
	id := ctx.Param("id")
	if id == "0" {
		service.CancelAllOps()
	} else {
		service.CancelOp(id)
	}

	go service.MyService.Notify().SendFileOperateNotify(true)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

func GetSize(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	path := json["path"]
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	size, err := file.GetFileOrDirSize(path)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: size})
}

func GetFileCount(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	path := json["path"]
	if err := checkPathAccess(ctx, path); err != nil {
		return err
	}
	list, err := ioutil.ReadDir(path)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: len(list)})
}

type CenterHandler struct {
	// Broadcast channel — when data arrives, loop over every user and broadcast it out
	broadcast chan []byte
	// Register channel — when a user connects, push it into the clients map
	register chan *Client
	// Unregister channel — when a user closes/errors the connection, evict it from the clients map
	unregister chan *Client
	// Client set — each user also has two goroutines running, monitoring its read/write state
	clients map[string]*Client
}

type Client struct {
	handler *CenterHandler
	conn    *websocket.Conn
	// Each user's own running-loop state monitoring
	send         chan []byte
	ID           string       `json:"id"`
	IP           string       `json:"ip"`
	Name         service.Name `json:"name"`
	RtcSupported bool         `json:"rtcSupported"`
	TimerId      int          `json:"timerId"`
	// mu guards LastBeat and superseded: readPump (on receiving pong/any
	// message) and monitoring's heartbeat timeout check are two different
	// goroutines; both reads and writes must go through the lock.
	mu       sync.Mutex
	LastBeat time.Time `json:"lastBeat"`
	// superseded marks a session that a newer connection under the same peer
	// id has replaced. Its readPump must stay quiet on the way out: the id it
	// would announce as gone belongs to a device that is online again.
	superseded bool
}

// markSuperseded flags this session as replaced by a newer one for the same id.
func (c *Client) markSuperseded() {
	c.mu.Lock()
	c.superseded = true
	c.mu.Unlock()
}

// isSuperseded reports whether a newer session has taken over this peer id.
func (c *Client) isSuperseded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.superseded
}

// touchLastBeat records a heartbeat/activity timestamp for monitoring's timeout check to read.
func (c *Client) touchLastBeat(now time.Time) {
	c.mu.Lock()
	c.LastBeat = now
	c.mu.Unlock()
}

// snapshotLastBeat reads the current heartbeat time (locked, to avoid a data race with touchLastBeat).
func (c *Client) snapshotLastBeat() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.LastBeat
}

type PeerModel struct {
	ID           string       `json:"id"`
	Name         service.Name `json:"name"`
	RtcSupported bool         `json:"rtcSupported"`
}

func ConnectWebSocket(ctx echo.Context) error {
	peerId := ctx.QueryParam("peer")
	writer := ctx.Response().Writer
	request := ctx.Request()
	key := uuid.NewString()
	// peerModel := service.MyService.Peer().GetPeerByUserAgent(ctx.Request().UserAgent())
	peerModel := model2.PeerDriveDBModel{}
	name := service.GetName(request)
	if conn, err = upgraderFile.Upgrade(writer, request, writer.Header()); err != nil {
		log.Println(err)
	}
	client := &Client{handler: &handler, conn: conn, send: make(chan []byte, 256), ID: service.GetPeerId(request, key), IP: service.GetIP(request), Name: name, RtcSupported: true, TimerId: 0, LastBeat: time.Now()}
	if peerId != "" || len(peerModel.ID) > 0 {
		if len(peerModel.ID) == 0 {
			peerModel = service.MyService.Peer().GetPeerByID(peerId)
		}
		if len(peerModel.ID) > 0 {
			key = peerId
			client.ID = peerModel.ID
			client.Name = service.GetNameByDB(peerModel)
			// Keep the row young: a device in daily use must outrank rows for
			// devices that never came back when the table overflows.
			service.MyService.Peer().TouchPeer(client.ID, time.Now().Unix())
		}
	}
	list := service.MyService.Peer().GetPeers()
	if len(peerModel.ID) == 0 {
		// The row must be keyed by the id the client is actually told to use
		// (client.ID), not by the locally generated `key` — a row under any
		// other id can never be found by the `?peer=` lookup on reconnect, so
		// the device would be handed a new identity every time.
		peerModel.ID = client.ID
		peerModel.DisplayName = name.DisplayName
		peerModel.DeviceName = name.DeviceName
		peerModel.Model = name.Model
		peerModel.OS = name.OS
		peerModel.Browser = name.Browser
		peerModel.UserAgent = ctx.Request().UserAgent()
		peerModel.IP = client.IP
		service.MyService.Peer().CreatePeer(&peerModel)
		list = append(list, peerModel)
	}

	cookie := http.Cookie{
		Name:  "peerid",
		Value: key,
		Path:  "/",
	}
	http.SetCookie(writer, &cookie)
	if len(list) > 10 {
		ids := make([]string, 0, len(list))
		for _, v := range list {
			ids = append(ids, v.ID)
		}
		isOnline := func(id string) bool { _, ok := handler.clients[id]; return ok }
		for _, id := range kickoutIDs(ids, isOnline, client.ID, 10) {
			service.MyService.Peer().DeletePeer(id)
		}
		// if len(kickoutList) > 0 {
		// 	other := make(map[string]interface{})
		// 	other["type"] = "kickout"
		// 	other["peers"] = kickoutList
		// 	otherBy, err := json.Marshal(other)
		// 	fmt.Println(err)
		// 	client.handler.broadcast <- otherBy
		// }
	}
	list = service.MyService.Peer().GetPeers()
	if len(list) > 10 {
		fmt.Println("still overflowing after resolving", list)
	}
	currentPeer := PeerModel{ID: client.ID, Name: client.Name, RtcSupported: client.RtcSupported}
	pmsg := make(map[string]interface{})
	pmsg["type"] = "peer-joined"
	pmsg["peer"] = currentPeer
	pby, err := json.Marshal(pmsg)
	fmt.Println(err)
	for _, v := range handler.clients {
		v.send <- pby
	}
	// client.handler.broadcast <- pby
	clients := []PeerModel{}
	for _, v := range client.handler.clients {
		if _, ok := handler.clients[v.ID]; ok {
			clients = append(clients, PeerModel{ID: v.ID, Name: v.Name, RtcSupported: v.RtcSupported})
		}
	}

	other := make(map[string]interface{})
	other["type"] = "peers"
	other["peers"] = clients
	otherBy, err := json.Marshal(other)
	fmt.Println(err)
	client.send <- otherBy

	// Push to the monitoring center to register into the client set
	handler.register <- client

	client.send <- []byte(`{"type":"ping"}`)

	data := make(map[string]string)
	data["displayName"] = client.Name.DisplayName
	data["deviceName"] = client.Name.DeviceName
	data["id"] = client.ID
	msg := make(map[string]interface{})
	msg["type"] = "display-name"
	msg["message"] = data
	by, _ := json.Marshal(msg)
	client.send <- by

	// Each client spins up 2 new goroutines, monitoring read/write state
	go client.writePump()
	go client.readPump()
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

var handler = CenterHandler{
	broadcast:  make(chan []byte),
	register:   make(chan *Client),
	unregister: make(chan *Client),
	clients:    make(map[string]*Client),
}

func init() {
	// Spin up a goroutine to listen on the 3 channels: register, unregister, message
	go handler.monitoring()

	crontab := cron.New(cron.WithSeconds()) // second-level precision
	// Define the task function invoked by the timer

	task := func() {
		handler.broadcast <- []byte(`{"type":"ping"}`)
	}
	// Scheduled task
	spec := "*/30 * * * * ?" // cron expression, once every 30 seconds
	// Add the scheduled task,
	crontab.AddFunc(spec, task)
	// Start the timer
	crontab.Start()
}

func (c *Client) writePump() {
	defer func() {
		c.handler.unregister <- c

		c.conn.Close()
	}()
	for {
		// New message pushed in from broadcast, immediately push it to self via websocket
		message, _ := <-c.send
		fmt.Println("pushing message", string(message), "1")
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
}

// Read: listen for whether the client has pushed content to the server
func (c *Client) readPump() {
	defer func() {
		c.handler.unregister <- c
		c.conn.Close()
	}()
	for {
		// Loop listening for whether this user wants to speak
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			// Handling for abnormal close
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			// A session replaced by a newer one for the same peer id leaves
			// silently: announcing peer-left here would tell everyone the
			// device is gone moments after it came back, and nothing would
			// re-announce it.
			if !c.isSuperseded() {
				c.handler.broadcast <- []byte(`{"type":"peer-left","peerId":"` + c.ID + `"}`)
			}
			break
		}
		// If so, push to the broadcast center, which then pushes to every user

		t := gjson.GetBytes(message, "type")
		if t.String() == "disconnect" {
			c.handler.unregister <- c
			c.conn.Close()
			// clients := []Client{}
			// list := service.MyService.Peer().GetPeers()
			// for _, v := range list {
			// 	if _, ok := handler.clients[v.ID]; ok {
			// 		clients = append(clients, *handler.clients[v.ID])
			// 	} else {
			// 		clients = append(clients, Client{ID: v.ID, Name: service.GetNameByDB(v), IP: v.IP, Offline: true})
			// 	}
			// }
			// other := make(map[string]interface{})
			// other["type"] = "peers"
			// other["peers"] = clients
			// otherBy, err := json.Marshal(other)
			// fmt.Println(err)
			// A session replaced by a newer one for the same peer id leaves
			// silently: announcing peer-left here would tell everyone the
			// device is gone moments after it came back, and nothing would
			// re-announce it.
			if !c.isSuperseded() {
				c.handler.broadcast <- []byte(`{"type":"peer-left","peerId":"` + c.ID + `"}`)
			}
			// c.handler.broadcast <- otherBy
			break
		} else if t.String() == "pong" {
			c.touchLastBeat(time.Now())
			continue
		}
		to := gjson.GetBytes(message, "to")

		if len(to.String()) > 0 {
			toC := c.handler.clients[to.String()]
			if toC == nil {
				continue
			}
			data := map[string]interface{}{}
			json.Unmarshal(message, &data)
			data["sender"] = c.ID
			delete(data, "to")
			message, err = json.Marshal(data)
			toC.send <- message
			continue
		}

		c.handler.broadcast <- message
	}
}

// heartbeatTimeout is the threshold for judging a connection dead-but-alive:
// ping broadcasts once every 30s, tolerating 3 cycles with no response
// (accounting for transient network jitter); beyond that it's judged dead
// (scenarios with no TCP FIN, such as an unplugged cable or system sleep).
const heartbeatTimeout = 90 * time.Second

// staleClientIDs returns clients whose heartbeat has timed out: lastBeats maps
// id → last heartbeat time; now-lastBeat > timeout is judged dead. Exactly
// equal to timeout does not count as a timeout.
func staleClientIDs(lastBeats map[string]time.Time, now time.Time, timeout time.Duration) []string {
	var stale []string
	for id, lastBeat := range lastBeats {
		if now.Sub(lastBeat) > timeout {
			stale = append(stale, id)
		}
	}
	return stale
}

func (ch *CenterHandler) monitoring() {
	// Sweeps for heartbeat-timed-out dead connections on the same cycle as the
	// ping broadcast (30s). ch.clients is only read/written inside the
	// monitoring goroutine, so the heartbeat-timeout eviction logic is also
	// handled in this select loop, with no need for extra locking on the
	// clients map.
	staleTicker := time.NewTicker(30 * time.Second)
	defer staleTicker.Stop()
	for {
		select {
		// Register: when a new user connects it's pushed into the register channel; here we receive the pushed-in user pointer
		case client := <-ch.register:
			// The replaced session keeps its socket on purpose. Closing it
			// would make that page reconnect five seconds later and take the
			// id straight back — two tabs of one device (they share the peer
			// id, it lives in localStorage) would then evict each other for
			// ever. Dropped from the client set it is already harmless: it is
			// no longer listed as a device, dialled, or broadcast to.
			ch.registerClient(client)
			// Unregister: closing the connection or a connection error pushes the user out of the room
		case client := <-ch.unregister:
			ch.unregisterClient(client)
			// Message: a new message has arrived
		case message := <-ch.broadcast:
			println("message arrived, message: " + string(message))
			// Push to each user's channel; every user has a running goroutine listening in writePump
			for _, client := range ch.clients {
				client.send <- message
			}
		case now := <-staleTicker.C:
			ch.kickStaleClients(now)
		}
	}
}

// registerClient makes c the live session for its peer id and returns the
// session it replaced, if any. Only ever called from the monitoring goroutine,
// which is the sole owner of ch.clients.
func (ch *CenterHandler) registerClient(c *Client) *Client {
	superseded, ok := ch.clients[c.ID]
	if ok && superseded == c {
		superseded = nil
	}
	if superseded != nil {
		superseded.markSuperseded()
	}
	ch.clients[c.ID] = c
	return superseded
}

// unregisterClient removes c only if it is still the live session for its id,
// and reports whether it removed anything. A session that has already been
// replaced must not delete its successor on the way out — the device would
// disappear from every peer list while it is in fact online.
func (ch *CenterHandler) unregisterClient(c *Client) bool {
	if current, ok := ch.clients[c.ID]; !ok || current != c {
		return false
	}
	delete(ch.clients, c.ID)
	return true
}

// kickoutIDs picks the peer rows to drop when the table overflows: ids come in
// `updated desc` order, so eviction walks from the end (oldest first) and takes
// only offline peers, never the peer that is connecting right now. That last
// exclusion is the point — the arriving peer's row is the one appended last, so
// walking from the end used to delete the row that had just been created for
// it, handing the same device a new identity on every connect.
func kickoutIDs(ids []string, isOnline func(string) bool, arrivingID string, keep int) []string {
	remaining := len(ids) - keep
	if remaining <= 0 {
		return nil
	}
	var kicked []string
	for i := len(ids) - 1; remaining > 0 && i >= 0; i-- {
		if ids[i] == arrivingID || isOnline(ids[i]) {
			continue
		}
		kicked = append(kicked, ids[i])
		remaining--
	}
	return kicked
}

// kickStaleClients evicts clients whose heartbeat has timed out (dead-but-alive):
// closes their connection, removes them from the clients set, and notifies the
// remaining online peers using the existing "peer-left" broadcast message shape
// (the frontend's Network.js already consumes this type — disconnects go
// through the same path).
//
// Note: this iterates ch.clients directly to push, rather than sending to the
// ch.broadcast channel — kickStaleClients itself is called from inside
// monitoring's select loop, so sending to ch.broadcast instead would deadlock
// since no other goroutine is receiving.
func (ch *CenterHandler) kickStaleClients(now time.Time) {
	lastBeats := make(map[string]time.Time, len(ch.clients))
	for id, c := range ch.clients {
		lastBeats[id] = c.snapshotLastBeat()
	}
	for _, id := range staleClientIDs(lastBeats, now, heartbeatTimeout) {
		c, ok := ch.clients[id]
		if !ok {
			continue
		}
		logger.Info("filesdrop: kicking stale peer (heartbeat timeout)",
			zap.String("peerId", id),
			zap.Duration("silence", now.Sub(lastBeats[id])))
		c.conn.Close()
		delete(ch.clients, id)
		peerLeft := []byte(`{"type":"peer-left","peerId":"` + id + `"}`)
		for _, other := range ch.clients {
			other.send <- peerLeft
		}
	}
}

func GetPeers(ctx echo.Context) error {
	peers := service.MyService.Peer().GetPeers()
	for i := 0; i < len(peers); i++ {
		if _, ok := handler.clients[peers[i].ID]; ok {
			peers[i].Online = true
		}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: peers})
}

func isProtectedName(name string) bool {
	protected := []string{"AppData", "Documents", "Downloads", "Gallery", "Media"}
	for _, p := range protected {
		if name == p {
			return true
		}
	}
	return false
}

func containsProtectedName(path string) (bool, string) {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if isProtectedName(part) {
			return true, part
		}
	}
	return false, ""
}

// isAncestorOfSystemPath returns true when path is a parent directory of any
// active system-critical path (AppData, Docker images, user data folders).
// This prevents deletion or movement of a folder that contains a migrated
// system directory as a child, which would break the anchor symlinks.
func isAncestorOfSystemPath(targetPath string) bool {
	cfg := service.ResolveActivePaths()
	systemPaths := []string{
		cfg.AppData,
		cfg.Images,
		filepath.Join(cfg.UserData, "Documents"),
		filepath.Join(cfg.UserData, "Downloads"),
		filepath.Join(cfg.UserData, "Gallery"),
		filepath.Join(cfg.UserData, "Media"),
	}
	clean := filepath.Clean(targetPath)
	for _, sp := range systemPaths {
		if sp == "" || sp == "/" {
			continue
		}
		// sp starts with clean+"/" means clean is a strict ancestor of sp
		if strings.HasPrefix(filepath.Clean(sp)+"/", clean+"/") && clean != filepath.Clean(sp) {
			return true
		}
	}
	return false
}
