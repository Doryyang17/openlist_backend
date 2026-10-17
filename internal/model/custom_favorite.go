package model

import "time"

// CustomFavorite is a personal bookmark, never an authorization to read its target.
// Paths are server-side virtual paths, not driver paths or downloadable URLs.
// Unnamed composite indexes inherit the table prefix through GORM's naming strategy.
type CustomFavorite struct {
	ID         uint      `gorm:"primaryKey;index:,composite:order,priority:3"`
	UserID     uint      `gorm:"not null;uniqueIndex:,composite:owner_path,priority:1;index:,composite:order,priority:1"`
	PathKey    string    `gorm:"size:64;not null;uniqueIndex:,composite:owner_path,priority:2"`
	Path       string    `gorm:"type:text;not null"`
	Name       string    `gorm:"type:text;not null"`
	SearchName string    `gorm:"type:text;not null"`
	IsDir      bool      `gorm:"not null"`
	CreatedAt  time.Time `gorm:"index:,composite:order,priority:2"`
	User       User      `gorm:"constraint:OnDelete:CASCADE"`
}
