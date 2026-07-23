package service_test

import (
	"testing"

	"github.com/NimoTech/NimoOS/service"
	"github.com/NimoTech/NimoOS/service/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 用内存库构造一个只含 RootGrants 的最小 repo(参照 shares_test 的建库方式)。
func newRGRepo(t *testing.T) service.RootGrantRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.RootGrant{}); err != nil {
		t.Fatal(err)
	}
	return service.NewRootGrantRepo(db) // 构造函数,Task1 实现里导出
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
		t.Fatalf("enabled 过滤错误: %v", got)
	}
}

func TestRootGrant_SeedVirtualIdempotent(t *testing.T) {
	r := newRGRepo(t)
	if err := r.SeedVirtual("photos"); err != nil {
		t.Fatal(err)
	}
	if err := r.SeedVirtual("photos"); err != nil { // 幂等
		t.Fatal(err)
	}
	got, _ := r.EnabledRootIDs()
	if !idset(got)["photos"] {
		t.Fatalf("photos 未 seed: %v", got)
	}
}

func TestRootGrant_ReconcileWikiKeepsVirtual(t *testing.T) {
	r := newRGRepo(t)
	r.SeedVirtual("photos")
	r.UpsertGrant("old1", "/a", true, "wiki")
	r.UpsertGrant("old2", "/b", true, "wiki")
	// 全量对账:只剩 new1,old1/old2 应被删,photos 必须留
	err := r.ReconcileWiki([]model.RootGrant{{RootID: "new1", Path: "/c", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	set := idset(mustEnabled(t, r))
	if !set["new1"] || !set["photos"] || set["old1"] || set["old2"] {
		t.Fatalf("对账结果错误: %v", set)
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
