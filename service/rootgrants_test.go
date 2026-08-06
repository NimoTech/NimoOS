package service_test

import (
	"testing"

	"github.com/NimoTech/NimoOS/service"
	"github.com/NimoTech/NimoOS/service/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Builds a minimal repo backed by an in-memory DB containing only RootGrants
// (following the same setup pattern as shares_test).
func newRGRepo(t *testing.T) service.RootGrantRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RootGrant{}); err != nil {
		t.Fatal(err)
	}
	return service.NewRootGrantRepo(db) // constructor, exported in the Task1 implementation
}

func idset(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestRootGrant_UpsertAndEnabledList(t *testing.T) {
	r := newRGRepo(t)
	if err := r.UpsertGrant("aabbcc", "/DATA/docs", true, "wiki"); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertGrant("ddeeff", "/DATA/private", false, "wiki"); err != nil {
		t.Fatal(err)
	}
	got, err := r.EnabledRootIDs()
	if err != nil {
		t.Fatal(err)
	}
	set := idset(got)
	if !set["aabbcc"] || set["ddeeff"] {
		t.Fatalf("enabled filtering error: %v", got)
	}
}

func TestRootGrant_SeedVirtualIdempotent(t *testing.T) {
	r := newRGRepo(t)
	if err := r.SeedVirtual("photos"); err != nil {
		t.Fatal(err)
	}
	if err := r.SeedVirtual("photos"); err != nil { // idempotent
		t.Fatal(err)
	}
	got, _ := r.EnabledRootIDs()
	if !idset(got)["photos"] {
		t.Fatalf("photos was not seeded: %v", got)
	}
}

func TestRootGrant_ReconcileWikiKeepsVirtual(t *testing.T) {
	r := newRGRepo(t)
	r.SeedVirtual("photos")
	r.UpsertGrant("old1", "/a", true, "wiki")
	r.UpsertGrant("old2", "/b", true, "wiki")
	// Full reconciliation: only new1 remains, old1/old2 should be deleted, photos must stay
	err := r.ReconcileWiki([]model.RootGrant{{RootID: "new1", Path: "/c", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	set := idset(mustEnabled(t, r))
	if !set["new1"] || !set["photos"] || set["old1"] || set["old2"] {
		t.Fatalf("reconciliation result error: %v", set)
	}
}

// TestRootGrant_EnabledRootIDs_EmptyTableReturnsEmptySlice hardens the
// contract: an empty table must return an initialized empty slice
// ([]string{}), not nil, so that callers (e.g. the SearchRoots handler)
// serialize it as JSON [] rather than null.
//
// Note: this can't reuse newRGRepo — it's pinned to the
// "file::memory:?cache=shared" anonymous shared in-memory DB, so every test
// in this file that calls newRGRepo actually shares the same physical
// table, which retains rows written by other tests; it's not truly empty.
// This test uses a uniquely named in-memory DB instead, independent of test
// execution order, to guarantee what's being tested is genuinely the
// "table just created, zero rows" state.
func TestRootGrant_EnabledRootIDs_EmptyTableReturnsEmptySlice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:rootgrant_empty_test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RootGrant{}); err != nil {
		t.Fatal(err)
	}
	r := service.NewRootGrantRepo(db)

	got, err := r.EnabledRootIDs()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("EnabledRootIDs should not return nil for an empty table")
	}
	if len(got) != 0 {
		t.Fatalf("empty table should return an empty slice, got %v", got)
	}
}

func mustEnabled(t *testing.T, r service.RootGrantRepo) []string {
	t.Helper()
	ids, err := r.EnabledRootIDs()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}
