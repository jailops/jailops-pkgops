package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// The package must compile and answer sensibly whether or not the UI was built.
func TestFS(t *testing.T) {
	sub, ok := FS()
	if !ok {
		t.Log("web UI not built (dist/index.html missing); API-only build")
		return
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		t.Fatal(err)
	}
	b, _ := fs.ReadFile(sub, "index.html")
	if len(b) == 0 {
		t.Fatal("empty index.html")
	}
}

// dist/.gitkeep must exist so go:embed compiles before the first UI build; the
// built UI, when present, must be self-contained (strict CSP, no CDN).
func TestEmbedPlaceholderAndSelfContained(t *testing.T) {
	if _, err := fs.Stat(assets, "dist/.gitkeep"); err != nil {
		t.Fatalf("dist/.gitkeep missing: %v", err)
	}
	sub, ok := FS()
	if !ok {
		t.Skip("UI not built")
	}
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css")) {
			return err
		}
		b, _ := fs.ReadFile(sub, p)
		for _, bad := range []string{"https://cdn", "https://fonts.", "unpkg.com", "jsdelivr"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s references an external host (%s)", p, bad)
			}
		}
		if strings.HasSuffix(p, ".html") && strings.Contains(string(b), "<style") {
			t.Errorf("%s has inline <style>, which the CSP forbids", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, _ := fs.ReadFile(sub, "index.html")
	if !strings.Contains(string(idx), `/ui/assets/`) || !strings.Contains(string(idx), "PkgOps registry") {
		t.Errorf("unexpected index.html: %s", idx)
	}
}
