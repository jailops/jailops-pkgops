package pkgops

import (
	"context"
	"crypto/rsa"

	"jailops.net/pkgops/internal/build"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/publish"
	"jailops.net/pkgops/internal/repository"
)

// This file is the whole public surface of the root package: the
// implementation lives in internal/ packages and is re-exported here under the
// same names the module has always had.

// Sentinel errors returned (wrapped) by the SDK. Use errors.Is to test them.
var (
	// ErrInvalidSpec is returned when a manifest (pkgops.yaml) or a Package
	// value is malformed or incomplete.
	ErrInvalidSpec = pkgerr.ErrInvalidSpec
	// ErrInvalidPackage is returned when a .pkg file cannot be parsed or fails
	// its integrity check.
	ErrInvalidPackage = pkgerr.ErrInvalidPackage
	// ErrSignature is returned when a signature is missing, malformed or does
	// not verify.
	ErrSignature = pkgerr.ErrSignature
	// ErrNotFound is returned when a package or repository does not exist.
	ErrNotFound = pkgerr.ErrNotFound
	// ErrUnsupportedABI is returned for ABI strings pkgops cannot build for.
	ErrUnsupportedABI = pkgerr.ErrUnsupportedABI
	// ErrBuild is returned when the external go build step fails.
	ErrBuild = pkgerr.ErrBuild
)

// Manifest (pkgops.yaml).
type (
	// Spec is the in-memory form of a pkgops.yaml manifest.
	Spec = build.Spec
	// DepSpec is a dependency on another package.
	DepSpec = build.DepSpec
	// BuildSpec describes the optional Go build step.
	BuildSpec = build.BuildSpec
	// GoPackage is one executable of a build.packages list.
	GoPackage = build.GoPackage
	// Mode is a Unix permission value written in octal in YAML.
	Mode = build.Mode
	// FileSpec places one file, symlink or directory tree into the package.
	FileSpec = build.FileSpec
	// DirSpec declares a directory owned by the package.
	DirSpec = build.DirSpec
	// ServiceDef is the service: section that generates an rc.d script.
	ServiceDef = build.ServiceDef
	// ServiceLog routes a generated service's output.
	ServiceLog = build.ServiceLog
	// ServiceSpec installs an rc.d script.
	ServiceSpec = build.ServiceSpec
	// UserSpec creates a system user in the generated pre-install script.
	UserSpec = build.UserSpec
	// GroupSpec creates a group in the generated pre-install script.
	GroupSpec = build.GroupSpec
	// ScriptSpec is a pre/post install/deinstall script.
	ScriptSpec = build.ScriptSpec
	// BuildOptions controls Spec.Package and BuildPackage.
	BuildOptions = build.BuildOptions
	// Built is the result of BuildPackage.
	Built = build.Built
)

// ScriptNames are the script phases pkg(8) knows about.
var ScriptNames = pkgfile.ScriptNames

// ParseSpec decodes pkgops.yaml content. Unknown keys are an error.
func ParseSpec(data []byte) (*Spec, error) { return build.ParseSpec(data) }

// LoadSpec reads and parses a manifest file.
func LoadSpec(file string) (*Spec, error) { return build.LoadSpec(file) }

// BuildPackage is the one-call pipeline used by `pkgops build`: go build (if
// configured), resolve the manifest, write the .pkg.
func BuildPackage(ctx context.Context, s *Spec, opt BuildOptions) (*Built, error) {
	return build.BuildPackage(ctx, s, opt)
}

// Packages.
type (
	// ABI is a parsed pkg ABI string such as FreeBSD:15:amd64.
	ABI = pkgfile.ABI
	// File is one regular file or symlink inside a package.
	File = pkgfile.File
	// Dir is a directory owned by the package.
	Dir = pkgfile.Dir
	// Dep is a package dependency.
	Dep = pkgfile.Dep
	// Package is the complete in-memory description of a FreeBSD package.
	Package = pkgfile.Package
	// WriteOptions tunes archive creation.
	WriteOptions = pkgfile.WriteOptions
	// Info describes a package as recorded in its compact manifest.
	Info = pkgfile.Info
)

// ParseABI parses and normalises an ABI string (amd64 and aarch64 only).
func ParseABI(s string) (ABI, error) { return pkgfile.ParseABI(s) }

// ReadInfo reads only the compact manifest of a .pkg file and fills Size and SHA256.
func ReadInfo(filename string) (*Info, error) { return pkgfile.ReadInfo(filename) }

// Verify fully reads a .pkg file and checks it against its manifest.
func Verify(filename string) (*Info, error) { return pkgfile.Verify(filename) }

// Repositories and signing.
type (
	// SignedEntry is one extra file stored in a catalog archive.
	SignedEntry = repository.SignedEntry
	// Signer signs repository catalogs.
	Signer = repository.Signer
	// SignMode selects how clients are expected to trust the key.
	SignMode = repository.SignMode
	// RSASigner signs with an RSA key as pkg-repo(8) does.
	RSASigner = repository.RSASigner
	// Repository is a static pkg repository tree.
	Repository = repository.Repository
	// RepoCheck is the outcome of VerifyRepository.
	RepoCheck = repository.RepoCheck
	// Problem is a package file Repository.Scan could not read.
	Problem = repository.Problem
)

// Repository policy, prune manifest and the published-file index.
type (
	// Policy is pkgops-repo.yaml: supported ABIs, minimum release, retention hints.
	Policy = policy.Policy
	// PruneManifest is prune.yaml: published files the next build leaves out.
	PruneManifest = prune.Manifest
	// PruneEntry is one file listed in a PruneManifest.
	PruneEntry = prune.Entry
	// Index is pkgops-index.json: every published package file with its sha256.
	Index = index.Index
	// IndexEntry is one published package file.
	IndexEntry = index.Entry
)

// LoadPolicy reads a repository policy file.
func LoadPolicy(file string) (*Policy, error) { return policy.Load(file) }

// LoadPruneManifest reads a prune manifest; a missing file is an empty manifest.
func LoadPruneManifest(file string) (*PruneManifest, error) { return prune.Load(file) }

// ReadIndex reads pkgops-index.json from a repository root; a missing file returns (nil, nil).
func ReadIndex(root string) (*Index, error) { return index.Read(root) }

// DefaultChannel is the directory below the ABI directory holding a repository.
const DefaultChannel = repository.DefaultChannel

// Signature modes.
const (
	// SignPubkey stores a single "signature" entry (client: signature_type pubkey).
	SignPubkey = repository.SignPubkey
	// SignFingerprints stores "<payload>.sig" and "<payload>.pub".
	SignFingerprints = repository.SignFingerprints
)

// GenerateRSAKey creates a new RSA key (bits >= 2048).
func GenerateRSAKey(bits int) (*rsa.PrivateKey, error) { return repository.GenerateRSAKey(bits) }

// CompareVersions orders pkg-style versions; it returns -1, 0 or 1.
func CompareVersions(a, b string) int { return repository.CompareVersions(a, b) }

// VerifyRepository checks one repository directory the way a pkg client would.
func VerifyRepository(dir string, pubPEM []byte) (*RepoCheck, error) {
	return repository.VerifyRepository(dir, pubPEM)
}

// Publishing.
type (
	// PublishOptions configures Publish.
	PublishOptions = publish.Options
)

// IsRemote reports whether dest looks like an rsync-over-ssh target.
func IsRemote(dest string) bool { return publish.IsRemote(dest) }

// Publish synchronises a repository tree to a directory or an rsync target,
// packages first and catalogs last.
func Publish(ctx context.Context, src, dest string, opt PublishOptions) error {
	return publish.Publish(ctx, src, dest, opt)
}

// Key handling.

// PrivateKeyPEM encodes a key as PKCS#8 PEM.
func PrivateKeyPEM(k *rsa.PrivateKey) ([]byte, error) { return repository.PrivateKeyPEM(k) }

// PublicKeyPEM encodes the public key as PKIX PEM, identical to `openssl rsa -pubout`.
func PublicKeyPEM(k *rsa.PublicKey) ([]byte, error) { return repository.PublicKeyPEM(k) }

// ParseRSAPrivateKey parses a PKCS#1 or PKCS#8 PEM private key.
func ParseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	return repository.ParseRSAPrivateKey(data)
}

// LoadRSASigner reads a PEM private key file.
func LoadRSASigner(file string, mode SignMode) (*RSASigner, error) {
	return repository.LoadRSASigner(file, mode)
}

// ParseRSAPublicKey parses a PKIX PEM public key.
func ParseRSAPublicKey(data []byte) (*rsa.PublicKey, error) {
	return repository.ParseRSAPublicKey(data)
}

// VerifyRSA checks a signature made by RSASigner (trailing NUL tolerated).
func VerifyRSA(pub *rsa.PublicKey, digestHex string, sig []byte) error {
	return repository.VerifyRSA(pub, digestHex, sig)
}

// Fingerprint returns the sha256 of the public key PEM as distributed.
func Fingerprint(pubPEM []byte) string { return repository.Fingerprint(pubPEM) }

// FingerprintFile renders the file clients place in <fingerprints dir>/trusted/.
func FingerprintFile(pubPEM []byte) []byte { return repository.FingerprintFile(pubPEM) }

// BuildAll builds the package for every ABI selected by opt.ABI ("" or "all" =
// every ABI the manifest lists via abis/abi, or a comma-separated list), in
// parallel, and returns one result per successful ABI in selector order. If any
// ABI fails the error names it (errors.Join) and the other results are kept.
func BuildAll(ctx context.Context, s *Spec, opt BuildOptions) ([]*Built, error) {
	return build.BuildAll(ctx, s, opt)
}
