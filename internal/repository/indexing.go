package repository

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/prune"
)

// applyPruneManifest adds to gone the files of abi that the prune manifest
// lists and that exist, after checking the manifest's newest-version rule
// against everything that is in All/ now.
func (r *Repository) applyPruneManifest(abi string, all []*Info, gone map[string]string) error {
	if r.PruneManifest == nil || len(r.PruneManifest.Entries) == 0 {
		return nil
	}
	cur := index.New()
	for _, i := range all {
		if _, ex := gone[i.Filename()]; ex {
			continue
		}
		cur.Packages = append(cur.Packages, index.Entry{ABI: abi, Name: i.Name, Version: i.Version, File: "x", SHA256: strings.Repeat("0", 64)})
	}
	var mine prune.Manifest
	for _, e := range r.PruneManifest.Entries {
		if e.ABI == abi {
			mine.Entries = append(mine.Entries, e)
		}
	}
	if err := mine.Check(cur); err != nil {
		return err
	}
	for _, e := range mine.Entries {
		file := e.Name + "-" + e.Version + ".pkg"
		for _, i := range all {
			if i.Filename() == file {
				why := e.Reason
				if why == "" {
					why = "prune manifest"
				}
				gone[file] = why
			}
		}
	}
	return nil
}

// nextIndex builds the index that goes with a catalog just written for abi:
// the other ABIs unchanged, abi from the files that stay, and every file that
// left (gone) recorded as pruned.
func (r *Repository) nextIndex(prev *index.Index, abi string, all []*Info, gone map[string]string, now time.Time) *index.Index {
	next := index.New()
	next.Generator = "pkgops"
	next.Generated = now.UTC()
	next.Pruned = append(next.Pruned, prev.Pruned...)
	old := map[string]index.Entry{}
	for _, e := range prev.Packages {
		if e.ABI != abi {
			next.Packages = append(next.Packages, e)
			continue
		}
		old[e.Name+"-"+e.Version] = e
	}
	for _, i := range all {
		if why, ok := gone[i.Filename()]; ok {
			key := i.Name + "-" + i.Version
			r.markPruned(next, abi, i.Name, i.Version, i, why, now)
			if o, had := old[key]; had {
				for n := range next.Pruned {
					if p := &next.Pruned[n]; p.ABI == abi && p.Name == i.Name && p.Version == i.Version && p.Built.IsZero() {
						p.Built = o.Built
					}
				}
			}
			delete(old, key)
			continue
		}
		e := index.Entry{
			ABI: abi, Name: i.Name, Version: i.Version,
			File:   abi + "/" + r.channel() + "/All/" + i.Filename(),
			SHA256: i.SHA256, Size: i.Size, Built: now.UTC(),
			MinOSVersion: i.MinOSVersion(), MinRelease: pkgfile.OSVersionRelease(i.MinOSVersion()),
			Comment: i.Comment,
		}
		if o, ok := old[i.Name+"-"+i.Version]; ok && !o.Built.IsZero() {
			e.Built = o.Built // first-seen time survives re-indexing
		}
		delete(old, i.Name+"-"+i.Version)
		next.Packages = append(next.Packages, e)
	}
	// indexed files that are gone from disk and not in `all`: only possible
	// for explicit deletes whose file is already removed
	for k, e := range old {
		why := gone[k+".pkg"]
		if why == "" {
			why = "removed"
		}
		next.Pruned = append(next.Pruned, index.Pruned{ABI: abi, Name: e.Name, Version: e.Version, SHA256: e.SHA256,
			Size: e.Size, Built: e.Built, Reason: why, PrunedAt: now.UTC()})
	}
	next.Sort()
	return next
}

// markPruned records abi name-version in ix.Pruned (once) and drops it from ix.Packages.
func (r *Repository) markPruned(ix *index.Index, abi, name, version string, info *Info, reason string, now time.Time) {
	p := index.Pruned{ABI: abi, Name: name, Version: version, Reason: reason, PrunedAt: now.UTC()}
	pk := ix.Packages[:0]
	for _, e := range ix.Packages {
		if e.ABI == abi && e.Name == name && e.Version == version {
			p.SHA256, p.Size, p.Built = e.SHA256, e.Size, e.Built
			continue
		}
		pk = append(pk, e)
	}
	ix.Packages = pk
	if info != nil && p.SHA256 == "" {
		p.SHA256, p.Size = info.SHA256, info.Size
	}
	if _, ok := ix.FindPruned(abi, name, version); ok {
		return
	}
	ix.Pruned = append(ix.Pruned, p)
}

// Index returns the index of the repository root (an empty one when none
// has been written yet).
func (r *Repository) Index() (*index.Index, error) {
	ix, err := index.Read(r.Root)
	if err != nil {
		return nil, err
	}
	if ix == nil {
		ix = index.New()
	}
	return ix, nil
}

// ErrNoIndex is returned by operations that need an index to compare against.
var ErrNoIndex = fmt.Errorf("%w: no %s", pkgerr.ErrNotFound, index.FileName)

// ABIsInIndex lists ABIs with packages in the index, sorted.
func ABIsInIndex(ix *index.Index) []string {
	a := ix.ABIs()
	sort.Strings(a)
	return a
}
