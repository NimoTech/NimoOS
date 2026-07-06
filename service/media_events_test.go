package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterMediaCreated(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "album")
	require.NoError(t, os.Mkdir(sub, 0o755))
	jpg := filepath.Join(dir, "a.JPG")
	txt := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(jpg, []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(txt, []byte("x"), 0o644))

	got := filterMediaCreated([]string{sub, jpg, txt, filepath.Join(dir, "gone.png")})
	// 目录放行(订阅方递归展开)、媒体扩展名放行(大小写不敏感)、
	// 非媒体文件与已消失路径丢弃
	require.Equal(t, []string{sub, jpg}, got)
}
