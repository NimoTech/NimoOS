/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-27 10:35:29
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-01 13:56:44
 * @FilePath: /NimoOS/pkg/samba/smaba.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package samba

import (
	"errors"
	"net"

	"github.com/hirochachacha/go-smb2"
)

func ConnectSambaService(host, port, username, password, directory string) error {
	conn, err := net.Dial("tcp", host+":"+port)
	if err != nil {
		return err
	}
	defer conn.Close()
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		return err
	}
	defer s.Logoff()
	names, err := s.ListSharenames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if name == directory {
			return nil
		}
	}
	return errors.New("directory not found")
}

// get share name list
func GetSambaSharesList(host, port, username, password string) ([]string, error) {
	conn, err := net.Dial("tcp", host+":"+port)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		return nil, err
	}
	defer s.Logoff()
	names, err := s.ListSharenames()
	if err != nil {
		return nil, err
	}
	return names, err
}
