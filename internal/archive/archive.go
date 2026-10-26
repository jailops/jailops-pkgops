// Package archive keeps every published .pkg file permanently in GitHub
// Releases, the durable copy behind the GitHub Pages site (the live copy).
//
// Layout: one release per package version, tag "pkg-<name>-<version>", with
// one asset per ABI named "<name>-<version>-<ABI with : as _>.pkg" and an
// "archive.json" asset (the release's manifest: sha256 and size of every
// asset). Tags are unique, releases are listable through the API, and a
// version built for two ABIs is one release with two assets. Pruning a
// version from the Pages site never touches this archive; deleting from it is
// the separate, explicit [Archive.Delete].
package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jailops.net/pkgops/internal/fetch"
	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
)

const (
	// TagPrefix starts the tag of every archive release.
	TagPrefix = "pkg-"
	// ManifestAsset is the name of a release's manifest asset.
	ManifestAsset = "archive.json"
	// AuditTag is the release whose body is the log of archive deletions.
	AuditTag = "pkgops-archive-audit"
	// SchemaVersion of ManifestAsset.
	SchemaVersion = 1
)

// Archive is the GitHub Releases archive of one repository.
type Archive struct {
	GH  *ghapp.Client
	Log io.Writer
	Now func() time.Time
}

func (a *Archive) logf(f string, args ...any) {
	if a.Log != nil {
		fmt.Fprintf(a.Log, f+"\n", args...)
	}
}

func (a *Archive) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Tag returns the release tag of a package version.
func Tag(name, version string) string { return TagPrefix + name + "-" + version }

// AssetName returns the asset name of a package version for an ABI.
func AssetName(name, version, abi string) string {
	return name + "-" + version + "-" + strings.ReplaceAll(abi, ":", "_") + ".pkg"
}

// Item is one archived file.
type Item struct {
	ABI          string    `json:"abi"`
	Asset        string    `json:"asset"`
	SHA256       string    `json:"sha256"`
	Size         int64     `json:"size"`
	MinOSVersion int       `json:"min_osversion,omitempty"`
	Comment      string    `json:"comment,omitempty"`
	Built        time.Time `json:"built,omitzero"`
}

// Manifest is the content of a release's archive.json.
type Manifest struct {
	Schema   int    `json:"schema"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Packages []Item `json:"packages"`
}

// Record is an archived file together with where it lives.
type Record struct {
	Item
	Name       string
	Version    string
	ReleaseID  int64
	ReleaseURL string
	AssetID    int64
}

// Key is "abi name-version".
func (r Record) Key() string { return r.ABI + " " + r.Name + "-" + r.Version }

func validRef(name, version string) error {
	if !pkgfile.ValidName(name) || version == "" || strings.ContainsAny(version, "/\\ \t\n:~^?*[@{") || strings.Contains(version, "..") {
		return fmt.Errorf("%w: cannot archive %q %q", pkgerr.ErrInvalidSpec, name, version)
	}
	return nil
}

// PushResult counts what Push did.
type PushResult struct {
	Uploaded int
	Skipped  int
}

// Push uploads every package file listed in ix (resolved below root) that the
// archive does not hold yet. It is idempotent: a file already archived with
// the same sha256 is skipped; the same name-version with a different sha256 is
// an error and nothing is overwritten.
func (a *Archive) Push(ctx context.Context, root string, ix *index.Index) (*PushResult, error) {
	res := &PushResult{}
	byRel := map[string][]index.Entry{}
	for _, e := range ix.Packages {
		if err := validRef(e.Name, e.Version); err != nil {
			return nil, err
		}
		byRel[Tag(e.Name, e.Version)] = append(byRel[Tag(e.Name, e.Version)], e)
	}
	tags := make([]string, 0, len(byRel))
	for t := range byRel {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		entries := byRel[tag]
		rel, man, err := a.release(ctx, tag, entries[0].Name, entries[0].Version)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, e := range entries {
			asset := AssetName(e.Name, e.Version, e.ABI)
			if it, ok := man.find(e.ABI); ok {
				if it.SHA256 != e.SHA256 {
					return nil, fmt.Errorf("%w: %s is already archived with sha256 %s but the repository has %s; a published version is immutable, bump the version",
						pkgerr.ErrInvalidPackage, asset, short(it.SHA256), short(e.SHA256))
				}
				res.Skipped++
				a.logf("archived already  %s", asset)
				continue
			}
			// uploaded by an interrupted run, not in the manifest yet?
			if ra := findAsset(rel, asset); ra != nil {
				sum, err := a.assetSum(ctx, *ra)
				if err != nil {
					return nil, err
				}
				if sum != e.SHA256 {
					return nil, fmt.Errorf("%w: asset %s exists with sha256 %s, the repository has %s", pkgerr.ErrInvalidPackage, asset, short(sum), short(e.SHA256))
				}
				man.set(Item{ABI: e.ABI, Asset: asset, SHA256: e.SHA256, Size: e.Size, MinOSVersion: e.MinOSVersion, Comment: e.Comment, Built: e.Built})
				changed = true
				res.Skipped++
				continue
			}
			f, err := os.Open(filepath.Join(root, filepath.FromSlash(e.File)))
			if err != nil {
				return nil, err
			}
			sum, err := hashFile(f)
			if err == nil && sum != e.SHA256 {
				err = fmt.Errorf("%w: %s on disk has sha256 %s, the index says %s", pkgerr.ErrInvalidPackage, e.File, short(sum), short(e.SHA256))
			}
			if err == nil {
				_, err = f.Seek(0, io.SeekStart)
			}
			if err != nil {
				f.Close()
				return nil, err
			}
			_, err = a.GH.UploadAsset(ctx, rel, asset, f, e.Size, "application/octet-stream")
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("uploading %s: %w", asset, err)
			}
			man.set(Item{ABI: e.ABI, Asset: asset, SHA256: e.SHA256, Size: e.Size, MinOSVersion: e.MinOSVersion, Comment: e.Comment, Built: e.Built})
			changed = true
			res.Uploaded++
			a.logf("archived          %s", asset)
		}
		if changed {
			if err := a.writeManifest(ctx, rel, man); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

func (m *Manifest) find(abi string) (Item, bool) {
	for _, it := range m.Packages {
		if it.ABI == abi {
			return it, true
		}
	}
	return Item{}, false
}

func (m *Manifest) set(it Item) {
	for i := range m.Packages {
		if m.Packages[i].ABI == it.ABI {
			m.Packages[i] = it
			return
		}
	}
	m.Packages = append(m.Packages, it)
	sort.Slice(m.Packages, func(i, j int) bool { return m.Packages[i].ABI < m.Packages[j].ABI })
}

func findAsset(rel *ghapp.Release, name string) *ghapp.Asset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func hashFile(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// release returns the release of tag (creating it) and its manifest.
func (a *Archive) release(ctx context.Context, tag, name, version string) (*ghapp.Release, *Manifest, error) {
	rel, err := a.GH.GetReleaseByTag(ctx, tag)
	if errors.Is(err, ghapp.ErrNotFound) {
		body := fmt.Sprintf("%s %s: archived package files (see %s for their sha256).\n\nCreated by pkgops. Do not edit; the live repository is the GitHub Pages site.", name, version, ManifestAsset)
		rel, err = a.GH.CreateRelease(ctx, tag, name+" "+version, body, false)
	}
	if err != nil {
		return nil, nil, err
	}
	man := &Manifest{Schema: SchemaVersion, Name: name, Version: version}
	if ma := findAsset(rel, ManifestAsset); ma != nil {
		rc, err := a.GH.DownloadAsset(ctx, *ma)
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(data, man); err != nil {
			return nil, nil, fmt.Errorf("%w: %s of %s: %v", pkgerr.ErrInvalidPackage, ManifestAsset, tag, err)
		}
		if man.Schema > SchemaVersion {
			return nil, nil, fmt.Errorf("%w: %s of %s uses schema %d; upgrade pkgops", pkgerr.ErrInvalidPackage, ManifestAsset, tag, man.Schema)
		}
	}
	return rel, man, nil
}

func (a *Archive) writeManifest(ctx context.Context, rel *ghapp.Release, man *Manifest) error {
	man.Schema = SchemaVersion
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if old := findAsset(rel, ManifestAsset); old != nil {
		if err := a.GH.DeleteAsset(ctx, old.ID); err != nil {
			return err
		}
	}
	_, err = a.GH.UploadAsset(ctx, rel, ManifestAsset, bytes.NewReader(data), int64(len(data)), "application/json")
	if err != nil {
		return err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s %s: archived package files.\n\n| ABI | file | sha256 |\n|---|---|---|\n", man.Name, man.Version)
	for _, it := range man.Packages {
		fmt.Fprintf(&body, "| %s | %s | %s |\n", it.ABI, it.Asset, it.SHA256)
	}
	body.WriteString("\nCreated by pkgops. The live repository is the GitHub Pages site; deleting assets here is a separate, deliberate action.\n")
	return a.GH.UpdateReleaseBody(ctx, rel.ID, body.String())
}

// assetSum returns the sha256 of an asset, from GitHub's digest when it gives
// one, else by downloading it.
func (a *Archive) assetSum(ctx context.Context, as ghapp.Asset) (string, error) {
	if s, ok := strings.CutPrefix(as.Digest, "sha256:"); ok && len(s) == 64 {
		return s, nil
	}
	rc, err := a.GH.DownloadAsset(ctx, as)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return hashFile(rc)
}

// List returns every archived file, read from the manifests of the archive
// releases.
func (a *Archive) List(ctx context.Context) ([]Record, error) {
	rels, err := a.GH.ListReleases(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for i := range rels {
		rel := &rels[i]
		if !strings.HasPrefix(rel.TagName, TagPrefix) {
			continue
		}
		ma := findAsset(rel, ManifestAsset)
		if ma == nil {
			continue // no manifest: nothing was archived properly
		}
		rc, err := a.GH.DownloadAsset(ctx, *ma)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		var man Manifest
		if err := json.Unmarshal(data, &man); err != nil {
			return nil, fmt.Errorf("%w: %s of %s: %v", pkgerr.ErrInvalidPackage, ManifestAsset, rel.TagName, err)
		}
		for _, it := range man.Packages {
			r := Record{Item: it, Name: man.Name, Version: man.Version, ReleaseID: rel.ID, ReleaseURL: rel.HTMLURL}
			if as := findAsset(rel, it.Asset); as != nil {
				r.AssetID = as.ID
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// Fetch rebuilds a baseline in o.Into from the archive: every archived file
// not listed in o.Prune, verified against the archive manifests. Use it when
// the Pages baseline is missing or damaged.
func (a *Archive) Fetch(ctx context.Context, o fetch.Options) (*fetch.Result, error) {
	recs, err := a.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%w: the archive holds no packages", pkgerr.ErrNotFound)
	}
	ix := index.New()
	ix.Generator = "pkgops (restored from the archive)"
	ix.Generated = a.now().UTC()
	by := map[string]Record{}
	for _, r := range recs {
		abi, err := pkgfile.ParseABI(r.ABI)
		if err != nil {
			return nil, err
		}
		file := abi.String() + "/" + "latest" + "/All/" + r.Name + "-" + r.Version + ".pkg"
		ix.Packages = append(ix.Packages, index.Entry{ABI: r.ABI, Name: r.Name, Version: r.Version, File: file, SHA256: r.SHA256, Size: r.Size,
			Built: r.Built, MinOSVersion: r.MinOSVersion, MinRelease: pkgfile.OSVersionRelease(r.MinOSVersion), Comment: r.Comment})
		by[file] = r
	}
	res := &fetch.Result{Source: "releases"}
	get := func(ctx context.Context, e index.Entry, dst string) error {
		r := by[e.File]
		rel, err := a.GH.GetReleaseByTag(ctx, Tag(r.Name, r.Version))
		if err != nil {
			return err
		}
		as := findAsset(rel, r.Asset)
		if as == nil {
			return fmt.Errorf("%w: the archive manifest lists %s but the release has no such asset", pkgerr.ErrNotFound, r.Asset)
		}
		rc, err := a.GH.DownloadAsset(ctx, *as)
		if err != nil {
			return err
		}
		defer rc.Close()
		f, err := os.Create(dst) // no resume: assets are re-downloaded whole
		if err != nil {
			return err
		}
		n, err := io.Copy(f, rc)
		res.Bytes += n
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if err := fetch.Materialize(ctx, &o, ix, res, get); err != nil {
		return nil, err
	}
	return res, nil
}

// Report compares the Pages index with the archive.
type Report struct {
	// OnPagesNotArchived are live Pages files the archive does not hold.
	OnPagesNotArchived []string
	// ArchivedNotOnPages are archived files that are neither live on Pages nor
	// recorded as pruned (nor listed in the prune manifest): something dropped
	// them without the prune process.
	ArchivedNotOnPages []string
	// Mismatched are files whose sha256 differs between Pages and archive.
	Mismatched []string
	// PrunedStillArchived are pruned from Pages and kept in the archive (normal).
	PrunedStillArchived []string
	// PrunedNotArchived are pruned from Pages and not in the archive: gone for good.
	PrunedNotArchived []string
}

// OK reports whether the report holds no problem (the normal states are fine).
func (r *Report) OK() bool {
	return len(r.OnPagesNotArchived) == 0 && len(r.ArchivedNotOnPages) == 0 && len(r.Mismatched) == 0
}

// Verify compares a published index with the archive records. listed is a set
// of keys ("abi name-version") from the prune manifest that count as pruned.
func Verify(ix *index.Index, recs []Record, listed map[string]bool) *Report {
	rep := &Report{}
	arch := map[string]Record{}
	for _, r := range recs {
		arch[r.Key()] = r
	}
	live := map[string]bool{}
	for _, e := range ix.Packages {
		live[e.Key()] = true
		r, ok := arch[e.Key()]
		switch {
		case !ok:
			rep.OnPagesNotArchived = append(rep.OnPagesNotArchived, e.Key())
		case r.SHA256 != e.SHA256:
			rep.Mismatched = append(rep.Mismatched, e.Key())
		}
	}
	pruned := map[string]bool{}
	for _, p := range ix.Pruned {
		pruned[p.Key()] = true
		if _, ok := arch[p.Key()]; ok {
			rep.PrunedStillArchived = append(rep.PrunedStillArchived, p.Key())
		} else {
			rep.PrunedNotArchived = append(rep.PrunedNotArchived, p.Key())
		}
	}
	for k := range arch {
		if !live[k] && !pruned[k] && !listed[k] {
			rep.ArchivedNotOnPages = append(rep.ArchivedNotOnPages, k)
		}
	}
	for _, l := range []*[]string{&rep.OnPagesNotArchived, &rep.ArchivedNotOnPages, &rep.Mismatched, &rep.PrunedStillArchived, &rep.PrunedNotArchived} {
		sort.Strings(*l)
	}
	return rep
}

// Ref names one archived file to delete.
type Ref struct{ ABI, Name, Version string }

// Delete removes archived files. It is deliberately separate from pruning the
// Pages site, is never called by a Pages prune, and appends who did it, when
// and why to the audit release's body before it deletes anything. A release
// left without package assets is deleted with its tag.
func (a *Archive) Delete(ctx context.Context, refs []Ref, who, reason string) error {
	if who == "" || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: archive deletion needs who and a reason", pkgerr.ErrInvalidSpec)
	}
	recs, err := a.List(ctx)
	if err != nil {
		return err
	}
	by := map[string]Record{}
	for _, r := range recs {
		by[r.Key()] = r
	}
	var todo []Record
	for _, ref := range refs {
		r, ok := by[ref.ABI+" "+ref.Name+"-"+ref.Version]
		if !ok {
			return fmt.Errorf("%w: %s %s-%s is not in the archive", pkgerr.ErrNotFound, ref.ABI, ref.Name, ref.Version)
		}
		todo = append(todo, r)
	}
	if err := a.audit(ctx, todo, who, reason); err != nil {
		return fmt.Errorf("writing the audit record (nothing was deleted): %w", err)
	}
	for _, r := range todo {
		rel, err := a.GH.GetReleaseByTag(ctx, Tag(r.Name, r.Version))
		if err != nil {
			return err
		}
		man := &Manifest{Schema: SchemaVersion, Name: r.Name, Version: r.Version}
		if ma := findAsset(rel, ManifestAsset); ma != nil {
			rc, err := a.GH.DownloadAsset(ctx, *ma)
			if err != nil {
				return err
			}
			data, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
			rc.Close()
			_ = json.Unmarshal(data, man)
		}
		if as := findAsset(rel, r.Asset); as != nil {
			if err := a.GH.DeleteAsset(ctx, as.ID); err != nil {
				return err
			}
		}
		var left []Item
		for _, it := range man.Packages {
			if it.ABI != r.ABI {
				left = append(left, it)
			}
		}
		man.Packages = left
		if len(left) == 0 {
			if err := a.GH.DeleteRelease(ctx, rel.ID); err != nil {
				return err
			}
			if err := a.GH.DeleteTag(ctx, rel.TagName); err != nil && !errors.Is(err, ghapp.ErrNotFound) {
				return err
			}
		} else {
			fresh, err := a.GH.GetReleaseByTag(ctx, rel.TagName)
			if err != nil {
				return err
			}
			if err := a.writeManifest(ctx, fresh, man); err != nil {
				return err
			}
		}
		a.logf("archive: deleted %s", r.Asset)
	}
	return nil
}

func (a *Archive) audit(ctx context.Context, recs []Record, who, reason string) error {
	rel, err := a.GH.GetReleaseByTag(ctx, AuditTag)
	if errors.Is(err, ghapp.ErrNotFound) {
		rel, err = a.GH.CreateRelease(ctx, AuditTag, "pkgops archive audit log", "Deletions from the package archive, newest last.\n", true)
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(rel.Body, "\n"))
	b.WriteString("\n")
	for _, r := range recs {
		fmt.Fprintf(&b, "- %s %s deleted %s %s-%s (sha256 %s): %s\n", a.now().UTC().Format(time.RFC3339), oneLine(who), r.ABI, r.Name, r.Version, r.SHA256, oneLine(reason))
	}
	return a.GH.UpdateReleaseBody(ctx, rel.ID, b.String())
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
