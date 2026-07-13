package file

import (
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

	err := CopyDirContents(src, dst, "replace")
	require.NoError(t, err)

	kept, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
	require.NoError(t, err, "CopyDirContents must not delete pre-existing destination content")
	require.Equal(t, []byte("keep-me"), kept)

	newContent, err := os.ReadFile(filepath.Join(dst, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("new"), newContent)
}
