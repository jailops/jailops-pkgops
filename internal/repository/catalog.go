package repository

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
)

// CatalogPackage is one package a catalog advertises.
type CatalogPackage struct {
	Name    string
	Version string
	// Path is relative to the repository directory (All/name-version.pkg).
	Path string
	// SHA256 is the hex digest of a "1$" sum; empty for any other checksum
	// format (pkg's own "2$" BLAKE2), which pkgops cannot compare.
	SHA256 string
	Sum    string // the sum exactly as the catalog has it
	Size   int64
}

// Catalog is what the two catalogs of one repository directory (data.pkg and
// packagesite.pkg) say, after their signatures were checked.
type Catalog struct {
	// Signature is "pubkey", "fingerprints" or "" (packagesite.pkg unsigned).
	Signature string
	// Verified is true when both catalogs verified against the key given to
	// ReadCatalog.
	Verified bool
	// Packages are the entries of data, Site those of packagesite.yaml.
	Packages []CatalogPackage
	Site     []CatalogPackage
	// Updated is the tar mtime of packagesite.yaml. The signature does not
	// cover it: use it for display only.
	Updated time.Time
}

type catalogRec struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Sum     string `json:"sum"`
	Pkgsize int64  `json:"pkgsize"`
}

func (r catalogRec) pkg() CatalogPackage {
	p := CatalogPackage{Name: r.Name, Version: r.Version, Path: r.Path, Sum: r.Sum, Size: r.Pkgsize}
	if h, ok := strings.CutPrefix(r.Sum, "1$"); ok && len(h) == 64 {
		p.SHA256 = h
	}
	return p
}

// catalogArchive reads one catalog archive and returns its payload and
// signature (nil when unsigned).
func catalogArchive(r io.Reader, archive, payload string) (content, sig []byte, kind string, mtime time.Time, err error) {
	tr, closeFn, err := pkgfile.OpenTar(r)
	if err != nil {
		return nil, nil, "", time.Time{}, err
	}
	defer closeFn()
	ents := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, "", time.Time{}, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidPackage, archive, err)
		}
		b, _ := io.ReadAll(io.LimitReader(tr, 256<<20))
		ents[h.Name] = b
		if h.Name == payload {
			mtime = h.ModTime
		}
	}
	content, ok := ents[payload]
	if !ok {
		return nil, nil, "", time.Time{}, fmt.Errorf("%w: %s lacks %s", pkgerr.ErrInvalidPackage, archive, payload)
	}
	switch {
	case ents["signature"] != nil:
		sig, kind = ents["signature"], "pubkey"
	case ents[payload+".sig"] != nil:
		sig, kind = ents[payload+".sig"], "fingerprints"
	}
	return content, sig, kind, mtime, nil
}

// ReadCatalog parses data.pkg and packagesite.pkg of one repository
// directory. With pk set, both must be signed and verify against it, or the
// result is an ErrSignature error; nothing unverified is returned then.
func ReadCatalog(data, site io.Reader, pk *rsa.PublicKey) (*Catalog, error) {
	c := &Catalog{}
	var dataBytes, siteBytes []byte
	for _, x := range []struct {
		r                io.Reader
		archive, payload string
	}{{data, "data.pkg", "data"}, {site, "packagesite.pkg", "packagesite.yaml"}} {
		content, sig, kind, mtime, err := catalogArchive(x.r, x.archive, x.payload)
		if err != nil {
			return nil, err
		}
		if pk != nil {
			if sig == nil {
				return nil, fmt.Errorf("%w: %s is not signed", pkgerr.ErrSignature, x.archive)
			}
			sum := sha256.Sum256(content)
			if err := VerifyRSA(pk, hex.EncodeToString(sum[:]), sig); err != nil {
				return nil, fmt.Errorf("%s: %w", x.archive, err)
			}
		}
		if x.payload == "data" {
			dataBytes = content
		} else {
			siteBytes, c.Signature, c.Updated = content, kind, mtime
		}
	}
	c.Verified = pk != nil
	var cat struct {
		Packages []catalogRec `json:"packages"`
	}
	if err := json.Unmarshal(dataBytes, &cat); err != nil {
		return nil, fmt.Errorf("%w: data: %v", pkgerr.ErrInvalidPackage, err)
	}
	for _, r := range cat.Packages {
		c.Packages = append(c.Packages, r.pkg())
	}
	for _, line := range bytes.Split(siteBytes, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r catalogRec
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%w: packagesite.yaml: %v", pkgerr.ErrInvalidPackage, err)
		}
		c.Site = append(c.Site, r.pkg())
	}
	return c, nil
}

// Same reports whether data and packagesite.yaml advertise the same packages
// with the same sums and sizes.
func (c *Catalog) Same() bool {
	key := func(p CatalogPackage) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", p.Name, p.Version, p.Path, p.Sum, p.Size)
	}
	a := map[string]int{}
	for _, p := range c.Packages {
		a[key(p)]++
	}
	for _, p := range c.Site {
		a[key(p)]--
	}
	for _, n := range a {
		if n != 0 {
			return false
		}
	}
	return true
}
