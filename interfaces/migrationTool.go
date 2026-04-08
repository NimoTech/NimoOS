/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-08-24 17:37:36
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-24 17:38:48
 * @FilePath: /NimoOS/interfaces/migrationTool.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package interfaces

type MigrationTool interface {
	IsMigrationNeeded() (bool, error)
	PostMigrate() error
	Migrate() error
	PreMigrate() error
}
