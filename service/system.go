package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	net2 "net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
}
type systemService struct{}

func (c *systemService) GetDeviceInfo() model.DeviceInfo {
	m := model.DeviceInfo{}
	m.OS_Version = common.VERSION
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

	go func() {
		logger.Info("starting system upgrade", zap.String("bundle", latestBundle))

		cmd := exec.Command("/usr/libexec/nimo_upgrade.sh", latestBundle)
		output, err := cmd.CombinedOutput()

		if err != nil || !strings.Contains(string(output), "NimoOS upgrade successfully") {
			logger.Error("system upgrade failed", zap.Error(err), zap.String("log", string(output)))
			return
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

type remoteVersion struct {
	Versions []versionInfo `json:"versions"`
}

type localVersionJSON struct {
	Version      string `json:"version"`
	UpdateServer string `json:"update_server"`
	Platform     string `json:"platform"`
	HardwareID   string `json:"hardware_id"`
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
	return &local, nil
}

func fetchRemoteVersionJSON(server string) (*remoteVersion, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Get(server + "/releases/version.json")
	if err != nil {
		return nil, fmt.Errorf("cannot fetch remote version: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read remote version: %v", err)
	}
	var remote remoteVersion
	if err := json.Unmarshal(body, &remote); err != nil {
		return nil, fmt.Errorf("cannot parse remote version: %v", err)
	}
	return &remote, nil
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
	HasUpdate      bool   `json:"has_update"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	IsDownloaded   bool   `json:"is_downloaded"`
	Changelog      string `json:"changelog"`
	Size           int    `json:"size"`
	Error          string `json:"error,omitempty"`
}

func (s *systemService) CheckUpdate() *UpgradeCheckResult {
	result := &UpgradeCheckResult{
		CurrentVersion: common.VERSION,
	}

	local, err := readLocalVersionInfo()
	if err != nil {
		return result
	}
	if local.Version != "" {
		result.CurrentVersion = local.Version
	}

	remote, err := fetchRemoteVersionJSON(local.UpdateServer)
	if err != nil {
		return result
	}

	bestVersion := findHighestCompatibleVersion(remote.Versions, result.CurrentVersion, local.Platform, local.HardwareID)
	if bestVersion == nil {
		return result
	}

	result.LatestVersion = bestVersion.Version
	result.Size = bestVersion.Size

	changelogParts := []string{}
	curParts := make([]int, 3)
	curVerParts := strings.Split(result.CurrentVersion, ".")
	for i, p := range curVerParts {
		curParts[i], _ = strconv.Atoi(p)
	}

	for _, v := range remote.Versions {
		if !isVersionCompatible(v, result.CurrentVersion, local.Platform, local.HardwareID) {
			continue
		}
		verParts := strings.Split(v.Version, ".")
		verNums := make([]int, 3)
		for i, p := range verParts {
			verNums[i], _ = strconv.Atoi(p)
		}
		if verNums[0] > curParts[0] || (verNums[0] == curParts[0] && verNums[1] > curParts[1]) || (verNums[0] == curParts[0] && verNums[1] == curParts[1] && verNums[2] > curParts[2]) {
			changelogParts = append(changelogParts, "v"+v.Version+":\n"+v.Changelog)
		}
	}
	result.Changelog = strings.Join(changelogParts, "\n\n")

	if compareVersions(result.LatestVersion, result.CurrentVersion) <= 0 {
		return result
	}

	downloadDir := "/var/lib/nimoos_data/upgrade/v" + bestVersion.Version
	if entries, err := os.ReadDir(downloadDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".raucb" {
				result.IsDownloaded = true
				break
			}
		}
	}

	result.HasUpdate = true
	return result
}

func (s *systemService) DownloadUpdate() error {
	logger.Info("DownloadUpdate: starting")
	check := s.CheckUpdate()
	if !check.HasUpdate {
		logger.Info("DownloadUpdate: no update available from CheckUpdate")
		return fmt.Errorf("no update available")
	}
	if check.IsDownloaded {
		logger.Info("DownloadUpdate: already downloaded")
		return nil
	}

	local, err := readLocalVersionInfo()
	if err != nil {
		logger.Error("DownloadUpdate: cannot read local version", zap.Error(err))
		return fmt.Errorf("cannot read local version: %v", err)
	}

	remote, err := fetchRemoteVersionJSON(local.UpdateServer)
	if err != nil {
		logger.Error("DownloadUpdate: cannot fetch remote version", zap.Error(err))
		return err
	}

	bestVersion := findHighestCompatibleVersion(remote.Versions, local.Version, local.Platform, local.HardwareID)
	if bestVersion == nil {
		logger.Error("DownloadUpdate: no compatible version found")
		return fmt.Errorf("no compatible version found for this platform")
	}

	logger.Info("DownloadUpdate: selected version", zap.String("version", bestVersion.Version), zap.String("url", bestVersion.DownloadURL))

	downloadDir := "/var/lib/nimoos_data/upgrade/v" + bestVersion.Version
	os.MkdirAll(downloadDir, 0755)

	tmpFile := filepath.Join(downloadDir, "nimo_update_"+bestVersion.Version+".raucb.tmp")
	finalFile := filepath.Join(downloadDir, "nimo_update_"+bestVersion.Version+".raucb")

	if _, err := os.Stat(finalFile); err == nil {
		logger.Info("DownloadUpdate: final file already exists")
		return nil
	}

	fullURL := local.UpdateServer + bestVersion.DownloadURL
	logger.Info("DownloadUpdate: downloading", zap.String("url", fullURL))
	downloadClient := &http.Client{Timeout: 5 * time.Minute}
	dlResp, err := downloadClient.Get(fullURL)
	if err != nil {
		logger.Error("DownloadUpdate: download failed", zap.Error(err), zap.String("url", fullURL))
		return fmt.Errorf("download failed: %v", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		logger.Error("DownloadUpdate: download bad status", zap.Int("status", dlResp.StatusCode))
		return fmt.Errorf("download failed: status %d", dlResp.StatusCode)
	}

	out, err := os.Create(tmpFile)
	if err != nil {
		logger.Error("DownloadUpdate: cannot create temp file", zap.Error(err))
		return fmt.Errorf("cannot create temp file: %v", err)
	}

	written, err := io.Copy(out, dlResp.Body)
	if err != nil {
		out.Close()
		os.Remove(tmpFile)
		logger.Error("DownloadUpdate: download write failed", zap.Error(err))
		return fmt.Errorf("download write failed: %v", err)
	}
	logger.Info("DownloadUpdate: downloaded bytes", zap.Int64("bytes", written))
	out.Close()

	hasher := sha256.New()
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		os.Remove(tmpFile)
		logger.Error("DownloadUpdate: cannot read temp file for checksum", zap.Error(err))
		return fmt.Errorf("cannot read temp file for checksum: %v", err)
	}
	hasher.Write(content)
	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))

	if !strings.EqualFold(actualSHA256, bestVersion.SHA256) {
		os.Remove(tmpFile)
		logger.Error("DownloadUpdate: SHA256 mismatch", zap.String("expected", bestVersion.SHA256), zap.String("actual", actualSHA256))
		return fmt.Errorf("SHA256 mismatch")
	}

	if err := os.Rename(tmpFile, finalFile); err != nil {
		os.Remove(tmpFile)
		logger.Error("DownloadUpdate: rename failed", zap.Error(err))
		return fmt.Errorf("rename failed: %v", err)
	}

	logger.Info("DownloadUpdate: download complete", zap.String("file", finalFile))

	// Cleanup old upgrade directories
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

	properties := map[string]string{
		"version": bestVersion.Version,
	}
	MyService.MessageBus().PublishEventWithResponse(context.Background(), common.SERVICENAME, "nimoos:upgrade:downloaded", properties)

	return nil
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

			time.Sleep(time.Until(next))

			check := s.CheckUpdate()
			if check.HasUpdate && !check.IsDownloaded {
				s.DownloadUpdate()
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

func (s *systemService) GetCPUTemperature() int {
	outPut := ""
	path := GetCPUThermalZone()
	if len(path) > 0 {
		outPut = string(file.ReadFullFile(path + "/temp"))
	} else {
		outPut = string(file.ReadFullFile("/sys/class/hwmon/hwmon0/temp1_input"))
		if len(outPut) == 0 {
			outPut = "0"
		}
	}

	celsius, _ := strconv.Atoi(strings.TrimSpace(outPut))

	if celsius > 1000 {
		celsius = celsius / 1000
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

// GpuStatus is one GPU's runtime metrics.
type GpuStatus struct {
	Index             int     `json:"index"`
	Name              string  `json:"name"`
	Vendor            string  `json:"vendor"`
	UtilizationGpu    float64 `json:"utilization_gpu"`
	UtilizationMemory float64 `json:"utilization_memory"`
	MemoryTotal       uint64  `json:"memory_total"`
	MemoryUsed        uint64  `json:"memory_used"`
	Temperature       float64 `json:"temperature"`
}

func (c *systemService) GetGpuStatus() []GpuStatus {
	gpus := []GpuStatus{}
	gpus = append(gpus, queryNvidiaGpuStatus()...)
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

func (c *systemService) GetRamDetail() map[string]string {
	data := make(map[string]string)
	// Speed
	if output, err := command.OnlyExec("sudo dmidecode -t memory | grep 'Speed' | head -n 1"); err == nil {
		parts := strings.Split(output, ":")
		if len(parts) > 1 {
			data["speed"] = strings.TrimSpace(parts[1])
		}
	}
	// Type
	if output, err := command.OnlyExec("sudo dmidecode -t memory | grep 'Type:' | head -n 1"); err == nil {
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

	var appDataSize, imagesSize, userDataSize int64

	// SAFETY: Prevent full-disk scan if path is "/" or empty
	if realAppData != "/" && realAppData != "" {
		appDataSize, _ = file.GetFileOrDirSize(realAppData)
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
	}
}

func (c *systemService) StartMigrateAppPath(jobID, migrationType, targetMountPoint string) {
	StartMigration(jobID, migrationType, targetMountPoint)
}

func (c *systemService) GetMigrateStatus(jobID string) (MigrateStatus, bool) {
	return GetMigrationStatus(jobID)
}

func NewSystemService() SystemService {
	return &systemService{}
}
