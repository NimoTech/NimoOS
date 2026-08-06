/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-08-10 16:06:12
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-10 16:11:37
 * @FilePath: /NimoOS/pkg/utils/udev_helper.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package utils

// func getOptionnalMatcher() (matcher netlink.Matcher, err error) {
// 	if filePath == nil || *filePath == "" {
// 		return nil, nil
// 	}

// 	stream, err := ioutil.ReadFile(*filePath)
// 	if err != nil {
// 		return nil, err
// 	}

// 	if stream == nil {
// 		return nil, fmt.Errorf("Empty, no rules provided in \"%s\", err: %w", *filePath, err)
// 	}

// 	var rules netlink.RuleDefinitions
// 	if err := json.Unmarshal(stream, &rules); err != nil {
// 		return nil, fmt.Errorf("Wrong rule syntax, err: %w", err)
// 	}

// 	return &rules, nil
// }
