/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-05-26 14:21:57
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-06-02 11:14:15
 * @FilePath: /NimoOS/model/notify/file.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package notify

type File struct {
	Finished       bool   `json:"finished"`
	ProcessedSize  int64  `json:"processed_size"`
	ProcessingPath string `json:"processing_path"`
	Status         string `json:"status"`
	TotalSize      int64  `json:"total_size"`
	Id             string `json:"id"`
	To             string `json:"to"`
	Type           string `json:"type"`
	// ParkedPath mirrors model.FileItem.ParkedPath: set only when a
	// conflict-replace rollback-restore itself failed, pointing at the
	// ".nimoos-replacing-<uuid>" staging dir still holding the user's
	// original data. Empty (omitted) for every normal task.
	ParkedPath string `json:"parked_path,omitempty"`
	// Cancelled mirrors model.FileOperate.Cancelled: set (alongside
	// Finished and Status=="CANCELLED") when this task was stopped via
	// DELETE /file/operate/:id or /0 instead of completing naturally. Absent
	// (omitted) for every normal task.
	Cancelled bool `json:"cancelled,omitempty"`
}
