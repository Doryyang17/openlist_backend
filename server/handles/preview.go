package handles

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/preview"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	perrors "github.com/pkg/errors"
)

// FsPreview uses the same user-relative path and directory-password checks as
// FsGet. It is independent of the media-library and transcoding feature gates.
func FsPreview(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	var req FsGetReq
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
		common.ErrorStrResp(c, "invalid preview request", 400)
		return
	}
	user, _ := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user == nil || (user.IsGuest() && user.Disabled) {
		common.ErrorStrResp(c, "login required", 401)
		return
	}
	// Shared links have their own password/auth model; do not interpret them as
	// ordinary file-system paths until that model is explicitly supported.
	if strings.HasPrefix(req.Path, "/@s") {
		common.ErrorStrResp(c, "preview unavailable", 422)
		return
	}
	path, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorStrResp(c, "path is not accessible", 403)
		return
	}
	meta, err := op.GetNearestMeta(path)
	if err != nil && !errors.Is(perrors.Cause(err), errs.MetaNotFound) {
		common.ErrorStrResp(c, "cannot check file access", 500)
		return
	}
	if !common.CanAccess(user, meta, path, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	storage, actual, err := op.GetStorageAndActualPath(path)
	if err != nil || storage.Config().Name != "Local" {
		common.ErrorStrResp(c, "preview requires a local file", 422)
		return
	}
	rooter, ok := storage.(driver.IRootPath)
	if !ok {
		common.ErrorStrResp(c, "preview unavailable", 422)
		return
	}
	if storage.Config().CheckStatus && storage.GetStorage().Status != op.WORK {
		common.ErrorStrResp(c, "storage unavailable", 503)
		return
	}
	local, stat, err := previewLocalFile(rooter.GetRootPath(), actual)
	if err != nil {
		common.ErrorStrResp(c, "preview file unavailable", 422)
		return
	}
	if info, err := os.Stat(preview.HelperPath()); err != nil || info.IsDir() {
		common.ErrorStrResp(c, "preview worker unavailable", 503)
		return
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("native-v1:%d:%s:%d:%d",
		storage.GetStorage().ID, local, stat.Size(), stat.ModTime().UnixNano()))))
	job, release, err := preview.Default.Acquire(key, local)
	if err != nil {
		common.ErrorStrResp(c, "preview busy", 429)
		return
	}
	defer release()
	c.Header("Content-Type", "application/vnd.openlist.preview")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-OpenList-Preview", "native-v1")
	c.Status(200)
	controller := http.NewResponseController(c.Writer)
	defer controller.SetWriteDeadline(time.Time{})
	cursor := 0
	for {
		records, done, failed, changed := job.Snapshot(cursor)
		for _, record := range records {
			if c.Request.Context().Err() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := preview.WriteRecord(c.Writer, record); err != nil {
				return
			}
			cursor++
			c.Writer.Flush()
		}
		if done || failed {
			kind := "done"
			if failed {
				kind = "unavailable"
			}
			_ = preview.WriteRecord(c.Writer, preview.Record{Event: preview.Event{Kind: kind}})
			c.Writer.Flush()
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-changed:
		}
	}
}

// Resolve symlinks and require a regular file inside this storage's root. A
// preview request never accepts a host path or follows a symlink outside it.
func previewLocalFile(root, actual string) (string, os.FileInfo, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", nil, err
	}
	file, err := filepath.EvalSymlinks(filepath.Join(base, actual))
	if err != nil {
		return "", nil, err
	}
	rel, err := filepath.Rel(base, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", nil, errors.New("preview path escapes storage")
	}
	stat, err := os.Stat(file)
	if err != nil {
		return "", nil, err
	}
	if !stat.Mode().IsRegular() {
		return "", nil, errors.New("not a regular video file")
	}
	return file, stat, nil
}
