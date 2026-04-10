package v2

import (
	"net/http"
	"strconv"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/codegen"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// Path: route/v2/file.go

func (s *NimoOS) GetFileTest(ctx echo.Context) error {

	//http.ServeFile(w, r, r.URL.Path[1:])
	http.ServeFile(ctx.Response().Writer, ctx.Request(), "/DATA/test.img")

	return ctx.String(200, "pong")
}

func (c *NimoOS) CheckUploadChunk(ctx echo.Context, params codegen.CheckUploadChunkParams) error {
	identifier := ctx.QueryParam("identifier")
	chunkNumber, err := strconv.ParseInt(ctx.QueryParam("chunkNumber"), 10, 64)
	if err != nil {
		return ctx.NoContent(http.StatusBadRequest)
	}

	err = c.fileUploadService.TestChunk(ctx, identifier, chunkNumber)
	if err != nil {
		return ctx.NoContent(http.StatusNoContent)
	}
	return ctx.NoContent(http.StatusOK)
}

func (c *NimoOS) PostUploadFile(ctx echo.Context) error {
	// path can come from URL query param (set by frontend uploader's "query" option)
	// or from form body (legacy). Check query param first.
	path := ctx.QueryParam("path")
	if path == "" {
		path = ctx.FormValue("path")
	}

	// handle the request
	chunkNumber, err := strconv.ParseInt(ctx.FormValue("chunkNumber"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	chunkSize, err := strconv.ParseInt(ctx.FormValue("chunkSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	currentChunkSize, err := strconv.ParseInt(ctx.FormValue("currentChunkSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	totalChunks, err := strconv.ParseInt(ctx.FormValue("totalChunks"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	totalSize, err := strconv.ParseInt(ctx.FormValue("totalSize"), 10, 64)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, err)
	}
	identifier := ctx.FormValue("identifier")
	fileName := ctx.FormValue("filename")
	relativePath := ctx.FormValue("relativePath")

	logger.Info("Upload Request Received", 
		zap.String("path", path), 
		zap.String("filename", fileName), 
		zap.String("identifier", identifier),
		zap.Int64("chunk", chunkNumber))

	bin, err := ctx.FormFile("file")

	if err != nil {
		logger.Error("FormFile error", zap.Error(err))
		return ctx.JSON(http.StatusBadRequest, err)
	}

	err = c.fileUploadService.UploadFile(
		ctx,
		path,
		chunkNumber,
		chunkSize,
		currentChunkSize,
		totalChunks,
		totalSize,
		identifier,
		relativePath,
		fileName,
		bin,
	)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, echo.Map{
			"message": err.Error(),
		})
	}
	return ctx.NoContent(http.StatusOK)
}
