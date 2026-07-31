/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-08-24 17:37:36
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-24 17:38:48
 * @FilePath: /NimoOS/interfaces/migrationTool.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package interfaces

type MigrationTool interface {
	IsMigrationNeeded() (bool, error)
	PostMigrate() error
	Migrate() error
	PreMigrate() error
}
