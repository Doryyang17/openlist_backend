package handles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewLocalFileBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path, stat, err := previewLocalFile(root, "/movie.mp4")
	expected, _ := filepath.EvalSymlinks(filepath.Join(root, "movie.mp4"))
	if err != nil || path != expected || stat.Size() != 7 {
		t.Fatalf("valid file: %s %v", path, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.mp4"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"/", "/escape/private.mp4", "../../outside.mp4", "/missing.mp4"} {
		if _, _, err := previewLocalFile(root, value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
