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
}

type sharesStruct struct {
	db *gorm.DB
}

func (s *sharesStruct) DeleteShareByPath(path string) {
	s.db.Where("path LIKE ?", path+"%").Delete(&model.SharesDBModel{})
	s.UpdateConfigFile()
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
