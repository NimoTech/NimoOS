/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-09-30 18:18:14
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-06-02 18:00:57
 * @FilePath: /NimoOS/service/rely.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package service

import (
	model2 "github.com/NimoTech/NimoOS/service/model"
	"gorm.io/gorm"
)

type RelyService interface {
	Create(rely model2.RelyDBModel)
	Delete(id string)
	GetInfo(id string) model2.RelyDBModel
}

type relyService struct {
	db *gorm.DB
}

func (r *relyService) Create(rely model2.RelyDBModel) {

	r.db.Create(&rely)

}

// 获取我的应用列表
func (r *relyService) GetInfo(id string) model2.RelyDBModel {
	var m model2.RelyDBModel
	r.db.Where("custom_id = ?", id).First(&m)

	// @tiger - 作为出参不应该直接返回数据库内的格式（见类似问题的注释）
	return m
}

func (r *relyService) Delete(id string) {
	var c model2.RelyDBModel
	r.db.Where("custom_id = ?", id).Delete(&c)
}

func NewRelyService(db *gorm.DB) RelyService {
	return &relyService{db: db}
}
