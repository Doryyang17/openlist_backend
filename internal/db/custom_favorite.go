package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func FavoritePathKey(path string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(path))) }

var favoriteSQLiteWrite = make(chan struct{}, 1)

// SQLite ignores FOR UPDATE. Serialize bookmark writes and account cleanup before
// opening their transactions so overlapping reads cannot fail to upgrade to writes.
func lockFavoriteWrite(ctx context.Context) (func(), error) {
	if db.Dialector.Name() != "sqlite" {
		return func() {}, nil
	}
	select {
	case favoriteSQLiteWrite <- struct{}{}:
		return func() { <-favoriteSQLiteWrite }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func AddFavorite(ctx context.Context, item *model.CustomFavorite) error {
	unlock, err := lockFavoriteWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	item.PathKey = FavoritePathKey(item.Path)
	item.SearchName = strings.ToLower(item.Name)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock in the same order as account deletion; do not create orphan bookmarks.
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, item.UserID).Error; err != nil {
			return err
		}
		if user.Disabled || user.IsGuest() {
			return fmt.Errorf("favorite owner unavailable")
		}
		if err := tx.Omit("User").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "path_key"}}, DoNothing: true}).Create(item).Error; err != nil {
			return err
		}
		var saved model.CustomFavorite
		if err := tx.Where("user_id = ? AND path_key = ?", item.UserID, item.PathKey).First(&saved).Error; err != nil {
			return err
		}
		if saved.Path != item.Path {
			return fmt.Errorf("favorite path identity conflict")
		}
		*item = saved
		return nil
	})
}

func ListFavorites(ctx context.Context, userID uint, page, perPage int, query, kind string) ([]model.CustomFavorite, int64, error) {
	q := db.WithContext(ctx).Model(&model.CustomFavorite{}).Where("user_id = ?", userID)
	if query != "" {
		query = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(query))
		q = q.Where("search_name LIKE ? ESCAPE '!'", "%"+query+"%")
	}
	if kind != "all" {
		q = q.Where("is_dir = ?", kind == "folder")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]model.CustomFavorite, 0)
	err := q.Order("created_at DESC").Order("id DESC").Offset((page - 1) * perPage).Limit(perPage).Find(&items).Error
	return items, total, err
}

func FavoriteStatuses(ctx context.Context, userID uint, paths []string) (map[string]uint, error) {
	result := make(map[string]uint)
	if len(paths) == 0 {
		return result, nil
	}
	keys := make([]string, 0, len(paths))
	for _, path := range paths {
		keys = append(keys, FavoritePathKey(path))
	}
	var items []model.CustomFavorite
	if err := db.WithContext(ctx).Where("user_id = ? AND path_key IN ?", userID, keys).Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		result[item.Path] = item.ID
	}
	return result, nil
}

func GetFavorite(ctx context.Context, userID, id uint) (*model.CustomFavorite, error) {
	var item model.CustomFavorite
	err := db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).First(&item).Error
	return &item, err
}

func DeleteFavorite(ctx context.Context, userID, id uint) error {
	unlock, err := lockFavoriteWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).Delete(&model.CustomFavorite{}).Error
}
