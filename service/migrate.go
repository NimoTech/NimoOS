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

	var srcPath, dirName string
	switch migrationType {
	case MigrateTypeAppData:
		srcPath = cfg.AppData
		dirName = filepath.Base(srcPath)
		if dirName == "" || dirName == "." {
			dirName = "AppData"
		}
	case MigrateTypeImages:
		srcPath = cfg.Images
		dirName = filepath.Base(srcPath)
		if dirName == "" || dirName == "." {
			dirName = ".docker"
		}
	case MigrateTypeUserData:
		srcPath = cfg.UserData
		dirName = filepath.Base(srcPath)
		if dirName == "" || dirName == "." {
			dirName = "Gallery & Downloads & Documents & Media"
		}
	default:
		return "", fmt.Errorf("unknown migration type: %s", migrationType)
	}

	// CopyDir appends the last segment of src to dst automatically,
	// so dst = targetMountPoint gives us targetMountPoint/<dirName>.
	newPath := filepath.Join(targetMountPoint, dirName)

	// Measure total size for progress tracking.
	totalSize, err := localfile.GetFileOrDirSize(srcPath)
	if err != nil {
		logger.Error("migration: failed to calculate total size", zap.String("path", srcPath), zap.Error(err))
	}
	logger.Info("migration: total size calculated", zap.String("id", jobID), zap.Int64("total", totalSize))
	updateProgress(jobID, 1, 0, totalSize) // Push 1% immediately to show life

	// Docker migration: stop daemon before touching its data dir.
	if migrationType == MigrateTypeImages {
		logger.Info("stopping docker for migration")
		_ = exec.Command("systemctl", "stop", "docker").Run()
	}

	// Start progress tracking goroutine.
	done := make(chan struct{})
	go trackProgress(jobID, newPath, totalSize, done)

	// Perform the copy.
	logger.Info("starting data copy", zap.String("src", srcPath), zap.String("dst", targetMountPoint))
	if err := localfile.CopyDir(srcPath, targetMountPoint, "overwrite"); err != nil {
		close(done)
		if migrationType == MigrateTypeImages {
			_ = exec.Command("systemctl", "start", "docker").Run()
		}
		return "", fmt.Errorf("copy failed: %w", err)
	}
	close(done)
	logger.Info("data copy completed successfully", zap.String("newPath", newPath))

	// Remove old data only after the copy is verified done.
	if srcPath != newPath {
		logger.Info("removing old data", zap.String("path", srcPath))
		if err := os.RemoveAll(srcPath); err != nil {
			logger.Error("failed to remove old path after migration", zap.String("path", srcPath), zap.Error(err))
		}
	}

	// Update config and links only after physical cleanup.
	switch migrationType {
	case MigrateTypeAppData:
		cfg.AppData = newPath
		// Establish /DATA/AppData symlink.
		updateSymlink("/DATA/AppData", newPath)

	case MigrateTypeImages:
		cfg.Images = newPath
		// Update docker root config file.
		dockerCfg := map[string]string{"docker_root_dir": newPath}
		if data, err := json.Marshal(dockerCfg); err == nil {
			_ = os.WriteFile("/var/lib/casaos/docker_root", data, 0644)
		}
		// Also update /etc/docker/daemon.json so Docker itself uses the new path.
		updateDockerDaemonRoot(newPath)
		logger.Info("starting docker after migration")
		_ = exec.Command("systemctl", "start", "docker").Run()

	case MigrateTypeUserData:
		cfg.UserData = newPath
		updateSymlink("/DATA/Gallery & Downloads & Documents & Media", newPath)
	}

	if err := SavePathConfig(cfg); err != nil {
		logger.Error("failed to save path config", zap.Error(err))
	}

	return newPath, nil
}

func trackProgress(jobID, destPath string, totalSize int64, done <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// Calculate processed size.
			processed, err := localfile.GetFileOrDirSize(destPath)
			if err != nil {
				// Log the error but keep the progress map updated with current values.
				logger.Info("migration: scanning destPath pending (normal during startup)", zap.String("path", destPath), zap.Error(err))
				processed = 0
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
