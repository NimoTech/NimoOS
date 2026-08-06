package v1

import (
	"context"
	"fmt"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
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

	// 用标准库 os/exec 而非 NimoOS-Common 的 safetext 包装:soffice 以「分开的参数」
	// 直接经 execve 调用(无 shell),文件名里的空格/特殊字符是字面量、无注入风险;
	// 而 safetext 的 shsprintf 会把带空格的路径(如「新建 DOC 文档.doc」)误判为
	// "Shell Injection Detected" 而拒绝执行。路径已由 checkPathAccess 鉴权。
	// (同 core migrate.go 对 rsync/docker 路径参数也用标准库 os/exec。)
	cmd := osexec.CommandContext(ctx, "soffice", sofficeArgs(profileDir, outdir, src)...)
	if cmd.Err != nil { // soffice 二进制未找到(LookPath 失败)
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

// GetFilePreview 把旧版 Office(doc/wps/xls/ppt/pptx)转成 PDF 返回。
// 响应体是原始 PDF 字节(application/pdf),非 Result 信封;错误时返回 Result JSON + 非 2xx。
func GetFilePreview(ctx echo.Context) error {
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
	if !isConvertibleOffice(filepath.Ext(filePath)) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
			Success: common_err.INVALID_PARAMS,
			Message: "unsupported preview format",
		})
	}
	if _, err := osexec.LookPath("soffice"); err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: "文档转换组件(LibreOffice)未安装",
		})
	}
	data, err := convertOfficeToPDF(filePath)
	if err != nil {
		logger.Error("file preview convert failed", zap.String("path", filePath), zap.Error(err))
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: "文档转换失败",
		})
	}
	return ctx.Blob(http.StatusOK, "application/pdf", data)
}
