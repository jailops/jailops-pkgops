// Package source is the read-through view of a published PkgOps site: it reads
// pkgops-index.json, the signed catalogs and the public key from the site over
// HTTPS and never writes. A key read from the site itself is a weaker trust
// than one from the operator's configuration; Status says which it used. `pkgops serve --source URL` uses it so the admin UI
// needs no copy of the packages, no database and no second registry.
package source

import (
	"bytes"
	"context"
	"crypto/rsa"
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

	// PubKey is the PEM public key the catalogs must verify against, from
	// the operator's own configuration. Without it the key is read from the
	// site (KeyPaths), which only shows that the site agrees with itself.
	PubKey []byte
	// KeyFingerprint pins the key read from the site: the sha256 of the
	// key file. A site key with another sha256 is invalid.
	KeyFingerprint string
	// KeyPaths are tried in order on the site (default DefaultKeyPaths).
	KeyPaths []string
	// RequireVerified makes the repository unhealthy unless every catalog
	// verifies against a key. A configured PubKey or KeyFingerprint implies it.
	RequireVerified bool

	mu    sync.Mutex
	idx   *index.Index
	idxAt time.Time
	idxEr error
	sigs  map[string]sigInfo
	key   *keyInfo
}

// DefaultKeyPaths are the names the documented setups publish the public
// key under, relative to the site root.
var DefaultKeyPaths = []string{"pkgops.pub", "jailops.pub", "repo.pub"}

// Verification states (sdk.Status.Verification).
const (
	StateVerified   = "verified"
	StateSigned     = "signed"
	StateUnverified = "unverified"
	StateInvalid    = "invalid"
	StateUnsigned   = "unsigned"
)

type keyInfo struct {
	at      time.Time
	pem     []byte
	pk      *rsa.PublicKey
	source  string // configured or site
	url     string // where the site publishes it
	problem string // the key is unusable (bad PEM, wrong fingerprint)
}

type sigInfo struct {
	at      time.Time
	kind    string // pubkey, fingerprints, unsigned
	state   string // see the State constants
	problem string
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
	s.key = nil
	s.mu.Unlock()
}

// trustedKey resolves the key catalogs are checked against: the configured
// one, else the first of KeyPaths the site has (pinned by KeyFingerprint when
// set). The result is cached for 5 minutes.
func (s *Source) trustedKey(ctx context.Context) keyInfo {
	s.mu.Lock()
	if s.key != nil && s.now().Sub(s.key.at) < 5*time.Minute {
		k := *s.key
		s.mu.Unlock()
		return k
	}
	s.mu.Unlock()
	k := keyInfo{at: s.now()}
	if s.PubKey != nil {
		k.pem, k.source = s.PubKey, "configured"
	} else {
		paths := s.KeyPaths
		if len(paths) == 0 {
			paths = DefaultKeyPaths
		}
		for _, p := range paths {
			data, code, err := s.get(ctx, p, 1<<20)
			if err == nil && code == http.StatusOK && len(data) > 0 {
				k.pem, k.source, k.url = data, "site", s.url(p)
				break
			}
		}
		switch {
		case k.pem == nil && s.KeyFingerprint != "":
			k.problem = fmt.Sprintf("no public key on the site (tried %s) to match the configured fingerprint", strings.Join(paths, ", "))
		case k.pem != nil && s.KeyFingerprint != "":
			if fp := repository.Fingerprint(k.pem); !strings.EqualFold(fp, s.KeyFingerprint) {
				k.problem = fmt.Sprintf("the site's key %s has fingerprint %s, not the configured %s", k.url, fp, s.KeyFingerprint)
			} else {
				k.source = "configured"
			}
		}
	}
	if k.pem != nil && k.problem == "" {
		pk, err := repository.ParseRSAPublicKey(k.pem)
		if err != nil {
			k.problem = "the public key is not usable: " + err.Error()
		} else {
			k.pk = pk
		}
	}
	s.mu.Lock()
	s.key = &k
	s.mu.Unlock()
	return k
}

func (s *Source) url(rel string) string {
	u, err := url.Parse(strings.TrimRight(s.Base, "/") + "/")
	if err != nil {
		return ""
	}
	return u.JoinPath(strings.Split(rel, "/")...).String()
}

// PublicKeyPEM returns the key catalogs are checked against (configured, or
// found on the site), nil when there is none or it is unusable.
func (s *Source) PublicKeyPEM() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	k := s.trustedKey(ctx)
	if k.pk == nil {
		return nil
	}
	return k.pem
}

// KeyURL is where the site publishes its public key ("" when it has none).
func (s *Source) KeyURL() string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.trustedKey(ctx).url
}

func (s *Source) required() bool {
	return s.RequireVerified || s.PubKey != nil || s.KeyFingerprint != ""
}

// signature checks one ABI's two catalogs, data.pkg and packagesite.pkg: are
// they signed, and do both signatures verify against the trusted key.
func (s *Source) signature(ctx context.Context, abi string) sigInfo {
	s.mu.Lock()
	if c, ok := s.sigs[abi]; ok && s.now().Sub(c.at) < 5*time.Minute {
		s.mu.Unlock()
		return c
	}
	s.mu.Unlock()
	si := sigInfo{at: s.now(), kind: "unsigned", state: StateUnsigned}
	var raw [2][]byte
	for n, f := range []string{"data.pkg", "packagesite.pkg"} {
		data, code, err := s.get(ctx, abi+"/latest/"+f, 256<<20)
		switch {
		case err != nil:
			si.problem = err.Error()
		case code != http.StatusOK:
			si.problem = fmt.Sprintf("%s answered HTTP %d", f, code)
		}
		if si.problem != "" {
			break
		}
		raw[n] = data
	}
	if si.problem == "" {
		si = s.inspectCatalogs(ctx, raw[0], raw[1], si)
	}
	s.mu.Lock()
	if s.sigs == nil {
		s.sigs = map[string]sigInfo{}
	}
	s.sigs[abi] = si
	s.mu.Unlock()
	return si
}

func (s *Source) inspectCatalogs(ctx context.Context, data, site []byte, si sigInfo) sigInfo {
	cat, err := repository.ReadCatalog(bytes.NewReader(data), bytes.NewReader(site), nil)
	if err != nil {
		si.problem = err.Error()
		return si
	}
	if cat.Signature == "" {
		return si
	}
	si.kind, si.state = cat.Signature, StateUnverified
	k := s.trustedKey(ctx)
	switch {
	case k.problem != "":
		si.state, si.problem = StateInvalid, k.problem
	case k.pk == nil:
		if s.required() {
			si.problem = "the catalogs are signed but there is no public key to verify them with"
		}
	default:
		if _, err := repository.ReadCatalog(bytes.NewReader(data), bytes.NewReader(site), k.pk); err != nil {
			si.state, si.problem = StateInvalid, "signature does not verify against the public key: "+err.Error()
		} else {
			si.state = StateVerified
		}
	}
	return si
}

// worse orders the states from good to bad for the overall verdict.
var worse = map[string]int{StateVerified: 0, StateSigned: 1, StateUnverified: 2, StateUnsigned: 3, StateInvalid: 4}

// Status implements the registry status over the index and the catalogs.
func (s *Source) Status() (*sdk.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	st := &sdk.Status{Healthy: true, Channel: repository.DefaultChannel, ABIs: []sdk.ABIStatus{}, Signature: "unsigned", Verification: StateUnsigned}
	if err != nil {
		st.Healthy = false
		st.Problems = append(st.Problems, "index: "+err.Error()+" (showing the last good copy)")
	}
	k := s.trustedKey(ctx)
	if k.pk != nil {
		st.Fingerprint = repository.Fingerprint(k.pem)
		st.KeySource = k.source
	}
	first := true
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
			Updated: ix.Generated, Signature: si.kind, Consistent: si.problem == "", Verification: si.state}
		if si.problem != "" {
			as.Problem = si.problem
			st.Healthy = false
			st.Problems = append(st.Problems, abi+": "+si.problem)
		}
		if si.kind != "unsigned" {
			st.Signature = si.kind
		}
		if first || worse[si.state] > worse[st.Verification] {
			st.Verification = si.state
		}
		first = false
		st.Packages += as.Packages
		st.Versions += as.Versions
		st.ABIs = append(st.ABIs, as)
	}
	if st.Verification == StateUnsigned && s.required() && len(st.ABIs) > 0 {
		st.Healthy = false
		st.Problems = append(st.Problems, "the catalogs are not signed, and this server requires verified signatures")
	}
	switch {
	case st.Verification == StateUnverified:
		st.Notes = append(st.Notes, "The catalogs are signed, but the signatures were not checked: no public key was found on the site or configured (--pubkey).")
	case st.Verification == StateVerified && st.KeySource == "site":
		st.Notes = append(st.Notes, "The public key was read from the same site as the catalogs. A verified signature then only shows that the site agrees with itself, not that it was signed by you: configure the key (--pubkey) or its fingerprint (--key-fingerprint) for that.")
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
