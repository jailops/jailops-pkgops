package repository

import "jailops.net/pkgops/internal/pkgver"

// CompareVersions orders pkg-style versions "[version][_revision][,epoch]"
// (see pkgver.Compare); it returns -1, 0 or 1.
func CompareVersions(a, b string) int { return pkgver.Compare(a, b) }
