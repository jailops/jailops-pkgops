// Package registry is the state machine behind `pkgops serve`: uploads,
// listing, deletion, retention and repository status over one Repository
// directory. It holds no state of its own besides a verification cache; the
// repository directory is the database. It knows nothing about HTTP.
package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/sdk"
)

// Registry operates on a repository tree.
type Registry struct {
	Repo *repository.Repository
	// Keep, when > 0, retains only the newest Keep versions per package.
	Keep int
	// Uploads reports whether writes are enabled (informational for status).
	Uploads bool

	mu    sync.Mutex
	cache map[string]cachedCheck
}

type cachedCheck struct {
	key string
	err error
}

// New returns a registry over repo.
func New(repo *repository.Repository, keep int, uploads bool) *Registry {
	return &Registry{Repo: repo, Keep: keep, Uploads: uploads, cache: map[string]cachedCheck{}}
}

func (r *Registry) channel() string {
	if r.Repo.Channel == "" {
		return repository.DefaultChannel
	}
	return r.Repo.Channel
}

// URLPath is the path of a package file relative to the registry root.
func (r *Registry) URLPath(abi, filename string) string {
	return "/" + abi + "/" + r.channel() + "/All/" + filename
}

// Upload adds the package file, re-indexes and signs, then applies retention.
func (r *Registry) Upload(ctx context.Context, file string) (*pkgfile.Info, []string, error) {
	infos, err := r.Repo.Update(ctx, file)
	if err != nil {
		return nil, nil, err
	}
	info := infos[0]
	var pruned []string
	if r.Keep > 0 {
		pruned, err = r.Repo.Prune(ctx, info.ABI, r.Keep)
		if err != nil {
			return info, pruned, fmt.Errorf("retention: %w", err)
		}
	}
	return info, pruned, nil
}

// Delete removes one package version; the catalog stops advertising it before
// the file disappears.
func (r *Registry) Delete(ctx context.Context, abi, name, version string) error {
	return r.Repo.Delete(ctx, abi, name, version)
}

func toPackage(r *Registry, i *pkgfile.Info, latest bool, mod time.Time) sdk.Package {
	return sdk.Package{
		Name: i.Name, Version: i.Version, ABI: i.ABI, Arch: i.Arch, Origin: i.Origin, Comment: i.Comment,
		Maintainer: i.Maintainer, WWW: i.WWW, Licenses: i.Licenses, Size: i.Size, SHA256: i.SHA256,
		Flatsize: i.Flatsize, Path: r.URLPath(i.ABI, i.Filename()), Latest: latest, Modified: mod,
		MinRelease: pkgfile.OSVersionRelease(i.MinOSVersion()),
	}
}

// Packages lists package files of one ABI (all ABIs when abi is empty), sorted
// by name then version descending, with Latest set for the advertised version.
func (r *Registry) Packages(abi string) ([]sdk.Package, error) {
	abis := []string{abi}
	if abi == "" {
		var err error
		if abis, err = r.Repo.ABIs(); err != nil {
			return nil, err
		}
	}
	out := []sdk.Package{}
	for _, a := range abis {
		infos, _, err := r.Repo.Scan(a) // unreadable files are reported by Status
		if err != nil {
			if abi == "" {
				continue
			}
			return nil, err
		}
		newest := map[string]string{}
		for _, i := range infos {
			if v, ok := newest[i.Name]; !ok || repository.CompareVersions(i.Version, v) > 0 {
				newest[i.Name] = i.Version
			}
		}
		for _, i := range infos {
			var mod time.Time
			if st, err := os.Stat(filepath.Join(r.Repo.Dir(a), "All", i.Filename())); err == nil {
				mod = st.ModTime().UTC()
			}
			out = append(out, toPackage(r, i, newest[i.Name] == i.Version, mod))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ABI != out[j].ABI {
			return out[i].ABI < out[j].ABI
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return repository.CompareVersions(out[i].Version, out[j].Version) > 0
	})
	return out, nil
}

// Detail returns one package with its manifest content.
func (r *Registry) Detail(abi, name, version string) (*sdk.Detail, error) {
	if _, err := pkgfile.ParseABI(abi); err != nil {
		return nil, err
	}
	if !pkgfile.ValidName(name) || version == "" || filepath.Base(version) != version {
		return nil, fmt.Errorf("%w: bad package name or version", pkgerr.ErrInvalidSpec)
	}
	file := filepath.Join(r.Repo.Dir(abi), "All", name+"-"+version+".pkg")
	m, err := pkgfile.ReadManifest(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s-%s for %s", pkgerr.ErrNotFound, name, version, abi)
		}
		return nil, err
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	sum, size, err := fileInfo(file)
	if err != nil {
		return nil, err
	}
	m.Info.SHA256, m.Info.Size = sum, size
	all, err := r.Packages(abi)
	if err != nil {
		return nil, err
	}
	latest := false
	for _, p := range all {
		if p.Name == name && p.Version == version {
			latest = p.Latest
		}
	}
	d := &sdk.Detail{Package: toPackage(r, m.Info, latest, st.ModTime().UTC()), Desc: m.Desc, Prefix: m.Prefix,
		Categories: m.Categories, Users: m.Users, Groups: m.Groups, Scripts: m.Scripts, Files: []sdk.FileEntry{}}
	for _, dep := range m.Deps {
		d.Deps = append(d.Deps, sdk.Dep{Name: dep.Name, Origin: dep.Origin, Version: dep.Version})
	}
	for _, f := range m.Files {
		d.Files = append(d.Files, sdk.FileEntry{Path: f.Path, Mode: f.Mode, Owner: f.Owner, Group: f.Group, Symlink: f.Symlink, Config: f.Config})
	}
	for _, f := range m.Directories {
		d.Directories = append(d.Directories, sdk.FileEntry{Path: f.Path, Mode: f.Mode, Owner: f.Owner, Group: f.Group})
	}
	return d, nil
}

// PublicKeyPEM returns the PEM of the key catalogs are signed with, or nil for
// an unsigned repository or a signer that is not an RSASigner.
func (r *Registry) PublicKeyPEM() []byte {
	s, ok := r.Repo.Signer.(*repository.RSASigner)
	if !ok || s == nil || s.Key == nil {
		return nil
	}
	pem, err := repository.PublicKeyPEM(&s.Key.PublicKey)
	if err != nil {
		return nil
	}
	return pem
}

func (r *Registry) signatureKind() string {
	s, ok := r.Repo.Signer.(*repository.RSASigner)
	switch {
	case r.Repo.Signer == nil:
		return "unsigned"
	case ok && s.Mode == repository.SignFingerprints:
		return "fingerprints"
	}
	return "pubkey"
}

// Status verifies every ABI's repository the way a client would (signature,
// both catalogs, every advertised package present with matching checksum) and
// summarises it. Verification reads all packages, so results are cached until a
// catalog file changes.
func (r *Registry) Status() (*sdk.Status, error) {
	abis, err := r.Repo.ABIs()
	if err != nil {
		return nil, err
	}
	pem := r.PublicKeyPEM()
	st := &sdk.Status{Healthy: true, Signature: r.signatureKind(), Uploads: r.Uploads, Keep: r.Keep,
		Channel: r.channel(), ABIs: []sdk.ABIStatus{}}
	if pem != nil {
		st.Fingerprint = repository.Fingerprint(pem)
	}
	for _, a := range abis {
		parsed, _ := pkgfile.ParseABI(a)
		infos, bad, err := r.Repo.Scan(a)
		if err != nil {
			return nil, err
		}
		names := map[string]bool{}
		for _, i := range infos {
			names[i.Name] = true
		}
		as := sdk.ABIStatus{ABI: a, Arch: parsed.Arch, Channel: r.channel(), Packages: len(names), Versions: len(infos),
			Signature: st.Signature, Consistent: true}
		dir := r.Repo.Dir(a)
		key := ""
		for _, f := range []string{"data.pkg", "packagesite.pkg", "meta.conf"} {
			if fi, err := os.Stat(filepath.Join(dir, f)); err == nil {
				key += fmt.Sprintf("%s:%d:%d;", f, fi.Size(), fi.ModTime().UnixNano())
				if f == "data.pkg" {
					as.Updated = fi.ModTime().UTC()
				}
			}
		}
		key += fmt.Sprintf("n=%d,bad=%d", len(infos), len(bad))
		r.mu.Lock()
		c, ok := r.cache[a]
		r.mu.Unlock()
		if !ok || c.key != key {
			_, verr := repository.VerifyRepository(dir, pem)
			c = cachedCheck{key: key, err: verr}
			r.mu.Lock()
			r.cache[a] = c
			r.mu.Unlock()
		}
		if len(bad) > 0 && c.err == nil {
			c = cachedCheck{key: key, err: fmt.Errorf("unreadable package file %s: %v", bad[0].File, bad[0].Err)}
		}
		if c.err != nil {
			as.Consistent = false
			as.Problem = c.err.Error()
			st.Healthy = false
			st.Problems = append(st.Problems, a+": "+c.err.Error())
		}
		st.Packages += as.Packages
		st.Versions += as.Versions
		if as.Updated.After(st.Updated) {
			st.Updated = as.Updated
		}
		st.ABIs = append(st.ABIs, as)
	}
	return st, nil
}
