package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestFavoritesIsolationPersistenceAndLifecycle(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "favorites.db")
	open := func() *gorm.DB {
		d, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NamingStrategy: schema.NamingStrategy{TablePrefix: "test_"}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	previous := db
	db = open()
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close(); db = previous })
	if err := db.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
		t.Fatal(err)
	}
	u := model.User{Username: "one", BasePath: "/"}
	v := model.User{Username: "two", BasePath: "/"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add := func(owner uint, p, name string, folder bool) model.CustomFavorite {
		f := model.CustomFavorite{UserID: owner, Path: p, Name: name, IsDir: folder}
		if err := AddFavorite(ctx, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	first := add(u.ID, "/data/100%_笔记.txt", "100%_笔记.txt", false)
	duplicate := add(u.ID, first.Path, "changed name", false)
	if duplicate.ID != first.ID || !duplicate.CreatedAt.Equal(first.CreatedAt) || duplicate.Name != first.Name {
		t.Fatal("duplicate changed bookmark")
	}
	other := add(v.ID, first.Path, first.Name, false)
	add(u.ID, "/data/folder", "Folder", true)
	add(u.ID, "/data/other", "100AA笔记.txt", false)
	items, total, err := ListFavorites(ctx, u.ID, 1, 50, "%_", "all")
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("literal search failed: %d %v", total, err)
	}
	_, total, err = ListFavorites(ctx, u.ID, 1, 50, "folder", "folder")
	if err != nil || total != 1 {
		t.Fatal("type/case filter")
	}
	if _, err = GetFavorite(ctx, v.ID, first.ID); err == nil {
		t.Fatal("cross-account read")
	}
	if err = DeleteFavorite(ctx, v.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	states, err := FavoriteStatuses(ctx, u.ID, []string{first.Path, "/missing"})
	if err != nil || states[first.Path] != first.ID || states["/missing"] != 0 {
		t.Fatal("status isolation")
	}
	if err = DeleteFavorite(ctx, u.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	readded := add(u.ID, first.Path, first.Name, false)
	if readded.ID == first.ID {
		t.Fatal("deleted id reused")
	}
	if err = DeleteFavorite(ctx, u.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = GetFavorite(ctx, u.ID, readded.ID); err != nil {
		t.Fatal("late deletion removed new record")
	}
	sql, _ := db.DB()
	_ = sql.Close()
	db = open()
	if _, err = GetFavorite(ctx, u.ID, readded.ID); err != nil {
		t.Fatal("not persistent")
	}
	if err = DeleteUserById(u.ID); err != nil {
		t.Fatal(err)
	}
	_, total, err = ListFavorites(ctx, u.ID, 1, 50, "", "all")
	if err != nil || total != 0 {
		t.Fatal("account cleanup")
	}
	if _, err = GetFavorite(ctx, v.ID, other.ID); err != nil {
		t.Fatal("other account changed")
	}
	missing := model.CustomFavorite{UserID: u.ID, Path: "/orphan", Name: "orphan"}
	if AddFavorite(ctx, &missing) == nil {
		t.Fatal("orphan accepted")
	}
}

func TestFavoritesPaginationAtScale(t *testing.T) {
	d, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scale.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := db
	db = d
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close(); db = previous })
	if err := db.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, count := range []int{1, 50, 51, 500, 5000} {
		u := model.User{Username: fmt.Sprintf("scale-%d", count), BasePath: "/"}
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		rows := make([]model.CustomFavorite, count)
		now := time.Now()
		for i := range rows {
			p := fmt.Sprintf("/data/item-%05d.txt", i)
			rows[i] = model.CustomFavorite{UserID: u.ID, Path: p, PathKey: FavoritePathKey(p), Name: p, SearchName: p, CreatedAt: now}
		}
		if err := db.Omit("User").CreateInBatches(rows, 100).Error; err != nil {
			t.Fatal(err)
		}
		seen := make(map[uint]bool)
		var previousID uint
		started := time.Now()
		for page := 1; page <= (count+49)/50; page++ {
			items, total, err := ListFavorites(ctx, u.ID, page, 50, "item-", "file")
			if err != nil || total != int64(count) || len(items) == 0 || len(items) > 50 {
				t.Fatalf("pagination count=%d page=%d total=%d err=%v", count, page, total, err)
			}
			for _, item := range items {
				if seen[item.ID] || (previousID != 0 && item.ID >= previousID) {
					t.Fatal("unstable ordering or duplicate")
				}
				seen[item.ID], previousID = true, item.ID
			}
		}
		if len(seen) != count {
			t.Fatalf("lost records: %d != %d", len(seen), count)
		}
		t.Logf("%d rows: read every 50-row page in %s (SQLite DB-level check, not HTTP latency)", count, time.Since(started))
	}
}
