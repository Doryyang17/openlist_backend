package model

import "time"

// CustomWatchHistory stores account-owned video progress at a virtual file path.
type CustomWatchHistory struct {
	ID              uint      `gorm:"primaryKey;index:,composite:order,priority:3"`
	UserID          uint      `gorm:"not null;uniqueIndex:,composite:owner_path,priority:1;index:,composite:order,priority:1"`
	PathKey         string    `gorm:"size:64;not null;uniqueIndex:,composite:owner_path,priority:2"`
	Path            string    `gorm:"type:text;not null"`
	Name            string    `gorm:"type:text;not null"`
	PositionSeconds float64   `gorm:"not null"`
	DurationSeconds float64   `gorm:"not null"`
	Completed       bool      `gorm:"not null"`
	Revision        uint64    `gorm:"not null"`
	LastWatchedAt   time.Time `gorm:"index:,composite:order,priority:2"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	User            User `gorm:"constraint:OnDelete:CASCADE"`
}
