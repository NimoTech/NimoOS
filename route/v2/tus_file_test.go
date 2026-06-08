package v2

import (
	"testing"

	"github.com/tus/tusd/v2/pkg/handler"
)

func mockFree(avail uint64) freeBytesFn {
	return func() (uint64, error) { return avail, nil }
}

func hookWith(meta map[string]string, size int64) handler.HookEvent {
	return handler.HookEvent{Upload: handler.FileInfo{MetaData: meta, Size: size}}
}

func TestValidateFileUploadMetadata(t *testing.T) {
	big := uint64(100 * 1024 * 1024 * 1024) // 100 GB free

	cases := []struct {
		name    string
		meta    map[string]string
		size    int64
		free    uint64
		wantErr bool
		wantSC  int
	}{
		{"ok", map[string]string{"filename": "a.txt", "targetPath": "/DATA/Documents-x"}, 10, big, false, 0},
		{"empty filename", map[string]string{"filename": "", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"illegal filename slash", map[string]string{"filename": "a/b.txt", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"illegal filename dotdot", map[string]string{"filename": "..", "targetPath": "/DATA/x"}, 10, big, true, 0},
		{"protected folder", map[string]string{"filename": "a.txt", "targetPath": "/DATA/Documents"}, 10, big, true, 0},
		{"protected in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "Media/a.txt"}, 10, big, true, 0},
		{"traversal in relpath", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x", "relativePath": "../../etc/a.txt"}, 10, big, true, 0},
		{"empty file", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 0, big, true, 0},
		{"too big", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, FileUploadMaxSizeForTest() + 1, big, true, 0},
		{"insufficient space", map[string]string{"filename": "a.txt", "targetPath": "/DATA/x"}, 1000, 100, true, 413},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _, err := validateFileUploadMetadataWithQuota(hookWith(c.meta, c.size), mockFree(c.free))
			if c.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantSC != 0 && resp.StatusCode != c.wantSC {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.wantSC)
			}
		})
	}
}
