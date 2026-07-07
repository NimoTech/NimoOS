package v1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexec "github.com/NimoTech/NimoOS-Common/utils/exec"
)

var convertibleExts = map[string]bool{
	".doc": true, ".wps": true, ".xls": true, ".ppt": true, ".pptx": true,
}

// soffice 每个进程 ~200MB、~3s 启动;串行化避免并发起多个。转换是慢操作,
// 但各 HTTP 请求在各自 goroutine,此锁只护 soffice 生成,不阻塞其它 API。
var sofficeGate sync.Mutex

const sofficeTimeout = 120 * time.Second

func isConvertibleOffice(ext string) bool {
	return convertibleExts[strings.ToLower(ext)]
}

// 私有 UserInstallation profile 避开 LibreOffice 全局 profile 锁死;profile 放 outdir 内,随清理一并删。
func sofficeArgs(profileDir, outdir, src string) []string {
	return []string{
		"--headless",
		"-env:UserInstallation=file://" + profileDir,
		"--convert-to", "pdf",
		"--outdir", outdir,
		src,
	}
}

// convertOfficeToPDF 把 src(旧版 Office)转成 PDF 字节返回。临时目录 defer 删除——
// 成功/失败/超时都不留残留(不缓存)。任何失败返回 error,调用方降级为「无法预览+下载」。
func convertOfficeToPDF(src string) ([]byte, error) {
	outdir, err := os.MkdirTemp("", "nimoos-preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outdir)
	profileDir := filepath.Join(outdir, "lo-profile")

	sofficeGate.Lock()
	defer sofficeGate.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), sofficeTimeout)
	defer cancel()

	cmd := nexec.CommandContext(ctx, "soffice", sofficeArgs(profileDir, outdir, src)...)
	if cmd.Err != nil { // safetext 拒绝了某个参数
		return nil, cmd.Err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("soffice failed: %w (%s)", err, string(out))
	}

	// LibreOffice 通常写 <basename>.pdf;偶发命名不同 → 兜底扫 outdir 里第一个 .pdf。
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + ".pdf"
	data, err := os.ReadFile(filepath.Join(outdir, base))
	if err != nil {
		entries, _ := os.ReadDir(outdir)
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
				data, err = os.ReadFile(filepath.Join(outdir, e.Name()))
				break
			}
		}
	}
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("soffice produced no pdf output")
	}
	return data, nil
}
