package v2

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/NimoTech/NimoOS/codegen"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service"
	"github.com/labstack/echo/v4"
	"github.com/mholt/archiver/v3"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"go.uber.org/zap"
)

func (s *NimoOS) GetHealthServices(ctx echo.Context) error {
	services, err := service.MyService.Health().Services()
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	return ctx.JSON(http.StatusOK, codegen.GetHealthServicesOK{
		Data: &codegen.HealthServices{
			Running:    services[true],
			NotRunning: services[false],
		},
	})
}

func (s *NimoOS) GetHealthPorts(ctx echo.Context) error {
	tcpPorts, udpPorts, err := service.MyService.Health().Ports()
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	return ctx.JSON(http.StatusOK, codegen.GetHealthPortsOK{
		Data: &codegen.HealthPorts{
			TCP: &tcpPorts,
			UDP: &udpPorts,
		},
	})
}
func (c *NimoOS) GetHealthlogs(ctx echo.Context) error {
	var name, commonDir, extension string
	var err error
	var ar archiver.Writer

	commonDir = "/var/log/nimoos"
	if !file.Exists(commonDir) {
		message := "log directory not found"
		return ctx.JSON(http.StatusNotFound, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	fileList, err := os.ReadDir(commonDir)
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	extension, ar, err = file.GetCompressionAlgorithm("zip")
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	// Prepare Headers BEFORE writing any bytes to the stream
	name = "NimoOS" + extension
	ctx.Response().Header().Set("Content-Type", "application/octet-stream")
	ctx.Response().Header().Set("Content-Transfer-Encoding", "binary")
	ctx.Response().Header().Set("Cache-Control", "no-cache")
	ctx.Response().Header().Set("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(name))
	ctx.Response().WriteHeader(http.StatusOK)

	// Create and start the stream
	err = ar.Create(ctx.Response().Writer)
	if err != nil {
		logger.Error("failed to create archiver stream", zap.Error(err))
		return nil // Cannot send JSON now, headers already sent
	}
	defer ar.Close()

	for _, fname := range fileList {
		if fname.IsDir() {
			continue
		}
		fullPath := filepath.Join(commonDir, fname.Name())
		err := file.AddFile(ar, fullPath, commonDir)
		if err != nil {
			logger.Error("failed to add file to log zip stream", zap.String("file", fullPath), zap.Error(err))
			continue
		}
	}
	return nil
}
