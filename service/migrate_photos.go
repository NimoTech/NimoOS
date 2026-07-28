package service

import (
	"os"
	"strings"
)

// photosConfDataPath 从 photos.conf 读取 DataPath。key 大小写不敏感——
// photos.conf 被 NimoOS-Photos 的 Settings.Save()(viper)回写后 key 会变成
// 全小写 datapath。返回 (值, 是否存在);文件不存在/无该 key 返回 ("", false)。
func photosConfDataPath(confPath string) (string, bool) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(k), "datapath") {
			continue
		}
		return strings.TrimSpace(v), true
	}
	return "", false
}
