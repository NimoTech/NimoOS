/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-05-26 14:21:11
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-05-27 11:15:59
 * @FilePath: /NimoOS/model/notify/result.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */

package notify

// Notify struct for Notify
type NotifyModel struct {
	Data  interface{} `json:"data"`
	State string      `json:"state"`
}
