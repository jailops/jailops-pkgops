package pkgfile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/pkgops/internal/pkgerr"
)

// ABI is a parsed pkg ABI string such as FreeBSD:15:amd64.
type ABI struct {
	OS    string // "FreeBSD"
	Major int
	Arch  string // "amd64" or "aarch64" (pkg spelling)
}

// ParseABI parses and normalises an ABI string. Only FreeBSD amd64 and
// aarch64 are supported.
func ParseABI(s string) (ABI, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "freebsd") {
		return ABI{}, fmt.Errorf("%w: %q (want FreeBSD:<major>:<amd64|aarch64>)", pkgerr.ErrUnsupportedABI, s)
	}
	major, err := strconv.Atoi(parts[1])
	if err != nil || major < 1 {
		return ABI{}, fmt.Errorf("%w: %q has a bad major version", pkgerr.ErrUnsupportedABI, s)
	}
	switch parts[2] {
	case "amd64", "aarch64":
	default:
		return ABI{}, fmt.Errorf("%w: %q (architecture must be amd64 or aarch64)", pkgerr.ErrUnsupportedABI, s)
	}
	return ABI{OS: "FreeBSD", Major: major, Arch: parts[2]}, nil
}

// String returns the canonical ABI, e.g. "FreeBSD:15:amd64".
func (a ABI) String() string { return fmt.Sprintf("%s:%d:%s", a.OS, a.Major, a.Arch) }

// PkgArch returns the legacy "arch" string pkg stores next to the ABI,
// e.g. "freebsd:15:amd64:64".
func (a ABI) PkgArch() string { return fmt.Sprintf("freebsd:%d:%s:64", a.Major, a.Arch) }

// GOARCH returns the Go architecture for this ABI.
func (a ABI) GOARCH() string {
	if a.Arch == "aarch64" {
		return "arm64"
	}
	return "amd64"
}

// ScriptNames are the script phases pkg(8) knows about.
var ScriptNames = []string{"pre-install", "post-install", "pre-deinstall", "post-deinstall"}

var reName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// ValidName reports whether s is a valid package name.
func ValidName(s string) bool { return reName.MatchString(s) }

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

var reRelease = regexp.MustCompile(`^([0-9]{1,3})\.([0-9]{1,2})$`)

// ReleaseOSVersion converts a release such as "15.1" to the __FreeBSD_version
// of its -RELEASE ("1501000", what `uname -U` prints on 15.1). FreeBSD numbers
// minor releases major*100000 + minor*1000; "15.0" gives 1500000, which is
// below every 15.0-based number and so never refuses a real 15.0 system
// (the 15.0 releases themselves carry 15000xx numbers).
func ReleaseOSVersion(rel string) (int, error) {
	m := reRelease.FindStringSubmatch(rel)
	if m == nil {
		return 0, fmt.Errorf("%w: release %q (want MAJOR.MINOR, for example 15.1)", pkgerr.ErrInvalidSpec, rel)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 1 || minor > 99 {
		return 0, fmt.Errorf("%w: release %q out of range", pkgerr.ErrInvalidSpec, rel)
	}
	return major*100000 + minor*1000, nil
}

// OSVersionRelease is the inverse of [ReleaseOSVersion]: 1501000 -> "15.1".
// It is "" for 0.
func OSVersionRelease(v int) string {
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d", v/100000, (v/1000)%100)
}

// OSVersionMajor returns the major FreeBSD release of a __FreeBSD_version.
func OSVersionMajor(v int) int { return v / 100000 }
