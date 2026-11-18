package server

import (
	"regexp"
	"strings"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgfile"
)

// A repository root holds what is distributed and, on a careless day, other
// files (a key, a token file, a backup). The static side serves only what a
// pkg client or a mirror fetches:
//
//	pkgops-index.json
//	NAME.pub                              a public key published next to the site
//	ABI/CHANNEL/meta, meta.conf, packagesite.pkg, data.pkg, files.pkg
//	ABI/CHANNEL/All/NAME-VERSION.pkg
//
// Everything else answers 404, whatever it is called.
var (
	channelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	pubRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.pub$`)
	pkgRE     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+~,-]*\.pkg$`)
)

var catalogFiles = map[string]bool{"meta": true, "meta.conf": true, "packagesite.pkg": true, "data.pkg": true, "files.pkg": true}

// servable reports whether the slash-separated path below the repository root
// (no leading slash) is a distribution file.
func servable(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") {
		return false
	}
	parts := strings.Split(rel, "/")
	switch len(parts) {
	case 1:
		return parts[0] == index.FileName || pubRE.MatchString(parts[0])
	case 3:
		return abiDir(parts[0]) && channelRE.MatchString(parts[1]) && catalogFiles[parts[2]]
	case 4:
		return abiDir(parts[0]) && channelRE.MatchString(parts[1]) && parts[2] == "All" && pkgRE.MatchString(parts[3])
	}
	return false
}

func abiDir(s string) bool {
	a, err := pkgfile.ParseABI(s)
	return err == nil && a.String() == s
}
