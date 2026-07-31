/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:12:12
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-27 14:58:55
 * @FilePath: /NimoOS/model/share.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

type Shares struct {
	ID        uint   `json:"id"`
	Anonymous bool   `json:"anonymous"`
	Path      string `json:"path"`
}
