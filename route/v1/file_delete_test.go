package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NimoTech/NimoOS-Common/external"
	"github.com/NimoTech/NimoOS/codegen/message_bus"
	"github.com/NimoTech/NimoOS/service"
	model2 "github.com/NimoTech/NimoOS/service/model"
	"github.com/labstack/echo/v4"
)

// gbkTestDirBytes is the GBK encoding of "测试目录" ("test directory"), the
// exact repro bytes from the bug report: real-world GBK-named folders created
// by Samba clients/scripts produce invalid UTF-8 byte sequences on a Linux
// filesystem, which os.Mkdir happily accepts (the kernel doesn't validate
// UTF-8) but encoding/json mangles on the way out over the list API.
var gbkTestDirBytes = []byte{0xb2, 0xe2, 0xca, 0xd4, 0xc4, 0xbf, 0xc2, 0xbc}

// --- fakes to satisfy service.Repository without touching real hardware/DB ---
//
// DeleteFile's mounted-check loop calls service.IsMounted, which reaches into
// service.MyService.Connections().GetConnectionsList(). service.MyService is a
// nil package-level interface unless main.init() (or a test) assigns it, and
// calling a method on a nil interface panics. These fakes are the minimum
// needed to make that call return an empty list instead of panicking; every
// other Repository member is unused by DeleteFile and returns nil.

type fakeConnectionsService struct{}

func (fakeConnectionsService) GetConnectionsList() (connections []model2.ConnectionsDBModel) {
	return nil
}
func (fakeConnectionsService) GetConnectionByHost(host string) (connections []model2.ConnectionsDBModel) {
	return nil
}
func (fakeConnectionsService) GetConnectionByID(id string) (connections model2.ConnectionsDBModel) {
	return model2.ConnectionsDBModel{}
}
func (fakeConnectionsService) CreateConnection(connection *model2.ConnectionsDBModel) {}
func (fakeConnectionsService) DeleteConnection(id string)                             {}
func (fakeConnectionsService) UpdateConnection(connection *model2.ConnectionsDBModel) {}
func (fakeConnectionsService) MountSmaba(username, host, directory, port, mountPoint, password string) error {
	return nil
}
func (fakeConnectionsService) UnmountSmaba(mountPoint string) error { return nil }

type fakeRepository struct{}

func (fakeRepository) Casa() service.CasaService                    { return nil }
func (fakeRepository) Connections() service.ConnectionsService      { return fakeConnectionsService{} }
func (fakeRepository) Gateway() external.ManagementService          { return nil }
func (fakeRepository) Health() service.HealthService                { return nil }
func (fakeRepository) Notify() service.NotifyServer                 { return nil }
func (fakeRepository) Rely() service.RelyService                    { return nil }
func (fakeRepository) Shares() service.SharesService                { return nil }
func (fakeRepository) System() service.SystemService                { return nil }
func (fakeRepository) Storage() service.StorageService              { return nil }
func (fakeRepository) MessageBus() *message_bus.ClientWithResponses { return nil }
func (fakeRepository) Peer() service.PeerService                    { return nil }
func (fakeRepository) Other() service.OtherService                  { return nil }
func (fakeRepository) User() service.UserService                    { return nil }

func init() {
	// Handler-level DeleteFile tests below exercise the real mounted-check
	// loop; wire the fake so it doesn't panic on the nil global.
	service.MyService = fakeRepository{}
}

func TestJsonMangledName(t *testing.T) {
	t.Run("valid utf8 unchanged", func(t *testing.T) {
		valid := "normal-name_中文_😀"
		if got := jsonMangledName(valid); got != valid {
			t.Fatalf("jsonMangledName(%q) = %q, want unchanged", valid, got)
		}
	})

	t.Run("GBK bytes match json round-trip semantics", func(t *testing.T) {
		gbk := string(gbkTestDirBytes)

		// Ground truth: exactly what encoding/json does to this string when
		// it's marshaled into a response and unmarshaled back out.
		b, err := json.Marshal(gbk)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		var want string
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}

		if got := jsonMangledName(gbk); got != want {
			t.Fatalf("jsonMangledName(%q) = %q, want %q (json round-trip)", gbk, got, want)
		}
	})
}

func TestResolveDeletePath(t *testing.T) {
	t.Run("existing path returned as-is", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "exists")
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, err := resolveDeletePath(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != p {
			t.Fatalf("resolveDeletePath(%q) = %q, want unchanged", p, got)
		}
	})

	t.Run("mangled name uniquely rescued", func(t *testing.T) {
		dir := t.TempDir()
		gbkName := string(gbkTestDirBytes)
		realPath := filepath.Join(dir, gbkName)
		if err := os.Mkdir(realPath, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		requested := filepath.Join(dir, jsonMangledName(gbkName))

		got, err := resolveDeletePath(requested)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != realPath {
			t.Fatalf("resolveDeletePath(%q) = %q, want %q", requested, got, realPath)
		}
	})

	t.Run("nonexistent path returns ErrNotExist", func(t *testing.T) {
		dir := t.TempDir()
		requested := filepath.Join(dir, "does-not-exist-at-all")
		_, err := resolveDeletePath(requested)
		if !os.IsNotExist(err) {
			t.Fatalf("resolveDeletePath(%q) error = %v, want ErrNotExist", requested, err)
		}
	})

	t.Run("ambiguous mangled match returns error, never guesses", func(t *testing.T) {
		dir := t.TempDir()
		name1 := string([]byte{0xb2})
		name2 := string([]byte{0xe2})
		if err := os.Mkdir(filepath.Join(dir, name1), 0o755); err != nil {
			t.Fatalf("mkdir name1: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, name2), 0o755); err != nil {
			t.Fatalf("mkdir name2: %v", err)
		}
		mangled1 := jsonMangledName(name1)
		mangled2 := jsonMangledName(name2)
		if mangled1 != mangled2 {
			t.Fatalf("test setup invalid: expected both single invalid bytes to mangle identically, got %q vs %q", mangled1, mangled2)
		}

		requested := filepath.Join(dir, mangled1)
		_, err := resolveDeletePath(requested)
		if err == nil {
			t.Fatalf("expected ambiguity error, got nil")
		}
		if os.IsNotExist(err) {
			t.Fatalf("expected ambiguity error, got ErrNotExist: %v", err)
		}
	})
}

// TestDeleteFileHandlerNotFoundIsNotFakeSuccess drives DeleteFile end-to-end
// through httptest for a path that never existed. Before this fix, os.RemoveAll
// on a nonexistent path returns nil and the handler answered 200 with nothing
// deleted (the "fake success" bug). It must now answer with a non-200/failure
// result instead.
func TestDeleteFileHandlerNotFoundIsNotFakeSuccess(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "never-existed")

	body, err := json.Marshal([]string{missing})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/file/delete", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	ctx := e.NewContext(req, rec)

	if err := DeleteFile(ctx); err != nil {
		t.Fatalf("DeleteFile returned error: %v", err)
	}

	var result struct {
		Success int `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v (body=%s)", err, rec.Body.String())
	}
	if result.Success == http.StatusOK {
		t.Fatalf("expected non-200 success code for delete of nonexistent path, got %d (http status %d, body=%s)", result.Success, rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("path should still not exist, stat err=%v", err)
	}
}

// TestDeleteFileHandlerMangledNameRescue reproduces the full bug end-to-end:
// a GBK-named directory on disk, the client sends back the JSON-mangled name
// it saw from the list API, and the handler must resolve it to the real path
// and actually remove it (rather than silently no-op-ing to a fake 200).
func TestDeleteFileHandlerMangledNameRescue(t *testing.T) {
	dir := t.TempDir()
	gbkName := string(gbkTestDirBytes)
	realPath := filepath.Join(dir, gbkName)
	if err := os.Mkdir(realPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	requested := filepath.Join(dir, jsonMangledName(gbkName))

	body, err := json.Marshal([]string{requested})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/file/delete", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	ctx := e.NewContext(req, rec)

	if err := DeleteFile(ctx); err != nil {
		t.Fatalf("DeleteFile returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var result struct {
		Success int `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v (body=%s)", err, rec.Body.String())
	}
	if result.Success != http.StatusOK {
		t.Fatalf("expected success=200, got %d (body=%s)", result.Success, rec.Body.String())
	}
	if _, err := os.Stat(realPath); !os.IsNotExist(err) {
		t.Fatalf("expected real directory to be deleted, stat err=%v", err)
	}
}
