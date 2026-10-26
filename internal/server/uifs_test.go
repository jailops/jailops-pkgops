package server

import (
	"io/fs"

	"jailops.net/pkgops/internal/webui"
)

func webuiFS() (fs.FS, bool) { return webui.FS() }
