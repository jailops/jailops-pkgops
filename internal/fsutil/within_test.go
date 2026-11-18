package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithin(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	os.Mkdir(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "f"), nil, 0o600)
	os.WriteFile(filepath.Join(other, "f"), nil, 0o600)
	link := filepath.Join(other, "into")
	linked := os.Symlink(root, link) == nil
	for _, tc := range []struct {
		file string
		want bool
	}{
		{filepath.Join(root, "sub", "f"), true},
		{filepath.Join(root, "not-yet", "key"), true}, // does not exist yet: judged by its place
		{root, true},
		{filepath.Join(root, "..", filepath.Base(other), "f"), false},
		{filepath.Join(other, "f"), false},
		{root + "-sibling", false}, // a longer name is not inside
	} {
		if got := Within(root, tc.file); got != tc.want {
			t.Errorf("Within(root, %s) = %v, want %v", tc.file, got, tc.want)
		}
	}
	if linked && !Within(root, filepath.Join(link, "sub", "f")) {
		t.Error("a path through a symbolic link into the root is inside")
	}
}
