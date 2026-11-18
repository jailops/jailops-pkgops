package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// Within reports whether file lies inside the directory root. Both are made
// absolute and symbolic links are resolved as far as the paths exist, so a link
// into the root does not hide a file inside it.
func Within(root, file string) bool {
	r, f := resolve(root), resolve(file)
	if r == "" || f == "" {
		return false
	}
	return f == r || strings.HasPrefix(f, strings.TrimRight(r, string(os.PathSeparator))+string(os.PathSeparator))
}

// resolve makes p absolute and resolves the symbolic links of its longest
// existing prefix.
func resolve(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	rest := ""
	for cur := a; ; {
		if ev, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(ev, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return a
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
