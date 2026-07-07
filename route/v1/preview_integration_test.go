//go:build integration

package v1

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 真 LibreOffice 端到端:先用 soffice 自己造一个 .doc,再 convertOfficeToPDF 转回 PDF。
func TestConvertOfficeToPDF_RealSoffice(t *testing.T) {
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not installed")
	}
	dir := t.TempDir()
	txt := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(txt, []byte("hello preview test"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 造 .doc
	if err := exec.Command("soffice", "--headless", "--convert-to", "doc", "--outdir", dir, txt).Run(); err != nil {
		t.Fatalf("build .doc: %v", err)
	}
	doc := filepath.Join(dir, "src.doc")
	data, err := convertOfficeToPDF(doc)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(data) < 4 || string(data[:4]) != "%PDF" {
		t.Fatalf("expected PDF magic, got %d bytes prefix %q", len(data), string(data[:min(4, len(data))]))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
