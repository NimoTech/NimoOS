package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestNameAccumulation(t *testing.T) {
	goleak.VerifyNone(t)

	fmt.Println("aaa")
	a := NameAccumulation("/mnt/test_1_1", "/")
	fmt.Println(a)
}

// TestCopyDirContents_DoesNotDeleteExistingDestination verifies R2: this
// helper must no longer hold independent delete authority over an existing
// dst (D1's second location). Conflict resolution belongs to the caller
// (service.FileOperate); this function should merge into an existing dst
// rather than wiping it.
func TestCopyDirContents_DoesNotDeleteExistingDestination(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.MkdirAll(dst, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(src, "new.txt"), []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep-me"), 0o644))

	err := CopyDirContents(context.Background(), src, dst, "replace")
	require.NoError(t, err)

	kept, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
	require.NoError(t, err, "CopyDirContents must not delete pre-existing destination content")
	require.Equal(t, []byte("keep-me"), kept)

	newContent, err := os.ReadFile(filepath.Join(dst, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("new"), newContent)
}

// TestCopyFile_DoesNotDeleteExistingDestination verifies CopyFile's sibling
// fix to CopyDirContents' D1 pattern: it must overwrite a pre-existing dst
// in place rather than os.Remove-ing it first. We assert this via a hardlink:
// os.Remove(dst) would detach dst from any existing hardlink (leaving the
// hardlink pointing at the old inode with stale content), whereas cp
// overwriting dst in place preserves the inode, so the hardlink observes the
// update too.
func TestCopyFile_DoesNotDeleteExistingDestination(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "dst.txt")
	linked := filepath.Join(root, "linked.txt")

	require.NoError(t, os.WriteFile(src, []byte("new-content"), 0o644))
	require.NoError(t, os.WriteFile(dst, []byte("old-content"), 0o644))
	require.NoError(t, os.Link(dst, linked))

	err := CopyFile(context.Background(), src, dst, "replace")
	require.NoError(t, err)

	dstContent, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, []byte("new-content"), dstContent)

	linkedContent, err := os.ReadFile(linked)
	require.NoError(t, err, "CopyFile must not delete pre-existing destination content")
	require.Equal(t, []byte("new-content"), linkedContent,
		"os.Remove(dst) would have broken the hardlink, leaving stale content behind")
}
