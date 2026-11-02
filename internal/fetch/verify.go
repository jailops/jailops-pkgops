package fetch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/repository"
)

// verifyPrevious checks the published index against the signed catalogs of
// the site, verified with o.TrustedKey, before anything is downloaded. The
// pkg catalog advertises only the newest version of each package, and that is
// the only part of a baseline the next `pkgops repo` signs again. So:
//
//   - both catalogs of every ABI in scope must verify against the key;
//   - every package they advertise must be in the index with the same file,
//     sha256 and size (the download is then checked against the signed hash);
//   - an index entry for a name the catalog does not advertise, or a version
//     at or above the advertised one, is refused: it would be signed next;
//   - older versions are history that no signature covers: they are checked
//     against the index only, as before, and counted as Unsigned;
//   - a prune manifest that removes the advertised version while older ones
//     stay is refused, since an unsigned version would become the newest.
//
// Nothing is written before this returns nil.
func verifyPrevious(ctx context.Context, o *Options, ix *index.Index, res *Result) error {
	pk, err := repository.ParseRSAPublicKey(o.TrustedKey)
	if err != nil {
		return fmt.Errorf("trusted key: %w", err)
	}
	fp := repository.Fingerprint(o.TrustedKey)
	want := map[string]bool{}
	for _, a := range o.ABIs {
		want[a] = true
	}
	// entries per ABI, and the channel directory they live in
	type abiSet struct {
		channel string
		entries []index.Entry
	}
	abis := map[string]*abiSet{}
	for _, a := range o.ABIs {
		abis[a] = &abiSet{channel: repository.DefaultChannel}
	}
	for _, e := range ix.Packages {
		if len(want) > 0 && !want[e.ABI] {
			continue
		}
		parts := strings.Split(e.File, "/")
		if len(parts) != 4 || parts[0] != e.ABI || parts[2] != "All" || parts[3] != e.Name+"-"+e.Version+".pkg" {
			return fmt.Errorf("%w: %s lists %s at %q, not at ABI/CHANNEL/All/NAME-VERSION.pkg; refusing an index that the signed catalog cannot vouch for",
				pkgerr.ErrSignature, index.FileName, e.Key(), e.File)
		}
		s := abis[e.ABI]
		if s == nil {
			s = &abiSet{channel: parts[1]}
			abis[e.ABI] = s
		}
		if len(s.entries) > 0 && s.channel != parts[1] {
			return fmt.Errorf("%w: %s mixes channels %q and %q for %s", pkgerr.ErrSignature, index.FileName, s.channel, parts[1], e.ABI)
		}
		s.channel = parts[1]
		s.entries = append(s.entries, e)
	}
	names := make([]string, 0, len(abis))
	for a := range abis {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, abi := range names {
		s := abis[abi]
		dir := abi + "/" + s.channel
		var raw [2][]byte
		missing := false
		for n, f := range []string{"data.pkg", "packagesite.pkg"} {
			u, err := join(o.From, dir+"/"+f)
			if err != nil {
				return err
			}
			b, code, err := getAll(ctx, o, u, 256<<20)
			switch {
			case err != nil:
				return fmt.Errorf("fetching %s: %w", u, err)
			case code == http.StatusNotFound:
				missing = true
			case code != http.StatusOK:
				return fmt.Errorf("fetching %s: HTTP %d", u, code)
			}
			raw[n] = b
		}
		if missing {
			if len(s.entries) == 0 {
				o.logf("signed %s: no catalog and nothing indexed", dir)
				continue
			}
			return fmt.Errorf("%w: %s lists %d package(s) for %s but the site has no signed catalog there; refusing an unsigned baseline",
				pkgerr.ErrSignature, index.FileName, len(s.entries), abi)
		}
		cat, err := repository.ReadCatalog(bytes.NewReader(raw[0]), bytes.NewReader(raw[1]), pk)
		if err != nil {
			return fmt.Errorf("%s: the published catalogs do not verify against the trusted key %s: %w", dir, fp[:16], err)
		}
		if !cat.Same() {
			return fmt.Errorf("%w: %s: data.pkg and packagesite.pkg advertise different packages", pkgerr.ErrSignature, dir)
		}
		signed := map[string]repository.CatalogPackage{}
		for _, p := range cat.Packages {
			if _, dup := signed[p.Name]; dup {
				return fmt.Errorf("%w: %s: the catalog advertises %s twice", pkgerr.ErrSignature, dir, p.Name)
			}
			if p.SHA256 == "" || p.Path != "All/"+p.Name+"-"+p.Version+".pkg" {
				return fmt.Errorf("%w: %s: catalog entry %s-%s (path %q, sum %q) is not one pkgops can compare", pkgerr.ErrSignature, dir, p.Name, p.Version, p.Path, p.Sum)
			}
			signed[p.Name] = p
		}
		for _, p := range cat.Packages {
			e, ok := ix.Find(abi, p.Name, p.Version)
			if !ok {
				if _, pruned := o.Prune.Has(abi, p.Name, p.Version); pruned {
					continue // leaves now; what would replace it is checked below
				}
				return fmt.Errorf("%w: the signed catalog of %s advertises %s-%s but %s does not list it", pkgerr.ErrSignature, abi, p.Name, p.Version, index.FileName)
			}
			if e.File != dir+"/"+p.Path || e.SHA256 != p.SHA256 || e.Size != p.Size {
				return fmt.Errorf("%w: %s says %s is %s (%d bytes), the signed catalog says %s (%d bytes); the site is damaged or tampered with",
					pkgerr.ErrSignature, index.FileName, e.File, short(e.SHA256), e.Size, short(p.SHA256), p.Size)
			}
		}
		for _, e := range s.entries {
			p, ok := signed[e.Name]
			switch {
			case !ok:
				return fmt.Errorf("%w: %s lists %s, a package the signed catalog of %s does not advertise; refusing it", pkgerr.ErrSignature, index.FileName, e.Key(), abi)
			case p.Version == e.Version:
				res.Signed++
				continue
			case repository.CompareVersions(e.Version, p.Version) >= 0:
				return fmt.Errorf("%w: %s lists %s, newer than %s-%s that the signed catalog advertises; refusing it", pkgerr.ErrSignature, index.FileName, e.Key(), p.Name, p.Version)
			}
			if _, pruned := o.Prune.Has(abi, e.Name, e.Version); pruned {
				continue
			}
			if _, gone := o.Prune.Has(abi, p.Name, p.Version); gone {
				return fmt.Errorf("%w: the prune manifest removes %s %s-%s, the newest signed version, and %s would then be advertised although no signature covers it; prune every version of %s, or publish a new version instead",
					pkgerr.ErrSignature, abi, p.Name, p.Version, e.Key(), p.Name)
			}
			res.Unsigned++
		}
		o.logf("signed %s: %d package(s) verified with key %s", dir, len(cat.Packages), fp[:16])
	}
	o.logf("verified baseline: %d file(s) match the signed catalogs; %d older version(s) are covered by no signature and are checked against %s only",
		res.Signed, res.Unsigned, index.FileName)
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
