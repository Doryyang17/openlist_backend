package db

import (
	"context"
	"errors"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrWatchHistoryConflict = errors.New("watch history revision conflict")

func GetWatchHistory(ctx context.Context, userID, id uint) (*model.CustomWatchHistory, error) {
	var item model.CustomWatchHistory
	err := db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).First(&item).Error
	return &item, err
}

func GetWatchHistoryByPath(ctx context.Context, userID uint, path string) (*model.CustomWatchHistory, error) {
	var item model.CustomWatchHistory
	err := db.WithContext(ctx).Where("user_id = ? AND path_key = ? AND path = ?", userID, FavoritePathKey(path), path).First(&item).Error
	return &item, err
}

func ListWatchHistory(ctx context.Context, userID uint, page, perPage int) ([]model.CustomWatchHistory, int64, error) {
	query := db.WithContext(ctx).Model(&model.CustomWatchHistory{}).Where("user_id = ?", userID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]model.CustomWatchHistory, 0)
	err := query.Order("last_watched_at DESC").Order("id DESC").Offset((page - 1) * perPage).Limit(perPage).Find(&items).Error
	return items, total, err
}

// SaveWatchHistory shares the account-deletion lock with bookmarks. The expected
// revision is mandatory, including zero for a new path, so a delayed tab cannot
// overwrite progress accepted from another player.
func SaveWatchHistory(ctx context.Context, item *model.CustomWatchHistory, expected uint64) error {
	unlock, err := lockFavoriteWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, item.UserID).Error; err != nil {
			return err
		}
		if user.Disabled || user.IsGuest() {
			return errors.New("watch history owner unavailable")
		}
		item.PathKey = FavoritePathKey(item.Path)
		var previous model.CustomWatchHistory
		err := tx.Where("user_id = ? AND path_key = ?", item.UserID, item.PathKey).First(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expected != 0 {
				return ErrWatchHistoryConflict
			}
			item.Revision = 1
			item.LastWatchedAt = time.Now().UTC()
			return tx.Omit("User").Create(item).Error
		}
		if previous.Path != item.Path || previous.Revision != expected {
			return ErrWatchHistoryConflict
		}
		item.ID, item.CreatedAt = previous.ID, previous.CreatedAt
		item.Revision = previous.Revision + 1
		item.LastWatchedAt = time.Now().UTC()
		item.UpdatedAt = item.LastWatchedAt
		result := tx.Model(&model.CustomWatchHistory{}).Where("id = ? AND user_id = ? AND revision = ?", previous.ID, item.UserID, expected).
			Updates(map[string]interface{}{
				"name": item.Name, "position_seconds": item.PositionSeconds, "duration_seconds": item.DurationSeconds,
				"completed": item.Completed, "revision": item.Revision, "last_watched_at": item.LastWatchedAt, "updated_at": item.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWatchHistoryConflict
		}
		return nil
	})
}
