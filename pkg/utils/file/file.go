package file

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"mime/multipart"
	"os"
	"os/exec"
	"path"
	path2 "path"
	"path/filepath"
	"strconv"
	"strings"

	"syscall"

	"github.com/mholt/archiver/v3"
)

// RunCopyCommand executes cmd, which CopyFile/CopyDirContents always build
// via exec.CommandContext so that cancelling the context kills the
// underlying cp subprocess. It is indirected (rather than called as
// cmd.Run() inline) purely so tests can substitute a stand-in that blocks
// until told to proceed — deterministically landing a cancellation while a
// copy is genuinely in flight, without depending on real file-size/disk
// timing, which would make such a test flaky. The default is a plain
// passthrough with no behavior change from before A3.
var RunCopyCommand = func(cmd *exec.Cmd) error {
	return cmd.Run()
}

// GetSize get the file size
func GetSize(f multipart.File) (int, error) {
	content, err := ioutil.ReadAll(f)
	return len(content), err
}

// GetExt get the file ext
func GetExt(fileName string) string {
	return path.Ext(fileName)
}

// CheckNotExist check if the file exists
func CheckNotExist(src string) bool {
	_, err := os.Stat(src)

	return os.IsNotExist(err)
}

// CheckPermission check if the file has permission
func CheckPermission(src string) bool {
	_, err := os.Stat(src)

	return os.IsPermission(err)
}

// IsNotExistMkDir create a directory if it does not exist
func IsNotExistMkDir(src string) error {
	if notExist := CheckNotExist(src); notExist {
		if err := MkDir(src); err != nil {
			return err
		}
	}

	return nil
}

// MkDir create a directory
func MkDir(src string) error {
	err := os.MkdirAll(src, os.ModePerm)
	if err != nil {
		return err
	}
	os.Chmod(src, 0o777)

	return nil
}

// RMDir remove a directory
func RMDir(src string) error {
	err := os.RemoveAll(src)
	if err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func RemoveAll(dir string) error {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.Remove(dir)
}

// Open a file according to a specific mode
func Open(name string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// MustOpen maximize trying to open the file
func MustOpen(fileName, filePath string) (*os.File, error) {
	//dir, err := os.Getwd()
	//if err != nil {
	//	return nil, fmt.Errorf("os.Getwd err: %v", err)
	//}

	src := filePath
	perm := CheckPermission(src)
	if perm == true {
		return nil, fmt.Errorf("file.CheckPermission Permission denied src: %s", src)
	}

	err := IsNotExistMkDir(src)
	if err != nil {
		return nil, fmt.Errorf("file.IsNotExistMkDir src: %s, err: %v", src, err)
	}

	f, err := Open(src+fileName, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("Fail to OpenFile :%v", err)
	}

	return f, nil
}

// Exists checks whether the given path (file/folder) exists.
func Exists(path string) bool {
	_, err := os.Stat(path) // os.Stat gets the file info
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

// IsDir checks whether the given path is a folder.
func IsDir(path string) bool {
	s, err := os.Stat(path)
	if err != nil {
		return false
	}
	return s.IsDir()
}

// IsFile checks whether the given path is a file.
func IsFile(path string) bool {
	return !IsDir(path)
}

func CreateFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return nil
}

func CreateFileAndWriteContent(path string, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}

	defer file.Close()
	write := bufio.NewWriter(file)

	write.WriteString(content)

	write.Flush()
	return nil
}

// IsNotExistCreateFile create a file if it does not exist
func IsNotExistCreateFile(src string) error {
	if notExist := CheckNotExist(src); notExist {
		if err := CreateFile(src); err != nil {
			return err
		}
	}

	return nil
}

func ReadFullFile(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return []byte("")
	}
	defer file.Close()
	content, err := ioutil.ReadAll(file)
	if err != nil {
		return []byte("")
	}
	return content
}

// CopyFile copies a single file from src to dst.
// dst should be the full target path including the filename.
// Sparse files (e.g. Docker's volumes/backingFsBlockDev) are preserved using
// "cp --sparse=always" so that unallocated regions are not expanded into real
// disk blocks, which would cause the copy to appear far larger than the source.
func CopyFile(ctx context.Context, src, dst, style string) error {
	var srcinfo os.FileInfo
	var err error

	if Exists(dst) && style == "skip" {
		return nil
	}
	// NOTE: dst is intentionally never deleted here (even when it already
	// exists and style != "skip"). Conflict resolution is the caller's
	// responsibility (see service.FileOperate's Style handling); cp below
	// overwrites dst in place (preserving its inode/hardlinks) rather than
	// wiping it first.

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Use cp --sparse=always to handle sparse files correctly.
	// Fall back to pure-Go copy only if cp is unavailable. exec.CommandContext
	// means a cancelled ctx kills the cp subprocess instead of letting it run
	// to completion.
	cmd := exec.CommandContext(ctx, "cp", "--sparse=always", "--preserve=mode", src, dst)
	if cpErr := RunCopyCommand(cmd); cpErr == nil {
		return nil
	} else if ctx.Err() != nil {
		// The cp subprocess was killed by cancellation, not a genuine copy
		// failure — do not fall through to the manual copy below, which
		// would silently ignore the cancellation and finish the copy anyway.
		return ctx.Err()
	}

	// Fallback: pure-Go copy (dense, no sparse support).
	srcfd, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcfd.Close()

	dstfd, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstfd.Close()

	if _, err = io.Copy(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}
	return os.Chmod(dst, srcinfo.Mode())
}

/**
 * @description:
 * @param {*} src
 * @param {*} dst
 * @param {string} style
 * @return {*}
 * @method:
 * @router:
 */
func CopySingleFile(src, dst, style string) error {
	var err error
	var srcfd *os.File
	var dstfd *os.File
	var srcinfo os.FileInfo

	if Exists(dst) {
		if style == "skip" {
			return nil
		} else {
			os.Remove(dst)
		}
	}

	if srcfd, err = os.Open(src); err != nil {
		return err
	}
	defer srcfd.Close()

	if dstfd, err = os.Create(dst); err != nil {
		return err
	}
	defer dstfd.Close()

	if _, err = io.Copy(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}
	return os.Chmod(dst, srcinfo.Mode())
}

// Check for duplicate file names
func GetNoDuplicateFileName(fullPath string) string {
	path, fileName := filepath.Split(fullPath)
	fileSuffix := path2.Ext(fileName)
	filenameOnly := strings.TrimSuffix(fileName, fileSuffix)
	for i := 0; Exists(fullPath); i++ {
		fullPath = path2.Join(path, filenameOnly+"("+strconv.Itoa(i+1)+")"+fileSuffix)
	}
	return fullPath
}

// CopyDir copies a whole directory recursively.
// It will create a new directory inside dst with the same name as src.
func CopyDir(ctx context.Context, src string, dst string, style string) error {
	lastPath := filepath.Base(src)
	targetDir := filepath.Join(dst, lastPath)
	return CopyDirContents(ctx, src, targetDir, style)
}

// CopyDirContents copies the contents of src directory into dst directory.
// It does NOT append the basename of src to dst.
//
// ctx is checked before the interruptible cp subprocess (which is killed on
// cancellation via exec.CommandContext) and, in the manual recursive
// fallback, before every individual file/subdirectory entry — so a
// cancellation lands within one file's copy, not partway through an
// unbounded number of them.
func CopyDirContents(ctx context.Context, src string, dst string, style string) error {
	var err error
	var fds []os.FileInfo
	var srcinfo os.FileInfo

	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}

	if !srcinfo.IsDir() {
		return CopyFile(ctx, src, dst, style)
	}

	if Exists(dst) && style == "skip" {
		return nil
	}
	// NOTE: dst is intentionally never deleted here (even when it already
	// exists and style != "skip"). Conflict resolution is the caller's
	// responsibility (see service.FileOperate's Style handling); this
	// helper only merges into an existing dst — cp -a -T below, and the
	// manual fallback loop further down, both overwrite/merge in place
	// rather than wiping dst first.

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// NEW: Use native cp -a -T to handle symlinks, sparse files, and hardlinks (critical for docker overlay) efficiently.
	// Fall back to manual loop if cp fails. exec.CommandContext means a
	// cancelled ctx kills the cp subprocess instead of letting it run to
	// completion.
	cmd := exec.CommandContext(ctx, "cp", "-a", "--sparse=always", "-T", src, dst)
	if cpErr := RunCopyCommand(cmd); cpErr == nil {
		return nil
	} else if ctx.Err() != nil {
		// Killed by cancellation, not a genuine cp failure — propagate it
		// instead of silently finishing the copy via the manual fallback.
		return ctx.Err()
	}

	// Fallback to manual recursive directory copy
	if err = os.MkdirAll(dst, srcinfo.Mode()); err != nil {
		return err
	}

	if fds, err = ioutil.ReadDir(src); err != nil {
		return err
	}

	for _, fd := range fds {
		if err := ctx.Err(); err != nil {
			return err
		}

		srcfp := filepath.Join(src, fd.Name())
		dstfp := filepath.Join(dst, fd.Name())

		if fd.IsDir() {
			if err = CopyDirContents(ctx, srcfp, dstfp, style); err != nil {
				return err
			}
		} else {
			if err = CopyFile(ctx, srcfp, dstfp, style); err != nil {
				return err
			}
		}
	}
	return nil
}

func WriteToPath(data []byte, path, name string) error {
	fullPath := path
	if strings.HasSuffix(path, "/") {
		fullPath += name
	} else {
		fullPath += "/" + name
	}
	return WriteToFullPath(data, fullPath, 0o666)
}

func WriteToFullPath(data []byte, fullPath string, perm fs.FileMode) error {
	if err := IsNotExistCreateFile(fullPath); err != nil {
		return err
	}

	file, err := os.OpenFile(fullPath,
		os.O_WRONLY|os.O_TRUNC|os.O_CREATE,
		perm,
	)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)

	return err
}

// Final splice — uses streaming copy to avoid reading the entire set of chunks into memory
func SpliceFiles(dir, path string, length int, startPoint int) error {
	fullPath := path

	if err := IsNotExistCreateFile(fullPath); err != nil {
		return err
	}

	file, err := os.OpenFile(fullPath,
		os.O_WRONLY|os.O_TRUNC|os.O_CREATE,
		0o666,
	)
	if err != nil {
		return err
	}
	defer file.Close()

	buf := make([]byte, 256*1024) // 256KB copy buffer

	for i := 0; i < length+startPoint-1; i++ {
		chunkPath := dir + "/" + strconv.Itoa(i+startPoint)
		chunk, err := os.Open(chunkPath)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(file, chunk, buf); err != nil {
			chunk.Close()
			return err
		}
		chunk.Close()
	}

	return nil
}

func GetCompressionAlgorithm(t string) (string, archiver.Writer, error) {
	switch t {
	case "zip", "":
		return ".zip", archiver.NewZip(), nil
	case "tar":
		return ".tar", archiver.NewTar(), nil
	case "targz":
		return ".tar.gz", archiver.NewTarGz(), nil
	case "tarbz2":
		return ".tar.bz2", archiver.NewTarBz2(), nil
	case "tarxz":
		return ".tar.xz", archiver.NewTarXz(), nil
	case "tarlz4":
		return ".tar.lz4", archiver.NewTarLz4(), nil
	case "tarsz":
		return ".tar.sz", archiver.NewTarSz(), nil
	default:
		return "", nil, errors.New("format not implemented")
	}
}

func AddFile(ar archiver.Writer, path, commonPath string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if path != commonPath {
		//filename := info.Name()
		filename := strings.TrimPrefix(path, commonPath)
		filename = strings.TrimPrefix(filename, string(filepath.Separator))
		err = ar.Write(archiver.File{
			FileInfo: archiver.FileInfo{
				FileInfo:   info,
				CustomName: filename,
			},
			ReadCloser: file,
		})
		if err != nil {
			return err
		}
	}

	if info.IsDir() {
		names, err := file.Readdirnames(0)
		if err != nil {
			return err
		}

		for _, name := range names {
			err = AddFile(ar, filepath.Join(path, name), commonPath)
			if err != nil {
				log.Printf("Failed to archive %v", err)
			}
		}
	}

	return nil
}

func CommonPrefix(sep byte, paths ...string) string {
	// Handle special cases.
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return path.Clean(paths[0])
	}

	// Note, we treat string as []byte, not []rune as is often
	// done in Go. (And sep as byte, not rune). This is because
	// most/all supported OS' treat paths as string of non-zero
	// bytes. A filename may be displayed as a sequence of Unicode
	// runes (typically encoded as UTF-8) but paths are
	// not required to be valid UTF-8 or in any normalized form
	// (e.g. "é" (U+00C9) and "é" (U+0065,U+0301) are different
	// file names.
	c := []byte(path.Clean(paths[0]))

	// We add a trailing sep to handle the case where the
	// common prefix directory is included in the path list
	// (e.g. /home/user1, /home/user1/foo, /home/user1/bar).
	// path.Clean will have cleaned off trailing / separators with
	// the exception of the root directory, "/" (in which case we
	// make it "//", but this will get fixed up to "/" bellow).
	c = append(c, sep)

	// Ignore the first path since it's already in c
	for _, v := range paths[1:] {
		// Clean up each path before testing it
		v = path.Clean(v) + string(sep)

		// Find the first non-common byte and truncate c
		if len(v) < len(c) {
			c = c[:len(v)]
		}
		for i := 0; i < len(c); i++ {
			if v[i] != c[i] {
				c = c[:i]
				break
			}
		}
	}

	// Remove trailing non-separator characters and the final separator
	for i := len(c) - 1; i >= 0; i-- {
		if c[i] == sep {
			c = c[:i]
			break
		}
	}

	return string(c)
}

func GetFileOrDirSize(path string) (int64, error) {
	// Use Stat (instead of Lstat) to follow the top-level symlink to the actual data.
	// This ensures we report the size of the target, not the symlink node itself.
	fileInfo, err := os.Stat(path)
	if err != nil {
		return 0, err
	}

	if fileInfo.IsDir() {
		return DirSizeB(path + "/")
	}
	return fileInfo.Size(), nil
}

// getFileSize get file size by path(B)
func DirSizeB(path string) (int64, error) {
	rootInfo, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("cannot get device ID for %s", path)
	}
	rootDev := rootStat.Dev

	var size int64
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		// Skip directories on a different device (mount points)
		if info.IsDir() && stat.Dev != rootDev {
			return filepath.SkipDir
		}
		if !info.IsDir() && stat.Dev == rootDev {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func MoveFile(sourcePath, destPath string) error {
	inputFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("Couldn't open source file: %s", err)
	}
	outputFile, err := os.Create(destPath)
	if err != nil {
		inputFile.Close()
		return fmt.Errorf("Couldn't open dest file: %s", err)
	}
	defer outputFile.Close()
	_, err = io.Copy(outputFile, inputFile)
	inputFile.Close()
	if err != nil {
		return fmt.Errorf("Writing to output file failed: %s", err)
	}
	err = os.Remove(sourcePath)
	if err != nil {
		return fmt.Errorf("Failed removing original file: %s", err)
	}
	return nil
}

func ReadLine(lineNumber int, path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	fileScanner := bufio.NewScanner(file)
	lineCount := 1
	for fileScanner.Scan() {
		if lineCount == lineNumber {
			return fileScanner.Text()
		}
		lineCount++
	}
	defer file.Close()
	return ""
}

func NameAccumulation(name string, dir string) string {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return name
	}
	base := name
	strings.Split(base, "_")
	index := strings.LastIndex(base, "_")
	if index < 0 {
		index = len(base)
	}
	for i := 1; ; i++ {
		newPath := filepath.Join(dir, fmt.Sprintf("%s_%d", base[:index], i))
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			return fmt.Sprintf("%s_%d", base[:index], i)
		}
	}
}

func ParseFileHeader(h []byte, boundary []byte) (map[string]string, bool) {
	arr := bytes.Split(h, boundary)
	//var out_header FileHeader
	//out_header.ContentLength = -1
	const (
		CONTENT_DISPOSITION = "Content-Disposition: "
		NAME                = "name=\""
		FILENAME            = "filename=\""
		CONTENT_TYPE        = "Content-Type: "
		CONTENT_LENGTH      = "Content-Length: "
	)
	result := make(map[string]string)
	for _, item := range arr {

		tarr := bytes.Split(item, []byte(";"))
		if len(tarr) != 2 {
			continue
		}

		tbyte := tarr[1]
		fmt.Println(string(tbyte))
		tbyte = bytes.ReplaceAll(tbyte, []byte("\r\n--"), []byte(""))
		tbyte = bytes.ReplaceAll(tbyte, []byte("name=\""), []byte(""))
		tempArr := bytes.Split(tbyte, []byte("\"\r\n\r\n"))
		if len(tempArr) != 2 {
			continue
		}
		bytes.HasPrefix(item, []byte("name="))
		result[strings.TrimSpace(string(tempArr[0]))] = strings.TrimSpace(string(tempArr[1]))
	}
	// for _, item := range arr {
	// 	if bytes.HasPrefix(item, []byte(CONTENT_DISPOSITION)) {
	// 		l := len(CONTENT_DISPOSITION)
	// 		arr1 := bytes.Split(item[l:], []byte("; "))
	// 		out_header.ContentDisposition = string(arr1[0])
	// 		if bytes.HasPrefix(arr1[1], []byte(NAME)) {
	// 			out_header.Name = string(arr1[1][len(NAME) : len(arr1[1])-1])
	// 		}
	// 		l = len(arr1[2])
	// 		if bytes.HasPrefix(arr1[2], []byte(FILENAME)) && arr1[2][l-1] == 0x22 {
	// 			out_header.FileName = string(arr1[2][len(FILENAME) : l-1])
	// 		}
	// 	} else if bytes.HasPrefix(item, []byte(CONTENT_TYPE)) {
	// 		l := len(CONTENT_TYPE)
	// 		out_header.ContentType = string(item[l:])
	// 	} else if bytes.HasPrefix(item, []byte(CONTENT_LENGTH)) {
	// 		l := len(CONTENT_LENGTH)
	// 		s := string(item[l:])
	// 		content_length, err := strconv.ParseInt(s, 10, 64)
	// 		if err != nil {
	// 			log.Printf("content length error:%s", string(item))
	// 			return out_header, false
	// 		} else {
	// 			out_header.ContentLength = content_length
	// 		}
	// 	} else {
	// 		log.Printf("unknown:%s\n", string(item))
	// 	}
	// }
	//fmt.Println(result)
	// if len(out_header.FileName) == 0 {
	// 	return out_header, false
	// }
	return result, true
}

func ReadToBoundary(boundary []byte, stream io.ReadCloser, target io.WriteCloser) ([]byte, bool, error) {
	read_data := make([]byte, 1024*8)
	read_data_len := 0
	buf := make([]byte, 1024*4)
	b_len := len(boundary)
	reach_end := false
	for !reach_end {
		read_len, err := stream.Read(buf)
		if err != nil {
			if err != io.EOF && read_len <= 0 {
				return nil, true, err
			}
			reach_end = true
		}

		copy(read_data[read_data_len:], buf[:read_len])
		read_data_len += read_len
		if read_data_len < b_len+4 {
			continue
		}
		loc := bytes.Index(read_data[:read_data_len], boundary)
		if loc >= 0 {

			target.Write(read_data[:loc-4])
			return read_data[loc:read_data_len], reach_end, nil
		}
		target.Write(read_data[:read_data_len-b_len-4])
		copy(read_data[0:], read_data[read_data_len-b_len-4:])
		read_data_len = b_len + 4
	}
	target.Write(read_data[:read_data_len])
	return nil, reach_end, nil
}

func ParseFromHead(read_data []byte, read_total int, boundary []byte, stream io.ReadCloser) (map[string]string, []byte, error) {

	buf := make([]byte, 1024*8)
	found_boundary := false
	boundary_loc := -1

	for {
		read_len, err := stream.Read(buf)
		if err != nil {
			if err != io.EOF {
				return nil, nil, err
			}
			break
		}
		if read_total+read_len > cap(read_data) {
			return nil, nil, fmt.Errorf("not found boundary")
		}
		copy(read_data[read_total:], buf[:read_len])
		read_total += read_len
		if !found_boundary {
			boundary_loc = bytes.LastIndex(read_data[:read_total], boundary)
			if boundary_loc == -1 {
				continue
			}
			found_boundary = true
		}
		start_loc := boundary_loc + len(boundary)
		fmt.Println(string(read_data))
		file_head_loc := bytes.Index(read_data[start_loc:read_total], []byte("\r\n\r\n"))
		if file_head_loc == -1 {
			continue
		}
		file_head_loc += start_loc
		ret := false
		headMap, ret := ParseFileHeader(read_data, boundary)
		if !ret {
			return headMap, nil, fmt.Errorf("ParseFileHeader fail:%s", string(read_data[start_loc:file_head_loc]))
		}
		return headMap, read_data[file_head_loc+4 : read_total], nil
	}
	return nil, nil, fmt.Errorf("reach to sream EOF")
}
