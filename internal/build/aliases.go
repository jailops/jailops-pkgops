package build

import "jailops.net/pkgops/internal/pkgfile"

// Local names for the pkgfile types the build step produces.
type (
	Package      = pkgfile.Package
	File         = pkgfile.File
	Dir          = pkgfile.Dir
	Dep          = pkgfile.Dep
	Info         = pkgfile.Info
	WriteOptions = pkgfile.WriteOptions
	ABI          = pkgfile.ABI
)

// ParseABI is pkgfile.ParseABI.
func ParseABI(s string) (ABI, error) { return pkgfile.ParseABI(s) }
