/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-07-15 10:43:00
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-15 10:56:17
 * @FilePath: /NimoOS/model/notify/storage.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package notify

type StorageMessage struct {
	Type   string `json:"type"`   //sata,usb
	Action string `json:"action"` //remove add
	Path   string `json:"path"`
	Volume string `json:"volume"`
	Size   uint64 `json:"size"`
}
