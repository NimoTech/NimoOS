package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPhotosConfDataPath(t *testing.T) {
	writeConf := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "photos.conf")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test conf: %v", err)
		}
		return path
	}

	t.Run("lowercase key (viper round-trip)", func(t *testing.T) {
		path := writeConf(t, "datapath=/x\n")
		v, ok := photosConfDataPath(path)
		if !ok || v != "/x" {
			t.Fatalf("got (%q, %v), want (\"/x\", true)", v, ok)
		}
	})

	t.Run("camelCase key with spaces", func(t *testing.T) {
		path := writeConf(t, "DataPath = /x\n")
		v, ok := photosConfDataPath(path)
		if !ok || v != "/x" {
			t.Fatalf("got (%q, %v), want (\"/x\", true)", v, ok)
		}
	})

	t.Run("commented line is skipped", func(t *testing.T) {
		path := writeConf(t, "# DataPath = /y\n")
		_, ok := photosConfDataPath(path)
		if ok {
			t.Fatalf("expected commented line to be ignored, got ok=true")
		}
	})

	t.Run("no matching key", func(t *testing.T) {
		path := writeConf(t, "other=1\n")
		_, ok := photosConfDataPath(path)
		if ok {
			t.Fatalf("expected no match, got ok=true")
		}
	})

	t.Run("file does not exist", func(t *testing.T) {
		_, ok := photosConfDataPath("/nonexistent/path/photos.conf")
		if ok {
			t.Fatalf("expected ok=false for missing file, got ok=true")
		}
	})
}
