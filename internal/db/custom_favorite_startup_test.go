package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// Exercise the real startup migration: model-only tests cannot detect a favorite
// table accidentally dropped from Init while merging upstream migrations.
func TestFavoritesStartupMigration(t *testing.T) {
	previousConfig := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = previousConfig })
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "upgrade"
		}
		t.Run(name, func(t *testing.T) {
			d, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "startup.db")), &gorm.Config{
				Logger:         logger.Default.LogMode(logger.Silent),
				NamingStrategy: schema.NamingStrategy{TablePrefix: "test_"},
			})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := d.DB()
			if err != nil {
				t.Fatal(err)
			}
			previous := db
			db = d
			t.Cleanup(func() { _ = sqlDB.Close(); db = previous })
			if legacy {
				if err := d.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
					t.Fatal(err)
				}
			} else {
				Init(d)
			}
			user := model.User{Username: "reader", BasePath: "/"}
			if err := d.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			item := model.CustomFavorite{UserID: user.ID, Path: "/saved.txt", Name: "saved.txt"}
			if err := AddFavorite(context.Background(), &item); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				Init(d)
			}
			for _, table := range []any{&model.CustomFavorite{}, &model.WebDAVProperty{}, &model.MediaItem{}} {
				if !d.Migrator().HasTable(table) {
					t.Fatalf("startup did not create table for %T", table)
				}
			}
			saved, err := GetFavorite(context.Background(), user.ID, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Path != item.Path || saved.Name != item.Name || !saved.CreatedAt.Equal(item.CreatedAt) {
				t.Fatal("startup migration changed the saved favorite")
			}
			duplicate := model.CustomFavorite{UserID: user.ID, Path: item.Path, Name: "changed.txt"}
			if err := AddFavorite(context.Background(), &duplicate); err != nil || duplicate.ID != item.ID {
				t.Fatalf("favorite uniqueness lost after migration: id=%d, err=%v", duplicate.ID, err)
			}
		})
	}
}
