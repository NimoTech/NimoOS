package v1

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/common_err"
	"github.com/NimoTech/NimoOS/drivers/base"
	"github.com/NimoTech/NimoOS/drivers/dropbox"
	"github.com/NimoTech/NimoOS/drivers/google_drive"
	"github.com/NimoTech/NimoOS/drivers/onedrive"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/service"
	"github.com/labstack/echo/v4"
)

func ListDriverInfo(ctx echo.Context) error {
	list := []model.Drive{}

	google := google_drive.GetConfig()
	list = append(list, model.Drive{
		Name:    "Google Drive",
		Icon:    google.Icon,
		AuthUrl: google.AuthUrl,
	})
	dp := dropbox.GetConfig()
	list = append(list, model.Drive{
		Name:    "Dropbox",
		Icon:    dp.Icon,
		AuthUrl: dp.AuthUrl,
	})
	od := onedrive.GetConfig()
	list = append(list, model.Drive{
		Name:    "OneDrive",
		Icon:    od.Icon,
		AuthUrl: od.AuthUrl,
	})
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: list})
}

// googleByoCred 暂存用户自建(BYO)的 Google OAuth 凭据。由 PostGoogleDriveAuth 写入
// service.Cache(随机 sid 为键、短 TTL),授权回调 GetRecoverStorage 按 state 里的 sid 取回。
// client_secret 只在服务器内存中短暂存在,绝不进入任何 URL / 中转页。
type googleByoCred struct {
	ClientID     string
	ClientSecret string
}

type googleDriveAuthReq struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// PostGoogleDriveAuth 接收用户自己的 Google OAuth client_id/client_secret,存入短期缓存,
// 并返回一个用该 client_id 拼好的 Google 授权 URL(state 携带一次性 sid,供回调取回凭据)。
func PostGoogleDriveAuth(ctx echo.Context) error {
	var req googleDriveAuthReq
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(common_err.INVALID_PARAMS, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.ClientSecret = strings.TrimSpace(req.ClientSecret)
	if req.ClientID == "" || req.ClientSecret == "" {
		return ctx.JSON(common_err.INVALID_PARAMS, model.Result{Success: common_err.INVALID_PARAMS, Message: "client_id and client_secret are required"})
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR)})
	}
	sid := hex.EncodeToString(b)
	service.Cache.Set(sid, googleByoCred{ClientID: req.ClientID, ClientSecret: req.ClientSecret}, 10*time.Minute)

	// 与 google_drive.GetConfig() 里的授权 URL 结构一致,只是 client_id 用用户填的、
	// 且 state 里带上 sid。${HOST} 占位符仍由前端替换成本机地址。
	authURL := "https://accounts.google.com/o/oauth2/auth/oauthchooseaccount?response_type=code" +
		"&client_id=" + url.QueryEscape(req.ClientID) +
		"&redirect_uri=" + url.QueryEscape(base.RedirectURI) +
		"&scope=" + url.QueryEscape("https://www.googleapis.com/auth/drive") +
		"&access_type=offline&approval_prompt=force" +
		"&state=${HOST}" + url.QueryEscape("/v1/recover/GoogleDrive?sid="+sid) +
		"&service=lso&o2v=1&flowName=GeneralOAuthFlow"

	return ctx.JSON(common_err.SUCCESS, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    map[string]string{"auth_url": authURL},
	})
}
