/*
 * @Author: LinkLeong a624669980@163.com
 * @Date: 2022-05-08 14:58:46
 * @LastEditors: LinkLeong a624669980@163.com
 * @LastEditTime: 2022-05-09 13:42:26
 * @FilePath: /NimoOS/pkg/utils/network_detection.go
 * @Description:
 *
 * Copyright (c) 2022 by LinkLeong a624669980@163.com
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package utils

import natType "github.com/Curtis-Milo/nat-type-identifier-go"

/**
 * @description:
 * @param {chanstring} data
 * @param {string} url
 * @return {*}
 */
func GetNetWorkTypeDetection(data chan string, url string) {
	// fmt.Println("url:", url)
	// httper.Get(url, nil)
	// aaa <- url
	result, err := natType.GetDeterminedNatType(true, 5, url)
	if err == nil {
		data <- result
	}

}
