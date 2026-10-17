package handles

import (
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type favoriteView struct {
	ID          uint      `json:"id"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	CreatedAt   time.Time `json:"created_at"`
	Path        *string   `json:"path"`
	Parent      *string   `json:"parent"`
	ScopeStatus string    `json:"scope_status"`
}

func favoriteUser(c *gin.Context) *model.User {
	return c.Request.Context().Value(conf.UserKey).(*model.User)
}
func favoriteError(c *gin.Context, code int, reason string) {
	// Never expose provider errors, server paths, or directory passwords.
	c.JSON(200, gin.H{"code": code, "message": reason, "data": gin.H{"reason": reason}})
}

func favoritePath(user *model.User, input string) (string, error) {
	if input == "" || len(input) > 4096 || !utf8.ValidString(input) || strings.ContainsAny(input, "\x00\r\n") || !strings.HasPrefix(input, "/") {
		return "", errors.New("invalid_path")
	}
	for _, part := range strings.Split(strings.ReplaceAll(input, "\\", "/"), "/") {
		if part == ".." {
			return "", errors.New("invalid_path")
		}
	}
	clean := utils.FixAndCleanPath(input)
	if clean == "/" || strings.HasPrefix(clean, "/@") {
		return "", errors.New("invalid_path")
	}
	return user.JoinPath(clean)
}

func favoriteRelative(user *model.User, target string) (string, bool) {
	base := utils.FixAndCleanPath(user.BasePath)
	if !utils.IsSubPath(base, target) {
		return "", false
	}
	if base == "/" {
		return target, true
	}
	rel := strings.TrimPrefix(target, base)
	if rel == "" {
		rel = "/"
	}
	return rel, true
}

func presentFavorite(user *model.User, item model.CustomFavorite) favoriteView {
	v := favoriteView{ID: item.ID, Name: item.Name, IsDir: item.IsDir, CreatedAt: item.CreatedAt, ScopeStatus: "outside_root"}
	if relative, ok := favoriteRelative(user, item.Path); ok {
		v.Path, v.ScopeStatus = &relative, "in_root"
		if parent, allowed := favoriteRelative(user, path.Dir(item.Path)); allowed {
			v.Parent = &parent
		}
	}
	return v
}

func FavoriteCapabilities(c *gin.Context) {
	common.SuccessResp(c, gin.H{"version": 1, "files": true, "folders": true})
}

func ListFavorites(c *gin.Context) {
	var req struct {
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
		Q       string `json:"q"`
		Type    string `json:"type"`
	}
	if c.ShouldBindJSON(&req) != nil {
		favoriteError(c, 400, "invalid_request")
		return
	}
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PerPage == 0 {
		req.PerPage = 50
	}
	if req.Type == "" {
		req.Type = "all"
	}
	req.Q = strings.TrimSpace(req.Q)
	if req.Page < 1 || req.Page > 1000000 || req.PerPage < 1 || req.PerPage > 100 || len(req.Q) > 1024 || (req.Type != "all" && req.Type != "file" && req.Type != "folder") {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	items, total, err := db.ListFavorites(c.Request.Context(), user.ID, req.Page, req.PerPage, req.Q, req.Type)
	if err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	content := make([]favoriteView, 0, len(items))
	for _, item := range items {
		content = append(content, presentFavorite(user, item))
	}
	common.SuccessResp(c, gin.H{"content": content, "total": total})
}

func FavoriteStatus(c *gin.Context) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Paths) > 100 {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	full := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		canonical, err := favoritePath(user, p)
		if err != nil {
			favoriteError(c, 400, "invalid_path")
			return
		}
		full = append(full, canonical)
	}
	states, err := db.FavoriteStatuses(c.Request.Context(), user.ID, full)
	if err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	result := make([]gin.H, 0, len(full))
	for i, p := range full {
		result = append(result, gin.H{"path": req.Paths[i], "id": states[p], "favorited": states[p] != 0})
	}
	common.SuccessResp(c, result)
}

func favoriteAccess(c *gin.Context, user *model.User, target, password string) bool {
	meta, err := op.GetNearestMeta(target)
	if err != nil && !errors.Is(err, errs.MetaNotFound) {
		favoriteError(c, 500, "unavailable")
		return false
	}
	// Check non-password restrictions first to avoid revealing a protected target.
	probe := *user
	probe.Permission |= 1 << 1
	if !common.CanAccess(&probe, meta, target, "") {
		favoriteError(c, 403, "forbidden")
		return false
	}
	if !common.CanAccess(user, meta, target, password) {
		favoriteError(c, 403, "password_required")
		return false
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	return true
}

func favoriteObject(c *gin.Context, target string) (model.Obj, bool) {
	obj, err := fs.Get(c.Request.Context(), target, &fs.GetArgs{})
	if err != nil {
		if errs.IsObjectNotFound(err) {
			favoriteError(c, 404, "not_found")
		} else {
			favoriteError(c, 503, "unavailable")
		}
		return nil, false
	}
	return obj, true
}

func AddFavorite(c *gin.Context) {
	var req struct {
		Path     string `json:"path"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	target, err := favoritePath(user, req.Path)
	if err != nil {
		favoriteError(c, 400, "invalid_path")
		return
	}
	if !favoriteAccess(c, user, target, req.Password) {
		return
	}
	obj, ok := favoriteObject(c, target)
	if !ok {
		return
	}
	item := model.CustomFavorite{UserID: user.ID, Path: target, Name: obj.GetName(), IsDir: obj.IsDir()}
	if err := db.AddFavorite(c.Request.Context(), &item); err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	common.SuccessResp(c, presentFavorite(user, item))
}

func DeleteFavorite(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.ID == 0 {
		favoriteError(c, 400, "invalid_request")
		return
	}
	if err := db.DeleteFavorite(c.Request.Context(), favoriteUser(c).ID, req.ID); err != nil {
		favoriteError(c, 500, "unavailable")
		return
	}
	common.SuccessResp(c)
}

func ResolveFavorite(c *gin.Context) {
	var req struct {
		ID       uint   `json:"id"`
		Action   string `json:"action"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&req) != nil || req.ID == 0 || (req.Action != "open" && req.Action != "parent") {
		favoriteError(c, 400, "invalid_request")
		return
	}
	user := favoriteUser(c)
	item, err := db.GetFavorite(c.Request.Context(), user.ID, req.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			favoriteError(c, 404, "favorite_not_found")
		} else {
			favoriteError(c, 500, "unavailable")
		}
		return
	}
	if _, ok := favoriteRelative(user, item.Path); !ok {
		favoriteError(c, 403, "outside_root")
		return
	}
	target := item.Path
	if req.Action == "parent" {
		target = path.Dir(target)
	}
	relative, ok := favoriteRelative(user, target)
	if !ok {
		favoriteError(c, 403, "outside_root")
		return
	}
	if !favoriteAccess(c, user, target, req.Password) {
		return
	}
	obj, ok := favoriteObject(c, target)
	if !ok {
		return
	}
	common.SuccessResp(c, gin.H{"path": relative, "is_dir": obj.IsDir()})
}
