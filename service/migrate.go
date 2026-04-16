package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	localfile "github.com/NimoTech/NimoOS/pkg/utils/file"
	"go.uber.org/zap"
)

const (
	PathConfigFile = "/var/lib/nimoos/path_config.json"

	MigrateTypeAppData  = "app_data"
	MigrateTypeImages   = "images"
	MigrateTypeUserData = "database"

	DefaultAppDataPath  = "/DATA/AppData"
	DefaultImagesPath   = "/var/lib/docker"
	DefaultUserDataPath = "/DATA"
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

var activeMigrationDst string
var activeMigrationMu sync.Mutex

func CleanupActiveMigration() {
	activeMigrationMu.Lock()
	dst := activeMigrationDst
	activeMigrationMu.Unlock()
	if dst == "" {
		return
	}
	logger.Info("removing partial migration destination on shutdown", zap.String("path", dst))
	_ = os.RemoveAll(dst)
}

// ResolveActivePaths detects where the data is actually living by checking config and standard symlinks.
// It auto-corrects the config if it finds reality differs from the stored JSON.
func ResolveActivePaths() PathConfig {
	cfg := LoadPathConfig()
	changed := false

	resolveReal := func(configuredPath, legacyPath string) string {
		// 1. If configured path exists, trust it
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		}

		// 2. If not, check if there's a legacy entry point (like /DATA/AppData) and where it leads
		if resolved, err := filepath.EvalSymlinks(legacyPath); err == nil {
			if _, serr := os.Stat(resolved); serr == nil {
				changed = true
				return resolved
			}
		}

		return configuredPath
	}

	cfg.AppData = resolveReal(cfg.AppData, DefaultAppDataPath)
	
	// Fast track for Images: The true and only source of truth is the current destination of /var/lib/docker
	if resolvedDocker, err := filepath.EvalSymlinks(DefaultImagesPath); err == nil {
		if cfg.Images != resolvedDocker {
			cfg.Images = resolvedDocker
			changed = true
		}
	} else {
		cfg.Images = DefaultImagesPath
	}
	
	// Special handling for UserData which usually contains subfolders
	if _, err := os.Stat(cfg.UserData); err != nil {
		if resolved, err := filepath.EvalSymlinks(DefaultUserDataPath); err == nil {
			// Check if it's a real directory (not just a broken link)
			if info, serr := os.Stat(resolved); serr == nil && info.IsDir() {
				cfg.UserData = resolved
				changed = true
			}
		}
	}

	if changed {
		logger.Info("auto-corrected path config based on system symlinks", zap.Any("new_cfg", cfg))
		_ = SavePathConfig(cfg)
	}

	return cfg
}

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

func SavePathConfig(cfg PathConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PathConfigFile, data, 0644)
}

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

func GetMigrationStatus(jobID string) (MigrateStatus, bool) {
	v, ok := MigrateJobs.Load(jobID)
	if !ok {
		return MigrateStatus{}, false
	}
	return v.(MigrateStatus), true
}

func executeMigration(jobID, migrationType, targetMountPoint string) (string, error) {
	cfg := ResolveActivePaths()

	type migrateUnit struct {
		anchor string
		src    string
		dst    string
	}
	var units []migrateUnit
	var newPath string

	isSystemRestore := (targetMountPoint == "/")

	switch migrationType {
	case MigrateTypeAppData:
		anchor := "/DATA/AppData"
		src, _ := filepath.EvalSymlinks(anchor)
		if src == "" {
			src = anchor
		}
		dst := filepath.Join(targetMountPoint, "AppData")
		if isSystemRestore {
			dst = anchor
			newPath = anchor
		} else {
			newPath = dst
		}
		units = append(units, migrateUnit{anchor, src, dst})

	case MigrateTypeImages:
		for _, name := range []string{"docker", "containerd"} {
			anchor := "/var/lib/" + name
			src, _ := filepath.EvalSymlinks(anchor)
			if src == "" {
				src = anchor
			}
			dst := filepath.Join(targetMountPoint, name)
			if isSystemRestore {
				dst = anchor
			}
			units = append(units, migrateUnit{anchor, src, dst})
		}
		newPath = filepath.Join(targetMountPoint, "docker")
		if isSystemRestore {
			newPath = "/var/lib/docker"
		}

	case MigrateTypeUserData:
		newPath = targetMountPoint
		if isSystemRestore {
			newPath = "/DATA"
		}
		for _, name := range []string{"Documents", "Downloads", "Gallery", "Media"} {
			anchor := "/DATA/" + name
			src, _ := filepath.EvalSymlinks(anchor)
			if src == "" {
				src = anchor
			}
			dst := filepath.Join(targetMountPoint, name)
			if isSystemRestore {
				dst = anchor
			}
			units = append(units, migrateUnit{anchor, src, dst})
		}

	default:
		return "", fmt.Errorf("unknown migration type: %s", migrationType)
	}

	activeMigrationMu.Lock()
	activeMigrationDst = newPath
	activeMigrationMu.Unlock()
	defer func() {
		activeMigrationMu.Lock()
		activeMigrationDst = ""
		activeMigrationMu.Unlock()
	}()

	// 1. Size tracking
	var totalSize int64
	for _, u := range units {
		if u.src == u.dst || strings.HasPrefix(u.dst, u.src) {
			continue
		}
		sz, _ := localfile.GetFileOrDirSize(u.src)
		totalSize += sz
	}
	updateProgress(jobID, 1, 0, totalSize)

	if migrationType == MigrateTypeImages {
		logger.Info("stopping docker and containerd for migration")
		_ = exec.Command("systemctl", "stop", "docker.socket", "docker", "containerd").Run()
	}

	done := make(chan struct{})
	go trackProgress(jobID, newPath, totalSize, done)

	// 2. Perform Copies & Cleanup Anchor
	for _, u := range units {
		if u.src == u.dst || strings.HasPrefix(u.dst, u.src) {
			continue
		}
		
		if _, err := os.Stat(u.src); os.IsNotExist(err) {
			continue
		}

		// If we are restoring to system, the destination IS the anchor.
		// We must severe the symlink connecting them BEFORE we copy, or rsync goes into a black hole.
		if isSystemRestore && u.dst == u.anchor {
			_ = os.Remove(u.anchor)
		}

		_ = os.MkdirAll(u.dst, 0755)

		srcWithSlash := strings.TrimRight(u.src, "/") + "/"
		dstWithSlash := strings.TrimRight(u.dst, "/") + "/"

		if err := exec.Command("rsync", "-a", srcWithSlash, dstWithSlash).Run(); err != nil {
			close(done)
			if migrationType == MigrateTypeImages {
				_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
			}
			return "", fmt.Errorf("copy failed during rsync of %s: %w", u.anchor, err)
		}
	}
	close(done)

	// 3. Anchor cleanup and redirection
	for _, u := range units {
		if u.src == u.dst {
			continue
		}

		// Delete the old exterior location
		if u.src != u.anchor {
			_ = os.RemoveAll(u.src)
		}

		// If it's a System Restore, the physical files are NOW solidly sitting at the anchor. No symlink needed!
		if !isSystemRestore || u.dst != u.anchor {
			_ = os.RemoveAll(u.anchor)
			_ = os.Symlink(u.dst, u.anchor)
		}
	}

	// 4. Update Config & finalize
	switch migrationType {
	case MigrateTypeAppData:
		cfg.AppData = newPath
	case MigrateTypeImages:
		cfg.Images = newPath
		logger.Info("starting docker and containerd after migration")
		_ = exec.Command("systemctl", "start", "containerd", "docker.socket", "docker").Run()
	case MigrateTypeUserData:
		cfg.UserData = newPath
	}

	if err := SavePathConfig(cfg); err != nil {
		logger.Error("failed to save path config", zap.Error(err))
	}

	return newPath, nil
}

func trackProgress(jobID, destPath string, totalSize int64, done <-chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			processed, _ := localfile.GetFileOrDirSize(destPath)
			pct := 0
			if totalSize > 0 {
				pct = int(processed * 95 / totalSize)
			}
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


