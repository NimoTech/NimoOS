package service

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sync"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

type FileInfo struct {
	init             bool
	uploaded         []bool
	uploadedChunkNum int64
}

type FileUploadService struct {
	uploadStatus sync.Map
	lock         sync.RWMutex
}

func NewFileUploadService() *FileUploadService {
	return &FileUploadService{
		uploadStatus: sync.Map{},
		lock:         sync.RWMutex{},
	}
}

func (s *FileUploadService) TestChunk(
	c echo.Context,
	identifier string,
	chunkNumber int64,
) error {
	fileInfoTemp, ok := s.uploadStatus.Load(identifier)

	if !ok {
		return fmt.Errorf("file not found")
	}

	fileInfo := fileInfoTemp.(*FileInfo)

	if !fileInfo.init {
		return fmt.Errorf("file not init")
	}

	// return StatusNoContent instead of 404
	// the is require by frontend
	if !fileInfo.uploaded[chunkNumber-1] {
		return fmt.Errorf("file not found")
	}

	return nil
}

func (s *FileUploadService) UploadFile(
	c echo.Context,
	path string,
	chunkNumber int64,
	chunkSize int64,
	currentChunkSize int64,
	totalChunks int64,
	totalSize int64,
	identifier string,
	relativePath string,
	fileName string,
	bin *multipart.FileHeader,
) error {
	s.lock.Lock()
	fileInfoTemp, ok := s.uploadStatus.Load(identifier)
	var fileInfo *FileInfo

	// Ensure target folder exists before creating the temp file
	tempFilePath := filepath.Join(path, relativePath+".tmp")
	finalFilePath := filepath.Join(path, relativePath)
	targetDir := filepath.Dir(tempFilePath)

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
			s.lock.Unlock()
			logger.Error("create folder error: ", zap.Error(err), zap.String("path", targetDir))
			return err
		}
	}

	// Open file for writing the chunk
	file, err := os.OpenFile(tempFilePath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		s.lock.Unlock()
		logger.Error("open file error: ", zap.Error(err), zap.String("path", tempFilePath))
		return err
	}

	if !ok {
		// file info init
		fileInfo = &FileInfo{
			init:             true,
			uploaded:         make([]bool, totalChunks),
			uploadedChunkNum: 0,
		}
		s.uploadStatus.Store(identifier, fileInfo)
	} else {
		fileInfo = fileInfoTemp.(*FileInfo)
	}
	s.lock.Unlock()

	// Ensure we close the file handle after writing this chunk
	defer file.Close()

	_, err = file.Seek((chunkNumber-1)*chunkSize, io.SeekStart)
	if err != nil {
		logger.Error("seek file error: ", zap.Error(err))
		return err
	}

	src, err := bin.Open()
	if err != nil {
		logger.Error("open multipart buffer error: ", zap.Error(err))
		return err
	}
	defer src.Close()

	_, err = io.Copy(file, src)
	if err != nil {
		logger.Error("copy chunk error: ", zap.Error(err))
		return err
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	// handle single chunk upload twice
	if !fileInfo.uploaded[chunkNumber-1] {
		fileInfo.uploadedChunkNum++
		fileInfo.uploaded[chunkNumber-1] = true
	}

	// handle file after write all chunk
	if fileInfo.uploadedChunkNum == totalChunks {
		// Close the handle before renaming
		file.Close()

		if err := os.Rename(tempFilePath, finalFilePath); err != nil {
			logger.Error("rename file error: ", zap.Error(err), zap.String("from", tempFilePath), zap.String("to", finalFilePath))
			return err
		}
		// remove upload status info after upload complete
		s.uploadStatus.Delete(identifier)
	}

	return nil
}
