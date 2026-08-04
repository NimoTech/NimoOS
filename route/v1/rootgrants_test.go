package v1_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/NimoTech/NimoOS/route/v1"
	"github.com/NimoTech/NimoOS/service/model"
	"github.com/labstack/echo/v4"
)

type fakeRG struct{ ids []string }

func (f *fakeRG) EnabledRootIDs() ([]string, error)              { return f.ids, nil }
func (f *fakeRG) UpsertGrant(string, string, bool, string) error { return nil }
func (f *fakeRG) DeleteGrant(string) error                       { return nil }
func (f *fakeRG) SeedVirtual(string) error                       { return nil }
func (f *fakeRG) ReconcileWiki([]model.RootGrant) error          { return nil }

func TestSearchRoots_ReturnsEnabled(t *testing.T) {
	e := echo.New()
	h := v1.NewRootGrantHandler(&fakeRG{ids: []string{"aabb", "photos"}})
	req := httptest.NewRequest(http.MethodGet, "/v1/nimoos/search-roots?user_id=ignored", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.SearchRoots(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var body struct {
		RootIDs []string `json:"root_ids"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.RootIDs) != 2 {
		t.Fatalf("root_ids=%v", body.RootIDs)
	}
}

func TestReconcileGrants_CallsRepo(t *testing.T) {
	e := echo.New()
	spy := &reconcileSpy{}
	h := v1.NewRootGrantHandler(spy)
	body := `{"grants":[{"root_id":"r1","path":"/a","enabled":true}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/nimoos/_internal/root-grants/reconcile",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.ReconcileGrants(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(spy.got) != 1 || spy.got[0].RootID != "r1" {
		t.Fatalf("reconcile not delegated correctly: code=%d got=%v", rec.Code, spy.got)
	}
	// Also assert Path/Enabled are delegated field-by-field correctly, not just RootID.
	if spy.got[0].Path != "/a" || !spy.got[0].Enabled {
		t.Fatalf("reconcile did not delegate path/enabled correctly: %+v", spy.got[0])
	}
}

// TestReconcileGrants_PreservesEnabledFalse specifically covers the enabled:false
// case, guarding against some future layer (bind/conversion) confusing the bool
// zero value with "not passed" and dropping false into true.
func TestReconcileGrants_PreservesEnabledFalse(t *testing.T) {
	e := echo.New()
	spy := &reconcileSpy{}
	h := v1.NewRootGrantHandler(spy)
	body := `{"grants":[{"root_id":"r1","path":"/a","enabled":true},{"root_id":"r2","path":"/b","enabled":false}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/nimoos/_internal/root-grants/reconcile",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.ReconcileGrants(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(spy.got) != 2 {
		t.Fatalf("reconcile not delegated correctly: code=%d got=%v", rec.Code, spy.got)
	}
	if spy.got[0].RootID != "r1" || spy.got[0].Path != "/a" || !spy.got[0].Enabled {
		t.Fatalf("first entry (enabled:true) delegated incorrectly: %+v", spy.got[0])
	}
	if spy.got[1].RootID != "r2" || spy.got[1].Path != "/b" || spy.got[1].Enabled {
		t.Fatalf("second entry (enabled:false) delegated incorrectly, bool was dropped/coerced to true: %+v", spy.got[1])
	}
}

// reconcileSpy implements service.RootGrantRepo, recording only the ReconcileWiki args.
type reconcileSpy struct{ got []model.RootGrant }

func (s *reconcileSpy) EnabledRootIDs() ([]string, error)              { return nil, nil }
func (s *reconcileSpy) UpsertGrant(string, string, bool, string) error { return nil }
func (s *reconcileSpy) DeleteGrant(string) error                       { return nil }
func (s *reconcileSpy) SeedVirtual(string) error                       { return nil }
func (s *reconcileSpy) ReconcileWiki(g []model.RootGrant) error        { s.got = g; return nil }

// upsertDeleteSpy records the args of UpsertGrant / DeleteGrant, reused by the two write-endpoint tests below.
type upsertDeleteSpy struct {
	upsertRootID  string
	upsertPath    string
	upsertEnabled bool
	upsertSource  string
	deleteRootID  string
}

func (s *upsertDeleteSpy) EnabledRootIDs() ([]string, error) { return nil, nil }
func (s *upsertDeleteSpy) UpsertGrant(rootID, path string, enabled bool, source string) error {
	s.upsertRootID, s.upsertPath, s.upsertEnabled, s.upsertSource = rootID, path, enabled, source
	return nil
}
func (s *upsertDeleteSpy) DeleteGrant(rootID string) error {
	s.deleteRootID = rootID
	return nil
}
func (s *upsertDeleteSpy) SeedVirtual(string) error                { return nil }
func (s *upsertDeleteSpy) ReconcileWiki(g []model.RootGrant) error { return nil }

func TestUpsertGrant_CallsRepoWithWikiSource(t *testing.T) {
	e := echo.New()
	spy := &upsertDeleteSpy{}
	h := v1.NewRootGrantHandler(spy)
	body := `{"path":"/DATA/docs","enabled":true}`
	req := httptest.NewRequest(http.MethodPut, "/v1/nimoos/_internal/root-grants/aabbcc",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("root_id")
	c.SetParamValues("aabbcc")
	if err := h.UpsertGrant(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if spy.upsertRootID != "aabbcc" || spy.upsertPath != "/DATA/docs" || !spy.upsertEnabled || spy.upsertSource != "wiki" {
		t.Fatalf("upsert not delegated correctly: %+v", spy)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestDeleteGrant_CallsRepo(t *testing.T) {
	e := echo.New()
	spy := &upsertDeleteSpy{}
	h := v1.NewRootGrantHandler(spy)
	req := httptest.NewRequest(http.MethodDelete, "/v1/nimoos/_internal/root-grants/aabbcc", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("root_id")
	c.SetParamValues("aabbcc")
	if err := h.DeleteGrant(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || spy.deleteRootID != "aabbcc" {
		t.Fatalf("delete not delegated correctly: code=%d rootID=%s", rec.Code, spy.deleteRootID)
	}
}
