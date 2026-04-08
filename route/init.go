/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-11-15 15:51:44
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-11-15 15:55:16
 * @FilePath: /NimoOS/route/init.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package route

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	file1 "github.com/NimoTech/NimoOS-Common/utils/file"
	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/common"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/samba"
	"github.com/NimoTech/NimoOS/pkg/utils/encryption"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/pkg/utils/httper"
	v1 "github.com/NimoTech/NimoOS/route/v1"
	"github.com/NimoTech/NimoOS/service"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func InitFunction() {
	go InitNetworkMount()
	go InitInfo()
	go InitDiskStandby()
	//go InitZerotier()
}

func InitInfo() {
	mb := model.BaseInfo{}
	if file.Exists(config.AppInfo.DBPath + "/baseinfo.conf") {
		err := json.Unmarshal(file.ReadFullFile(config.AppInfo.DBPath+"/baseinfo.conf"), &mb)
		if err != nil {
			logger.Error("baseinfo.conf", zap.String("error", err.Error()))
		}
	}
	if file.Exists("/etc/CHANNEL") {
		channel := file.ReadFullFile("/etc/CHANNEL")
		mb.Channel = string(channel)
	}
	mac, err := service.MyService.System().GetMacAddress()
	if err != nil {
		logger.Error("GetMacAddress", zap.String("error", err.Error()))
	}
	mb.Hash = encryption.GetMD5ByStr(mac)
	mb.Version = common.VERSION
	osRelease, _ := file1.ReadOSRelease()

	mb.DriveModel = osRelease["MODEL"]
	if len(mb.DriveModel) == 0 {
		mb.DriveModel = "Casa"
	}
	os.Remove(config.AppInfo.DBPath + "/baseinfo.conf")
	by, err := json.Marshal(mb)
	if err != nil {
		logger.Error("init info err", zap.Any("err", err))
		return
	}
	file.WriteToFullPath(by, config.AppInfo.DBPath+"/baseinfo.conf", 0o666)
}

func InitNetworkMount() {
	time.Sleep(time.Second * 10)
	connections := service.MyService.Connections().GetConnectionsList()
	for _, v := range connections {
		connection := service.MyService.Connections().GetConnectionByID(fmt.Sprint(v.ID))
		directories, err := samba.GetSambaSharesList(connection.Host, connection.Port, connection.Username, connection.Password)
		if err != nil {
			service.MyService.Connections().DeleteConnection(fmt.Sprint(connection.ID))
			logger.Error("mount samba err", zap.Any("err", err), zap.Any("info", connection))
			continue
		}
		baseHostPath := "/mnt/" + connection.Host

		mountPointList, err := service.MyService.System().GetDirPath(baseHostPath)
		if err != nil {
			logger.Error("get mount point err", zap.Any("err", err))
			continue
		}
		for _, v := range mountPointList {
			service.MyService.Connections().UnmountSmaba(v.Path)
		}

		os.RemoveAll(baseHostPath)

		file.IsNotExistMkDir(baseHostPath)
		for _, v := range directories {
			mountPoint := baseHostPath + "/" + v
			file.IsNotExistMkDir(mountPoint)
			service.MyService.Connections().MountSmaba(connection.Username, connection.Host, v, connection.Port, mountPoint, connection.Password)
		}
		connection.Directories = strings.Join(directories, ",")
		service.MyService.Connections().UpdateConnection(&connection)
	}
	err := service.MyService.Storage().CheckAndMountAll()
	if err != nil {
		logger.Error("mount storage err", zap.Any("err", err))
	}
}
func InitDiskStandby() {
	// Wait for gateway and other services to be ready
	time.Sleep(time.Second * 35)

	err, portStr := service.MyService.Gateway().GetPort()
	if err != nil {
		logger.Error("failed to get gateway port for disk standby init", zap.Error(err))
		return
	}
	port := gjson.Get(portStr, "data").String()
	if port == "" {
		port = "80" // Default fallback
	}

	url := fmt.Sprintf("http://127.0.0.1:%s/v1/users/custom/system", port)
	
	// Internal request to fetch system settings
	res := httper.Get(url, nil)
	if res == "" {
		logger.Error("failed to fetch system custom storage for disk standby init")
		return
	}

	diskStandby := gjson.Get(res, "data.disk_standby").String()
	if diskStandby != "" {
		minutes := parseStandbyMinutes(diskStandby)
		logger.Info("applying boot disk standby setting", zap.String("setting", diskStandby), zap.Int("minutes", minutes))
		service.MyService.System().SetDiskStandby(minutes)
	}
}

func parseStandbyMinutes(standby string) int {
	if standby == "never" || standby == "" {
		return 0
	}
	if strings.HasSuffix(standby, "m") {
		m, _ := strconv.Atoi(strings.TrimSuffix(standby, "m"))
		return m
	}
	if strings.HasSuffix(standby, "h") {
		h, _ := strconv.Atoi(strings.TrimSuffix(standby, "h"))
		return h * 60
	}
	return 0
}

func InitZerotier() {
	v1.CheckNetwork()
}
