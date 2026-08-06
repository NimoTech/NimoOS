/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-05-20 16:27:12
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-06-09 18:18:46
 * @FilePath: /NimoOS/model/file.go
 * @Description:
 * Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
 * Copyright (c) 2026 NimoTech
 * Licensed under the Apache License, Version 2.0.
 * Modified from the original CasaOS source by NimoTech.
 */
package model

type FileOperate struct {
	Type          string     `json:"type" binding:"required"`
	Item          []FileItem `json:"item" binding:"required"`
	TotalSize     int64      `json:"total_size"`
	ProcessedSize int64      `json:"processed_size"`
	To            string     `json:"to" binding:"required"`
	Style         string     `json:"style"`
	Finished      bool       `json:"finished"`
	// Cancelled is set when this task was stopped via
	// DELETE /file/operate/:id (or /0) instead of running to natural
	// completion. Terminal either way (Finished is also set), but callers
	// that need to distinguish "done" from "stopped early" (the UI) can
	// check this. Omitted from JSON for every normal, non-cancelled task.
	Cancelled bool `json:"cancelled,omitempty"`
}

type FileItem struct {
	From          string `json:"from" binding:"required"`
	Finished      bool   `json:"finished"`
	Size          int64  `json:"size"`
	ProcessedSize int64  `json:"processed_size"`
	// ParkedPath is set only in the rare case where a conflict-replace
	// rollback itself failed (see service.replaceConflict): the user's
	// original destination data could not be renamed back from its
	// ".nimoos-replacing-<uuid>" staging path and is left there instead of
	// at From/dst. It is surfaced here (rather than only logged) so the
	// task-status JSON the UI already polls can report the parked location
	// instead of silently losing track of the data.
	ParkedPath string `json:"parked_path,omitempty"`
}

type FileUpdate struct {
	FilePath    string `json:"path" binding:"required"`
	FileContent string `json:"content" binding:"required"`
}
