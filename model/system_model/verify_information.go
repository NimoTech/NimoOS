/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-06-15 11:30:47
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-06-23 18:40:40
 * @FilePath: /NimoOS/model/system_model/verify_information.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package system_model

type VerifyInformation struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	ExpiresAt    int64  `json:"expires_at"`
}
