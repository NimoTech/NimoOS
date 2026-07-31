/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-06-14 14:33:25
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-06-14 14:33:49
 * @FilePath: /NimoOS/pkg/utils/encryption/md5_helper.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package encryption

import (
	"crypto/md5"
	"encoding/hex"
)

func GetMD5ByStr(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}
