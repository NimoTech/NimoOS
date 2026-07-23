package v1_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
