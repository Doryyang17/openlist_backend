package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestWatchHistoryIsolationUpdatesAndDeletion(t *testing.T) {
	d := favoriteConcurrencyDB(t, "WAL")
	ctx := context.Background()
	a, b := model.User{Username: "history-a"}, model.User{Username: "history-b"}
	if err := d.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	first := model.CustomWatchHistory{UserID: a.ID, Path: "/video/中文 #?.mp4", Name: "中文 #?.mp4", PositionSeconds: 30, DurationSeconds: 100}
	if err := SaveWatchHistory(ctx, &first, 0); err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.LastWatchedAt.IsZero() {
		t.Fatal("missing revision/time")
	}
	other := first
	other.ID, other.UserID = 0, b.ID
	if err := SaveWatchHistory(ctx, &other, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := GetWatchHistory(ctx, b.ID, first.ID); err == nil {
		t.Fatal("cross-account read")
	}
	stale := first
	first.PositionSeconds = 5 // An intentional rewind must be saved.
	if err := SaveWatchHistory(ctx, &first, first.Revision); err != nil {
		t.Fatal(err)
	}
	stale.PositionSeconds = 90
	if err := SaveWatchHistory(ctx, &stale, stale.Revision); !errors.Is(err, ErrWatchHistoryConflict) {
		t.Fatalf("stale update: %v", err)
	}
	duplicate := model.CustomWatchHistory{UserID: a.ID, Path: first.Path}
	if err := SaveWatchHistory(ctx, &duplicate, 0); !errors.Is(err, ErrWatchHistoryConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	saved, err := GetWatchHistoryByPath(ctx, a.ID, first.Path)
	if err != nil || saved.PositionSeconds != 5 || saved.Revision != 2 {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	rows, total, err := ListWatchHistory(ctx, a.ID, 1, 20)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("dedupe: %d %v", total, err)
	}
	if err := DeleteUserById(a.ID); err != nil {
		t.Fatal(err)
	}
	_, total, err = ListWatchHistory(ctx, a.ID, 1, 20)
	if err != nil || total != 0 {
		t.Fatal("account history not removed")
	}
	if _, err := GetWatchHistory(ctx, b.ID, other.ID); err != nil {
		t.Fatal("other account modified")
	}
	if err := SaveWatchHistory(ctx, &first, first.Revision); err == nil {
		t.Fatal("deleted owner accepted")
	}
	b.Disabled = true
	if err := d.Save(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := SaveWatchHistory(ctx, &other, other.Revision); err == nil {
		t.Fatal("disabled owner accepted")
	}
}

func TestWatchHistoryPaginationAndPathIdentity(t *testing.T) {
	d := favoriteConcurrencyDB(t, "DELETE")
	user := model.User{Username: "history-pages"}
	if err := d.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 43; i++ {
		item := model.CustomWatchHistory{UserID: user.ID, Path: fmt.Sprintf("/folder-%d/same.mp4", i), Name: "same.mp4", PositionSeconds: 10, DurationSeconds: 100}
		if err := SaveWatchHistory(ctx, &item, 0); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uint]bool{}
	for page, want := range []int{20, 20, 3} {
		rows, total, err := ListWatchHistory(ctx, user.ID, page+1, 20)
		if err != nil || total != 43 || len(rows) != want {
			t.Fatalf("page %d: %d/%d %v", page, len(rows), total, err)
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatal("duplicate page row")
			}
			seen[row.ID] = true
		}
	}
}
