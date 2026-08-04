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

// googleByoCred temporarily holds a user's self-supplied (BYO) Google OAuth
// credentials. Written by PostGoogleDriveAuth into service.Cache (keyed by a
// random sid, short TTL); the auth callback GetRecoverStorage retrieves it by
// the sid carried in state. client_secret only ever lives briefly in server
// memory and never enters any URL or intermediate page.
type googleByoCred struct {
	ClientID     string
	ClientSecret string
}

type googleDriveAuthReq struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// PostGoogleDriveAuth accepts the user's own Google OAuth client_id/client_secret,
// stores them in a short-lived cache, and returns a Google auth URL built with
// that client_id (state carries a one-time sid so the callback can retrieve the credentials).
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

	// Same auth URL structure as in google_drive.GetConfig(), just with the
	// user-supplied client_id and sid carried in state. The ${HOST} placeholder
	// is still substituted with the local address by the frontend.
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
