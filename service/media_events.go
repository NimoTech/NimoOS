package service

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"go.uber.org/zap"
)

// mediaCreatedExts is a verbatim copy of route/v1/file.go's deletedMediaExts —
// the image/video types Photos indexes. Keep the two lists in sync when adding
// formats (the two live in different packages; a shared source would be nicer
// but is out of scope here).
var mediaCreatedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".webp": true,
	".gif": true, ".bmp": true, ".tiff": true, ".tif": true, ".avif": true,
	".mp4": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true,
	".m4v": true, ".3gp": true,
}

// filterMediaCreated keeps directories (subscriber expands them) and files
// whose extension is a known media type. Vanished paths are dropped.
func filterMediaCreated(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			out = append(out, p)
			continue
		}
		ext := strings.ToLower(filepath.Ext(filepath.Base(p)))
		if mediaCreatedExts[ext] {
			out = append(out, p)
		}
	}
	return out
}

// PublishMediaCreated fires nimoos:media:created for the given landed paths.
// Fire-and-forget: MessageBus 是软依赖,失败只记日志,绝不影响文件操作本身。
func PublishMediaCreated(paths []string) {
	media := filterMediaCreated(paths)
	if len(media) == 0 {
		return
	}
	b, err := json.Marshal(media)
	if err != nil {
		return
	}
	response, err := MyService.MessageBus().PublishEventWithResponse(
		context.Background(), common.SERVICENAME, common.EventMediaCreated,
		map[string]string{"paths": string(b)},
	)
	if err != nil {
		logger.Error("failed to publish nimoos:media:created", zap.Error(err))
		return
	}
	if response.StatusCode() != http.StatusOK {
		logger.Error("failed to publish nimoos:media:created", zap.String("status", response.Status()))
	}
}
