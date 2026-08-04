package service

import (
	"os"
	"strings"
)

// photosConfDataPath reads DataPath from photos.conf. The key is
// case-insensitive — once photos.conf is rewritten by NimoOS-Photos'
// Settings.Save() (viper), the key becomes all-lowercase datapath. Returns
// (value, whether it exists); if the file doesn't exist or has no such key,
// returns ("", false).
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
