package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestUploadTaskTableNameAndMigrate(t *testing.T) {
	if (UploadTaskDBModel{}).TableName() != "o_upload_tasks" {
		t.Fatalf("unexpected table name: %s", (UploadTaskDBModel{}).TableName())
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&UploadTaskDBModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row := UploadTaskDBModel{ID: "abc", OwnerUserID: "7", Status: UploadStatusUploading, Size: 10}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got UploadTaskDBModel
	if err := db.Where("id = ?", "abc").First(&got).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.OwnerUserID != "7" || got.Status != UploadStatusUploading {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}
