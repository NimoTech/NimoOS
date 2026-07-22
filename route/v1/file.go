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
	// 升级成 WebSocket 协议
	upgraderFile = websocket.Upgrader{
		// 允许CORS跨域请求
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	conn *websocket.Conn
	err  error

	// uploadBatchStore 惰性初始化:复用全局 gorm 单例(与 route/v2.go 的构造同源)。
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

	// 1. 内部调用/本地回环认证豁免权限检查。
	// Localhost bypass: JWT middleware skipped, no headers set.
	if role == "" && userID == "" {
		return nil
	}
	// 2. 超级管理员特权判定。
	// Only User ID 1 (Root Admin) gets the "Skeleton Key" to all paths.
	// Other admins (like admin1) must still follow explicit folder grants for security isolation.
	isSuperAdmin := userID == "1"
	cleanPath := filepath.Clean(path)

	// Fast path: Super-admin gets everything.
	if isSuperAdmin {
		return nil
	}

	// 3. 基本放行规则检测。
	// Base safety check (empty for users now that prefixes are removed).
	if utils.IsPathAllowed(cleanPath, false) {
		return nil
	}

	// 4. 显式文件夹授权校验。
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

// @Summary 读取文件
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string true "路径"
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
	// 文件读取任务是将文件内容读取到内存中。
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

			// 打开文件
			fileTmp, _ := os.Open(filePath)
			defer fileTmp.Close()

			// 获取文件的名称
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

	// 这三个头只对打包分支生效;单文件分支上面已经 return,
	// 走到这里必然是归档打包响应,ctx.File 自带的 Content-Type 不受影响。
	// Content-Type 只对 zip 明确声明,其余格式(tar/targz…,前端当前不传)交给
	// net/http 首写时的内容嗅探,避免张冠李戴。
	if extension == ".zip" {
		ctx.Response().Header().Set("Content-Type", "application/zip")
	}
	ctx.Response().Header().Set("Content-Transfer-Encoding", "binary")
	ctx.Response().Header().Set("Cache-Control", "no-cache")

	name := downloadArchiveName(list, extension)
	// 必须在 ar.Create(写响应体) 之前设置,写入响应体后响应头会被锁死。
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

// downloadArchiveName 决定批量下载归档的对外文件名:
// 单个文件夹 → 文件夹名+扩展名;多选 → 公共父目录名+扩展名(群晖式:在 photos
// 目录选 5 个文件 → photos.zip);公共父目录是根("/" 或空)时兜底 NimoOS。
// extension 来自 GetCompressionAlgorithm(如 ".zip"/".tar.gz"),跟随 format 参数。
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
	// Content-Type/Last-Modified/Content-Length 由 ServeContent 自行设置
	// (按文件名扩展/内容嗅探 + modtime + seeker 长度)。此前这里手工把这三个头
	// 加在 ctx.Request().Header 上——写错了对象,纯空操作,已删。
	http.ServeContent(ctx.Response().Writer, ctx.Request(), fileName, node.ModTime(), fi)
	return nil
}

// @Summary 获取目录列表
// @Produce  application/json
// @Accept application/json
// @Tags file
// @Security ApiKeyAuth
// @Param path query string false "路径"
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
	// 上传中断角标:凡有 interrupted 批次的缺失文件落在某子条目路径下,该条目
	// 注入 extensions.upload,前端据此叠加「裂开」角标。查询失败只降级不报错——
	// 角标是提示性信息,不能拖垮列目录主流程。
	if broken, berr := getUploadBatchStore().BrokenChildren(req.Path); berr == nil && len(broken) > 0 {
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

	success, err := service.MyService.System().RenameFile(op, np)
	if success == common_err.SUCCESS {
		// 重命名/移动成功后,挂在 op 自身/子树上的分享记录 path 仍指旧位置,
		// 会在「已共享」Tab 里变成永远打不开的悬挂项——正确语义是改写路径,
		// 不是删除(与删除目录时的 DeleteShareByPath 对应)。
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

// jsonMangledName 复现 encoding/json 对非法 UTF-8 的处理:每个非法字节替换为
// U+FFFD,合法多字节 rune 原样保留。用于把磁盘上的真实目录名映射到「前端经
// 列表接口看到的名字」,以便删除时反向匹配。
//
// 不用 strings.ToValidUTF8——它把连续非法字节整段换成一个 U+FFFD,与 JSON 的
// 逐字节替换语义不一致(encoding/json 的字符串编码器也是按 utf8.DecodeRuneInString
// 逐 rune 前进,非法字节时该函数返回 (RuneError, 1),即每个非法字节各自产出一个
// U+FFFD,这里的循环与之完全对应)。
func jsonMangledName(name string) string {
	if utf8.ValidString(name) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		b.WriteRune(r) // 非法字节时 DecodeRuneInString 返回 (RuneError, 1),恰好逐字节替换
		i += size
	}
	return b.String()
}

// resolveDeletePath 把客户端送来的删除路径解析为磁盘真实路径。
// 路径存在 → 原样返回。不存在(Lstat ENOENT)→ 在父目录中找「JSON 整形后
// 恰好等于请求名」的真实条目:唯一命中返回真实路径;零命中返回 os.ErrNotExist;
// 多个命中返回歧义错误(绝不猜删)。其余 Lstat 错误原样返回。
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
		// 父目录本身都读不到,没有救援的余地。
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

	// 把每个请求路径解析为磁盘真实路径。不存在时消灭"假成功":直接返回
	// FILE_DOES_NOT_EXIST,而不是让后面的 os.RemoveAll 对着一个不存在的路径
	// 悄悄返回 nil。命中救援匹配的真实路径与请求路径可能不同名,需要重新过一遍
	// 保护名/系统路径祖先检查(checkPathAccess 基于父目录路径语义等价,不必重跑)。
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
		err := os.RemoveAll(v)
		unlock()
		if err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: err})
		}
		// 目录删掉后,挂在它自身/子树上的 Samba 分享记录就成了「已共享」Tab 里
		// 永远打不开的悬挂项(实测:删父目录后其下已分享的子文件夹仍列在 Tab 里)。
		// 同步清掉并重写 smb 配置;DeleteShareByPath 自带 "/" 边界,不伤同前缀
		// 兄弟目录的分享。
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
	// 广播通道，有数据则循环每个用户广播出去
	broadcast chan []byte
	// 注册通道，有用户进来 则推到用户集合map中
	register chan *Client
	// 注销通道，有用户关闭连接 则将该用户剔出集合map中
	unregister chan *Client
	// 用户集合，每个用户本身也在跑两个协程，监听用户的读、写的状态
	clients map[string]*Client
}

type Client struct {
	handler *CenterHandler
	conn    *websocket.Conn
	// 每个用户自己的循环跑起来的状态监控
	send         chan []byte
	ID           string       `json:"id"`
	IP           string       `json:"ip"`
	Name         service.Name `json:"name"`
	RtcSupported bool         `json:"rtcSupported"`
	TimerId      int          `json:"timerId"`
	// mu 只保护 LastBeat：readPump（收到 pong/任意消息时）与 monitoring 的
	// 心跳超时检查分属两个 goroutine，读写都要过锁。
	mu       sync.Mutex
	LastBeat time.Time `json:"lastBeat"`
}

// touchLastBeat 记录一次心跳/活动时间，供 monitoring 的超时检查读取。
func (c *Client) touchLastBeat(now time.Time) {
	c.mu.Lock()
	c.LastBeat = now
	c.mu.Unlock()
}

// snapshotLastBeat 读取当前心跳时间（加锁，避免与 touchLastBeat 数据竞争）。
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
		}
	}
	list := service.MyService.Peer().GetPeers()
	if len(peerModel.ID) == 0 {
		peerModel.ID = key
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
		kickoutList := []Client{}
		count := len(list) - 10
		for i := len(list) - 1; count > 0 && i > -1; i-- {
			if _, ok := handler.clients[list[i].ID]; !ok {
				count--
				kickoutList = append(kickoutList, Client{ID: list[i].ID, Name: service.GetNameByDB(list[i]), IP: list[i].IP})
				service.MyService.Peer().DeletePeer(list[i].ID)
			}
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
		fmt.Println("解决完后依然有溢出", list)
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

	// 推给监控中心注册到用户集合中
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

	// 每个 client 都挂起 2 个新的协程，监控读、写状态
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
	// 起个协程跑起来，监听注册、注销、消息 3 个 channel
	go handler.monitoring()

	crontab := cron.New(cron.WithSeconds()) // 精确到秒
	// 定义定时器调用的任务函数

	task := func() {
		handler.broadcast <- []byte(`{"type":"ping"}`)
	}
	// 定时任务
	spec := "*/30 * * * * ?" // cron表达式，每五秒一次
	// 添加定时任务,
	crontab.AddFunc(spec, task)
	// 启动定时器
	crontab.Start()
}

func (c *Client) writePump() {
	defer func() {
		c.handler.unregister <- c

		c.conn.Close()
	}()
	for {
		// 广播推过来的新消息，马上通过websocket推给自己
		message, _ := <-c.send
		fmt.Println("推送消息", string(message), "1")
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
}

// 读，监听客户端是否有推送内容过来服务端
func (c *Client) readPump() {
	defer func() {
		c.handler.unregister <- c
		c.conn.Close()
	}()
	for {
		// 循环监听是否该用户是否要发言
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			// 异常关闭的处理
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			c.handler.broadcast <- []byte(`{"type":"peer-left","peerId":"` + c.ID + `"}`)
			break
		}
		// 要的话，推给广播中心，广播中心再推给每个用户

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
			c.handler.broadcast <- []byte(`{"type":"peer-left","peerId":"` + c.ID + `"}`)
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

// heartbeatTimeout 心跳假死判定阈值：ping 广播每 30s 一次，容忍 3 个周期
// 没有回应（考虑到瞬时网络抖动），超过判定为假死（网线拔出/系统休眠等无
// TCP FIN 的场景）。
const heartbeatTimeout = 90 * time.Second

// staleClientIDs 返回心跳超时的客户端：lastBeats 为 id→最后心跳时间，
// now-lastBeat > timeout 判定假死。恰好等于 timeout 不算超时。
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
	// 与 ping 广播同周期（30s）巡检一次心跳超时的假死连接。ch.clients 只在
	// monitoring 这个 goroutine 里被读写，因此心跳超时的踢出逻辑也放在这个
	// select 循环里处理，不需要额外给 clients map 加锁。
	staleTicker := time.NewTicker(30 * time.Second)
	defer staleTicker.Stop()
	for {
		select {
		// 注册，新用户连接过来会推进注册通道，这里接收推进来的用户指针
		case client := <-ch.register:
			ch.clients[client.ID] = client
			// 注销，关闭连接或连接异常会将用户推出群聊
		case client := <-ch.unregister:
			delete(ch.clients, client.ID)
			// 消息，监听到有新消息到来
		case message := <-ch.broadcast:
			println("消息来了，message：" + string(message))
			// 推送给每个用户的通道，每个用户都有跑协程起了writePump的监听
			for _, client := range ch.clients {
				client.send <- message
			}
		case now := <-staleTicker.C:
			ch.kickStaleClients(now)
		}
	}
}

// kickStaleClients 踢出心跳超时（假死）的 client：关闭其连接、从 clients
// 集合中移除，并按现有「peer-left」的广播消息形态通知其余在线 peer（前端
// Network.js 已在消费这个 type，断线时走的是同一条路径）。
//
// 注意：这里直接遍历 ch.clients 推送，而不是发去 ch.broadcast 通道——
// kickStaleClients 本身就是在 monitoring 的 select 循环内被调用，
// 若改成往 ch.broadcast 发送会因为没有其它 goroutine 接收而死锁。
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
