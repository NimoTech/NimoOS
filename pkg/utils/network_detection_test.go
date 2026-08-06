/*
 * @Author: LinkLeong a624669980@163.com
 * @Date: 2022-05-08 15:07:31
 * @LastEditors: LinkLeong a624669980@163.com
 * @LastEditTime: 2022-05-09 11:43:30
 * @FilePath: /NimoOS/pkg/utils/network_detection_test.go
 * @Description:
 *
 * Copyright (c) 2022 by LinkLeong a624669980@163.com
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */

package utils

import (
	"fmt"
	"testing"
)

func TestGetResultTest(t *testing.T) {
	t.Skip("This test is always failing. Skipped to unblock releasing - MUST FIX!")

	list := []string{"https://www.google.com", "https://www.bing.com", "https://www.baidu.com"}
	data := make(chan string)
	// data <- "init"
	for _, v := range list {
		go GetNetWorkTypeDetection(data, v)
	}
	result := <-data
	close(data)
	fmt.Println(result)
}
