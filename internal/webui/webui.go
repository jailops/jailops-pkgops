// Package webui embeds the optional registry web UI (web/, built with Vite
// into dist/). The directory always compiles: dist/.gitkeep keeps go:embed
// satisfied before the first `npm run build`, and without a built index.html
// FS reports that no UI is available, so the registry API and static serving
// work exactly the same.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the built UI rooted at its index.html, and whether one is present.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
