/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:21:14
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-18 11:16:25
 * @FilePath: /NimoOS/service/shares.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"path/filepath"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/command"
	"github.com/NimoTech/NimoOS/pkg/config"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service/model"
	model2 "github.com/NimoTech/NimoOS/service/model"
	"gorm.io/gorm"
)

type SharesService interface {
	GetSharesList() (shares []model2.SharesDBModel)
	GetSharesByPath(path string) (shares []model2.SharesDBModel)
	GetSharesByName(name string) (shares []model2.SharesDBModel)
	CreateShare(share model2.SharesDBModel)
	DeleteShare(id string)
	UpdateConfigFile()
	InitSambaConfig()
	DeleteShareByPath(path string)
	RewriteSharePathPrefix(oldPath, newPath string) int
}

type sharesStruct struct {
	db *gorm.DB
}

// sharePathScope 把「清理某路径上/下所有分享」的匹配范围规范化为
// (精确路径, 子树 LIKE 模式):尾斜杠归一;LIKE 通配符(\ % _)转义,防止
// 路径里出现通配符时误匹配;子树模式带 "/" 边界——旧实现裸 `path+"%"` 会在
// 删 /a/b 时误删 /a/bc 上的分享。
func sharePathScope(path string) (exact string, subtreePattern string) {
	exact = strings.TrimRight(path, "/")
	if exact == "" {
		exact = "/"
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(exact)
	if exact == "/" {
		return exact, "/%"
	}
	return exact, esc + "/%"
}

// DeleteShareByPath 删除挂在 path 本身及其子树上的全部分享记录并重写 smb
// 配置。供删除文件夹的链路调用:目录没了,分享再留着就是「已共享」Tab 里
// 永远打不开的悬挂项。
func (s *sharesStruct) DeleteShareByPath(path string) {
	exact, subtree := sharePathScope(path)
	s.db.Where(`path = ? OR path LIKE ? ESCAPE '\'`, exact, subtree).Delete(&model.SharesDBModel{})
	s.UpdateConfigFile()
}

// rewriteSharePath 把 sharePath 从 oldExact 子树映射到 newExact 子树:恰为
// oldExact 本身 → newExact;在其子树内 → 前缀替换;不在范围内原样返回。
// oldExact/newExact 须已做尾斜杠归一(调用方经 sharePathScope/TrimRight)。
func rewriteSharePath(sharePath, oldExact, newExact string) string {
	if sharePath == oldExact {
		return newExact
	}
	if strings.HasPrefix(sharePath, oldExact+"/") {
		return newExact + sharePath[len(oldExact):]
	}
	return sharePath
}

// RewriteSharePathPrefix 把挂在 oldPath 自身/子树上的分享记录改写到 newPath
// 下,并同步更新 Name(smb 配置节名取自 basename)。返回改写条数;仅在确有
// 改写时才重写 smb 配置(避免普通移动触发无谓的 smbd 重启)。供移动/重命名
// 含分享(自身或子树)目录的链路调用:分享记录的 path 若仍指旧位置,会在
// 「已共享」Tab 里成为永远打不开的悬挂项——正确语义是改写路径,不是删除。
func (s *sharesStruct) RewriteSharePathPrefix(oldPath, newPath string) int {
	oldExact, subtree := sharePathScope(oldPath)
	newExact := strings.TrimRight(newPath, "/")

	rows := []model2.SharesDBModel{}
	s.db.Where(`path = ? OR path LIKE ? ESCAPE '\'`, oldExact, subtree).Find(&rows)

	count := 0
	for _, row := range rows {
		newSharePath := rewriteSharePath(row.Path, oldExact, newExact)
		if newSharePath == row.Path {
			continue
		}
		s.db.Model(&model.SharesDBModel{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
			"path": newSharePath,
			"name": filepath.Base(newSharePath),
		})
		count++
	}

	if count > 0 {
		s.UpdateConfigFile()
	}
	return count
}

func (s *sharesStruct) GetSharesByName(name string) (shares []model2.SharesDBModel) {
	s.db.Select("anonymous,path,id").Where("name = ?", name).Find(&shares)
	return
}

func (s *sharesStruct) GetSharesByPath(path string) (shares []model2.SharesDBModel) {
	s.db.Select("anonymous,path,id").Where("path = ?", path).Find(&shares)
	return
}

func (s *sharesStruct) GetSharesList() (shares []model2.SharesDBModel) {
	s.db.Select("anonymous,path,id").Find(&shares)
	return
}

func (s *sharesStruct) CreateShare(share model2.SharesDBModel) {
	s.db.Create(&share)
	s.UpdateConfigFile()
}

func (s *sharesStruct) DeleteShare(id string) {
	s.db.Where("id= ?", id).Delete(&model.SharesDBModel{})
	s.UpdateConfigFile()
}

// globalSmbConf is the canonical [global] section written on every config update.
// Keeping it here ensures deployed systems always get the latest settings without
// requiring a manual file deletion or factory reset.
const globalSmbConf = `# Copyright (c) 2021-2022 NimoOS Inc. All rights reserved.
#
#
#                          ______     _______
#                        (  __  \   (  ___  )
#                        | (  \  )  | (   ) |
#                        | |   ) |  | |   | |
#                        | |   | |  | |   | |
#                        | |   ) |  | |   | |
#                        | (__/  )  | (___) |
#                        (______/   (_______)
#
#                   _          _______   _________
#                  ( (    /|  (  ___  )  \__   __/
#                  |  \  ( |  | (   ) |     ) (
#                  |   \ | |  | |   | |     | |
#                  | (\ \) |  | |   | |     | |
#                  | | \   |  | |   | |     | |
#                  | )  \  |  | (___) |     | |
#                  |/    )_)  (_______)     )_(
#
#   _______    _______    ______    _________   _______
#  (       )  (  ___  )  (  __  \   \__   __/  (  ____ \  |\     /|
#  | () () |  | (   ) |  | (  \  )     ) (     | (    \/  ( \   / )
#  | || || |  | |   | |  | |   ) |     | |     | (__       \ (_) /
#  | |(_)| |  | |   | |  | |   | |     | |     |  __)       \   /
#  | |   | |  | |   | |  | |   ) |     | |     | (           ) (
#  | |   | |  | (___) |  | (__/  )  ___) (___  | )           | |
#  |/     \|  (_______)  (______/   \_______/  |/            \_/
#
#
# IMPORTANT: NimoOS will not provide technical support for any issues
#            caused by unauthorized modification to the configuration.

[global]
   workgroup = WORKGROUP
   netbios name = NIMOOS
   server string = NimoOS
   security = user
   passdb backend = tdbsam
   access based share enum = yes
   min protocol = SMB2
   ntlm auth = ntlmv2-only
   ea support = yes
   fruit:metadata = stream
   fruit:model = Macmini
   fruit:veto_appledouble = no
   fruit:posix_rename = yes
   fruit:zero_file_id = yes
   fruit:wipe_intentionally_left_blank_rfork = yes
   fruit:delete_empty_adfiles = yes
   include=/etc/samba/smb.casa.conf`

// UpdateConfigFile writes both the global smb.conf and the per-share smb.casa.conf,
// then restarts Samba. Called on every share create/delete so the on-disk config
// always reflects the current code and database state.
func (s *sharesStruct) UpdateConfigFile() {
	// Always overwrite the global config so settings like access based share enum
	// take effect on existing deployments without manual intervention.
	file.WriteToPath([]byte(globalSmbConf), "/etc/samba", "smb.conf")

	// Build per-share config.
	shares := []model2.SharesDBModel{}
	s.db.Select("anonymous,path,username").Find(&shares)
	configStr := ""
	for _, share := range shares {
		dirName := filepath.Base(share.Path)
		validUsers := share.Username
		if validUsers == "" {
			validUsers = "@users"
		}
		configStr += `
[` + dirName + `]
comment = NimoOS share ` + dirName + `
path = ` + share.Path + `
browseable = Yes
read only = No
valid users = ` + validUsers + `
create mask = 0777
directory mask = 0777
force user = root

`
	}
	file.WriteToPath([]byte(configStr), "/etc/samba", "smb.casa.conf")
	command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;RestartSMBD")
}

// InitSambaConfig is kept for interface compatibility. Config is now always
// written by UpdateConfigFile, so this is a no-op.
func (s *sharesStruct) InitSambaConfig() {}

func NewSharesService(db *gorm.DB) SharesService {
	return &sharesStruct{db: db}
}
