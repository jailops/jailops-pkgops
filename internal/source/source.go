// Package source is the read-through view of a published PkgOps site: it reads
// pkgops-index.json, the signed catalogs and the public key from the site over
// HTTPS and never writes. `pkgops serve --source URL` uses it so the admin UI
// needs no copy of the packages, no database and no second registry.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"jailops.net/pkgops/internal/fetch"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/pkgver"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/sdk"
)

// Source reads a published site.
type Source struct {
	Base string // https://pkg.example.org
	HTTP *http.Client
	// TTL is how long the index and the signature checks are cached (default 20 s).
	TTL time.Duration
	Now func() time.Time

	mu    sync.Mutex
	idx   *index.Index
	idxAt time.Time
	idxEr error
	sigs  map[string]sigInfo
	pub   []byte
	pubAt time.Time
	pubOK bool
}

type sigInfo struct {
	at       time.Time
	kind     string // pubkey, fingerprints, unsigned
	verified bool
	problem  string
	updated  time.Time
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 20 * time.Second
}

func (s *Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Source) get(ctx context.Context, rel string, limit int64) ([]byte, int, error) {
	u, err := url.Parse(strings.TrimRight(s.Base, "/") + "/")
	if err != nil {
		return nil, 0, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.JoinPath(strings.Split(rel, "/")...).String(), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return data, resp.StatusCode, err
}

// Index returns the published index. A failed refresh serves the last good
// index together with the error, so a short outage of the site does not blank
// the UI; with no good index the error is all there is.
func (s *Source) Index(ctx context.Context) (*index.Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx != nil && s.now().Sub(s.idxAt) < s.ttl() {
		return s.idx, nil
	}
	ix, _, err := fetch.FetchIndex(ctx, &fetch.Options{From: s.Base, HTTP: s.client()})
	if err != nil {
		s.idxEr = err
		if s.idx != nil {
			return s.idx, err
		}
		return nil, err
	}
	s.idx, s.idxAt, s.idxEr = ix, s.now(), nil
	return ix, nil
}

// Refresh drops the cache so the next Index call reads the site.
func (s *Source) Refresh() {
	s.mu.Lock()
	s.idxAt = time.Time{}
	s.sigs = nil
	s.mu.Unlock()
}

// PublicKeyPEM returns the site's pkgops.pub, nil when it has none.
func (s *Source) PublicKeyPEM() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pubOK && s.now().Sub(s.pubAt) < 5*time.Minute {
		return s.pub
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, code, err := s.get(ctx, "pkgops.pub", 1<<20)
	s.pubOK, s.pubAt = true, s.now()
	if err != nil || code != http.StatusOK {
		s.pub = nil
		return nil
	}
	s.pub = data
	return data
}

// signature inspects one ABI's packagesite.pkg: is it signed, and does the
// signature verify against the site's public key.
func (s *Source) signature(ctx context.Context, abi string) sigInfo {
	s.mu.Lock()
	if c, ok := s.sigs[abi]; ok && s.now().Sub(c.at) < 5*time.Minute {
		s.mu.Unlock()
		return c
	}
	s.mu.Unlock()
	si := sigInfo{at: s.now(), kind: "unsigned"}
	data, code, err := s.get(ctx, abi+"/latest/packagesite.pkg", 256<<20)
	switch {
	case err != nil:
		si.problem = err.Error()
	case code != http.StatusOK:
		si.problem = fmt.Sprintf("packagesite.pkg answered HTTP %d", code)
	default:
		si = s.inspectCatalog(data, si)
	}
	s.mu.Lock()
	if s.sigs == nil {
		s.sigs = map[string]sigInfo{}
	}
	s.sigs[abi] = si
	s.mu.Unlock()
	return si
}

func (s *Source) inspectCatalog(data []byte, si sigInfo) sigInfo {
	tr, closeFn, err := pkgfile.OpenTar(strings.NewReader(string(data)))
	if err != nil {
		si.problem = err.Error()
		return si
	}
	defer closeFn()
	ents := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(tr, 256<<20))
		ents[h.Name] = b
		if !h.ModTime.IsZero() && h.Name == "packagesite.yaml" {
			si.updated = h.ModTime
		}
	}
	content, ok := ents["packagesite.yaml"]
	if !ok {
		si.problem = "packagesite.pkg has no packagesite.yaml"
		return si
	}
	var sig []byte
	switch {
	case ents["signature"] != nil:
		si.kind, sig = "pubkey", ents["signature"]
	case ents["packagesite.yaml.sig"] != nil:
		si.kind, sig = "fingerprints", ents["packagesite.yaml.sig"]
	}
	if sig == nil {
		return si
	}
	if pem := s.PublicKeyPEM(); pem != nil {
		pk, err := repository.ParseRSAPublicKey(pem)
		if err == nil {
			sum := sha256.Sum256(content)
			if err := repository.VerifyRSA(pk, hex.EncodeToString(sum[:]), sig); err != nil {
				si.problem = "signature does not verify against pkgops.pub: " + err.Error()
			} else {
				si.verified = true
			}
		}
	}
	return si
}

// Status implements the registry status over the index and the catalogs.
func (s *Source) Status() (*sdk.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	st := &sdk.Status{Healthy: true, Channel: repository.DefaultChannel, ABIs: []sdk.ABIStatus{}, Signature: "unsigned"}
	if err != nil {
		st.Healthy = false
		st.Problems = append(st.Problems, "index: "+err.Error()+" (showing the last good copy)")
	}
	if pem := s.PublicKeyPEM(); pem != nil {
		st.Fingerprint = repository.Fingerprint(pem)
	}
	for _, abi := range ix.ABIs() {
		parsed, _ := pkgfile.ParseABI(abi)
		names := map[string]bool{}
		versions := 0
		for _, e := range ix.Packages {
			if e.ABI == abi {
				names[e.Name] = true
				versions++
			}
		}
		si := s.signature(ctx, abi)
		as := sdk.ABIStatus{ABI: abi, Arch: parsed.Arch, Channel: repository.DefaultChannel, Packages: len(names), Versions: versions,
			Updated: ix.Generated, Signature: si.kind, Consistent: si.problem == ""}
		if si.problem != "" {
			as.Problem = si.problem
			st.Healthy = false
			st.Problems = append(st.Problems, abi+": "+si.problem)
		}
		if si.kind != "unsigned" {
			st.Signature = si.kind
		}
		st.Packages += as.Packages
		st.Versions += as.Versions
		st.ABIs = append(st.ABIs, as)
	}
	st.Updated = ix.Generated
	return st, nil
}

func pkg(e index.Entry, latest bool) sdk.Package {
	a, _ := pkgfile.ParseABI(e.ABI)
	return sdk.Package{Name: e.Name, Version: e.Version, ABI: e.ABI, Arch: a.Arch, Comment: e.Comment, Size: e.Size, SHA256: e.SHA256,
		Path: "/" + e.File, Latest: latest, Modified: e.Built, MinRelease: e.MinRelease}
}

// Packages lists the indexed files of one ABI (all when empty).
func (s *Source) Packages(abi string) ([]sdk.Package, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	out := []sdk.Package{}
	for _, e := range ix.Packages {
		if abi != "" && e.ABI != abi {
			continue
		}
		n, _ := ix.Newest(e.ABI, e.Name)
		out = append(out, pkg(e, n.Version == e.Version))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ABI != out[j].ABI {
			return out[i].ABI < out[j].ABI
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return pkgver.Compare(out[i].Version, out[j].Version) > 0
	})
	return out, nil
}

// Detail returns what the index knows; the manifest's file list needs the
// package itself, which the read-through view does not download.
func (s *Source) Detail(abi, name, version string) (*sdk.Detail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	e, ok := ix.Find(abi, name, version)
	if !ok {
		return nil, fmt.Errorf("%w: %s-%s for %s", pkgerr.ErrNotFound, name, version, abi)
	}
	n, _ := ix.Newest(abi, name)
	return &sdk.Detail{Package: pkg(e, n.Version == version), Desc: e.Comment, Files: []sdk.FileEntry{}}, nil
}
