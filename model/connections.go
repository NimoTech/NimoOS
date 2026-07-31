/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-27 10:30:43
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-04 20:06:04
 * @FilePath: /NimoOS/model/connections.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

type Connections struct {
	ID         uint   `json:"id"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	Host       string `json:"host"`
	Port       string `json:"port"`
	MountPoint string `json:"mount_point"`
}
