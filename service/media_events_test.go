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
	// directories pass through (subscribers expand them recursively), media
	// extensions pass through (case-insensitive), non-media files and
	// vanished paths are dropped
	require.Equal(t, []string{sub, jpg}, got)
}
