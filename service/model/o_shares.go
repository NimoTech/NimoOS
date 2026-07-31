/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:17:17
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-27 15:25:07
 * @FilePath: /NimoOS/service/model/o_shares.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

type SharesDBModel struct {
	ID        uint   `gorm:"column:id;primary_key" json:"id"`
	Anonymous bool   `json:"anonymous"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Updated   int64  `gorm:"autoUpdateTime"`
	Created   int64  `gorm:"autoCreateTime"`
}

func (p *SharesDBModel) TableName() string {
	return "o_shares"
}
