package upload

import (
	"encoding/json"
	"time"

	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/tus/tusd/v2/pkg/handler"
)

// NewTaskFromHook maps one tus CreatedUploads event to an uploading task row.
func NewTaskFromHook(ev handler.HookEvent, now time.Time) *commonUpload.UploadTask {
	meta := ev.Upload.MetaData
	rel := meta["relativePath"]
	if rel == "" {
		rel = meta["filename"]
	}
	cm, _ := json.Marshal(map[string]string{
		"user_agent":  ev.HTTPRequest.Header.Get("User-Agent"),
		"remote_addr": ev.HTTPRequest.RemoteAddr,
	})
	return &commonUpload.UploadTask{
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
		Status:       commonUpload.UploadStatusUploading,
		ExpiresAt:    now.Unix() + common.UploadIdleTimeoutSeconds,
	}
}
