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

// TestRootGrant_EnabledRootIDs_EmptyTableReturnsEmptySlice 硬化契约:空表要返回
// 初始化过的空切片([]string{}),而不是 nil,调用方(如 SearchRoots handler)
// 序列化后才能得到 JSON [] 而不是 null。
//
// 注意:这里不能复用 newRGRepo——它固定用 "file::memory:?cache=shared" 这个
// 匿名共享内存库,本文件里所有调用 newRGRepo 的测试实际共享同一张物理表,
// 表里会残留其它测试写入的行,不是真正的空表。这里用一个带唯一名字的内存库,
// 独立于测试执行顺序,保证测出的确实是「刚建表、一行都没有」的状态。
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
		t.Fatal("EnabledRootIDs 空表时不应返回 nil")
	}
	if len(got) != 0 {
		t.Fatalf("空表应返回空切片,got %v", got)
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
