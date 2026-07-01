package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	http2 "github.com/NimoTech/NimoOS-Common/utils/http"
	"github.com/NimoTech/NimoOS-Common/utils/port"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/utils"
	"github.com/NimoTech/NimoOS/pkg/utils/common_err"
	"github.com/NimoTech/NimoOS/pkg/utils/encryption"
	"github.com/NimoTech/NimoOS/service"
	model2 "github.com/NimoTech/NimoOS/service/model"
	"github.com/NimoTech/NimoOS/types"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/tidwall/gjson"
)

// @Summary check version
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/version/check [get]
func GetFirmwareCheckVersion(ctx echo.Context) error {
	checkResult := service.MyService.System().CheckUpdate()

	if checkResult.HasUpdate {
		customId := "sys_upgrade_" + checkResult.LatestVersion
		if service.MyService.Notify().GetLog(customId).CustomId == "" {
			installLog := model2.AppNotify{}
			installLog.State = 0
			installLog.Message = "New version " + checkResult.LatestVersion + " is ready, ready to upgrade"
			installLog.Type = types.NOTIFY_TYPE_NEED_CONFIRM
			installLog.CreatedAt = strconv.FormatInt(time.Now().Unix(), 10)
			installLog.UpdatedAt = strconv.FormatInt(time.Now().Unix(), 10)
			installLog.Name = "NimoOS System"
			installLog.CustomId = customId
			service.MyService.Notify().AddLog(installLog)
		}
	}

	if !checkResult.HasUpdate {
		data := make(map[string]interface{}, 2)
		data["need_update"] = false
		data["current_version"] = checkResult.CurrentVersion
		return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
	}

	ver := model.Version{
		Version:   checkResult.LatestVersion,
		ChangeLog: checkResult.Changelog,
	}

	data := make(map[string]interface{}, 8)
	data["need_update"] = true
	data["version"] = ver
	data["current_version"] = checkResult.CurrentVersion
	data["latest_version"] = checkResult.LatestVersion
	data["is_downloaded"] = checkResult.IsDownloaded
	data["is_downloading"] = checkResult.IsDownloading
	data["is_paused"] = checkResult.IsPaused
	data["download_progress"] = checkResult.DownloadProgress

	if !checkResult.IsDownloaded && !checkResult.IsDownloading && utils.DefaultQuery(ctx, "trigger_download", "0") == "1" {
		// DownloadUpdate 内部启动协程，立即返回
		service.MyService.System().DownloadUpdate()
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary 系统信息
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/update [post]
func FirmwareUpdate(ctx echo.Context) error {
	err := service.MyService.System().UpdateSystemVersion("")
	if err != nil {
		return ctx.JSON(common_err.SUCCESS, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: err.Error(),
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary check app version
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/app_version/check [get]
func GetSystemCheckVersion(ctx echo.Context) error {
	checkResult := service.MyService.System().CheckAppUpdate()

	if !checkResult.HasUpdate {
		data := make(map[string]interface{}, 2)
		data["need_update"] = false
		data["current_version"] = checkResult.CurrentVersion
		return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
	}

	ver := model.Version{
		Version:   checkResult.LatestVersion,
		ChangeLog: checkResult.Changelog,
	}

	data := make(map[string]interface{}, 8)
	data["need_update"] = true
	data["version"] = ver
	data["current_version"] = checkResult.CurrentVersion
	data["latest_version"] = checkResult.LatestVersion
	data["is_downloaded"] = checkResult.IsDownloaded
	data["is_downloading"] = checkResult.IsDownloading
	data["download_progress"] = checkResult.DownloadProgress

	if !checkResult.IsDownloaded && !checkResult.IsDownloading && utils.DefaultQuery(ctx, "trigger_download", "0") == "1" {
		service.MyService.System().DownloadAppUpdate()
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary 应用更新
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/app_update [post]
func SystemUpdate(ctx echo.Context) error {
	err := service.MyService.System().UpdateAppVersion("")
	if err != nil {
		return ctx.JSON(common_err.SUCCESS, model.Result{
			Success: common_err.SERVICE_ERROR,
			Message: err.Error(),
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary cancel download
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/download/cancel [post]
func CancelDownload(ctx echo.Context) error {
	service.MyService.System().CancelDownload()
	service.MyService.System().CancelAppDownload()
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// @Summary  get logs
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/error/logs [get]
func GetNimoOSErrorLogs(ctx echo.Context) error {
	line, _ := strconv.Atoi(utils.DefaultQuery(ctx, "line", "100"))
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: service.MyService.System().GetNimoOSLogs(line)})
}

// 系统配置
func GetSystemConfigDebug(ctx echo.Context) error {
	array := service.MyService.System().GetSystemConfigDebug()
	disk := service.MyService.System().GetDiskInfo()
	sys := service.MyService.System().GetSysInfo()
	version := service.MyService.Casa().GetNimoosVersion()
	var bugContent string = fmt.Sprintf(`
	 - OS: %s
	 - NimoOS Version: %s
	 - Disk Total: %v 
	 - Disk Used: %v 
	 - System Info: %s
	 - Remote Version: %s
	 - Browser: $Browser$ 
	 - Version: $Version$
`, sys.OS, service.MyService.System().GetVersion(), disk.Total>>20, disk.Used>>20, array, version.Version)

	//	array = append(array, fmt.Sprintf("disk,total:%v,used:%v,UsedPercent:%v", disk.Total>>20, disk.Used>>20, disk.UsedPercent))

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: bugContent})
}

// @Summary get nimoos server port
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/port [get]
func GetNimoOSPort(ctx echo.Context) error {
	return ctx.JSON(common_err.SUCCESS,
		model.Result{
			Success: common_err.SUCCESS,
			Message: common_err.GetMsg(common_err.SUCCESS),
			Data:    config.ServerInfo.HttpPort,
		})
}

// @Summary edit nimoos server port
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param port json string true "port"
// @Success 200 {string} string "ok"
// @Router /sys/port [put]
func PutNimoOSPort(ctx echo.Context) error {
	json := make(map[string]string)
	ctx.Bind(&json)
	portStr := json["port"]
	portNumber, err := strconv.Atoi(portStr)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR,
			model.Result{
				Success: common_err.SERVICE_ERROR,
				Message: err.Error(),
			})
	}

	isAvailable := port.IsPortAvailable(portNumber, "tcp")
	if !isAvailable {
		return ctx.JSON(common_err.SERVICE_ERROR,
			model.Result{
				Success: common_err.PORT_IS_OCCUPIED,
				Message: common_err.GetMsg(common_err.PORT_IS_OCCUPIED),
			})
	}
	service.MyService.System().UpSystemPort(strconv.Itoa(portNumber))
	return ctx.JSON(common_err.SUCCESS,
		model.Result{
			Success: common_err.SUCCESS,
			Message: common_err.GetMsg(common_err.SUCCESS),
		})
}

// @Summary active killing nimoos
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/restart [post]
func PostKillNimoOS(ctx echo.Context) error {
	os.Exit(0)
	return nil
}

// @Summary get system base info (device id, version, model)
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/baseinfo [get]
func GetSystemBaseInfo(ctx echo.Context) error {
	mac, err := service.MyService.System().GetMacAddress()
	if err != nil {
		mac = ""
	}
	data := map[string]string{
		"device_id": encryption.GetMD5ByStr(mac),
		"version":   service.MyService.System().GetVersion(),
		"model":     service.MyService.System().GetDeviceTree(),
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    data,
	})
}

// @Summary get system hardware info
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/hardware/info [get]
func GetSystemHardwareInfo(ctx echo.Context) error {
	data := make(map[string]interface{})
	data["version"] = service.MyService.System().GetVersion()
	data["hardware_id"] = service.MyService.System().GetHardwareID()
	data["hardware_name"] = service.MyService.System().GetHardwareName()
	data["drive_model"] = service.MyService.System().GetDeviceTree()
	data["arch"] = runtime.GOARCH

	// CPU
	if cpuInfo := service.MyService.System().GetCpuInfo(); len(cpuInfo) > 0 {
		data["cpu_model"] = cpuInfo[0].ModelName
		data["cpu_cores"] = service.MyService.System().GetCpuCoreNum()
		data["cpu_freq"] = cpuInfo[0].Mhz
	}

	// RAM
	memInfo := service.MyService.System().GetMemInfo()
	data["ram_total"] = memInfo["total"]
	ramDetail := service.MyService.System().GetRamDetail()
	data["ram_speed"] = ramDetail["speed"]
	data["ram_type"] = ramDetail["type"]

	// GPU
	data["gpu_list"] = service.MyService.System().GetGpuInfo()

	return ctx.JSON(common_err.SUCCESS,
		model.Result{
			Success: common_err.SUCCESS,
			Message: common_err.GetMsg(common_err.SUCCESS),
			Data:    data,
		})
}

// @Summary system utilization
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/utilization [get]
func GetSystemUtilization(ctx echo.Context) error {
	data := make(map[string]interface{})
	cpu := service.MyService.System().GetCpuPercent()
	num := service.MyService.System().GetCpuCoreNum()
	cpuModel := "arm"
	if cpu := service.MyService.System().GetCpuInfo(); len(cpu) > 0 {
		if strings.Count(strings.ToLower(strings.TrimSpace(cpu[0].ModelName)), "intel") > 0 {
			cpuModel = "intel"
		} else if strings.Count(strings.ToLower(strings.TrimSpace(cpu[0].ModelName)), "amd") > 0 {
			cpuModel = "amd"
		}
	}
	cpuData := make(map[string]interface{})
	cpuData["percent"] = cpu
	cpuData["num"] = num
	cpuData["temperature"] = service.MyService.System().GetCPUTemperature()
	cpuData["power"] = service.MyService.System().GetCPUPower()
	cpuData["model"] = cpuModel

	data["cpu"] = cpuData
	data["mem"] = service.MyService.System().GetMemInfo()

	// 拼装网络信息
	netList := service.MyService.System().GetNetInfo()
	newNet := []model.IOCountersStat{}
	nets := service.MyService.System().GetNet(true)
	for _, n := range netList {
		for _, netCardName := range nets {
			if n.Name == netCardName {
				item := model.IOCountersStat{
					Name:        n.Name,
					BytesSent:   n.BytesSent,
					BytesRecv:   n.BytesRecv,
					PacketsSent: n.PacketsSent,
					PacketsRecv: n.PacketsRecv,
					Errin:       n.Errin,
					Errout:      n.Errout,
					Dropin:      n.Dropin,
					Dropout:     n.Dropout,
					Fifoin:      n.Fifoin,
					Fifoout:     n.Fifoout,
				}
				item.State = strings.TrimSpace(service.MyService.System().GetNetState(n.Name))
				item.Addr = service.MyService.System().GetNetAddr(n.Name)
				item.Speed = service.MyService.System().GetNetSpeed(n.Name)
				item.MaxSpeed = service.MyService.System().GetNetMaxSpeed(n.Name)
				item.Time = time.Now().Unix()
				newNet = append(newNet, item)
				break
			}
		}
	}
	data["net"] = newNet
	data["gpu"] = service.MyService.System().GetGpuStatus()
	systemMap := service.MyService.Notify().GetSystemTempMap()
	systemMap.Range(func(key, value interface{}) bool {
		data[key.(string)] = value
		return true
	})
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary get system paths and sizes
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/paths [get]
func GetSystemPaths(ctx echo.Context) error {
	data := service.MyService.System().GetSystemPaths()
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary start app-path migration to a new mount point
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param body body object true "type: app_data|images|database, target_mount: /media/XXX"
// @Success 200 {string} string "ok"
// @Router /sys/migrate [post]
func PostMigrateAppPath(ctx echo.Context) error {
	var body struct {
		Type        string `json:"type"`
		TargetMount string `json:"target_mount"`
	}
	if err := ctx.Bind(&body); err != nil || body.Type == "" || body.TargetMount == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	jobID := uuid.NewString()
	service.MyService.System().StartMigrateAppPath(jobID, body.Type, body.TargetMount)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: map[string]string{"job_id": jobID}})
}

// @Summary get migration job status
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "job ID"
// @Success 200 {string} string "ok"
// @Router /sys/migrate/:id [get]
func GetMigrateStatus(ctx echo.Context) error {
	jobID := ctx.Param("id")
	status, ok := service.MyService.System().GetMigrateStatus(jobID)
	if !ok {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: "job not found"})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: status})
}

// @Summary get cpu info
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/cpu [get]
func GetSystemCupInfo(ctx echo.Context) error {
	cpu := service.MyService.System().GetCpuPercent()
	num := service.MyService.System().GetCpuCoreNum()
	data := make(map[string]interface{})
	data["percent"] = cpu
	data["num"] = num
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary get mem info
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/mem [get]
func GetSystemMemInfo(ctx echo.Context) error {
	mem := service.MyService.System().GetMemInfo()
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: mem})
}

// @Summary get disk info
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/disk [get]
func GetSystemDiskInfo(ctx echo.Context) error {
	disk := service.MyService.System().GetDiskInfo()
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: disk})
}

// @Summary get Net info
// @Produce  application/json
// @Accept application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /sys/net [get]
func GetSystemNetInfo(ctx echo.Context) error {
	netList := service.MyService.System().GetNetInfo()
	newNet := []model.IOCountersStat{}
	for _, n := range netList {
		for _, netCardName := range service.MyService.System().GetNet(true) {
			if n.Name == netCardName {
				item := model.IOCountersStat{
					Name:        n.Name,
					BytesSent:   n.BytesSent,
					BytesRecv:   n.BytesRecv,
					PacketsSent: n.PacketsSent,
					PacketsRecv: n.PacketsRecv,
					Errin:       n.Errin,
					Errout:      n.Errout,
					Dropin:      n.Dropin,
					Dropout:     n.Dropout,
					Fifoin:      n.Fifoin,
					Fifoout:     n.Fifoout,
				}
				item.State = strings.TrimSpace(service.MyService.System().GetNetState(n.Name))
				item.Time = time.Now().Unix()
				newNet = append(newNet, item)
				break
			}
		}
	}

	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: newNet})
}

func GetSystemProxy(ctx echo.Context) error {
	url := ctx.QueryParam("url")
	resp, err := http2.Get(url, 30*time.Second)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	defer resp.Body.Close()
	for k, v := range ctx.Request().Header {
		ctx.Request().Header.Add(k, v[0])
	}
	rda, _ := ioutil.ReadAll(resp.Body)
	//	json.NewEncoder(c.Writer).Encode(json.RawMessage(string(rda)))
	// 响应状态码
	ctx.Response().Writer.WriteHeader(resp.StatusCode)
	// 复制转发的响应Body到响应Body
	io.Copy(ctx.Response().Writer, ioutil.NopCloser(bytes.NewBuffer(rda)))
	return nil
}

func PutSystemState(ctx echo.Context) error {
	state := ctx.Param("state")
	if strings.ToLower(state) == "off" {
		service.MyService.System().SystemShutdown()
	} else if strings.ToLower(state) == "restart" {
		service.MyService.System().SystemReboot()
	}
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: "The operation will be completed shortly."})
}

// @Summary 获取一个可用端口
// @Produce  application/json
// @Accept application/json
// @Tags app
// @Param  type query string true "端口类型 udp/tcp"
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/getport [get]
func GetPort(ctx echo.Context) error {
	t := utils.DefaultQuery(ctx, "type", "tcp")
	var p int
	ok := true
	for ok {
		p, _ = port.GetAvailablePort(t)
		ok = !port.IsPortAvailable(p, t)
	}
	// @tiger 这里最好封装成 {'port': ...} 的形式，来体现出参的上下文
	return ctx.JSON(common_err.SUCCESS, &model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: p})
}

// @Summary 检查端口是否可用
// @Produce  application/json
// @Accept application/json
// @Tags app
// @Param  port path int true "端口号"
// @Param  type query string true "端口类型 udp/tcp"
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /app/check/{port} [get]
func PortCheck(ctx echo.Context) error {
	p, _ := strconv.Atoi(ctx.Param("port"))
	t := utils.DefaultQuery(ctx, "type", "tcp")
	return ctx.JSON(common_err.SUCCESS, &model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: port.IsPortAvailable(p, t)})
}

func GetSystemEntry(ctx echo.Context) error {
	entry := service.MyService.System().GetSystemEntry()
	str := json.RawMessage(entry)
	if !gjson.ValidBytes(str) {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: entry, Data: json.RawMessage("[]")})
	}
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: str})
}

func PutDiskStandby(ctx echo.Context) error {
	data := make(map[string]interface{})
	if err := ctx.Bind(&data); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	minutesVal, ok := data["minutes"]
	if !ok {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: "minutes is required"})
	}

	var minutes int
	switch v := minutesVal.(type) {
	case float64:
		minutes = int(v)
	case int:
		minutes = v
	default:
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: "minutes must be an integer"})
	}

	service.MyService.System().SetDiskStandby(minutes)
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}
