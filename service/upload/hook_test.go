package upload

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NimoTech/NimoOS/common"
	commonUpload "github.com/NimoTech/NimoOS-Common/upload"
	"github.com/tus/tusd/v2/pkg/handler"
)

func TestNewTaskFromHook(t *testing.T) {
	ev := handler.HookEvent{
		Upload: handler.FileInfo{
			ID:   "up123",
			Size: 4096,
			MetaData: map[string]string{
				"filename":     "a.bin",
				"targetPath":   "/DATA/Documents",
				"relativePath": "sub/a.bin",
				"filetype":     "application/octet-stream",
				"fingerprint":  "a.bin:4096:1700",
				"batch_id":     "batch-1",
				"client_id":    "dev-xyz",
			},
		},
		HTTPRequest: handler.HTTPRequest{
			Method:     "POST",
			RemoteAddr: "10.0.0.5:5555",
			Header:     http.Header{"User-Agent": {"Mozilla/5.0"}, "User_id": {"42"}},
		},
	}
	now := time.Unix(1000, 0)
	got := NewTaskFromHook(ev, now)

	if got.ID != "up123" || got.OwnerUserID != "42" || got.Status != commonUpload.UploadStatusUploading {
		t.Fatalf("core fields: %+v", got)
	}
	if got.Filename != "a.bin" || got.TargetPath != "/DATA/Documents" || got.RelativePath != "sub/a.bin" {
		t.Fatalf("path fields: %+v", got)
	}
	if got.Mime != "application/octet-stream" || got.Fingerprint != "a.bin:4096:1700" {
		t.Fatalf("meta fields: %+v", got)
	}
	if got.BatchID != "batch-1" || got.ClientID != "dev-xyz" || got.Size != 4096 {
		t.Fatalf("misc fields: %+v", got)
	}
	if got.ExpiresAt != now.Unix()+common.UploadIdleTimeoutSeconds {
		t.Fatalf("expires: %d", got.ExpiresAt)
	}
	if !strings.Contains(got.ClientMeta, "Mozilla/5.0") || !strings.Contains(got.ClientMeta, "10.0.0.5") {
		t.Fatalf("client_meta should carry UA+IP: %s", got.ClientMeta)
	}
}
