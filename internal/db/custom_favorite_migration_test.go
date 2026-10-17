package db

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestFavoritesTablePrefixMigration(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "fresh"
		if legacy {
			name = "legacy_indexes"
		}
		t.Run(name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "favorites.db")
			type fixture struct {
				db   *gorm.DB
				item model.CustomFavorite
			}
			var fixtures []fixture
			for i, prefix := range []string{"first_", "second_", ""} {
				d, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
					Logger:         logger.Default.LogMode(logger.Silent),
					NamingStrategy: schema.NamingStrategy{TablePrefix: prefix},
				})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, err := d.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sqlDB.Close() })
				if err := d.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
					t.Fatalf("migrate prefix %q: %v", prefix, err)
				}
				user := model.User{Username: "reader", BasePath: "/"}
				if err := d.Create(&user).Error; err != nil {
					t.Fatal(err)
				}
				item := model.CustomFavorite{UserID: user.ID, Path: "/saved.txt", PathKey: FavoritePathKey("/saved.txt"), Name: "saved.txt", SearchName: "saved.txt"}
				if err := d.Omit("User").Create(&item).Error; err != nil {
					t.Fatal(err)
				}
				if legacy && i == 0 {
					// Recreate the old index names on a populated table, without changing its data.
					table := d.NamingStrategy.TableName("CustomFavorite")
					for _, suffix := range []string{"owner_path", "order"} {
						oldName := "idx_custom_favorite_" + suffix
						if !d.Migrator().HasIndex(&model.CustomFavorite{}, oldName) {
							newName := d.NamingStrategy.IndexName(table, suffix)
							if err := d.Migrator().RenameIndex(&model.CustomFavorite{}, newName, oldName); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				fixtures = append(fixtures, fixture{db: d, item: item})
			}
			for _, fixture := range fixtures {
				d := fixture.db
				// Repeated startup migrations must preserve data and retain both composite indexes.
				for range 2 {
					if err := d.AutoMigrate(&model.User{}, &model.CustomFavorite{}); err != nil {
						t.Fatal(err)
					}
				}
				var saved model.CustomFavorite
				if err := d.First(&saved, fixture.item.ID).Error; err != nil {
					t.Fatal(err)
				}
				if saved.UserID != fixture.item.UserID || saved.Path != fixture.item.Path || saved.Name != fixture.item.Name || !saved.CreatedAt.Equal(fixture.item.CreatedAt) {
					t.Fatal("migration changed the saved favorite")
				}
				indexes, err := d.Migrator().GetIndexes(&model.CustomFavorite{})
				if err != nil {
					t.Fatal(err)
				}
				table := d.NamingStrategy.TableName("CustomFavorite")
				for suffix, columns := range map[string][]string{
					"owner_path": {"user_id", "path_key"},
					"order":      {"user_id", "created_at", "id"},
				} {
					name := d.NamingStrategy.IndexName(table, suffix)
					found := false
					for _, index := range indexes {
						if index.Name() != name {
							continue
						}
						found = true
						unique, known := index.Unique()
						if !slices.Equal(index.Columns(), columns) || !known || unique != (suffix == "owner_path") {
							t.Fatalf("incorrect columns or uniqueness for %s", name)
						}
					}
					if !found {
						t.Fatalf("missing table-specific index %s", name)
					}
				}
				duplicate := fixture.item
				duplicate.ID = 0
				if err := d.Omit("User").Create(&duplicate).Error; err == nil {
					t.Fatal("migration lost the owner/path unique constraint")
				}
			}
		})
	}
}
