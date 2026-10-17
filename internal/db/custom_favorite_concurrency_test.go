package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func favoriteConcurrencyDB(t *testing.T, mode string) *gorm.DB {
	t.Helper()
	d, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "favorites.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := db
	db = d
	t.Cleanup(func() { sql, _ := d.DB(); _ = sql.Close(); db = previous })
	if err := d.Exec("PRAGMA journal_mode=" + mode).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
		t.Fatal(err)
	}
	return d
}

// Hold the first user read so a second request arrives before the first writes.
func holdFavoriteUserRead(t *testing.T, d *gorm.DB) (<-chan struct{}, func()) {
	t.Helper()
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	if err := d.Callback().Query().After("gorm:query").Register("test:favorite-overlap", func(tx *gorm.DB) {
		if tx.Statement.Table != "users" {
			return
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		case <-time.After(5 * time.Second):
			tx.AddError(context.DeadlineExceeded)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resume(); _ = d.Callback().Query().Remove("test:favorite-overlap") })
	return arrived, resume
}

func waitFavoriteResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("favorite write did not finish")
		return nil
	}
}

func TestFavoritesConcurrentSQLiteWrites(t *testing.T) {
	for _, mode := range []string{"DELETE", "WAL"} {
		for _, operation := range []string{"new_duplicate", "duplicate", "different_path", "delete_favorite", "delete_user"} {
			t.Run(mode+"/"+operation, func(t *testing.T) {
				d := favoriteConcurrencyDB(t, mode)
				u := model.User{Username: "concurrent", BasePath: "/"}
				if err := d.Create(&u).Error; err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				seed := model.CustomFavorite{UserID: u.ID, Path: "/first.txt", Name: "first.txt"}
				if operation != "new_duplicate" {
					if err := AddFavorite(ctx, &seed); err != nil {
						t.Fatal(err)
					}
				}
				arrived, resume := holdFavoriteUserRead(t, d)
				first, second := make(chan error, 1), make(chan error, 1)
				a := model.CustomFavorite{UserID: u.ID, Path: seed.Path, Name: "ignored.txt"}
				b := model.CustomFavorite{UserID: u.ID, Path: seed.Path, Name: "also-ignored.txt"}
				go func() { first <- AddFavorite(ctx, &a) }()
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					t.Fatal("first request did not reach user read")
				}
				go func() {
					switch operation {
					case "new_duplicate", "duplicate":
						second <- AddFavorite(ctx, &b)
					case "different_path":
						b.Path, b.Name = "/second.txt", "second.txt"
						second <- AddFavorite(ctx, &b)
					case "delete_favorite":
						second <- DeleteFavorite(ctx, u.ID, seed.ID)
					case "delete_user":
						second <- DeleteUserById(u.ID)
					}
				}()
				// The unfixed code admits a second reader; serialized writes wait here.
				select {
				case <-arrived:
				case <-time.After(50 * time.Millisecond):
				}
				resume()
				if err := waitFavoriteResult(t, first); err != nil {
					t.Errorf("first request: %v", err)
				}
				if err := waitFavoriteResult(t, second); err != nil {
					t.Errorf("second request: %v", err)
				}
				want := int64(1)
				switch operation {
				case "new_duplicate":
					if a.ID == 0 || b.ID != a.ID || b.Name != a.Name || !b.CreatedAt.Equal(a.CreatedAt) {
						t.Error("concurrent first adds did not return the same bookmark")
					}
				case "duplicate":
					for _, item := range []model.CustomFavorite{a, b} {
						if item.ID != seed.ID || item.Name != seed.Name || !item.CreatedAt.Equal(seed.CreatedAt) {
							t.Error("concurrent duplicate changed bookmark identity or metadata")
						}
					}
				case "different_path":
					want = 2
				case "delete_favorite", "delete_user":
					want = 0
				}
				_, total, err := ListFavorites(ctx, u.ID, 1, 50, "", "all")
				if err != nil || total != want {
					t.Fatalf("total=%d, want=%d, err=%v", total, want, err)
				}
			})
		}
	}
}

func TestFavoritesQueuedWriteCancellation(t *testing.T) {
	d := favoriteConcurrencyDB(t, "WAL")
	u := model.User{Username: "cancellation", BasePath: "/"}
	if err := d.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	arrived, resume := holdFavoriteUserRead(t, d)
	first := make(chan error, 1)
	a := model.CustomFavorite{UserID: u.ID, Path: "/first.txt", Name: "first.txt"}
	go func() { first <- AddFavorite(context.Background(), &a) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach user read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	b := model.CustomFavorite{UserID: u.ID, Path: "/canceled.txt", Name: "canceled.txt"}
	if err := AddFavorite(ctx, &b); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("queued request should observe its deadline, got %v", err)
	}
	resume()
	if err := waitFavoriteResult(t, first); err != nil {
		t.Fatal(err)
	}
	items, total, err := ListFavorites(context.Background(), u.ID, 1, 50, "", "all")
	if err != nil || total != 1 || items[0].ID != a.ID {
		t.Fatalf("canceled write changed bookmarks: total=%d err=%v", total, err)
	}
	if err := DeleteFavorite(context.Background(), u.ID, a.ID); err != nil {
		t.Fatalf("canceled request blocked later writes: %v", err)
	}
}
