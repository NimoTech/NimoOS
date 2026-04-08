/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-09-30 18:18:14
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-31 17:04:02
 * @FilePath: /NimoOS/pkg/config/config.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package config

import (
	"path/filepath"

	"github.com/NimoTech/NimoOS-Common/utils/constants"
)

var NimoOSConfigFilePath = filepath.Join(constants.DefaultConfigPath, "nimoos.conf")
