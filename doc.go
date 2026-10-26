// Package pkgops builds native FreeBSD pkg(8) packages and pkg repositories
// from any operating system, in pure Go.
//
// It has three layers, each usable on its own:
//
//   - Packages. [Spec] (the pkgops.yaml manifest) or a plain [Package] value
//     describes the content; [BuildPackage] / [Package.Write] produce a .pkg
//     (tar + zstd with +MANIFEST and +COMPACT_MANIFEST). [ReadInfo] and
//     [Verify] read them back. Output is deterministic: the same inputs and
//     [Package.Time] give byte-identical files.
//
//   - Repositories. [Repository] maintains the static tree FreeBSD's pkg
//     expects (<ABI>/latest/All/*.pkg plus meta.conf, data.pkg, packagesite.pkg
//     and optionally files.pkg), signs catalogs with an [RSASigner], prunes old
//     versions and re-indexes. [VerifyRepository] checks a tree the way a
//     client would. [Publish] copies a tree to a web root, packages first and
//     catalogs last, or to a remote host with rsync.
//
//   - Serving. The sub-package jailops.net/pkgops/serve adds an optional HTTP
//     registry (token-authenticated uploads) on top of a Repository. A plain
//     nginx pointed at the same directory serves the same content.
//
// Everything is derived from the pkg-create(8), pkg-repo(8) and
// pkg-repository(5) manual pages and from inspecting packages and catalogs
// written by FreeBSD's own pkg 2.8; see docs/format.md for what was learned and
// which parts are verified.
//
// Errors wrap the sentinel values ErrInvalidSpec, ErrInvalidPackage,
// ErrSignature, ErrNotFound, ErrUnsupportedABI and ErrBuild; test them with
// errors.Is.
package pkgops
