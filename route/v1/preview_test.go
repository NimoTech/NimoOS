package v1

import (
	"strings"
	"testing"
)

func TestIsConvertibleOffice(t *testing.T) {
	for _, ext := range []string{".doc", ".DOC", ".wps", ".xls", ".ppt", ".pptx"} {
		if !isConvertibleOffice(ext) {
			t.Errorf("expected %s convertible", ext)
		}
	}
	for _, ext := range []string{".docx", ".xlsx", ".csv", ".pdf", ".txt", ""} {
		if isConvertibleOffice(ext) {
			t.Errorf("expected %s NOT convertible", ext)
		}
	}
}

func TestSofficeArgs(t *testing.T) {
	args := sofficeArgs("/tmp/out/lo-profile", "/tmp/out", "/DATA/a.doc")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--headless", "--convert-to pdf", "--outdir /tmp/out", "/DATA/a.doc", "-env:UserInstallation=file:///tmp/out/lo-profile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q; got %q", want, joined)
		}
	}
}
