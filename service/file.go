/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-12-20 14:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-04 16:18:23
 * @FilePath: /NimoOS/service/file.go
 * @Description:
 * @Website: https://www.nimoos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS/model"
	"github.com/NimoTech/NimoOS/pkg/utils/file"
	"github.com/NimoTech/NimoOS/service/pathlock"
	"github.com/moby/sys/mountinfo"
	"go.uber.org/zap"
)

var FileQueue sync.Map

// opQueue replaces the former bare []string OpStrArr.
// All access goes through EnqueueOp / DequeueOp / PeekOps / ClearOps so
// concurrent handler goroutines can never race on the slice.
var opQueue struct {
	sync.Mutex
	ids []string
}

// EnqueueOp adds id to the queue and reports whether this is the first entry
// (i.e. the caller should kick off ExecOpFile + CheckFileStatus).
func EnqueueOp(id string) (isFirst bool) {
	opQueue.Lock()
	defer opQueue.Unlock()
	opQueue.ids = append(opQueue.ids, id)
	return len(opQueue.ids) == 1
}

// DequeueOp removes id from the queue.
func DequeueOp(id string) {
	opQueue.Lock()
	defer opQueue.Unlock()
	out := opQueue.ids[:0]
	for _, v := range opQueue.ids {
		if v != id {
			out = append(out, v)
		}
	}
	opQueue.ids = out
}

// PeekOps returns a snapshot copy of the current queue.
func PeekOps() []string {
	opQueue.Lock()
	defer opQueue.Unlock()
	cp := make([]string, len(opQueue.ids))
	copy(cp, opQueue.ids)
	return cp
}

// ClearOps empties the queue.
func ClearOps() {
	opQueue.Lock()
	defer opQueue.Unlock()
	opQueue.ids = nil
}

type reader struct {
	ctx context.Context
	r   io.Reader
}

// NewReader wraps an io.Reader to handle context cancellation.
//
// Context state is checked BEFORE every Read.
func NewReader(ctx context.Context, r io.Reader) io.Reader {
	if r, ok := r.(*reader); ok && ctx == r.ctx {
		return r
	}
	return &reader{ctx: ctx, r: r}
}

func (r *reader) Read(p []byte) (n int, err error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

type writer struct {
	ctx context.Context
	w   io.Writer
}

type copier struct {
	writer
}

func NewWriter(ctx context.Context, w io.Writer) io.Writer {
	if w, ok := w.(*copier); ok && ctx == w.ctx {
		return w
	}
	return &copier{writer{ctx: ctx, w: w}}
}

// Write implements io.Writer, but with context awareness.
func (w *writer) Write(p []byte) (n int, err error) {
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	default:
		return w.w.Write(p)
	}
}

// opDestPath returns the destination CopyDir/move will actually create.
// filepath.Base 会剥离尾部斜杠;此前手写的 strings.LastIndex 切分在 from 以
// "/" 结尾时得到空文件名,dst 退化为 to 本身(必然存在),skip 判断永真,
// 整个复制会被静默跳过(CopyDir 都不会被调用)。
func opDestPath(from, to string) string {
	return filepath.Join(to, filepath.Base(from))
}

func FileOperate(k string) {
	list, ok := FileQueue.Load(k)
	if !ok {
		return
	}

	temp := list.(model.FileOperate)
	if temp.ProcessedSize > 0 {
		return
	}

	// Hold write locks on every source path and the destination for the
	// duration of this operation batch.
	var unlocks []func()
	for _, item := range temp.Item {
		unlocks = append(unlocks, pathlock.LockWrite(item.From))
	}
	if temp.To != "" {
		unlocks = append(unlocks, pathlock.LockWrite(temp.To))
	}
	defer func() {
		for _, u := range unlocks {
			u()
		}
	}()

	createdPaths := make([]string, 0, len(temp.Item))

	for i := 0; i < len(temp.Item); i++ {
		v := temp.Item[i]
		if temp.Type == "move" {
			dst := opDestPath(v.From, temp.To)
			if !file.CheckNotExist(dst) {
				if temp.Style == "skip" {
					temp.Item[i].Finished = true
					continue
				}
				os.RemoveAll(dst)
			}
			if err := file.CopyDir(v.From, temp.To, temp.Style); err != nil {
				logger.Error("move: copy phase failed", zap.String("from", v.From), zap.Error(err))
				continue
			}
			// Verify destination size matches source before removing source.
			srcSize, err := file.GetFileOrDirSize(v.From)
			if err != nil {
				logger.Error("move: stat source failed", zap.String("from", v.From), zap.Error(err))
				os.RemoveAll(dst)
				continue
			}
			dstSize, err := file.GetFileOrDirSize(dst)
			if err != nil || dstSize != srcSize {
				logger.Error("move: destination size mismatch, aborting remove", zap.String("dst", dst))
				os.RemoveAll(dst)
				continue
			}
			if err := os.RemoveAll(v.From); err != nil {
				logger.Error("move: remove source failed", zap.String("from", v.From), zap.Error(err))
				// Source still intact; dst is a valid copy — leave both, log only.
			}
			createdPaths = append(createdPaths, dst)

		} else if temp.Type == "copy" {
			dst := opDestPath(v.From, temp.To)
			if temp.Style == "skip" && !file.CheckNotExist(dst) {
				// 目的地已存在且策略为 skip:CopyDir 为空操作,没有真正落盘,不发 media:created
				continue
			}
			if err := file.CopyDir(v.From, temp.To, temp.Style); err != nil {
				continue
			}
			createdPaths = append(createdPaths, dst)
		} else {
			continue
		}

	}
	temp.Finished = true
	FileQueue.Store(k, temp)

	if len(createdPaths) > 0 {
		go PublishMediaCreated(createdPaths)
	}
}

func ExecOpFile() {
	ids := PeekOps()
	if len(ids) == 0 {
		return
	}
	go FileOperate(ids[0])
}

// file move or copy and send notify
func CheckFileStatus() {
	for {
		ids := PeekOps()
		if len(ids) == 0 {
			return
		}
		for _, v := range ids {
			var total int64 = 0
			item, ok := FileQueue.Load(v)
			if !ok {
				continue
			}
			temp := item.(model.FileOperate)
			for i := 0; i < len(temp.Item); i++ {
				if !temp.Item[i].Finished {
					size, err := file.GetFileOrDirSize(temp.To + "/" + filepath.Base(temp.Item[i].From))
					if err != nil {
						continue
					}
					temp.Item[i].ProcessedSize = size
					if size == temp.Item[i].Size {
						temp.Item[i].Finished = true
					}
					total += size
				} else {
					total += temp.Item[i].ProcessedSize
				}
			}
			temp.ProcessedSize = total
			FileQueue.Store(v, temp)
		}
		time.Sleep(time.Second * 3)
	}
}
func IsMounted(path string) bool {
	mounted, _ := mountinfo.Mounted(path)
	if mounted {
		return true
	}
	connections := MyService.Connections().GetConnectionsList()
	for _, v := range connections {
		if v.MountPoint == path {
			return true
		}
	}
	return false
}

// HasChildMounts returns true if any mount point exists at or below path.
// Docker bind-mounts AppData subdirectories into running containers; those
// active mounts make os.RemoveAll fail with EBUSY.  Call this before
// attempting to delete a directory tree to surface the problem early.
func HasChildMounts(path string) bool {
	mounts, err := mountinfo.GetMounts(func(info *mountinfo.Info) (bool, bool) {
		// skip=false means include; stop=false means keep scanning
		return false, false
	})
	if err != nil {
		return false
	}
	prefix := filepath.Clean(path)
	for _, m := range mounts {
		mp := filepath.Clean(m.Mountpoint)
		if mp == prefix || strings.HasPrefix(mp, prefix+"/") {
			return true
		}
	}
	return false
}
