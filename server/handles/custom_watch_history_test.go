package handles

import (
	"math"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestWatchProgressValidation(t *testing.T) {
	revision := uint64(0)
	good := watchProgressReq{Position: 12.5, Duration: 90, Revision: &revision}
	if !validWatchProgress(good) {
		t.Fatal("valid position rejected")
	}
	for _, value := range []float64{-1, math.Inf(1), math.Inf(-1), math.NaN(), 1e10} {
		req := good
		req.Position = value
		if validWatchProgress(req) {
			t.Fatalf("accepted position %v", value)
		}
		req = good
		req.Duration = value
		if validWatchProgress(req) {
			t.Fatalf("accepted duration %v", value)
		}
	}
	good.Revision = nil
	if validWatchProgress(good) {
		t.Fatal("missing revision accepted")
	}
}

func TestWatchHistoryRootPresentation(t *testing.T) {
	item := model.CustomWatchHistory{ID: 1, Path: "/team/video/one.mp4", Name: "one.mp4", PositionSeconds: 10, Revision: 2}
	v := presentWatchHistory(&model.User{BasePath: "/team"}, item)
	if v.Path == nil || *v.Path != "/video/one.mp4" || v.Parent == nil || *v.Parent != "/video" {
		t.Fatal("relative path")
	}
	v = presentWatchHistory(&model.User{BasePath: "/other"}, item)
	if v.Path != nil || v.Parent != nil || v.ScopeStatus != "outside_root" {
		t.Fatal("path exposed outside root")
	}
}
