package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"testing"
)

func TestFavoritePathAndAccountRoot(t *testing.T) {
	u := &model.User{BasePath: "/team"}
	for _, p := range []string{"/", "/@s/share/file", "/../outside", "/a/../../b", "relative", "/a\x00b"} {
		if _, err := favoritePath(u, p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	p, err := favoritePath(u, "/资料/100% #?.txt")
	if err != nil || p != "/team/资料/100% #?.txt" {
		t.Fatalf("path %q %v", p, err)
	}
	item := model.CustomFavorite{ID: 1, Path: p, Name: "100% #?.txt"}
	view := presentFavorite(u, item)
	if view.Path == nil || *view.Path != "/资料/100% #?.txt" {
		t.Fatal("relative path")
	}
	u.BasePath = "/team/资料"
	view = presentFavorite(u, item)
	if view.Path == nil || *view.Path != "/100% #?.txt" {
		t.Fatal("new root mapping")
	}
	u.BasePath = "/another"
	view = presentFavorite(u, item)
	if view.Path != nil || view.Parent != nil || view.ScopeStatus != "outside_root" {
		t.Fatal("old root exposed or retargeted")
	}
	if _, ok := favoriteRelative(&model.User{BasePath: "/team"}, "/team-other/file"); ok {
		t.Fatal("prefix boundary")
	}
	root := presentFavorite(&model.User{BasePath: "/team"}, model.CustomFavorite{Path: "/team", IsDir: true})
	if root.Path == nil || *root.Path != "/" || root.Parent != nil || root.ScopeStatus != "in_root" {
		t.Fatal("account root bookmark must not expose its parent")
	}
}
