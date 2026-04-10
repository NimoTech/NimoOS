package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	localfile "github.com/NimoTech/NimoOS/pkg/utils/file"
	"go.uber.org/zap"
)

const (
	PathConfigFile = "/var/lib/casaos/path_config.json"

	MigrateTypeAppData  = "app_data"
	MigrateTypeImages   = "images"
	MigrateTypeUserData = "database"

	DefaultAppDataPath  = "/DATA/AppData"
	DefaultImagesPath   = "/DATA/.docker"
	DefaultUserDataPath = "/DATA/Gallery & Downloads & Documents & Media"
)

// PathConfig stores current configured paths for the three data locations.
type PathConfig struct {
	AppData  string `json:"app_data"`
	Images   string `json:"images"`
	UserData string `json:"database"`
}

// MigrateStatus describes a running or finished migration job.
type MigrateStatus struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Status        string `json:"status"` // "running" | "done" | "error"
	Progress      int    `json:"progress"`
	ProcessedSize int64  `json:"processed_size"`
	TotalSize     int64  `json:"total_size"`
	NewPath       string `json:"new_path,omitempty"`
	Error         string `json:"error,omitempty"`
}

// MigrateJobs holds all active/completed migration jobs.
var MigrateJobs sync.Map // map[string]MigrateStatus

// LoadPathConfig reads the stored path config or returns defaults.
func LoadPathConfig() PathConfig {
	cfg := PathConfig{
		AppData:  DefaultAppDataPath,
		Images:   DefaultImagesPath,
		UserData: DefaultUserDataPath,
	}
	data, err := os.ReadFile(PathConfigFile)
	if err != nil {
		return cfg
	}
	var stored PathConfig
	if err := json.Unmarshal(data, &stored); err != nil {
		return cfg
	}
	if stored.AppData != "" {
		cfg.AppData = stored.AppData
	}
	if stored.Images != "" {
		cfg.Images = stored.Images
	}
	if stored.UserData != "" {
		cfg.UserData = stored.UserData
	}
	return cfg
}

// SavePathConfig persists the path config to disk.
func SavePathConfig(cfg PathConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PathConfigFile, data, 0644)
}

// StartMigration kicks off an async migration job and returns immediately.
func StartMigration(jobID, migrationType, targetMountPoint string) {
	status := MigrateStatus{
		ID:     jobID,
		Type:   migrationType,
		Status: "running",
	}
	MigrateJobs.Store(jobID, status)

	go func() {
		newPath, err := executeMigration(jobID, migrationType, targetMountPoint)
		if v, ok := MigrateJobs.Load(jobID); ok {
			s := v.(MigrateStatus)
			if err != nil {
				s.Status = "error"
				s.Error = err.Error()
				logger.Error("migration failed", zap.String("type", migrationType), zap.Error(err))
			} else {
				s.Status = "done"
				s.Progress = 100
				s.NewPath = newPath
			}
			MigrateJobs.Store(jobID, s)
		}
	}()
}

// GetMigrationStatus looks up a job by ID.
func GetMigrationStatus(jobID string) (MigrateStatus, bool) {
	v, ok := MigrateJobs.Load(jobID)
	if !ok {
		return MigrateStatus{}, false
	}
	return v.(MigrateStatus), true
}

func executeMigration(jobID, migrationType, targetMountPoint string) (string, error) {
	cfg := LoadPathConfig()

	// 1. Define paths based on type
	var totalSize int64
	var subFolders []string
	var srcPath string
	isBatch := false

	switch migrationType {
	case MigrateTypeAppData:
		srcPath = cfg.AppData
		totalSize, _ = localfile.GetFileOrDirSize(srcPath)
	case MigrateTypeImages:
		srcPath = cfg.Images
		totalSize, _ = localfile.GetFileOrDirSize(srcPath)
	case MigrateTypeUserData:
		isBatch = true
		subFolders = []string{"Gallery", "Downloads", "Documents", "Media"}
		srcBase := "/DATA"
		for _, f := range subFolders {
			s, _ := localfile.GetFileOrDirSize(filepath.Join(srcBase, f))
			totalSize += s
		}
	default:
		return "", fmt.Errorf("unknown migration type: %s", migrationType)
	}

	updateProgress(jobID, 1, 0, totalSize)
	done := make(chan struct{})

	// 2. Specialized Logic (Pre-copy)
	if migrationType == MigrateTypeImages {
		logger.Info("stopping docker for migration")
		_ = exec.Command("systemctl", "stop", "docker").Run()
	}

	// 3. Start Progress Tracking
	if isBatch {
		destFolders := []string{}
		for _, f := range subFolders {
			destFolders = append(destFolders, filepath.Join(targetMountPoint, f))
		}
		go trackBatchProgress(jobID, destFolders, totalSize, done)
	} else {
		newPath := filepath.Join(targetMountPoint, filepath.Base(srcPath))
		go trackBatchProgress(jobID, []string{newPath}, totalSize, done)
	}

	// 4. Perform Data Movement
	if isBatch {
		for _, f := range subFolders {
			fullSrc := filepath.Join("/DATA", f)
			if _, err := os.Stat(fullSrc); os.IsNotExist(err) {
				continue
			}
			if err := localfile.CopyDir(fullSrc, targetMountPoint, "overwrite"); err != nil {
				close(done)
				return "", fmt.Errorf("batch copy failed at %s: %w", f, err)
			}
			_ = os.RemoveAll(fullSrc)
			updateSymlink(fullSrc, filepath.Join(targetMountPoint, f))
		}
		cfg.UserData = targetMountPoint
	} else {
		if err := localfile.CopyDir(srcPath, targetMountPoint, "overwrite"); err != nil {
			close(done)
			if migrationType == MigrateTypeImages {
				_ = exec.Command("systemctl", "start", "docker").Run()
			}
			return "", fmt.Errorf("copy failed: %w", err)
		}
		newPath := filepath.Join(targetMountPoint, filepath.Base(srcPath))
		_ = os.RemoveAll(srcPath)
		updateSymlink(srcPath, newPath)

		if migrationType == MigrateTypeAppData {
			cfg.AppData = newPath
		} else if migrationType == MigrateTypeImages {
			cfg.Images = newPath
			dockerCfg := map[string]string{"docker_root_dir": newPath}
			if data, err := json.Marshal(dockerCfg); err == nil {
				_ = os.WriteFile("/var/lib/casaos/docker_root", data, 0644)
			}
			updateDockerDaemonRoot(newPath)
			_ = exec.Command("systemctl", "start", "docker").Run()
		}
	}

	close(done)
	if err := SavePathConfig(cfg); err != nil {
		logger.Error("failed to save path config", zap.Error(err))
	}

	if isBatch {
		return targetMountPoint, nil
	}
	return filepath.Join(targetMountPoint, filepath.Base(srcPath)), nil
}

func trackProgress(jobID, destPath string, totalSize int64, done <-chan struct{}) {
	trackBatchProgress(jobID, []string{destPath}, totalSize, done)
}

func trackBatchProgress(jobID string, destFolders []string, totalSize int64, done <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// Calculate aggregate processed size across all folders in batch.
			var processed int64
			for _, path := range destFolders {
				s, _ := localfile.GetFileOrDirSize(path)
				processed += s
			}

			pct := 0
			if totalSize > 0 {
				pct = int(processed * 95 / totalSize) // cap at 95 until fully done
			}
			if pct > 95 {
				pct = 95
			}
			// Diagnostic heart-beat log
			logger.Info("migration progress trace", 
				zap.String("id", jobID), 
				zap.Int("progress", pct), 
				zap.Int64("processed", processed), 
				zap.Int64("total", totalSize))
			
			updateProgress(jobID, pct, processed, totalSize)
		}
	}
}

func updateProgress(jobID string, pct int, processed, total int64) {
	if v, ok := MigrateJobs.Load(jobID); ok {
		s := v.(MigrateStatus)
		s.Progress = pct
		s.ProcessedSize = processed
		s.TotalSize = total
		MigrateJobs.Store(jobID, s)
	}
}

func updateSymlink(linkPath, target string) {
	// Use RemoveAll to ensure we can delete the existing directory if it wasn't a symlink yet.
	_ = os.RemoveAll(linkPath)
	if err := os.Symlink(target, linkPath); err != nil {
		logger.Error("failed to update symlink", zap.String("link", linkPath), zap.String("target", target), zap.Error(err))
	}
}

func updateDockerDaemonRoot(newRoot string) {
	const daemonJSON = "/etc/docker/daemon.json"
	existing := map[string]interface{}{}
	if data, err := os.ReadFile(daemonJSON); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	existing["data-root"] = newRoot
	if data, err := json.MarshalIndent(existing, "", "  "); err == nil {
		_ = os.WriteFile(daemonJSON, data, 0644)
	}
}
