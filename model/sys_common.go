/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-05-13 18:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-09-02 22:12:34
 * @FilePath: /NimoOS/model/sys_common.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

import "time"

// System config
type SysInfoModel struct {
	Name string // System name
}

// Service config
type ServerModel struct {
	HttpPort     string
	RunMode      string
	ServerApi    string
	LockAccount  bool
	Token        string
	USBAutoMount string
	UpdateUrl    string
}

// Service config
type APPModel struct {
	LogPath        string
	LogSaveName    string
	LogFileExt     string
	DateStrFormat  string
	DateTimeFormat string
	UserDataPath   string
	TimeFormat     string
	DateFormat     string
	DBPath         string
	ShellPath      string
}
type CommonModel struct {
	RuntimePath string
}

// Common response model
type Result struct {
	Success int         `json:"success" example:"200"`
	Message string      `json:"message" example:"ok"`
	Data    interface{} `json:"data" example:"response result"`
}

// Redis config file
type RedisModel struct {
	Host        string
	Password    string
	MaxIdle     int
	MaxActive   int
	IdleTimeout time.Duration
}

type SystemConfig struct {
	ConfigPath string `json:"config_path"`
}

type FileSetting struct {
	ShareDir      []string `json:"share_dir" delim:"|"`
	DownloadDir   string   `json:"download_dir"`
	ThumbCacheDir string   `json:"thumb_cache_dir"`
}
type BaseInfo struct {
	Hash       string `json:"i"`
	Version    string `json:"v"`
	Channel    string `json:"c,omitempty"`
	DriveModel string `json:"m,omitempty"`
}
