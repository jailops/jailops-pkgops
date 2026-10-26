package pkgerr

import "errors"

// Sentinel errors returned (wrapped) by the SDK. Use errors.Is to test them.
var (
	// ErrInvalidSpec is returned when a manifest (pkgops.yaml) or a Package
	// value is malformed or incomplete.
	ErrInvalidSpec = errors.New("pkgops: invalid package specification")
	// ErrInvalidPackage is returned when a .pkg file cannot be parsed or fails
	// its integrity check.
	ErrInvalidPackage = errors.New("pkgops: invalid package file")
	// ErrSignature is returned when a signature is missing, malformed or does
	// not verify.
	ErrSignature = errors.New("pkgops: signature error")
	// ErrNotFound is returned when a package or repository does not exist.
	ErrNotFound = errors.New("pkgops: not found")
	// ErrUnsupportedABI is returned for ABI strings pkgops cannot build for.
	ErrUnsupportedABI = errors.New("pkgops: unsupported ABI")
	// ErrBuild is returned when the external go build step fails.
	ErrBuild = errors.New("pkgops: go build failed")
)
