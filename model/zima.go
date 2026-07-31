/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-05-13 18:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-01 18:32:57
 * @FilePath: /NimoOS/model/zima.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

import "time"

type Path struct {
	Name       string                 `json:"name"`
	Path       string                 `json:"path"`
	IsDir      bool                   `json:"is_dir"`
	IsSymlink  bool                   `json:"is_symlink"`
	Date       time.Time              `json:"date"`
	Size       int64                  `json:"size"`
	Type       string                 `json:"type,omitempty"`
	Label      string                 `json:"label,omitempty"`
	Write      bool                   `json:"write"`
	Extensions map[string]interface{} `json:"extensions"`
}

type DeviceInfo struct {
	LanIpv4     []string `json:"lan_ipv4"`
	Port        int      `json:"port"`
	DeviceName  string   `json:"device_name"`
	DeviceModel string   `json:"device_model"`
	DeviceSN    string   `json:"device_sn"`
	Initialized bool     `json:"initialized"`
	OS_Version  string   `json:"os_version"`
	Hash        string   `json:"hash"`
}
