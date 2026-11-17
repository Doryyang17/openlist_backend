package handles

import (
	"errors"
	"math"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/go-cache"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

var watchLimiters = cache.NewMemCache[*rate.Limiter]()
var watchLimiterMu sync.Mutex

// WatchHistoryLimits bounds authenticated traffic and request bodies before any
// file-provider work. Idle account limiters expire rather than growing forever.
func WatchHistoryLimits(c *gin.Context) {
	key := strconv.FormatUint(uint64(favoriteUser(c).ID), 10)
	watchLimiterMu.Lock()
	limiter, ok := watchLimiters.Get(key)
	if !ok {
		limiter = rate.NewLimiter(2, 20)
		watchLimiters.Set(key, limiter, cache.WithEx[*rate.Limiter](10*time.Minute))
	}
	watchLimiterMu.Unlock()
	if !limiter.Allow() {
		favoriteError(c, 429, "rate_limited")
		c.Abort()
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
}

type watchHistoryView struct {
	ID              uint      `json:"id"`
	Name            string    `json:"name"`
	Path            *string   `json:"path"`
	Parent          *string   `json:"parent"`
	ScopeStatus     string    `json:"scope_status"`
	PositionSeconds float64   `json:"position_seconds"`
	DurationSeconds float64   `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	Revision        uint64    `json:"revision"`
	LastWatchedAt   time.Time `json:"last_watched_at"`
}

func presentWatchHistory(user *model.User, item model.CustomWatchHistory) watchHistoryView {
	v := watchHistoryView{ID: item.ID, Name: item.Name, ScopeStatus: "outside_root", PositionSeconds: item.PositionSeconds,
		DurationSeconds: item.DurationSeconds, Completed: item.Completed, Revision: item.Revision, LastWatchedAt: item.LastWatchedAt}
	if relative, ok := favoriteRelative(user, item.Path); ok {
		v.Path, v.ScopeStatus = &relative, "in_root"
		if parent, allowed := favoriteRelative(user, path.Dir(item.Path)); allowed {
			v.Parent = &parent
		}
	}
	return v
}

func WatchHistoryCapabilities(c *gin.Context) {
	common.SuccessResp(c, gin.H{"version": 1})
}

func ListWatchHistory(c *gin.Context) {
	var req struct {
		Page    int `json:"page"`
		PerPage int `json:"per_page"`
	}
	if c.ShouldBindJSON(&req) != nil {
		favoriteError(c, 400, "invalid_request")
		return
	}
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PerPage == 0 {
		req.PerPage = 20
	}
	if req.Page < 1 || req.Page > 1000000 || req.PerPage < 1 || req.PerPage > 100 {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	items, total, err := db.ListWatchHistory(c.Request.Context(), user.ID, req.Page, req.PerPage)
	if err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	content := make([]watchHistoryView, 0, len(items))
	for _, item := range items {
		content = append(content, presentWatchHistory(user, item))
	}
	common.SuccessResp(c, gin.H{"content": content, "total": total})
}

// Reuse the file browser's path and password boundaries; never persist a URL,
// a browser-supplied filename, or a directory password.
func watchVideo(c *gin.Context, input, password string) (string, model.Obj, bool) {
	user := favoriteUser(c)
	target, err := favoritePath(user, input)
	if err != nil {
		favoriteError(c, 400, "invalid_path")
		return "", nil, false
	}
	if !favoriteAccess(c, user, target, password) {
		return "", nil, false
	}
	obj, ok := favoriteObject(c, target)
	if !ok {
		return "", nil, false
	}
	if utils.GetObjType(obj.GetName(), obj.IsDir()) != conf.VIDEO {
		favoriteError(c, 400, "not_video")
		return "", nil, false
	}
	return target, obj, true
}

func GetWatchHistory(c *gin.Context) {
	var req struct {
		Path     string `json:"path"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil {
		favoriteError(c, 400, "invalid_request")
		return
	}
	target, _, ok := watchVideo(c, req.Path, req.Password)
	if !ok {
		return
	}
	user := favoriteUser(c)
	item, err := db.GetWatchHistoryByPath(c.Request.Context(), user.ID, target)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.SuccessResp(c, nil)
		return
	}
	if err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	common.SuccessResp(c, presentWatchHistory(user, *item))
}

type watchProgressReq struct {
	Path      string  `json:"path"`
	Password  string  `json:"password"`
	Position  float64 `json:"position"`
	Duration  float64 `json:"duration"`
	Completed bool    `json:"completed"`
	Revision  *uint64 `json:"revision"`
}

func validWatchProgress(req watchProgressReq) bool {
	return req.Revision != nil && *req.Revision < 9007199254740991 &&
		!math.IsNaN(req.Position) && !math.IsInf(req.Position, 0) && req.Position >= 0 && req.Position <= 1e9 &&
		!math.IsNaN(req.Duration) && !math.IsInf(req.Duration, 0) && req.Duration >= 0 && req.Duration <= 1e9
}

func SaveWatchHistory(c *gin.Context) {
	var req watchProgressReq
	if c.ShouldBindJSON(&req) != nil || !validWatchProgress(req) {
		favoriteError(c, 400, "invalid_request")
		return
	}
	target, obj, ok := watchVideo(c, req.Path, req.Password)
	if !ok {
		return
	}
	if req.Duration > 0 {
		req.Position = math.Min(req.Position, req.Duration)
	} else {
		req.Position, req.Completed = 0, false
	}
	user := favoriteUser(c)
	item := model.CustomWatchHistory{UserID: user.ID, Path: target, Name: obj.GetName(),
		PositionSeconds: req.Position, DurationSeconds: req.Duration, Completed: req.Completed}
	if err := db.SaveWatchHistory(c.Request.Context(), &item, *req.Revision); err != nil {
		if errors.Is(err, db.ErrWatchHistoryConflict) {
			favoriteError(c, 409, "conflict")
		} else {
			favoriteError(c, 500, "unavailable")
		}
		return
	}
	common.SuccessResp(c, presentWatchHistory(user, item))
}

func ResolveWatchHistory(c *gin.Context) {
	var req struct {
		ID       uint   `json:"id"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || req.ID == 0 {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	item, err := db.GetWatchHistory(c.Request.Context(), user.ID, req.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		favoriteError(c, 404, "not_found")
		return
	}
	if err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	relative, ok := favoriteRelative(user, item.Path)
	if !ok {
		favoriteError(c, 403, "outside_root")
		return
	}
	if _, _, ok := watchVideo(c, relative, req.Password); !ok {
		return
	}
	common.SuccessResp(c, gin.H{"path": relative})
}
