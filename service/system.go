package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	net2 "net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/command"
	exec2 "github.com/NimoTech/NimoOS-Common/utils/exec"

	"github.com/NimoTech/NimoOS-Common/utils/file"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/utils/common_err"
	"github.com/NimoTech/NimoOS/pkg/utils/httper"
	"github.com/NimoTech/NimoOS/pkg/utils/ip_helper"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

type SystemService interface {
	UpdateSystemVersion(version string) error
	CheckUpdate() *UpgradeCheckResult
	DownloadUpdate() error
	CancelDownload()
	GetDownloadStatus() (string, float64) // returns (status, progress)
	StartDailyDownloadChecker()
	GetSystemConfigDebug() []string
	GetNimoOSLogs(lineNumber int) string
	UpdateAssist()
	UpSystemPort(port string)
	GetTimeZone() string
	UpAppOrderFile(str, id string)
	GetAppOrderFile(id string) []byte
	GetNet(physics bool) []string
	GetNetInfo() []net.IOCountersStat
	GetCpuCoreNum() int
	GetCpuPercent() float64
	GetMemInfo() map[string]interface{}

	GetCpuInfo() []cpu.InfoStat
	GetDirPath(path string) ([]model.Path, error)
	GetDirPathOne(path string) (m model.Path)
	GetNetState(name string) string
	GetDiskInfo() *disk.UsageStat
	GetSysInfo() host.InfoStat
	GetDeviceTree() string
	GetDeviceInfo() model.DeviceInfo
	CreateFile(path string) (int, error)
	RenameFile(oldF, newF string) (int, error)
	MkdirAll(path string) (int, error)
	GetCPUTemperature() int
	GetCPUPower() map[string]string
	GetMacAddress() (string, error)
	SystemReboot() error
	SystemShutdown() error
	GetSystemEntry() string
	GenreateSystemEntry()
	GetGpuInfo() []string
	GetGpuStatus() []GpuStatus
	GetRamDetail() map[string]string
	SetDiskStandby(minutes int) error
	GetNetAddr(name string) string
	GetNetSpeed(name string) int
	GetNetMaxSpeed(name string) int
	GetSystemPaths() map[string]interface{}
	StartMigrateAppPath(jobID, migrationType, targetMountPoint string)
	GetMigrateStatus(jobID string) (MigrateStatus, bool)
	GetOSVersion() string
	GetVersion() string
	GetHardwareID() string
	GetHardwareName() string
	CheckAppUpdate() *UpgradeCheckResult
	DownloadAppUpdate() error
	CancelAppDownload()
	UpdateAppVersion(version string) error
	SyncStartupUpgradeStatus()
}

// downloaderState manages the lifecycle of the system upgrade bundle download goroutine
type downloaderState struct {
	mu          sync.RWMutex
	status      string // "idle" | "running" | "paused" | "completed" | "failed"
	version     string
	progress    float64 // 0.00-100.00
	cancel      context.CancelFunc
	downloadDir string // current download directory, used to clean up .tmp on cancel
}

type systemService struct {
	downloader    downloaderState
	appDownloader downloaderState
}

func (s *systemService) GetOSVersion() string {
	version := common.VERSION
	local, err := readLocalVersionInfo()
	if err == nil && local.Version != "" {
		version = local.Version
	}
	return version
}

func (s *systemService) GetVersion() string {
	version := common.VERSION
	local, err := readLocalVersionInfo()
	if err == nil && local.AppVersion != "" && local.AppVersion != "0.0.0" {
		version = local.AppVersion
	}
	return version
}

func (s *systemService) GetHardwareID() string {
	local, err := readLocalVersionInfo()
	if err != nil {
		return ""
	}
	return local.HardwareID
}

func (s *systemService) GetHardwareName() string {
	local, err := readLocalVersionInfo()
	if err != nil {
		return ""
	}
	return local.HardwareName
}

func (c *systemService) GetDeviceInfo() model.DeviceInfo {
	m := model.DeviceInfo{}
	m.OS_Version = c.GetOSVersion()
	err, portStr := MyService.Gateway().GetPort()
	if err != nil {
		m.Port = 80
	} else {
		port := gjson.Get(portStr, "data")
		if len(port.Raw) == 0 {
			m.Port = 80
		} else {
			p, err := strconv.Atoi(port.Raw)
			if err != nil {
				m.Port = 80
			} else {
				m.Port = p
			}
		}
	}
	allIpv4 := ip_helper.GetDeviceAllIPv4()
	ip := []string{}
	nets := MyService.System().GetNet(true)
	for _, n := range nets {
		if v, ok := allIpv4[n]; ok {
			{
				ip = append(ip, v)
			}
		}
	}

	m.LanIpv4 = ip
	h, err := host.Info() /*  */
	if err == nil {
		m.DeviceName = h.Hostname
	}
	mb := model.BaseInfo{}

	err = json.Unmarshal(file.ReadFullFile(config.AppInfo.DBPath+"/baseinfo.conf"), &mb)
	if err == nil {
		m.Hash = mb.Hash
	}

	osRelease, _ := file.ReadOSRelease()
	m.DeviceModel = osRelease["MODEL"]
	m.DeviceSN = osRelease["SN"]
	res := httper.Get("http://127.0.0.1:"+strconv.Itoa(m.Port)+"/v1/users/status", nil)
	init := gjson.Get(res, "data.initialized")
	m.Initialized, _ = strconv.ParseBool(init.Raw)

	return m
}

func (c *systemService) GenreateSystemEntry() {
	modelsPath := "/var/lib/nimoos/www/modules"
	entryFileName := "entry.json"
	entryFilePath := filepath.Join(config.AppInfo.DBPath, "db", entryFileName)
	file.IsNotExistCreateFile(entryFilePath)

	dir, err := os.ReadDir(modelsPath)
	if err != nil {
		logger.Error("read dir error", zap.Error(err))
		return
	}
	json := "["
	for _, v := range dir {
		data, err := os.ReadFile(filepath.Join(modelsPath, v.Name(), entryFileName))
		if err != nil {
			logger.Error("read entry file error", zap.Error(err))
			continue
		}
		json += string(data) + ","
	}
	json = strings.TrimRight(json, ",")
	json += "]"
	err = os.WriteFile(entryFilePath, []byte(json), 0o666)
	if err != nil {
		logger.Error("write entry file error", zap.Error(err))
		return
	}
}

func (c *systemService) GetSystemEntry() string {
	modelsPath := "/var/lib/nimoos/www/modules"
	entryFileName := "entry.json"
	dir, err := os.ReadDir(modelsPath)
	if err != nil {
		logger.Error("read dir error", zap.Error(err))
		return ""
	}
	json := "["
	for _, v := range dir {
		data, err := os.ReadFile(filepath.Join(modelsPath, v.Name(), entryFileName))
		if err != nil {
			logger.Error("read entry file error", zap.Error(err))
			continue
		}
		json += string(data) + ","
	}
	json = strings.TrimRight(json, ",")
	json += "]"
	if err != nil {
		logger.Error("write entry file error", zap.Error(err))
		return ""
	}
	return json
}

func (c *systemService) GetMacAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	nets := MyService.System().GetNet(true)
	for _, v := range interfaces {
		for _, n := range nets {
			if v.Name == n {
				return v.HardwareAddr, nil
			}
		}
	}
	return "", errors.New("not found")
}

func (c *systemService) MkdirAll(path string) (int, error) {
	_, err := os.Stat(path)
	if err == nil {
		return common_err.DIR_ALREADY_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			os.MkdirAll(path, os.ModePerm)
			return common_err.SUCCESS, nil
		} else if strings.Contains(err.Error(), ": not a directory") {
			return common_err.FILE_OR_DIR_EXISTS, err
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) RenameFile(oldF, newF string) (int, error) {
	_, err := os.Stat(newF)
	if err == nil {
		return common_err.DIR_ALREADY_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			err := os.Rename(oldF, newF)
			if err != nil {
				return common_err.SERVICE_ERROR, err
			}
			return common_err.SUCCESS, nil
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) CreateFile(path string) (int, error) {
	_, err := os.Stat(path)
	if err == nil {
		return common_err.FILE_OR_DIR_EXISTS, nil
	} else {
		if os.IsNotExist(err) {
			file.CreateFile(path)
			return common_err.SUCCESS, nil
		}
	}
	return common_err.SERVICE_ERROR, err
}

func (c *systemService) GetDeviceTree() string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetDeviceTree"); err != nil {
		return ""
	} else {
		return output
	}
}

func (c *systemService) GetSysInfo() host.InfoStat {
	info, _ := host.Info()
	return *info
}

func (c *systemService) GetDiskInfo() *disk.UsageStat {
	path := "/"
	if runtime.GOOS == "windows" {
		path = "C:"
	}
	diskInfo, _ := disk.Usage(path)
	diskInfo.UsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", diskInfo.UsedPercent), 64)
	diskInfo.InodesUsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", diskInfo.InodesUsedPercent), 64)
	return diskInfo
}

func (c *systemService) GetNetState(name string) string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;CatNetCardState " + name); err != nil {
		return ""
	} else {
		return output
	}
}

func (c *systemService) GetDirPathOne(path string) (m model.Path) {
	f, err := os.Stat(path)
	if err != nil {
		return
	}
	m.IsDir = f.IsDir()
	m.Name = f.Name()
	m.Path = path
	m.Size = f.Size()
	m.Date = f.ModTime()
	return
}

func (c *systemService) GetDirPath(path string) ([]model.Path, error) {
	if path == "/DATA" {
		sysType := runtime.GOOS
		if sysType == "windows" {
			path = "C:\\NimoOS\\DATA"
		}
		if sysType == "darwin" {
			path = "./NimoOS/DATA"
		}

	}

	ls, err := os.ReadDir(path)
	if err != nil {
		logger.Error("when read dir", zap.Error(err))
		return []model.Path{}, err
	}
	dirs := []model.Path{}
	if len(path) > 0 {
		for _, l := range ls {
			filePath := filepath.Join(path, l.Name())
			tempFile, err := l.Info()
			if err != nil {
				logger.Error("when read dir", zap.Error(err))
				return []model.Path{}, err
			}
			// os.Lstat never follows symlinks, so ModeSymlink is set iff the entry itself is a link.
			linfo, lerr := os.Lstat(filePath)
			isSymlink := lerr == nil && linfo.Mode()&os.ModeSymlink != 0
			// For the IsDir flag, follow the symlink so callers get the real type.
			isDir := l.IsDir()
			if isSymlink {
				if file, serr := os.Stat(filePath); serr == nil {
					isDir = file.IsDir()
				} else {
					isDir = false
				}
			}
			temp := model.Path{Name: l.Name(), Path: filePath, IsDir: isDir, IsSymlink: isSymlink, Date: tempFile.ModTime(), Size: tempFile.Size()}
			dirs = append(dirs, temp)
		}
	} else {
		dirs = append(dirs, model.Path{Name: "DATA", Path: "/DATA/", IsDir: true, Date: time.Now()})
	}
	return dirs, nil
}

func (c *systemService) GetCpuInfo() []cpu.InfoStat {
	info, _ := cpu.Info()
	return info
}

func (c *systemService) GetMemInfo() map[string]interface{} {
	memInfo, _ := mem.VirtualMemory()
	memInfo.UsedPercent, _ = strconv.ParseFloat(fmt.Sprintf("%.1f", memInfo.UsedPercent), 64)
	memData := make(map[string]interface{})
	memData["total"] = memInfo.Total
	memData["available"] = memInfo.Available
	memData["used"] = memInfo.Used
	memData["free"] = memInfo.Free
	memData["usedPercent"] = memInfo.UsedPercent
	return memData
}

func (c *systemService) GetCpuPercent() float64 {
	percent, _ := cpu.Percent(0, false)
	value, _ := strconv.ParseFloat(fmt.Sprintf("%.1f", percent[0]), 64)
	return value
}

func (c *systemService) GetCpuCoreNum() int {
	count, _ := cpu.Counts(false)
	return count
}

func (c *systemService) GetNetInfo() []net.IOCountersStat {
	parts, _ := net.IOCounters(true)
	return parts
}

func (c *systemService) GetNet(physics bool) []string {
	t := "1"
	if physics {
		t = "2"
	}

	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetNetCard " + t); err == nil {
		if trimmed := strings.TrimSpace(output); len(trimmed) > 0 {
			return strings.Split(trimmed, "\n")
		}
	}

	// Fallback: enumerate directly from /sys/class/net when helper.sh is unavailable
	allNets, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return []string{}
	}

	virtualSet := make(map[string]bool)
	if virtualNets, err := os.ReadDir("/sys/devices/virtual/net"); err == nil {
		for _, n := range virtualNets {
			virtualSet[n.Name()] = true
		}
	}

	result := []string{}
	for _, n := range allNets {
		name := n.Name()
		isVirtual := virtualSet[name]
		if physics && !isVirtual {
			result = append(result, name)
		} else if !physics && isVirtual {
			result = append(result, name)
		}
	}
	return result
}

func (s *systemService) UpdateSystemVersion(version string) error {
	if locked, err := isFileLocked("/var/run/nimo_upgrade.lock"); err == nil && locked {
		return fmt.Errorf("another system upgrade is already running")
	}

	keyName := "casa_version"
	Cache.Delete(keyName)

	upgradeDir := "/var/lib/nimoos_data/upgrade"
	entries, err := os.ReadDir(upgradeDir)
	if err != nil {
		return fmt.Errorf("upgrade directory not found: %v", err)
	}

	var latestBundle string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bundleDir := filepath.Join(upgradeDir, entry.Name())
		bundleEntries, _ := os.ReadDir(bundleDir)
		for _, be := range bundleEntries {
			if !be.IsDir() && filepath.Ext(be.Name()) == ".raucb" {
				latestBundle = filepath.Join(bundleDir, be.Name())
			}
		}
	}

	if latestBundle == "" {
		return fmt.Errorf("no downloaded upgrade bundle found")
	}

	// extract the target version from the directory name: /var/lib/nimoos_data/upgrade/v1.0.1/ → 1.0.1
	bundleDir := filepath.Dir(latestBundle)
	targetVersion := strings.TrimPrefix(filepath.Base(bundleDir), "v")

	local, _ := readLocalVersionInfo()
	currentVersion := s.GetOSVersion()

	go func() {
		logger.Info("starting system upgrade", zap.String("bundle", latestBundle),
			zap.String("target", targetVersion))

		// report installing status
		if local != nil {
			reportUpgradeResult(local.UpdateServer, local.DeviceID, "os", currentVersion, targetVersion, "installing", "", "")
		}

		cmd := exec.Command("systemd-run", "--scope", "--unit=nimoos-upgrade", "--quiet",
			"/usr/libexec/nimo_upgrade.sh", latestBundle)
		output, err := cmd.CombinedOutput()

		if err != nil || !strings.Contains(string(output), "NimoOS upgrade successfully") {
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 9 {
					logger.Error("another system upgrade is already running, ignoring this request")
					return
				}
			}
			logger.Error("system upgrade failed", zap.Error(err), zap.String("log", string(output)))
			if local != nil {
				logPath := "/var/log/nimoos_upgrade.log"
				_ = os.WriteFile(logPath, output, 0644)
				logURL, uploadErr := uploadUpgradeLog(local.UpdateServer, local.DeviceID, logPath)
				if uploadErr != nil {
					logger.Error("Failed to upload OS upgrade log", zap.Error(uploadErr))
				}
				reportUpgradeResult(local.UpdateServer, local.DeviceID, "os", currentVersion, targetVersion, "failed", "rauc_install_failed", logURL)
			}
			return
		}

		logger.Info("system upgrade successful, cleaning up downloaded bundles")
		upgradeDir := "/var/lib/nimoos_data/upgrade"
		if cleanupEntries, err := os.ReadDir(upgradeDir); err == nil {
			for _, entry := range cleanupEntries {
				if !entry.IsDir() {
					continue
				}
				os.RemoveAll(filepath.Join(upgradeDir, entry.Name()))
			}
		}
		Cache.Delete("check_update_result")

		// record the system upgrade status file, for syncing after reboot
		systemStatusFilePath := "/var/lib/nimoos_data/upgrade/system_status.json"
		statusData := map[string]string{
			"update_type":  "os",
			"from_version": currentVersion,
			"to_version":   targetVersion,
			"status":       "installing",
		}
		if statusBytes, serr := json.Marshal(statusData); serr == nil {
			_ = os.WriteFile(systemStatusFilePath, statusBytes, 0644)
		}

		logger.Info("system upgrade successful, rebooting")
		time.Sleep(3 * time.Second)
		command.OnlyExec("systemctl reboot")
	}()

	return nil
}

func (s *systemService) IsUpgradeDownloaded(version string) bool {
	upgradeDir := "/var/lib/nimoos_data/upgrade/v" + version
	entries, err := os.ReadDir(upgradeDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".raucb" {
			return true
		}
	}
	return false
}

type localVersionJSON struct {
	Version      string `json:"version"`
	AppVersion   string `json:"app_version"`
	UpdateServer string `json:"update_server"`
	Platform     string `json:"platform"`
	HardwareID   string `json:"hardware_id"`
	HardwareName string `json:"hardware_name"`
	DeviceID     string `json:"device_id"`
}

func readLocalVersionInfo() (*localVersionJSON, error) {
	versionFile := "/etc/nimoos/version.json"
	if _, err := os.Stat(versionFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("version file not found")
	}
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read version file: %v", err)
	}
	var local localVersionJSON
	if err := json.Unmarshal(data, &local); err != nil {
		return nil, fmt.Errorf("cannot parse version file: %v", err)
	}
	if local.Version == "" {
		return nil, fmt.Errorf("version field is empty")
	}

	// if device_id is missing, read it from /etc/machine-id
	if local.DeviceID == "" {
		if mid, err := os.ReadFile("/etc/machine-id"); err == nil {
			local.DeviceID = strings.TrimSpace(string(mid))
		}
	}

	return &local, nil
}

// generateCookie generates the NimoOS-Cookie header: SHA256(device_id + ":" + secret)[:16]
func generateCookie(deviceID string) string {
	data := deviceID + ":" + common.CookieSecret
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

// readRAUCBootStatus reads boot_status for the A/B partitions and the currently booted partition
// return format: "{A's status},{B's status},booted:{booted partition}", e.g. "good,bad,booted:A"
func readRAUCBootStatus() string {
	output, err := command.OnlyExec("rauc status --output-format=json 2>/dev/null")
	if err != nil {
		return ""
	}

	booted := gjson.Get(output, "booted").String()
	statusA, statusB := "", ""
	for _, slot := range gjson.Get(output, "slots").Array() {
		for _, v := range slot.Map() {
			bootname := v.Get("bootname").String()
			status := v.Get("boot_status").String()
			if bootname == "A" && status != "" {
				statusA = status
			} else if bootname == "B" && status != "" {
				statusB = status
			}
		}
	}

	if statusA == "" && statusB == "" {
		return ""
	}
	if statusA == "" {
		statusA = "unknown"
	}
	if statusB == "" {
		statusB = "unknown"
	}
	if booted == "" {
		return statusA + "," + statusB
	}
	return statusA + "," + statusB + ",booted:" + booted
}

type upgradeCheckResponse struct {
	Success int    `json:"success"`
	Message string `json:"message"`
	Data    *struct {
		NeedUpdate     bool   `json:"need_update"`
		CurrentVersion string `json:"current_version"`
		LatestVersion  string `json:"latest_version"`
		Version        *struct {
			Version     string `json:"version"`
			ChangeLog   string `json:"change_log"`
			SHA256      string `json:"sha256"`
			Size        int    `json:"size"`
			DownloadURL string `json:"download_url"`
		} `json:"version"`
	} `json:"data"`
}

// checkCloudUpdate calls the cloud OTA service to check for updates.
// Cloud API: GET {update_server}/v1/sys/version?current_version=X
// Auth uses the NimoOS-Cookie: SHA256[:16] of deviceID + timestamp + CookieSecret
// Returns: (hasUpdate, versionInfo, error)
//   - 401 means auth failed → hasUpdate=false, error=nil (silent degrade, may be an expired key)
//   - 404 means no update available → hasUpdate=false, error=nil
//   - 503 means the service is unavailable → hasUpdate=false, error=nil (silent degrade)
//   - other errors → hasUpdate=false, error=err (caller decides whether to retry)
func checkCloudUpdate(server, currentVersion, platform, hardwareID, deviceID string) (bool, *versionInfo, error) {
	url := fmt.Sprintf("%s/v1/sys/version?current_version=%s", server, currentVersion)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, nil, fmt.Errorf("create request: %w", err)
	}

	// NimoOS-Cookie auth headers
	req.Header.Set("X-Nimo-Device-ID", deviceID)
	req.Header.Set("X-Nimo-Cookie", generateCookie(deviceID))
	req.Header.Set("X-Nimo-Platform", platform)
	req.Header.Set("X-Nimo-Hardware-ID", hardwareID)
	req.Header.Set("X-Nimo-RAUC-Status", readRAUCBootStatus())

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// 401: auth failed (silent degrade)
	if resp.StatusCode == http.StatusUnauthorized {
		logger.Info("checkCloudUpdate: authentication failed (401)")
		return false, nil, nil
	}
	// 404: no update available (silent handling)
	if resp.StatusCode == http.StatusNotFound {
		return false, nil, nil
	}
	// 503: service unavailable (silent degrade)
	if resp.StatusCode == http.StatusServiceUnavailable {
		logger.Info("checkCloudUpdate: cloud service unavailable (503)")
		return false, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("read response: %w", err)
	}

	var result upgradeCheckResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return false, nil, fmt.Errorf("parse response: %w", err)
	}

	if result.Data == nil || result.Data.Version == nil {
		return false, nil, nil
	}

	vi := &versionInfo{
		Version:     result.Data.Version.Version,
		Changelog:   result.Data.Version.ChangeLog,
		DownloadURL: result.Data.Version.DownloadURL,
		SHA256:      result.Data.Version.SHA256,
		Size:        result.Data.Version.Size,
	}

	return true, vi, nil
}

// reportUpgradeResult reports the upgrade result to the cloud
// status: "downloading" / "installing" / "completed" / "failed"
// Cookie algorithm matches checkCloudUpdate: SHA256(device_id + ":" + secret)[:16]
func reportUpgradeResult(server, deviceID, updateType, fromVersion, toVersion, status, errorMsg, logURL string) {
	url := fmt.Sprintf("%s/v1/sys/report", server)

	body := map[string]interface{}{
		"device_id":     deviceID,
		"event":         "upgrade_result",
		"update_type":   updateType,
		"from_version":  fromVersion,
		"to_version":    toVersion,
		"status":        status,
		"error_message": errorMsg,
		"log_s3_url":    logURL,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		logger.Error("reportUpgradeResult: marshal failed", zap.Error(err))
		return
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		logger.Error("reportUpgradeResult: create request failed", zap.Error(err))
		return
	}

	// NimoOS-Cookie auth headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nimo-Device-ID", deviceID)
	req.Header.Set("X-Nimo-Cookie", generateCookie(deviceID))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("reportUpgradeResult: request failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("reportUpgradeResult: unexpected status", zap.Int("status", resp.StatusCode))
	}
}

type compatibleRule struct {
	Platform   []string `json:"platform,omitempty"`
	HardwareID []string `json:"hardware_id,omitempty"`
	MinVersion string   `json:"min_version,omitempty"`
	MaxVersion string   `json:"max_version,omitempty"`
}

type versionInfo struct {
	Version     string          `json:"version"`
	Changelog   string          `json:"changelog"`
	DownloadURL string          `json:"download_url"`
	SHA256      string          `json:"sha256"`
	Size        int             `json:"size"`
	Compatible  *compatibleRule `json:"compatible,omitempty"`
}

// reportFailed uploads the upgrade log and reports the failure result to the cloud
func (s *systemService) reportFailed(server, deviceID, updateType, fromVersion, toVersion, errorMsg string) {
	logPath := "/var/log/nimoos_app_upgrade.log"
	if updateType == "os" {
		logPath = "/var/log/nimoos/upgrade.log"
	}
	logURL, _ := uploadUpgradeLog(server, deviceID, logPath)
	reportUpgradeResult(server, deviceID, updateType, fromVersion, toVersion, "failed", errorMsg, logURL)
}

func compareVersions(v1, v2 string) int {
	p1 := strings.Split(v1, ".")
	p2 := strings.Split(v2, ".")
	for i := 0; i < 3; i++ {
		n1, _ := strconv.Atoi(p1[i])
		n2, _ := strconv.Atoi(p2[i])
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

func isVersionCompatible(v versionInfo, currentVersion, platform, hardwareID string) bool {
	if v.Compatible == nil {
		return true
	}
	if len(v.Compatible.Platform) > 0 && !containsString(v.Compatible.Platform, platform) {
		return false
	}
	if len(v.Compatible.HardwareID) > 0 && !containsString(v.Compatible.HardwareID, hardwareID) {
		return false
	}
	if v.Compatible.MinVersion != "" && compareVersions(currentVersion, v.Compatible.MinVersion) < 0 {
		return false
	}
	if v.Compatible.MaxVersion != "" && compareVersions(currentVersion, v.Compatible.MaxVersion) > 0 {
		return false
	}
	return true
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func findHighestCompatibleVersion(versions []versionInfo, currentVersion, platform, hardwareID string) *versionInfo {
	var best *versionInfo
	var bestNums []int
	for i, v := range versions {
		if !isVersionCompatible(v, currentVersion, platform, hardwareID) {
			continue
		}
		parts := strings.Split(v.Version, ".")
		nums := make([]int, 3)
		for j, p := range parts {
			nums[j], _ = strconv.Atoi(p)
		}
		if best == nil {
			best = &versions[i]
			bestNums = nums
		} else {
			if nums[0] > bestNums[0] || (nums[0] == bestNums[0] && nums[1] > bestNums[1]) || (nums[0] == bestNums[0] && nums[1] == bestNums[1] && nums[2] > bestNums[2]) {
				best = &versions[i]
				bestNums = nums
			}
		}
	}
	return best
}

type UpgradeCheckResult struct {
	HasUpdate        bool    `json:"has_update"`
	CurrentVersion   string  `json:"current_version"`
	LatestVersion    string  `json:"latest_version"`
	IsDownloaded     bool    `json:"is_downloaded"`
	IsDownloading    bool    `json:"is_downloading"`              // downloader goroutine is running, a download is in progress
	IsPaused         bool    `json:"is_paused"`                   // .tmp leftover but goroutine idle, can resume
	DownloadProgress float64 `json:"download_progress,omitempty"` // 0.00-100.00
	Changelog        string  `json:"changelog"`
	Size             int     `json:"size"`
	Error            string  `json:"error,omitempty"`
}

func (s *systemService) CheckUpdate() *UpgradeCheckResult {
	const cacheKey = "check_update_result"
	if cached, ok := Cache.Get(cacheKey); ok {
		if r, ok := cached.(*UpgradeCheckResult); ok {
			// downloading/completed: the cache doesn't hold live progress or disk state, needs rechecking
			s.downloader.mu.RLock()
			status := s.downloader.status
			isRunning := status == "running"
			s.downloader.mu.RUnlock()
			if isRunning || status == "completed" {
				Cache.Delete(cacheKey)
			} else if r.IsDownloaded {
				downloadDir := "/var/lib/nimoos_data/upgrade/v" + r.LatestVersion
				found := false
				if entries, err := os.ReadDir(downloadDir); err == nil {
					for _, entry := range entries {
						if !entry.IsDir() && filepath.Ext(entry.Name()) == ".raucb" {
							found = true
							break
						}
					}
				}
				if !found {
					Cache.Delete(cacheKey)
				} else {
					return r
				}
			} else {
				return r
			}
		}
	}

	result := &UpgradeCheckResult{
		CurrentVersion: s.GetOSVersion(),
	}

	local, err := readLocalVersionInfo()
	if err != nil {
		Cache.Set(cacheKey, result, 1*time.Minute)
		return result
	}

	hasUpdate, bestVersion, err := checkCloudUpdate(local.UpdateServer, result.CurrentVersion, local.Platform, local.HardwareID, local.DeviceID)
	if err != nil {
		logger.Info("CheckUpdate: cloud check failed, will retry later", zap.Error(err))
		Cache.Set(cacheKey, result, 1*time.Minute)
		return result
	}
	if !hasUpdate || bestVersion == nil {
		Cache.Set(cacheKey, result, 5*time.Minute)
		return result
	}

	result.LatestVersion = bestVersion.Version
	result.Size = bestVersion.Size
	result.Changelog = bestVersion.Changelog

	if compareVersions(result.LatestVersion, result.CurrentVersion) <= 0 {
		return result
	}

	downloadDir := "/var/lib/nimoos_data/upgrade/v" + bestVersion.Version
	if entries, err := os.ReadDir(downloadDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := filepath.Ext(entry.Name())
			if ext == ".raucb" {
				result.IsDownloaded = true
				break
			}
			if ext == ".tmp" {
				result.IsPaused = true
			}
		}
	}

	// if the downloader goroutine is running, override to downloading (higher priority than paused)
	s.downloader.mu.RLock()
	if s.downloader.status == "running" {
		result.IsDownloading = true
		result.IsPaused = false
		result.DownloadProgress = s.downloader.progress
	} else if s.downloader.status == "paused" {
		result.IsPaused = true
		result.DownloadProgress = s.downloader.progress
	}
	s.downloader.mu.RUnlock()

	result.HasUpdate = true
	Cache.Set(cacheKey, result, 5*time.Minute)
	return result
}

func (s *systemService) DownloadUpdate() error {
	logger.Info("DownloadUpdate: starting")

	// check goroutine status
	s.downloader.mu.Lock()
	if s.downloader.status == "running" {
		s.downloader.mu.Unlock()
		logger.Info("DownloadUpdate: downloader goroutine already running")
		return nil
	}
	// reset state
	s.downloader.status = "running"
	s.downloader.progress = 0
	ctx, cancel := context.WithCancel(context.Background())
	s.downloader.cancel = cancel
	s.downloader.mu.Unlock()

	go s.runDownload(ctx)
	return nil
}

func (s *systemService) CancelDownload() {
	s.downloader.mu.RLock()
	cancel := s.downloader.cancel
	s.downloader.mu.RUnlock()

	if cancel != nil {
		cancel()
	}
	s.setDownloaderStatus("idle")
	// clear the cache so the next frontend request gets the correct idle status
	Cache.Delete("check_update_result")
}

func (s *systemService) GetDownloadStatus() (string, float64) {
	s.downloader.mu.RLock()
	defer s.downloader.mu.RUnlock()
	return s.downloader.status, s.downloader.progress
}

// --- APP downloader ---

func (s *systemService) DownloadAppUpdate() error {
	logger.Info("DownloadAppUpdate: starting")

	s.appDownloader.mu.Lock()
	if s.appDownloader.status == "running" {
		s.appDownloader.mu.Unlock()
		logger.Info("DownloadAppUpdate: downloader already running")
		return nil
	}
	s.appDownloader.status = "running"
	s.appDownloader.progress = 0
	ctx, cancel := context.WithCancel(context.Background())
	s.appDownloader.cancel = cancel
	s.appDownloader.mu.Unlock()

	go s.runAppDownload(ctx)
	return nil
}

func (s *systemService) CancelAppDownload() {
	s.appDownloader.mu.RLock()
	cancel := s.appDownloader.cancel
	s.appDownloader.mu.RUnlock()

	if cancel != nil {
		cancel()
	}
	s.setAppDownloaderStatus("idle")
	// clear the cache so the next frontend request gets the correct idle status
	Cache.Delete("check_app_update_result")
}

func (s *systemService) GetAppDownloadStatus() (string, float64) {
	s.appDownloader.mu.RLock()
	defer s.appDownloader.mu.RUnlock()
	return s.appDownloader.status, s.appDownloader.progress
}

func (s *systemService) runAppDownload(ctx context.Context) {
	logger.Info("runAppDownload: goroutine started")

	local, err := readLocalVersionInfo()
	if err != nil {
		logger.Error("runAppDownload: cannot read local version", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}

	currentAppVersion := common.VERSION
	if local.AppVersion != "" && local.AppVersion != "0.0.0" {
		currentAppVersion = local.AppVersion
	}

	hasUpdate, bestVersion, err := checkAppCloudUpdate(local.UpdateServer, currentAppVersion, local.Version, local.Platform, local.HardwareID, local.DeviceID)
	if err != nil {
		logger.Error("runAppDownload: cloud check failed", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}
	if !hasUpdate || bestVersion == nil {
		logger.Error("runAppDownload: no compatible version found")
		s.setAppDownloaderStatus("failed")
		return
	}

	s.appDownloader.mu.Lock()
	s.appDownloader.version = bestVersion.Version
	s.appDownloader.mu.Unlock()

	appUpgradeDir := fmt.Sprintf("/var/lib/nimoos_data/upgrade/app_v%s", bestVersion.Version)
	os.MkdirAll(appUpgradeDir, 0755)

	s.appDownloader.mu.Lock()
	s.appDownloader.downloadDir = appUpgradeDir
	s.appDownloader.mu.Unlock()

	bundlePath := filepath.Join(appUpgradeDir, "bundle.tar.gz")
	tmpFile := bundlePath + ".tmp"

	// clean up leftover .tmp from a previous interruption
	os.Remove(tmpFile)

	// already downloaded, complete directly
	if _, err := os.Stat(bundlePath); err == nil {
		logger.Info("runAppDownload: bundle already downloaded")
		s.setAppDownloaderStatus("completed")
		return
	}

	// report downloading status
	reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, bestVersion.Version, "downloading", "", "")

	fullURL := local.UpdateServer + bestVersion.DownloadURL
	logger.Info("runAppDownload: downloading", zap.String("url", fullURL), zap.Int64("size", int64(bestVersion.Size)))

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		logger.Error("runAppDownload: create request failed", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}

	downloadClient := &http.Client{Timeout: 30 * time.Minute}
	dlResp, err := downloadClient.Do(req)
	if err != nil {
		logger.Error("runAppDownload: download request failed", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		logger.Info("runAppDownload: download failed, refreshing URL and retrying", zap.Int("status", dlResp.StatusCode))
		dlResp.Body.Close()
		hasUpdate, bestVersion, err = checkAppCloudUpdate(local.UpdateServer, currentAppVersion, local.Version, local.Platform, local.HardwareID, local.DeviceID)
		if err != nil || !hasUpdate || bestVersion == nil {
			logger.Error("runAppDownload: version check failed after download error")
			s.setAppDownloaderStatus("failed")
			return
		}
		fullURL = local.UpdateServer + bestVersion.DownloadURL
		req, _ = http.NewRequestWithContext(ctx, "GET", fullURL, nil)
		dlResp, err = downloadClient.Do(req)
		if err != nil {
			logger.Error("runAppDownload: retry request failed", zap.Error(err))
			s.setAppDownloaderStatus("failed")
			return
		}
		defer dlResp.Body.Close()
		if dlResp.StatusCode != http.StatusOK {
			logger.Error("runAppDownload: retry still failed", zap.Int("status", dlResp.StatusCode))
			s.setAppDownloaderStatus("failed")
			return
		}
	}

	out, err := os.Create(tmpFile)
	if err != nil {
		logger.Error("runAppDownload: create file failed", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}
	defer out.Close()

	totalSize := bestVersion.Size
	logger.Info("runAppDownload: size from cloud", zap.Int("cloud_size", bestVersion.Size), zap.Int64("content_length", dlResp.ContentLength))
	if totalSize == 0 {
		totalSize = int(dlResp.ContentLength)
		if totalSize < 0 {
			logger.Info("runAppDownload: cannot determine total size, progress will show 0 until complete")
			totalSize = 0
		}
	}
	var totalWritten int64
	buf := make([]byte, 256*1024)
	lastPublishedProgress := -1.0
	reportedAny := false // marks whether at least one progress update was sent while totalSize=0

	for {
		select {
		case <-ctx.Done():
			logger.Info("runAppDownload: cancelled, cleaning up")
			out.Close()
			os.Remove(tmpFile)
			s.setAppDownloaderStatus("idle")
			return
		default:
		}

		n, readErr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				logger.Error("runAppDownload: write failed", zap.Error(writeErr))
				s.setAppDownloaderStatus("failed")
				return
			}
			totalWritten += int64(n)
			progress := 0.0
			if totalSize > 0 {
				progress = float64(totalWritten) * 100.0 / float64(totalSize)
				if progress > 100 {
					progress = 100
				}
				progress = math.Round(progress*100) / 100 // keep two decimal places
			}
			s.appDownloader.mu.Lock()
			s.appDownloader.progress = progress
			s.appDownloader.mu.Unlock()

			shouldReport := progress-lastPublishedProgress >= 0.01 || progress == 100
			if totalSize == 0 && totalWritten > 0 && !reportedAny {
				shouldReport = true
				reportedAny = true
			}
			if shouldReport {
				lastPublishedProgress = progress
				props := map[string]string{
					"version":  bestVersion.Version,
					"progress": strconv.FormatFloat(progress, 'f', 2, 64),
				}
				MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:app:download:progress", props)
				if int(progress)%10 == 0 {
					logger.Info("runAppDownload: progress", zap.Float64("pct", progress), zap.Int64("written", totalWritten), zap.Int("total", totalSize))
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				logger.Info("runAppDownload: cancelled, cleaning up")
				out.Close()
				os.Remove(tmpFile)
				s.setAppDownloaderStatus("idle")
				return
			}
			logger.Error("runAppDownload: read error", zap.Error(readErr))
			s.setAppDownloaderStatus("failed")
			return
		}
	}

	out.Close()

	// if the total size was unknown during download (Content-Length was -1), use the actual size after completion
	if totalSize == 0 {
		totalSize = int(totalWritten)
	}

	logger.Info("runAppDownload: download complete", zap.Int64("total_bytes", totalWritten), zap.Int64("expected_size", int64(totalSize)))

	// verify the download is complete
	if totalSize > 0 && totalWritten != int64(totalSize) {
		logger.Error("runAppDownload: incomplete download", zap.Int64("expected", int64(totalSize)), zap.Int64("actual", totalWritten))
		os.Remove(tmpFile)
		s.setAppDownloaderStatus("failed")
		return
	}

	// SHA256 verification
	if bestVersion.SHA256 == "" {
		logger.Error("runAppDownload: SHA256 not provided by cloud, rejecting download")
		s.reportFailed(local.UpdateServer, local.DeviceID, "app", currentAppVersion, bestVersion.Version, "sha256_missing")
		os.Remove(tmpFile)
		s.setAppDownloaderStatus("failed")
		return
	}
	hasher := sha256.New()
	f, err := os.Open(tmpFile)
	if err == nil {
		io.Copy(hasher, f)
		f.Close()
		actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualSHA256, bestVersion.SHA256) {
			s.reportFailed(local.UpdateServer, local.DeviceID, "app", currentAppVersion, bestVersion.Version, "sha256_mismatch")
			os.Remove(tmpFile)
			logger.Error("runAppDownload: SHA256 mismatch", zap.String("expected", bestVersion.SHA256), zap.String("actual", actualSHA256))
			s.setAppDownloaderStatus("failed")
			return
		}
	}

	if err := os.Rename(tmpFile, bundlePath); err != nil {
		os.Remove(tmpFile)
		logger.Error("runAppDownload: rename failed", zap.Error(err))
		s.setAppDownloaderStatus("failed")
		return
	}

	logger.Info("runAppDownload: download completed successfully")

	// clean up old version APP directories
	upgradeDir := "/var/lib/nimoos_data/upgrade"
	entries, _ := os.ReadDir(upgradeDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == "app_v"+bestVersion.Version {
			continue
		}
		if strings.HasPrefix(entry.Name(), "app_v") {
			os.RemoveAll(filepath.Join(upgradeDir, entry.Name()))
		}
	}

	properties := map[string]string{"version": bestVersion.Version}
	MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:app:downloaded", properties)
	s.setAppDownloaderStatus("completed")
}

// runDownload is the actual download logic, run in its own goroutine
func (s *systemService) runDownload(ctx context.Context) {
	logger.Info("runDownload: goroutine started")

	local, err := readLocalVersionInfo()
	if err != nil {
		logger.Error("runDownload: cannot read local version", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}

	check := s.CheckUpdate()
	if !check.HasUpdate {
		logger.Info("runDownload: no update available")
		s.setDownloaderStatus("failed")
		return
	}
	if check.IsDownloaded {
		logger.Info("runDownload: already downloaded")
		s.setDownloaderStatus("completed")
		return
	}

	// re-query the cloud to get the downloadURL
	hasUpdate, bestVersion, err := checkCloudUpdate(local.UpdateServer, local.Version, local.Platform, local.HardwareID, local.DeviceID)
	if err != nil {
		logger.Error("runDownload: cloud check failed", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}
	if !hasUpdate || bestVersion == nil {
		logger.Error("runDownload: no compatible version found")
		s.setDownloaderStatus("failed")
		return
	}

	// save the version
	s.downloader.mu.Lock()
	s.downloader.version = bestVersion.Version
	s.downloader.mu.Unlock()

	logger.Info("runDownload: selected version", zap.String("version", bestVersion.Version), zap.String("url", bestVersion.DownloadURL))

	downloadDir := "/var/lib/nimoos_data/upgrade/v" + bestVersion.Version
	os.MkdirAll(downloadDir, 0755)

	s.downloader.mu.Lock()
	s.downloader.downloadDir = downloadDir
	s.downloader.mu.Unlock()

	// report downloading status
	reportUpgradeResult(local.UpdateServer, local.DeviceID, "os", local.Version, bestVersion.Version, "downloading", "", "")

	tmpFile := filepath.Join(downloadDir, "nimo_update_"+bestVersion.Version+".raucb.tmp")
	finalFile := filepath.Join(downloadDir, "nimo_update_"+bestVersion.Version+".raucb")

	if _, err := os.Stat(finalFile); err == nil {
		logger.Info("runDownload: final file already exists")
		properties := map[string]string{"version": bestVersion.Version}
		MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:upgrade:downloaded", properties)
		s.setDownloaderStatus("completed")
		return
	}

	// resumable download: check the size of any existing .tmp file
	var resumeOffset int64
	if fi, err := os.Stat(tmpFile); err == nil {
		resumeOffset = fi.Size()
		logger.Info("runDownload: resuming from offset", zap.Int64("offset", resumeOffset))
	}

	fullURL := local.UpdateServer + bestVersion.DownloadURL
	logger.Info("runDownload: downloading", zap.String("url", fullURL), zap.Int64("resume_offset", resumeOffset))

	transport := &http.Transport{
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       10 * time.Minute,
	}
	downloadClient := &http.Client{Transport: transport, Timeout: 0}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		logger.Error("runDownload: create request failed", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
	}

	dlResp, err := downloadClient.Do(req)
	if err != nil {
		logger.Error("runDownload: download request failed", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}
	defer dlResp.Body.Close()

	// non 200/206: refresh the URL and retry once
	if dlResp.StatusCode != http.StatusOK && dlResp.StatusCode != http.StatusPartialContent {
		logger.Info("runDownload: download failed with status, refreshing URL", zap.Int("status", dlResp.StatusCode))
		dlResp.Body.Close()
		hasUpdate, bestVersion, err = checkCloudUpdate(local.UpdateServer, local.Version, local.Platform, local.HardwareID, local.DeviceID)
		if err != nil || !hasUpdate || bestVersion == nil {
			logger.Error("runDownload: version check failed after download error")
			s.setDownloaderStatus("failed")
			return
		}
		fullURL = local.UpdateServer + bestVersion.DownloadURL
		req, _ = http.NewRequestWithContext(ctx, "GET", fullURL, nil)
		if resumeOffset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
		}
		dlResp, err = downloadClient.Do(req)
		if err != nil {
			logger.Error("runDownload: retry request failed", zap.Error(err))
			s.setDownloaderStatus("failed")
			return
		}
		defer dlResp.Body.Close()
		if dlResp.StatusCode != http.StatusOK && dlResp.StatusCode != http.StatusPartialContent {
			logger.Error("runDownload: retry still failed", zap.Int("status", dlResp.StatusCode))
			s.setDownloaderStatus("failed")
			return
		}
	}

	// decide the file open mode based on the response code
	openFlags := os.O_CREATE | os.O_WRONLY
	if dlResp.StatusCode == http.StatusPartialContent {
		openFlags |= os.O_APPEND
		logger.Info("runDownload: Server accepted Range, appending to existing file")
	} else {
		openFlags |= os.O_TRUNC
		resumeOffset = 0
		logger.Info("runDownload: Server ignored Range, downloading from scratch")
	}

	out, err := os.OpenFile(tmpFile, openFlags, 0644)
	if err != nil {
		logger.Error("runDownload: cannot open temp file", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}
	defer out.Close()

	// chunked write + progress push
	buf := make([]byte, 256*1024) // 256KB buffer
	var totalWritten int64
	totalSize := int64(bestVersion.Size)
	logger.Info("runDownload: size from cloud", zap.Int64("cloud_size", int64(bestVersion.Size)), zap.Int64("content_length", dlResp.ContentLength))
	if totalSize == 0 {
		totalSize = dlResp.ContentLength
		if totalSize > 0 && dlResp.StatusCode == http.StatusPartialContent {
			totalSize += resumeOffset
		}
		if totalSize < 0 {
			logger.Info("runDownload: cannot determine total size, progress disabled")
			totalSize = 0
		}
	}
	lastPublishedProgress := -1.0
	for {
		select {
		case <-ctx.Done():
			logger.Info("runDownload: cancelled, cleaning up")
			out.Close()
			os.Remove(tmpFile)
			s.setDownloaderStatus("idle")
			return
		default:
		}

		n, readErr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				logger.Error("runDownload: write failed", zap.Error(writeErr))
				s.setDownloaderStatus("failed")
				return
			}
			totalWritten += int64(n)
			// compute progress percentage (keep two decimal places)
			progress := 0.0
			if totalSize > 0 {
				progress = float64(resumeOffset+totalWritten) * 100.0 / float64(totalSize)
				if progress > 100 {
					progress = 100
				}
				progress = math.Round(progress*100) / 100
			}
			s.downloader.mu.Lock()
			s.downloader.progress = progress
			s.downloader.mu.Unlock()

			// only push an event when progress changes by ≥ 0.01
			if progress-lastPublishedProgress >= 0.01 || progress == 100 {
				lastPublishedProgress = progress
				props := map[string]string{
					"version":  bestVersion.Version,
					"progress": strconv.FormatFloat(progress, 'f', 2, 64),
				}
				MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:upgrade:progress", props)
				if int(progress)%10 == 0 {
					logger.Info("runDownload: progress", zap.Float64("pct", progress), zap.Int64("written", resumeOffset+totalWritten), zap.Int64("total", totalSize))
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				logger.Info("runDownload: cancelled, cleaning up")
				out.Close()
				os.Remove(tmpFile)
				s.setDownloaderStatus("idle")
				return
			}
			logger.Error("runDownload: read interrupted", zap.Error(readErr), zap.Int64("progress_bytes", resumeOffset+totalWritten))
			s.setDownloaderStatus("paused") // network interrupted = keep the .tmp for resuming
			return
		}
	}

	out.Close()
	logger.Info("runDownload: downloaded bytes", zap.Int64("total_bytes", resumeOffset+totalWritten))

	// SHA256 verification
	hasher := sha256.New()
	f, err := os.Open(tmpFile)
	if err != nil {
		os.Remove(tmpFile)
		logger.Error("runDownload: cannot read temp file for checksum", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}
	io.Copy(hasher, f)
	f.Close()
	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))

	if !strings.EqualFold(actualSHA256, bestVersion.SHA256) {
		os.Remove(tmpFile)
		logger.Error("runDownload: SHA256 mismatch", zap.String("expected", bestVersion.SHA256), zap.String("actual", actualSHA256))
		s.reportFailed(local.UpdateServer, local.DeviceID, "os", local.Version, bestVersion.Version, "sha256_mismatch")
		s.setDownloaderStatus("failed")
		return
	}

	if err := os.Rename(tmpFile, finalFile); err != nil {
		os.Remove(tmpFile)
		logger.Error("runDownload: rename failed", zap.Error(err))
		s.setDownloaderStatus("failed")
		return
	}

	logger.Info("runDownload: download complete", zap.String("file", finalFile))

	// clean up old version directories
	upgradeDir := "/var/lib/nimoos_data/upgrade"
	entries, _ := os.ReadDir(upgradeDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == "v"+bestVersion.Version {
			continue
		}
		os.RemoveAll(filepath.Join(upgradeDir, entry.Name()))
	}

	properties := map[string]string{"version": bestVersion.Version}
	MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:upgrade:downloaded", properties)
	s.setDownloaderStatus("completed")
}

// setDownloaderStatus thread-safely sets the downloader status
func (s *systemService) setDownloaderStatus(status string) {
	s.downloader.mu.Lock()
	defer s.downloader.mu.Unlock()
	s.downloader.status = status
	s.downloader.progress = 0
	if status == "completed" || status == "failed" || status == "idle" {
		s.downloader.cancel = nil
	}
}

func (s *systemService) setAppDownloaderStatus(status string) {
	s.appDownloader.mu.Lock()
	defer s.appDownloader.mu.Unlock()
	s.appDownloader.status = status
	s.appDownloader.progress = 0
	if status == "completed" || status == "failed" || status == "idle" {
		s.appDownloader.cancel = nil
	}
}

func (s *systemService) StartDailyDownloadChecker() {
	go func() {
		rand.Seed(time.Now().UnixNano())
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, now.Location())
			if !now.Before(next) {
				next = next.Add(24 * time.Hour)
			}
			delay := time.Duration(rand.Intn(10800)) * time.Second
			next = next.Add(delay)

			logger.Info("DailyDownloadChecker: next check scheduled",
				zap.Time("next_run", next),
				zap.Duration("delay", delay))
			time.Sleep(time.Until(next))

			logger.Info("DailyDownloadChecker: checking OS firmware update")
			check := s.CheckUpdate()
			if check.HasUpdate && !check.IsDownloaded && !check.IsDownloading {
				logger.Info("DailyDownloadChecker: starting OS firmware download")
				s.DownloadUpdate()
			} else {
				logger.Info("DailyDownloadChecker: OS firmware skip",
					zap.Bool("has_update", check.HasUpdate),
					zap.Bool("downloaded", check.IsDownloaded),
					zap.Bool("downloading", check.IsDownloading))
			}

			logger.Info("DailyDownloadChecker: checking APP update")
			appCheck := s.CheckAppUpdate()
			if appCheck.HasUpdate && !appCheck.IsDownloaded && !appCheck.IsDownloading {
				logger.Info("DailyDownloadChecker: starting APP download")
				s.DownloadAppUpdate()
			} else {
				logger.Info("DailyDownloadChecker: APP skip",
					zap.Bool("has_update", appCheck.HasUpdate),
					zap.Bool("downloaded", appCheck.IsDownloaded),
					zap.Bool("downloading", appCheck.IsDownloading))
			}
		}
	}()
}

func (s *systemService) UpdateAssist() {
	command.ExecResultStrArray("source " + config.AppInfo.ShellPath + "/assist.sh")
}

func (s *systemService) GetTimeZone() string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetTimeZone"); err != nil {
		return ""
	} else {
		return output
	}
}

func (s *systemService) GetSystemConfigDebug() []string {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;GetSysInfo"); err != nil {
		return []string{}
	} else {
		return strings.Split(output, "\n")
	}
}

func (s *systemService) UpAppOrderFile(str, id string) {
	file.WriteToPath([]byte(str), config.AppInfo.DBPath+"/"+id, "app_order.json")
}

func (s *systemService) GetAppOrderFile(id string) []byte {
	return file.ReadFullFile(config.AppInfo.UserDataPath + "/" + id + "/app_order.json")
}

func (s *systemService) UpSystemPort(port string) {
	if len(port) > 0 && port != config.ServerInfo.HttpPort {
		config.Cfg.Section("server").Key("HttpPort").SetValue(port)
		config.ServerInfo.HttpPort = port
	}
	config.Cfg.SaveTo(config.SystemConfigInfo.ConfigPath)
}

func (s *systemService) GetNimoOSLogs(lineNumber int) string {
	file, err := os.Open(filepath.Join(config.AppInfo.LogPath, fmt.Sprintf("%s.%s",
		config.AppInfo.LogSaveName,
		config.AppInfo.LogFileExt,
	)))
	if err != nil {
		return err.Error()
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return err.Error()
	}

	return string(content)
}

func GetDeviceAllIP() []string {
	var address []string
	addrs, err := net2.InterfaceAddrs()
	if err != nil {
		return address
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net2.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To16() != nil {
				address = append(address, ipNet.IP.String())
			}
		}
	}
	return address
}

// find thermal_zone of cpu.
// assertions:
//   - thermal_zone "type" and "temp" are required fields
//     (https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-class-thermal)
func GetCPUThermalZone() string {
	keyName := "cpu_thermal_zone"

	var path string
	if result, ok := Cache.Get(keyName); ok {
		path, ok = result.(string)
		if ok {
			return path
		}
	}

	var name string
	cpu_types := []string{"x86_pkg_temp", "cpu", "CPU", "soc"}
	stub := "/sys/devices/virtual/thermal/thermal_zone"
	for i := 0; i < 100; i++ {
		path = stub + strconv.Itoa(i)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			name = strings.TrimSuffix(string(file.ReadFullFile(path+"/type")), "\n")
			for _, s := range cpu_types {
				if strings.HasPrefix(name, s) {
					//logger.Info(fmt.Sprintf("CPU thermal zone found: %s, path: %s.", name, path))
					Cache.SetDefault(keyName, path)
					return path
				}
			}
		} else {
			if len(name) > 0 { // proves at least one zone
				path = stub + "0"
			} else {
				path = ""
			}
			break
		}
	}

	Cache.SetDefault(keyName, path)
	return path
}

// GetCPUHwmonPath finds the hwmon device that reports CPU temperature, by
// matching the driver name. Covers AMD (k10temp/zenpower), Intel (coretemp),
// and ARM SoCs (cpu_thermal). ACPI thermal zones often report 0 on AMD, so
// hwmon is the reliable source.
func GetCPUHwmonPath() string {
	keyName := "cpu_hwmon_path"
	if result, ok := Cache.Get(keyName); ok {
		if path, ok := result.(string); ok {
			return path
		}
	}

	cpuDrivers := []string{"k10temp", "zenpower", "coretemp", "cpu_thermal"}
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		Cache.SetDefault(keyName, "")
		return ""
	}
	for _, e := range entries {
		hwPath := "/sys/class/hwmon/" + e.Name()
		name := strings.TrimSpace(string(file.ReadFullFile(hwPath + "/name")))
		for _, d := range cpuDrivers {
			if name == d {
				Cache.SetDefault(keyName, hwPath)
				return hwPath
			}
		}
	}
	Cache.SetDefault(keyName, "")
	return ""
}

func (s *systemService) GetCPUTemperature() int {
	celsius := 0

	// Prefer hwmon — ACPI thermal_zone reports 0 on many AMD systems.
	if hwPath := GetCPUHwmonPath(); hwPath != "" {
		raw := strings.TrimSpace(string(file.ReadFullFile(hwPath + "/temp1_input")))
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			if v > 1000 {
				v = v / 1000
			}
			celsius = v
		}
	}

	if celsius == 0 {
		outPut := ""
		path := GetCPUThermalZone()
		if len(path) > 0 {
			outPut = string(file.ReadFullFile(path + "/temp"))
		} else {
			outPut = string(file.ReadFullFile("/sys/class/hwmon/hwmon0/temp1_input"))
		}
		v, _ := strconv.Atoi(strings.TrimSpace(outPut))
		if v > 1000 {
			v = v / 1000
		}
		celsius = v
	}

	return celsius
}

func (s *systemService) GetCPUPower() map[string]string {
	data := make(map[string]string, 2)
	data["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
	if file.Exists("/sys/class/powercap/intel-rapl/intel-rapl:0/energy_uj") {
		data["value"] = strings.TrimSpace(string(file.ReadFullFile("/sys/class/powercap/intel-rapl/intel-rapl:0/energy_uj")))
	} else {
		data["value"] = "0"
	}
	return data
}

func (s *systemService) SystemReboot() error {
	arg := []string{"6"}
	cmd := exec2.Command("init", arg...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return nil
}

func (s *systemService) SystemShutdown() error {
	arg := []string{"0"}
	cmd := exec2.Command("init", arg...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return nil
}

func (c *systemService) GetGpuInfo() []string {
	gpuList := []string{}
	if output, err := command.OnlyExec("lspci | grep -E 'VGA|3D'"); err == nil {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			parts := strings.SplitN(line, ": ", 2)
			if len(parts) > 1 {
				gpuList = append(gpuList, parts[1])
			}
		}
	}
	return gpuList
}

// GpuStatus is one GPU's runtime metrics, consumed by the frontend widget
// (see NimoOS-UI/src/widgets/Gpu.vue, which reads utilization_gpu /
// utilization_memory / memory_total / temperature on each entry).
type GpuStatus struct {
	Index             int     `json:"index"`
	Name              string  `json:"name"`
	Vendor            string  `json:"vendor"`
	UtilizationGpu    float64 `json:"utilization_gpu"`
	UtilizationMemory float64 `json:"utilization_memory"`
	MemoryTotal       uint64  `json:"memory_total"`
	MemoryUsed        uint64  `json:"memory_used"`
	Temperature       float64 `json:"temperature"`
	FreqMHz           float64 `json:"freq_mhz,omitempty"`
}

// GetGpuStatus enumerates all GPUs: NVIDIA via nvidia-smi, then Intel via sysfs
// (/sys/class/drm). Without this, Intel-only hosts (no NVIDIA, nvidia-smi fails)
// reported an empty list and the UI showed "No GPU detected".
func (c *systemService) GetGpuStatus() []GpuStatus {
	gpus := []GpuStatus{}
	gpus = append(gpus, queryNvidiaGpuStatus()...)
	gpus = append(gpus, scanIntelGpus("/sys/class/drm")...)
	return gpus
}

func queryNvidiaGpuStatus() []GpuStatus {
	out, err := command.ExecResultStr("nvidia-smi --query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu --format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	gpus := []GpuStatus{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 6 {
			continue
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		g := GpuStatus{Vendor: "nvidia", Name: parts[1]}
		if v, err := strconv.Atoi(parts[0]); err == nil {
			g.Index = v
		}
		if v, err := strconv.ParseFloat(parts[2], 64); err == nil {
			g.UtilizationGpu = v
		}
		if v, err := strconv.ParseUint(parts[3], 10, 64); err == nil {
			g.MemoryTotal = v * 1024 * 1024
		}
		if v, err := strconv.ParseUint(parts[4], 10, 64); err == nil {
			g.MemoryUsed = v * 1024 * 1024
		}
		if g.MemoryTotal > 0 {
			g.UtilizationMemory = float64(g.MemoryUsed) / float64(g.MemoryTotal) * 100
		}
		if v, err := strconv.ParseFloat(parts[5], 64); err == nil {
			g.Temperature = v
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// scanIntelGpus enumerates Intel GPUs under drmDir (/sys/class/drm) and reads
// name (lspci), temperature (hwmon) and frequency (xe/i915 sysfs). Utilization
// and VRAM are not exposed by the driver sysfs; live utilization is filled from
// intel_gpu_top when that tool is installed (a single aggregate, attached to the
// first Intel GPU). drmDir is a parameter so the logic is unit-testable.
func scanIntelGpus(drmDir string) []GpuStatus {
	entries, err := os.ReadDir(drmDir)
	if err != nil {
		return nil
	}
	gpus := []GpuStatus{}
	for _, e := range entries {
		if !isDrmCard(e.Name()) {
			continue
		}
		dev := filepath.Join(drmDir, e.Name(), "device")
		if strings.TrimSpace(string(file.ReadFullFile(filepath.Join(dev, "vendor")))) != "0x8086" {
			continue // Intel only
		}
		g := GpuStatus{
			Index:          len(gpus),
			Vendor:         "intel",
			Name:           intelGpuName(dev),
			Temperature:    gpuHwmonTempC(dev),
			FreqMHz:        intelGpuFreqMHz(dev),
			UtilizationGpu: intelGpuBusyPct(e.Name(), dev),
		}
		if total, used := intelGpuVramBytes(dev); total > 0 {
			g.MemoryTotal = total
			g.MemoryUsed = used
			g.UtilizationMemory = float64(used) / float64(total) * 100
		}
		gpus = append(gpus, g)
	}
	// Put GPUs that report a temperature first (stable), so the widget's primary
	// ring (gpu[0]) shows a live reading from e.g. a discrete card rather than an
	// integrated GPU's blank 0. Reindex to match the new order.
	withTemp, without := make([]GpuStatus, 0, len(gpus)), make([]GpuStatus, 0, len(gpus))
	for _, g := range gpus {
		if g.Temperature > 0 {
			withTemp = append(withTemp, g)
		} else {
			without = append(without, g)
		}
	}
	gpus = append(withTemp, without...)
	for i := range gpus {
		gpus[i].Index = i
	}
	return gpus
}

// gpuIdleSample remembers the previous per-GT idle-residency readings for a card
// so the next call can derive utilization from the delta.
type gpuIdleSample struct {
	idleMs []int64 // one entry per GT (gt0-rc render/compute, gt1-mc media, ...)
	at     time.Time
	busy   float64
}

var (
	gpuIdleMu   sync.Mutex
	gpuIdlePrev = map[string]gpuIdleSample{}
)

// intelGpuBusyPct estimates GPU utilization for an Intel card from the GT
// idle-residency counters (time each GT spent in deep idle / RC6). It samples
// every GT and returns the busiest, because compute/render load lands on gt0-rc
// while media (transcode) load lands on gt1-mc — reading only one would miss the
// other. It is stateful: each call diffs against the previous reading for that
// card, so the 5s periodical broadcast yields a 5s-averaged figure. intel_gpu_top
// is not used because it only supports the i915 driver, not xe. Readings closer
// than 500ms reuse the last value to avoid noise from rapid back-to-back calls.
func intelGpuBusyPct(card, dev string) float64 {
	idles := readGpuIdleResidenciesMs(dev)
	if len(idles) == 0 {
		return 0 // counters unavailable
	}
	now := time.Now()
	gpuIdleMu.Lock()
	defer gpuIdleMu.Unlock()
	prev, ok := gpuIdlePrev[card]
	if ok && len(prev.idleMs) == len(idles) {
		elapsed := now.Sub(prev.at).Milliseconds()
		if elapsed < 500 {
			return prev.busy // too soon to resample; keep prev anchor
		}
		busy := 0.0
		for i := range idles {
			if b := computeBusyPct(idles[i]-prev.idleMs[i], elapsed); b > busy {
				busy = b
			}
		}
		gpuIdlePrev[card] = gpuIdleSample{idleMs: idles, at: now, busy: busy}
		return busy
	}
	gpuIdlePrev[card] = gpuIdleSample{idleMs: idles, at: now}
	return 0 // first sample establishes the baseline
}

// computeBusyPct converts an idle-time delta over an interval into a busy
// percentage, clamped to [0,100].
func computeBusyPct(idleDeltaMs, elapsedMs int64) float64 {
	if elapsedMs <= 0 {
		return 0
	}
	busy := 100 * (1 - float64(idleDeltaMs)/float64(elapsedMs))
	if busy < 0 {
		return 0
	}
	if busy > 100 {
		return 100
	}
	return busy
}

// readGpuIdleResidenciesMs returns GT deep-idle residency (ms) for each GT of the
// device: the xe driver exposes one per tile*/gt* (gt0-rc render/compute, gt1-mc
// media, ...), the older i915 a single power/rc6_residency_ms. Empty when none
// are present. Glob order is stable (sorted), so per-GT deltas line up call over
// call.
func readGpuIdleResidenciesMs(dev string) []int64 {
	var out []int64
	for _, p := range globUnder(dev, "tile*/gt*/gtidle/idle_residency_ms") {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(file.ReadFullFile(p))), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(file.ReadFullFile(filepath.Join(dev, "power", "rc6_residency_ms")))), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// isDrmCard reports whether name is a primary DRM card node (cardN), not a
// render node (renderD*) or other entry.
func isDrmCard(name string) bool {
	if !strings.HasPrefix(name, "card") {
		return false
	}
	digits := name[len("card"):]
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// intelGpuName resolves a human-readable name via lspci on the device's PCI
// slot, falling back to a generic label when lspci is unavailable.
func intelGpuName(dev string) string {
	if real, err := filepath.EvalSymlinks(dev); err == nil {
		slot := filepath.Base(real) // e.g. 0000:04:00.0
		if out, err := command.OnlyExec("lspci -s " + slot); err == nil {
			if parts := strings.SplitN(strings.TrimSpace(out), ": ", 2); len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "Intel GPU"
}

// gpuHwmonTempC returns the GPU temperature in Celsius from the device's hwmon,
// preferring the "pkg" (package) sensor, else the hottest available reading.
func gpuHwmonTempC(dev string) float64 {
	inputs, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*", "temp*_input"))
	pkg, best := -1.0, 0.0
	for _, in := range inputs {
		v, err := strconv.Atoi(strings.TrimSpace(string(file.ReadFullFile(in))))
		if err != nil || v <= 0 {
			continue
		}
		c := float64(v) / 1000
		if strings.TrimSpace(string(file.ReadFullFile(strings.TrimSuffix(in, "_input")+"_label"))) == "pkg" {
			pkg = c
		}
		if c > best {
			best = c
		}
	}
	if pkg >= 0 {
		return pkg
	}
	return best
}

// intelGpuFreqMHz reads the current GPU frequency, handling both the xe driver
// (tile*/gt*/freq0/{act,cur}_freq) and the older i915 (gt_{act,cur}_freq_mhz).
func intelGpuFreqMHz(dev string) float64 {
	for _, pat := range []string{"tile*/gt*/freq0/act_freq", "tile*/gt*/freq0/cur_freq"} {
		for _, m := range globUnder(dev, pat) {
			if v, err := strconv.Atoi(strings.TrimSpace(string(file.ReadFullFile(m)))); err == nil && v > 0 {
				return float64(v)
			}
		}
	}
	for _, f := range []string{"gt_act_freq_mhz", "gt_cur_freq_mhz"} {
		if v, err := strconv.Atoi(strings.TrimSpace(string(file.ReadFullFile(filepath.Join(dev, f))))); err == nil && v > 0 {
			return float64(v)
		}
	}
	return 0
}

func globUnder(dev, pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(dev, pattern))
	return m
}

// intelGpuVramBytes returns total and used dedicated VRAM for a discrete Intel
// GPU. The xe driver exposes this only in debugfs (not sysfs), so it reads
// /sys/kernel/debug/dri/<pci-slot>/vram0_mm — which requires root; the service
// runs as root. Integrated GPUs have no VRAM region and yield 0, 0.
func intelGpuVramBytes(dev string) (total, used uint64) {
	real, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return 0, 0
	}
	slot := filepath.Base(real) // e.g. 0000:04:00.0
	return parseVramMM(string(file.ReadFullFile(filepath.Join("/sys/kernel/debug/dri", slot, "vram0_mm"))))
}

// parseVramMM extracts total ("size:") and used ("usage:") bytes from the body
// of an xe vram0_mm debugfs file, ignoring the later free-list and the *MiB
// summary lines (visible_size etc.).
func parseVramMM(content string) (total, used uint64) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "size":
			if total == 0 {
				total, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		case "usage":
			if used == 0 {
				used, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	return
}

func (c *systemService) GetRamDetail() map[string]string {
	data := make(map[string]string)
	// Speed
	if output, err := command.OnlyExec("dmidecode -t memory | grep 'Speed:' | grep -v 'Supported' | head -n 1"); err == nil {
		parts := strings.Split(output, ":")
		if len(parts) > 1 {
			data["speed"] = strings.TrimSpace(parts[1])
		}
	}
	// Type — grep '^[[:space:]]*Type:' to avoid matching Error Correction Type / PMIC Device Type
	if output, err := command.OnlyExec("dmidecode -t memory | grep -E '^[[:space:]]*Type:' | head -n 1"); err == nil {
		parts := strings.Split(output, ":")
		if len(parts) > 1 {
			data["type"] = strings.TrimSpace(parts[1])
		}
	}
	return data
}

func (s *systemService) SetDiskStandby(minutes int) error {
	var hdparmValue int
	if minutes == 0 {
		hdparmValue = 0 // Never
	} else if minutes <= 20 {
		hdparmValue = minutes * 12
	} else if minutes >= 30 && minutes <= 330 {
		hdparmValue = (minutes / 30) + 240
	} else {
		hdparmValue = 251 // Max 5.5h
	}

	// Get all physical sda-sdz and nvme disks
	devices, _ := filepath.Glob("/dev/sd[a-z]")
	nvmeDevices, _ := filepath.Glob("/dev/nvme[0-9]n[0-9]")
	devices = append(devices, nvmeDevices...)

	for _, dev := range devices {
		// exec hdparm -S <value> <device>
		// We use sudo because hdparm requires root privileges
		cmd := exec2.Command("sudo", "hdparm", "-S", fmt.Sprintf("%d", hdparmValue), dev)
		if output, err := cmd.CombinedOutput(); err != nil {
			logger.Info("failed to set disk standby for " + dev + ": " + string(output))
		} else {
			logger.Info("successfully set disk standby for " + dev + " to hdparm value " + fmt.Sprintf("%d", hdparmValue))
		}
	}
	return nil
}

func (c *systemService) GetNetAddr(name string) string {
	allIpv4 := ip_helper.GetDeviceAllIPv4()
	if v, ok := allIpv4[name]; ok {
		return v
	}
	return ""
}

func (c *systemService) GetNetSpeed(name string) int {
	speedPath := filepath.Join("/sys/class/net", name, "speed")
	if !file.Exists(speedPath) {
		// For WiFi interfaces, try to get the link rate from iw
		if strings.HasPrefix(name, "wl") || strings.HasPrefix(name, "wlan") {
			return c.GetWifiSpeed(name)
		}
		return 0
	}
	content := string(file.ReadFullFile(speedPath))
	speed, err := strconv.Atoi(strings.TrimSpace(content))
	if err != nil {
		return 0
	}
	// Speed is in Mbps, -1 means unknown or virtual
	if speed < 0 {
		return 0
	}
	return speed
}

// GetWifiSpeed parses iw dev <name> link output for tx bitrate (in Mbps).
func (c *systemService) GetWifiSpeed(name string) int {
	out, err := exec.Command("iw", "dev", name, "link").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "tx bitrate:") {
			// Format: "tx bitrate: 1200.9 MBit/s 80MHz EHT-MCS 11 ..."
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				// parts[0]="tx" parts[1]="bitrate:" parts[2]="1200.9"
				rateStr := strings.TrimSuffix(parts[2], ".")
				rate, err := strconv.ParseFloat(parts[2], 64)
				if err == nil {
					return int(rate + 0.5) // round to nearest integer
				}
				_ = rateStr
			}
		}
	}
	return 0
}

func (c *systemService) GetNetMaxSpeed(name string) int {
	speed, _ := getNetMaxSpeedViaIoctl(name)
	return speed
}

func (c *systemService) GetSystemPaths() map[string]interface{} {
	cfg := ResolveActivePaths()

	// Evaluate real physical paths to avoid "Shadow Migration" UI confusion.
	// resolveForSize resolves symlinks for size calculation; if resolution fails
	// (e.g. broken/circular symlink chain), fall back to os.Stat-based resolution
	// so we measure real content rather than the symlink node itself.
	resolveForSize := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		// EvalSymlinks failed — try one level of Readlink to get past the first hop
		if target, err := os.Readlink(p); err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(p), target)
			}
			if resolved, err := filepath.EvalSymlinks(target); err == nil {
				return resolved
			}
			return target
		}
		return p
	}
	realAppData := resolveForSize(cfg.AppData)
	realImages := resolveForSize(cfg.Images)
	realUserData := resolveForSize(cfg.UserData)
	realPhotos := resolveForSize(cfg.Photos)

	var appDataSize, imagesSize, userDataSize, photosSize int64

	// SAFETY: Prevent full-disk scan if path is "/" or empty
	if realAppData != "/" && realAppData != "" {
		appDataSize, _ = file.GetFileOrDirSize(realAppData)
	}

	if realPhotos != "/" && realPhotos != "" {
		photosSize, _ = file.GetFileOrDirSize(realPhotos)
	}

	if realImages != "/" && realImages != "" {
		imagesSize, _ = file.GetFileOrDirSize(realImages)
		containerdPath := filepath.Join(filepath.Dir(realImages), ".containerd")
		if containerdSize, err := file.GetFileOrDirSize(containerdPath); err == nil {
			imagesSize += containerdSize
		}
	}

	if realUserData != "/" && realUserData != "" {
		// Aggregate size for batch folders (Gallery, Downloads, etc.)
		subFolders := []string{"Gallery", "Downloads", "Documents", "Media"}
		for _, f := range subFolders {
			s, _ := file.GetFileOrDirSize(filepath.Join(realUserData, f))
			userDataSize += s
		}
	}

	return map[string]interface{}{
		"app_data": map[string]interface{}{
			"path": realAppData,
			"size": appDataSize,
		},
		"images": map[string]interface{}{
			"path": realImages + " & .containerd",
			"size": imagesSize,
		},
		"database": map[string]interface{}{
			"path": realUserData,
			"size": userDataSize,
		},
		"photos_data": map[string]interface{}{
			"path": realPhotos,
			"size": photosSize,
		},
	}
}

func (c *systemService) StartMigrateAppPath(jobID, migrationType, targetMountPoint string) {
	StartMigration(jobID, migrationType, targetMountPoint)
}

func (c *systemService) GetMigrateStatus(jobID string) (MigrateStatus, bool) {
	return GetMigrationStatus(jobID)
}

func NewSystemService() SystemService {
	s := &systemService{}
	s.StartDailyDownloadChecker()
	return s
}

// uploadUpgradeLog gets a presigned S3 upload URL from the device endpoint and uploads the log file, returning a public download link
func uploadUpgradeLog(server, deviceID, logPath string) (string, error) {
	// 1. get the presigned upload URL
	url := fmt.Sprintf("%s/v1/sys/log-upload-url", server)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Nimo-Device-ID", deviceID)
	req.Header.Set("X-Nimo-Cookie", generateCookie(deviceID))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("request upload url failed with status %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		UploadURL string `json:"upload_url"`
		LogURL    string `json:"log_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	// 2. open the log file
	file, err := os.Open(logPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return "", err
	}

	// 3. perform the PUT upload
	putReq, err := http.NewRequest("PUT", res.UploadURL, file)
	if err != nil {
		return "", err
	}
	putReq.ContentLength = fi.Size()
	putReq.Header.Set("Content-Type", "text/plain")

	putResp, err := client.Do(putReq)
	if err != nil {
		return "", err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(putResp.Body)
		return "", fmt.Errorf("upload to S3 failed with status %d: %s", putResp.StatusCode, string(body))
	}

	return res.LogURL, nil
}

// checkAppCloudUpdate calls the cloud OTA service to check for app bundle updates, caching the result for 3 minutes
func checkAppCloudUpdate(server, currentAppVersion, currentOsVersion, platform, hardwareID, deviceID string) (bool, *versionInfo, error) {
	cacheKey := "cloud_update_app_" + currentAppVersion
	if cached, ok := Cache.Get(cacheKey); ok {
		type cacheVal struct {
			Has bool
			VI  *versionInfo
		}
		if cv, ok2 := cached.(cacheVal); ok2 {
			return cv.Has, cv.VI, nil
		}
	}

	url := fmt.Sprintf("%s/v1/sys/app_version?current_app=%s", server, currentAppVersion)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, nil, fmt.Errorf("create request: %w", err)
	}

	// NimoOS-Cookie auth
	req.Header.Set("X-Nimo-Device-ID", deviceID)
	req.Header.Set("X-Nimo-Cookie", generateCookie(deviceID))
	req.Header.Set("X-Nimo-Platform", platform)
	req.Header.Set("X-Nimo-Hardware-ID", hardwareID)
	req.Header.Set("X-Nimo-Version", currentOsVersion)

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		logger.Info("checkAppCloudUpdate: authentication failed (401)")
		return false, nil, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		type cacheVal struct {
			Has bool
			VI  *versionInfo
		}
		Cache.Set(cacheKey, cacheVal{Has: false, VI: nil}, 3*time.Minute)
		return false, nil, nil
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		logger.Info("checkAppCloudUpdate: cloud service unavailable (503)")
		return false, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("read response: %w", err)
	}

	var result upgradeCheckResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return false, nil, fmt.Errorf("parse response: %w", err)
	}

	if result.Data == nil || result.Data.Version == nil {
		type cacheVal struct {
			Has bool
			VI  *versionInfo
		}
		Cache.Set(cacheKey, cacheVal{Has: false, VI: nil}, 3*time.Minute)
		return false, nil, nil
	}

	vi := &versionInfo{
		Version:     result.Data.Version.Version,
		Changelog:   result.Data.Version.ChangeLog,
		DownloadURL: result.Data.Version.DownloadURL,
		SHA256:      result.Data.Version.SHA256,
		Size:        result.Data.Version.Size,
	}

	type cacheVal struct {
		Has bool
		VI  *versionInfo
	}
	Cache.Set(cacheKey, cacheVal{Has: true, VI: vi}, 3*time.Minute)
	return true, vi, nil
}

func (s *systemService) CheckAppUpdate() *UpgradeCheckResult {
	const cacheKey = "check_app_update_result"
	if cached, ok := Cache.Get(cacheKey); ok {
		if r, ok := cached.(*UpgradeCheckResult); ok {
			// downloading/completed: the cache doesn't hold live progress or disk state, needs rechecking
			s.appDownloader.mu.RLock()
			status := s.appDownloader.status
			isRunning := status == "running"
			s.appDownloader.mu.RUnlock()
			if isRunning || status == "completed" {
				Cache.Delete(cacheKey)
			} else if r.IsDownloaded {
				// if the cache says it's downloaded, verify the file is still on disk
				appUpgradeDir := fmt.Sprintf("/var/lib/nimoos_data/upgrade/app_v%s", r.LatestVersion)
				bundlePath := filepath.Join(appUpgradeDir, "bundle.tar.gz")
				if fi, err := os.Stat(bundlePath); os.IsNotExist(err) {
					Cache.Delete(cacheKey)
				} else if err == nil && fi.Size() == 0 {
					Cache.Delete(cacheKey)
				} else {
					return r
				}
			} else {
				return r
			}
		}
	}

	result := &UpgradeCheckResult{
		CurrentVersion: common.VERSION,
	}

	local, err := readLocalVersionInfo()
	if err != nil {
		Cache.Set(cacheKey, result, 1*time.Minute)
		return result
	}

	if local.AppVersion != "" && local.AppVersion != "0.0.0" {
		result.CurrentVersion = local.AppVersion
	}

	hasUpdate, bestVersion, err := checkAppCloudUpdate(local.UpdateServer, result.CurrentVersion, local.Version, local.Platform, local.HardwareID, local.DeviceID)
	if err != nil {
		logger.Info("CheckAppUpdate: cloud check failed, will retry later", zap.Error(err))
		Cache.Set(cacheKey, result, 1*time.Minute)
		return result
	}
	if !hasUpdate || bestVersion == nil {
		Cache.Set(cacheKey, result, 5*time.Minute)
		return result
	}

	result.LatestVersion = bestVersion.Version
	result.Size = bestVersion.Size
	result.Changelog = bestVersion.Changelog

	if compareVersions(result.LatestVersion, result.CurrentVersion) <= 0 {
		Cache.Set(cacheKey, result, 5*time.Minute)
		return result
	}

	// check download status
	appUpgradeDir := fmt.Sprintf("/var/lib/nimoos_data/upgrade/app_v%s", bestVersion.Version)
	bundlePath := filepath.Join(appUpgradeDir, "bundle.tar.gz")
	if _, err := os.Stat(bundlePath); err == nil {
		result.IsDownloaded = true
	}

	s.appDownloader.mu.RLock()
	if s.appDownloader.status == "running" {
		result.IsDownloading = true
		result.DownloadProgress = s.appDownloader.progress
	}
	s.appDownloader.mu.RUnlock()

	result.HasUpdate = true
	Cache.Set(cacheKey, result, 5*time.Minute)
	return result
}

func (s *systemService) UpdateAppVersion(version string) error {
	if locked, err := isFileLocked("/var/run/nimo_app_upgrade.lock"); err == nil && locked {
		return fmt.Errorf("another app upgrade is already running")
	}

	check := s.CheckAppUpdate()
	if !check.HasUpdate {
		return fmt.Errorf("no app update available")
	}

	local, _ := readLocalVersionInfo()
	if local == nil {
		return fmt.Errorf("cannot read local version info")
	}

	currentAppVersion := common.VERSION
	if local.AppVersion != "" && local.AppVersion != "0.0.0" {
		currentAppVersion = local.AppVersion
	}

	// force-fetch the latest version from the cloud, bypassing the cache
	Cache.Delete("cloud_update_app_" + currentAppVersion)

	hasUpdate, bestVersion, err := checkAppCloudUpdate(local.UpdateServer, currentAppVersion, local.Version, local.Platform, local.HardwareID, local.DeviceID)
	if err != nil || !hasUpdate || bestVersion == nil {
		return fmt.Errorf("failed to get download URL")
	}

	targetAppVersion := bestVersion.Version

	appUpgradeDir := fmt.Sprintf("/var/lib/nimoos_data/upgrade/app_v%s", targetAppVersion)
	if err := os.MkdirAll(appUpgradeDir, 0755); err != nil {
		reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed", "mkdir_failed", "")
		return fmt.Errorf("failed to create upgrade dir: %v", err)
	}

	bundlePath := filepath.Join(appUpgradeDir, "bundle.tar.gz")

	// if it wasn't pre-downloaded, download it here
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		logger.Info("UpdateAppVersion: downloading bundle", zap.String("url", local.UpdateServer+bestVersion.DownloadURL))
		// report downloading status
		reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "downloading", "", "")

		dlResp, err := http.Get(local.UpdateServer + bestVersion.DownloadURL)
		if err != nil {
			reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed", "download_failed", "")
			return fmt.Errorf("failed to download bundle: %v", err)
		}

		if dlResp.StatusCode != 200 {
			dlResp.Body.Close()
			reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed",
				fmt.Sprintf("download_http_%d", dlResp.StatusCode), "")
			return fmt.Errorf("download failed with status: %d", dlResp.StatusCode)
		}

		out, err := os.Create(bundlePath)
		if err != nil {
			dlResp.Body.Close()
			reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed", "create_file_failed", "")
			return fmt.Errorf("failed to create bundle file: %v", err)
		}
		_, err = io.Copy(out, dlResp.Body)
		out.Close()
		dlResp.Body.Close()
		if err != nil {
			reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed", "save_file_failed", "")
			return fmt.Errorf("failed to save bundle file: %v", err)
		}
	} else {
		logger.Info("UpdateAppVersion: bundle already downloaded", zap.String("path", bundlePath))
	}

	// record the initial status.json, for syncing state after a restart or on error
	statusFilePath := "/var/lib/nimoos_data/upgrade/status.json"
	initialStatus := map[string]string{
		"update_type":   "app",
		"from_version":  currentAppVersion,
		"to_version":    targetAppVersion,
		"status":        "installing",
		"error_message": "",
	}
	statusBytes, _ := json.Marshal(initialStatus)
	_ = os.WriteFile(statusFilePath, statusBytes, 0644)

	// report installing status
	reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "installing", "", "")

	// asynchronously launch the detached upgrade script so a process restart doesn't kill the child process
	logger.Info("UpdateAppVersion: launching scaffold script", zap.String("bundle", bundlePath))

	// use systemd-run --scope to create an independent systemd scope,
	// running the upgrade script in its own cgroup. Even if nimoos.service is stopped by systemd,
	// the scope unit isn't killed along with it, achieving a true detached upgrade.
	cmd := exec.Command("systemd-run", "--scope", "--unit=nimoos-app-upgrade", "--quiet",
		"bash", "/usr/libexec/nimo_app_upgrade.sh", bundlePath, currentAppVersion, targetAppVersion)

	// route all output to upgrade.log (truncated on each upgrade)
	logFile, ferr := os.OpenFile("/var/log/nimoos_app_upgrade.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if ferr == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if startErr := cmd.Start(); startErr != nil {
		logger.Error("UpdateAppVersion: failed to start detached script", zap.Error(startErr))
		if ferr == nil {
			logFile.Close()
		}
		reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", currentAppVersion, targetAppVersion, "failed", "detached_start_failed", "")
		return fmt.Errorf("failed to start upgrade script: %v", startErr)
	}

	go func() {
		if ferr == nil {
			cmd.Wait()
			logFile.Close()
		}
	}()

	return nil
}

func (s *systemService) SyncStartupUpgradeStatus() {
	// 1. sync app upgrade status
	appStatusFilePath := "/var/lib/nimoos_data/upgrade/status.json"
	if _, err := os.Stat(appStatusFilePath); err == nil {
		if statusBytes, err := os.ReadFile(appStatusFilePath); err == nil {
			var status struct {
				UpdateType   string `json:"update_type"`
				FromVersion  string `json:"from_version"`
				ToVersion    string `json:"to_version"`
				Status       string `json:"status"`
				ErrorMessage string `json:"error_message"`
			}
			if err := json.Unmarshal(statusBytes, &status); err == nil {
				local, _ := readLocalVersionInfo()
				if local != nil {
					logger.Info("SyncStartupUpgradeStatus: found pending app upgrade status",
						zap.String("status", status.Status), zap.String("to", status.ToVersion))
					if status.Status == "completed" {
						reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", status.FromVersion, status.ToVersion, "completed", "", "")
					} else if status.Status == "failed" {
						logPath := "/var/log/nimoos_app_upgrade.log"
						logURL, uploadErr := uploadUpgradeLog(local.UpdateServer, local.DeviceID, logPath)
						if uploadErr != nil {
							logger.Error("SyncStartupUpgradeStatus: failed to upload logs", zap.Error(uploadErr))
						}
						reportUpgradeResult(local.UpdateServer, local.DeviceID, "app", status.FromVersion, status.ToVersion, "failed", status.ErrorMessage, logURL)
					}
				}
			}
		}
		_ = os.Remove(appStatusFilePath)
	}

	// 2. sync system (OS) upgrade status
	systemStatusFilePath := "/var/lib/nimoos_data/upgrade/system_status.json"
	if _, err := os.Stat(systemStatusFilePath); err == nil {
		if statusBytes, err := os.ReadFile(systemStatusFilePath); err == nil {
			var status struct {
				UpdateType   string `json:"update_type"`
				FromVersion  string `json:"from_version"`
				ToVersion    string `json:"to_version"`
				Status       string `json:"status"`
				ErrorMessage string `json:"error_message"`
			}
			if err := json.Unmarshal(statusBytes, &status); err == nil {
				local, _ := readLocalVersionInfo()
				if local != nil {
					currentOSVersion := s.GetOSVersion()
					logger.Info("SyncStartupUpgradeStatus: found pending OS upgrade status",
						zap.String("current", currentOSVersion), zap.String("from", status.FromVersion), zap.String("to", status.ToVersion))
					if currentOSVersion == status.ToVersion {
						reportUpgradeResult(local.UpdateServer, local.DeviceID, "os", status.FromVersion, status.ToVersion, "completed", "", "")
						logger.Info("SyncStartupUpgradeStatus: OS upgrade completed successfully")
					} else {
						logPath := "/var/log/nimoos/upgrade.log"
						logURL, uploadErr := uploadUpgradeLog(local.UpdateServer, local.DeviceID, logPath)
						if uploadErr != nil {
							logger.Error("SyncStartupUpgradeStatus: failed to upload OS logs", zap.Error(uploadErr))
						}
						reportUpgradeResult(local.UpdateServer, local.DeviceID, "os", status.FromVersion, status.ToVersion, "failed", "boot_fallback", logURL)
						logger.Error("SyncStartupUpgradeStatus: OS upgrade failed, rolled back to original version")
					}
				}
			}
		}
		_ = os.Remove(systemStatusFilePath)
	}
}

// isFileLocked checks whether the file is locked via flock
func isFileLocked(lockPath string) (bool, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		if err == syscall.EWOULDBLOCK {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
