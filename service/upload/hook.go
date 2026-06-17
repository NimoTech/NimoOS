package upload

import (
	"encoding/json"
	"time"

	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/service/model"
	"github.com/tus/tusd/v2/pkg/handler"
)

// NewTaskFromHook 把一次 tus CreatedUploads 事件映射为一条 uploading 任务行。
func NewTaskFromHook(ev handler.HookEvent, now time.Time) *model.UploadTaskDBModel {
	meta := ev.Upload.MetaData
	rel := meta["relativePath"]
	if rel == "" {
		rel = meta["filename"]
	}
	cm, _ := json.Marshal(map[string]string{
		"user_agent":  ev.HTTPRequest.Header.Get("User-Agent"),
		"remote_addr": ev.HTTPRequest.RemoteAddr,
	})
	return &model.UploadTaskDBModel{
		ID:           ev.Upload.ID,
		OwnerUserID:  ev.HTTPRequest.Header.Get("user_id"),
		Filename:     meta["filename"],
		TargetPath:   meta["targetPath"],
		RelativePath: rel,
		Size:         ev.Upload.Size,
		Mime:         meta["filetype"],
		Fingerprint:  meta["fingerprint"],
		BatchID:      meta["batch_id"],
		ClientID:     meta["client_id"],
		ClientMeta:   string(cm),
		Status:       model.UploadStatusUploading,
		ExpiresAt:    now.Unix() + common.UploadIdleTimeoutSeconds,
	}
}
