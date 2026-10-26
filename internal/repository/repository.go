package repository

import (
	"jailops.net/pkgops/internal/fsutil"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/ordjson"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/prune"

	"archive/tar"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// DefaultChannel is the directory below the ABI directory that holds a
// repository ("latest" mirrors pkg.FreeBSD.org's naming).
const DefaultChannel = "latest"

// metaConf is the repository descriptor `pkg repo` writes for format version 2.
const metaConf = `version = 2;
packing_format = "tzst";
manifests = "packagesite.yaml";
data = "data";
filesite = "files";
manifests_archive = "packagesite";
filesite_archive = "files";
`

// Repository is a static pkg repository tree:
//
//	<Root>/<ABI>/<Channel>/All/*.pkg            packages
//	<Root>/<ABI>/<Channel>/{meta.conf,meta,data.pkg,packagesite.pkg[,files.pkg]}
//
// Clients use url "https://host/${ABI}/latest". Any web server that serves the
// directory works. A Repository value is safe for concurrent use within one
// process; it is not safe for several writer processes.
type Repository struct {
	Root    string
	Channel string // default "latest"
	Signer  Signer // optional; nil produces an unsigned repository
	// FileList additionally writes files.pkg (enables `pkg rquery`/`rwhich`
	// style lookups of files).
	FileList bool
	// Replace allows re-adding a name-version that exists with different
	// bytes (CI re-runs). Without it that is an error: published versions are
	// immutable, and clients cache by name-version and checksum.
	Replace bool
	// Time stamps the catalog archives. Zero means SOURCE_DATE_EPOCH, then now.
	Time time.Time
	// Policy, when set, refuses packages built for an unsupported ABI or
	// with a minimum release below the policy's (see package policy).
	Policy *policy.Policy
	// PruneManifest, when set, lists published files that must not be in the
	// site: every re-index removes the listed files from All/, leaves them out
	// of the catalog and the index, and records them as pruned. Newest
	// versions are refused unless the entry says force.
	PruneManifest *prune.Manifest

	mu sync.Mutex
}

func (r *Repository) channel() string {
	if r.Channel == "" {
		return DefaultChannel
	}
	return r.Channel
}

func (r *Repository) now() time.Time {
	if !r.Time.IsZero() {
		return r.Time
	}
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Now()
}

// Dir returns the directory of the repository for abi.
func (r *Repository) Dir(abi string) string {
	return filepath.Join(r.Root, abi, r.channel())
}

// ABIs lists the ABI directories that contain a repository.
func (r *Repository) ABIs() ([]string, error) {
	ents, err := os.ReadDir(r.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := pkgfile.ParseABI(e.Name()); err != nil {
			continue
		}
		if st, err := os.Stat(filepath.Join(r.Dir(e.Name()), "All")); err == nil && st.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Add copies (atomically) a .pkg into the repository's All directory without
// touching the catalog. Call [Repository.Reindex] afterwards, or use
// [Repository.Update].
func (r *Repository) Add(ctx context.Context, pkgFile string) (*Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, _, err := r.add(ctx, pkgFile)
	return info, err
}

// add copies the package in. created is the new file when this call created it
// (not when the identical file was already there or was replaced).
func (r *Repository) add(ctx context.Context, pkgFile string) (info *Info, created string, err error) {
	info, err = pkgfile.Verify(pkgFile)
	if err != nil {
		return nil, "", err
	}
	abi, err := pkgfile.ParseABI(info.ABI)
	if err != nil {
		return nil, "", err
	}
	if err := r.Policy.Check(abi.String(), info.MinOSVersion(), info.Name, info.Version); err != nil {
		return nil, "", err
	}
	if _, ok := r.PruneManifest.Has(abi.String(), info.Name, info.Version); ok {
		return nil, "", fmt.Errorf("%w: %s %s is listed in the prune manifest; bump the version instead of re-adding it", pkgerr.ErrInvalidPackage, abi, info.Filename())
	}
	if ix, err := index.Read(r.Root); err == nil && ix != nil && !r.Replace {
		if p, ok := ix.FindPruned(abi.String(), info.Name, info.Version); ok {
			return nil, "", fmt.Errorf("%w: %s %s was pruned on %s; a published version is never reused, bump the version", pkgerr.ErrInvalidPackage, abi, info.Filename(), p.PrunedAt.Format("2006-01-02"))
		}
	}
	dst := filepath.Join(r.Dir(abi.String()), "All", info.Filename())
	existed := false
	if st, err := os.Stat(dst); err == nil {
		existed = true
		// Same name-version already published: only accept identical bytes.
		if old, err := pkgfile.ReadInfo(dst); err == nil && old.SHA256 == info.SHA256 {
			return info, "", nil
		}
		_ = st
		if !r.Replace {
			return nil, "", fmt.Errorf("%w: %s already exists with different content; bump the version", pkgerr.ErrInvalidPackage, info.Filename())
		}
	}
	src, err := os.Open(pkgFile)
	if err != nil {
		return nil, "", err
	}
	defer src.Close()
	err = fsutil.AtomicWrite(dst, 0o644, func(w io.Writer) error {
		_, err := io.Copy(w, &ctxReader{ctx, src})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	crashPoint("add:copied")
	if existed {
		return info, "", nil
	}
	return info, dst, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Update adds the packages and re-indexes every ABI they touch.
func (r *Repository) Update(ctx context.Context, pkgFiles ...string) ([]*Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var infos []*Info
	var created []string
	// A failed update must not leave half of a batch in All/: remove what this
	// call created (files that were already there are never touched).
	rollback := func() {
		for _, c := range created {
			os.Remove(c)
		}
	}
	touched := map[string]bool{}
	for _, f := range pkgFiles {
		info, c, err := r.add(ctx, f)
		if err != nil {
			rollback()
			return nil, err
		}
		if c != "" {
			created = append(created, c)
		}
		infos = append(infos, info)
		abi, _ := pkgfile.ParseABI(info.ABI)
		touched[abi.String()] = true
	}
	if len(pkgFiles) == 0 {
		abis, err := r.ABIs()
		if err != nil {
			return nil, err
		}
		for _, a := range abis {
			touched[a] = true
		}
	}
	var abis []string
	for a := range touched {
		abis = append(abis, a)
	}
	sort.Strings(abis)
	for _, a := range abis {
		if err := r.reindex(ctx, a, nil); err != nil {
			rollback()
			return nil, err
		}
	}
	return infos, nil
}

// Reindex rebuilds and (if a Signer is set) signs the catalog of one ABI from
// the packages currently in All/. Catalog files are replaced atomically, one
// by one, with meta.conf last.
func (r *Repository) Reindex(ctx context.Context, abi string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reindex(ctx, abi, nil)
}

// List returns every package file in the repository for abi, oldest first by
// name then version.
func (r *Repository) List(abi string) ([]*Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(abi)
}

// checkABI rejects anything but a canonical ABI string, which also keeps
// untrusted input from escaping the repository root through the path join.
func checkABI(abi string) error {
	a, err := pkgfile.ParseABI(abi)
	if err != nil {
		return err
	}
	if a.String() != abi {
		return fmt.Errorf("%w: %q is not a canonical ABI (want %s)", pkgerr.ErrUnsupportedABI, abi, a)
	}
	return nil
}

// Problem is a package file in All/ that could not be read.
type Problem struct {
	File string
	Err  error
}

// Scan reads every package file of abi. Unreadable files are reported as
// problems instead of failing the whole scan, so a damaged file cannot hide the
// rest of the repository (or block its deletion).
func (r *Repository) Scan(abi string) ([]*Info, []Problem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scan(abi)
}

func (r *Repository) scan(abi string) ([]*Info, []Problem, error) {
	if err := checkABI(abi); err != nil {
		return nil, nil, err
	}
	all := filepath.Join(r.Dir(abi), "All")
	ents, err := os.ReadDir(all)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("%w: no repository for %s", pkgerr.ErrNotFound, abi)
		}
		return nil, nil, err
	}
	var out []*Info
	var bad []Problem
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".pkg") || strings.HasPrefix(n, ".") {
			continue
		}
		info, err := pkgfile.ReadInfo(filepath.Join(all, n))
		if err != nil {
			bad = append(bad, Problem{File: n, Err: err})
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return CompareVersions(out[i].Version, out[j].Version) < 0
	})
	return out, bad, nil
}

func (r *Repository) list(abi string) ([]*Info, error) {
	out, bad, err := r.scan(abi)
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return nil, bad[0].Err
	}
	return out, nil
}

// Remove deletes one package file; the catalog is not rebuilt.
func (r *Repository) Remove(abi, name, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remove(abi, name, version)
}

func (r *Repository) remove(abi, name, version string) error {
	if err := checkABI(abi); err != nil {
		return err
	}
	if !pkgfile.ValidName(name) || strings.ContainsAny(version, "/\\") || version == "" {
		return fmt.Errorf("%w: bad package name or version", pkgerr.ErrInvalidSpec)
	}
	f := filepath.Join(r.Dir(abi), "All", name+"-"+version+".pkg")
	info, _ := pkgfile.ReadInfo(f)
	if err := os.Remove(f); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s-%s for %s", pkgerr.ErrNotFound, name, version, abi)
		}
		return err
	}
	// keep the index truthful: the file is gone, so it is history now
	ix, err := index.Read(r.Root)
	if err != nil || ix == nil {
		return err
	}
	r.markPruned(ix, abi, name, version, info, "removed", r.now())
	return ix.Write(r.Root)
}

// Prune keeps the newest `keep` versions of every package for abi, deletes the
// rest and re-indexes. It returns the removed file names.
func (r *Repository) Prune(ctx context.Context, abi string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("%w: keep must be >= 1", pkgerr.ErrInvalidSpec)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	infos, err := r.list(abi)
	if err != nil {
		return nil, err
	}
	byName := map[string][]*Info{}
	for _, i := range infos {
		byName[i.Name] = append(byName[i.Name], i)
	}
	var old []string
	for _, vs := range byName {
		if len(vs) > keep {
			for _, o := range vs[:len(vs)-keep] { // list is sorted ascending
				old = append(old, o.Filename())
			}
		}
	}
	if len(old) == 0 {
		return nil, nil
	}
	return r.deleteLocked(ctx, abi, old, fmt.Sprintf("retention: keep the newest %d", keep))
}

// Delete removes one package version from the repository and the catalog. The
// catalog is rebuilt without the package first and the file is removed only
// afterwards, so at no instant does any catalog on disk advertise a package
// that is missing. A crash in between leaves an unadvertised file that the
// next re-index or Delete ignores or removes.
func (r *Repository) Delete(ctx context.Context, abi, name, version string) error {
	if !pkgfile.ValidName(name) || strings.ContainsAny(version, "/\\") || version == "" {
		return fmt.Errorf("%w: bad package name or version", pkgerr.ErrInvalidSpec)
	}
	if err := checkABI(abi); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	file := name + "-" + version + ".pkg"
	if _, err := os.Stat(filepath.Join(r.Dir(abi), "All", file)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s-%s for %s", pkgerr.ErrNotFound, name, version, abi)
		}
		return err
	}
	_, err := r.deleteLocked(ctx, abi, []string{file}, "deleted")
	return err
}

func (r *Repository) deleteLocked(ctx context.Context, abi string, files []string, reason string) ([]string, error) {
	exclude := map[string]string{}
	for _, f := range files {
		exclude[f] = reason
	}
	sort.Strings(files)
	if err := r.reindex(ctx, abi, exclude); err != nil {
		return nil, err
	}
	crashPoint("delete:catalog")
	for _, n := range files {
		if err := os.Remove(filepath.Join(r.Dir(abi), "All", n)); err != nil && !os.IsNotExist(err) {
			return files, err
		}
		crashPoint("delete:removed:" + n)
	}
	return files, nil
}

type catalogEntry struct {
	info *Info
	sum  string
	size int64
}

func (r *Repository) reindex(ctx context.Context, abi string, exclude map[string]string) error {
	if _, err := pkgfile.ParseABI(abi); err != nil {
		return err
	}
	all, bad, err := r.scan(abi)
	if err != nil {
		return err
	}
	for _, p := range bad {
		if _, ex := exclude[p.File]; !ex {
			return p.Err // never publish a catalog next to a package we cannot read
		}
	}
	for _, i := range all {
		if i.ABI != abi {
			return fmt.Errorf("%w: %s is built for %s but sits in the %s repository", pkgerr.ErrInvalidPackage, i.Filename(), i.ABI, abi)
		}
	}
	prev, err := index.Read(r.Root)
	if err != nil {
		return err
	}
	if prev == nil {
		prev = index.New()
	}
	// Files that leave the site with this index: the explicit ones (Delete,
	// retention), those the prune manifest lists, and leftovers of an earlier
	// crash (already recorded as pruned, but still on disk).
	gone := map[string]string{}
	for f, why := range exclude {
		gone[f] = why
	}
	if err := r.applyPruneManifest(abi, all, gone); err != nil {
		return err
	}
	if !r.Replace {
		for _, i := range all {
			if _, ok := gone[i.Filename()]; ok {
				continue
			}
			if p, ok := prev.FindPruned(abi, i.Name, i.Version); ok && p.SHA256 == i.SHA256 {
				gone[i.Filename()] = "leftover of an interrupted prune"
			}
		}
	}
	present := map[string]*Info{}
	for _, i := range all {
		present[i.Name+"-"+i.Version] = i
	}
	// A file the index lists must still be there with the same bytes: never
	// drop a published package silently.
	for _, e := range prev.Packages {
		if e.ABI != abi {
			continue
		}
		i, ok := present[e.Name+"-"+e.Version]
		switch {
		case ok && i.SHA256 != e.SHA256 && !r.Replace:
			return fmt.Errorf("%w: %s changed since it was published (index sha256 %s, file %s); published files are immutable, bump the version", pkgerr.ErrInvalidPackage, e.File, e.SHA256[:12], i.SHA256[:12])
		case !ok:
			if _, ex := gone[e.Name+"-"+e.Version+".pkg"]; !ex {
				return fmt.Errorf("%w: %s is listed in %s but missing; restore it (pkgops fetch) or list it in the prune manifest", pkgerr.ErrNotFound, e.File, index.FileName)
			}
		}
	}
	infos := all
	// Only the newest version of each name is advertised: a pkg catalog holds
	// one entry per package name.
	newest := map[string]*Info{}
	for _, i := range infos {
		if _, ex := gone[i.Filename()]; ex {
			continue
		}
		if cur, ok := newest[i.Name]; !ok || CompareVersions(i.Version, cur.Version) > 0 {
			newest[i.Name] = i
		}
	}
	var names []string
	for n := range newest {
		names = append(names, n)
	}
	sort.Strings(names)

	var lines [][]byte
	var files filesIndex
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		i := newest[n]
		line, err := catalogLine(i)
		if err != nil {
			return err
		}
		lines = append(lines, line)
		if r.FileList {
			fl, err := packageFileList(filepath.Join(r.Dir(abi), "All", i.Filename()))
			if err != nil {
				return err
			}
			files.add(i.Name, i.Version, fl)
		}
	}

	var site bytes.Buffer
	for _, l := range lines {
		site.Write(l)
		site.WriteByte('\n')
	}
	var data bytes.Buffer
	data.WriteString(`{"groups":[],"expired_packages":[],"packages":[`)
	for i, l := range lines {
		if i > 0 {
			data.WriteByte(',')
		}
		data.Write(l)
	}
	data.WriteString("]}")

	dir := r.Dir(abi)
	ts := r.now()
	// Build every new catalog file in a private staging directory first, then
	// move them into place one rename at a time (files.pkg, packagesite.pkg,
	// data.pkg, meta, meta.conf). Each rename is atomic, every file that exists
	// at any instant is a complete signed catalog, and the packages they list
	// are all present because package files are only removed after this.
	cleanStale(dir)
	cleanStale(filepath.Join(dir, "All"))
	stage, err := os.MkdirTemp(dir, ".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var order []string
	put := func(name string, fn func(io.Writer) error) error {
		f, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		order = append(order, name)
		return f.Close()
	}
	catalog := func(archive, payload string, content []byte) error {
		return put(archive, func(w io.Writer) error { return r.writeCatalog(w, payload, content, ts) })
	}
	if r.FileList {
		if err := catalog("files.pkg", "files", files.render()); err != nil {
			return err
		}
	}
	if err := catalog("packagesite.pkg", "packagesite.yaml", site.Bytes()); err != nil {
		return err
	}
	if err := catalog("data.pkg", "data", data.Bytes()); err != nil {
		return err
	}
	for _, m := range []string{"meta", "meta.conf"} {
		if err := put(m, func(w io.Writer) error { _, err := io.WriteString(w, metaConf); return err }); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	crashPoint("reindex:staged")
	if !r.FileList {
		_ = os.Remove(filepath.Join(dir, "files.pkg"))
	}
	for _, name := range order {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(dir, name)); err != nil {
			return err
		}
		crashPoint("reindex:renamed:" + name)
	}
	// The index goes last: a crash before this line leaves a catalog that is
	// consistent and an index that still lists every file that exists.
	next := r.nextIndex(prev, abi, all, gone, ts)
	if err := next.Write(r.Root); err != nil {
		return err
	}
	crashPoint("reindex:index")
	// Files the prune manifest or an interrupted prune dropped are removed
	// only now that no catalog or index advertises them. Explicit deletes
	// (exclude) are removed by their caller.
	for f := range gone {
		if _, explicit := exclude[f]; explicit {
			continue
		}
		if err := os.Remove(filepath.Join(r.Dir(abi), "All", f)); err != nil && !os.IsNotExist(err) {
			return err
		}
		crashPoint("reindex:removed:" + f)
	}
	return nil
}

// crashPoint is a seam for the crash-simulation tests; it does nothing in
// production.
var crashPoint = func(step string) {}

// cleanStale removes staging directories and temp files a crashed earlier
// process may have left behind. There is a single writer per repository.
func cleanStale(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".stage-") || strings.HasPrefix(e.Name(), ".tmp-") {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// catalogLine turns a compact manifest into the repository catalog record by
// adding sum, path, repopath and pkgsize where pkg-repo(8) places them.
func catalogLine(i *Info) ([]byte, error) {
	o, err := ordjson.Parse(i.CompactManifest())
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidPackage, i.Filename(), err)
	}
	rel := "All/" + i.Filename()
	out := &ordjson.Obj{}
	var haveSum, havePath, haveSize bool
	addPath := func() {
		if !havePath {
			out.Set("path", rel)
			out.Set("repopath", rel)
			havePath = true
		}
	}
	addSize := func() {
		if !haveSize {
			out.Set("pkgsize", i.Size)
			haveSize = true
		}
	}
	for k, key := range o.Keys {
		switch key {
		case "sum", "path", "repopath", "pkgsize":
			continue
		case "desc":
			addPath()
			addSize()
		}
		out.Raw(key, o.Vals[k])
		switch key {
		case "prefix":
			out.Set("sum", "1$"+i.SHA256)
			haveSum = true
		case "flatsize":
			addPath()
		}
	}
	if !haveSum {
		out.Set("sum", "1$"+i.SHA256)
	}
	addPath()
	addSize()
	return out.Bytes(), nil
}

func (r *Repository) writeCatalog(w io.Writer, payload string, content []byte, ts time.Time) error {
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	put := func(name string, data []byte, mt time.Time) error {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)),
			Mode: 0o644, Uname: "root", Gname: "wheel", ModTime: time.Unix(mt.Unix(), 0)}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if r.Signer != nil {
		sum := sha256.Sum256(content)
		ents, err := r.Signer.SignCatalog(payload, hex.EncodeToString(sum[:]))
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := put(e.Name, e.Data, time.Unix(0, 0)); err != nil {
				return err
			}
		}
	}
	if err := put(payload, content, ts); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// ---- files.pkg ------------------------------------------------------------

type filesIndex struct {
	pkgs []filesPkg
}
type filesPkg struct {
	name, version string
	byDir         map[string][]string
}

func packageFileList(pkgFile string) (map[string][]string, error) {
	f, err := os.Open(pkgFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, closeFn, err := pkgfile.OpenTar(f)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	out := map[string][]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", pkgerr.ErrInvalidPackage, err)
		}
		if h.Typeflag == tar.TypeDir || strings.HasPrefix(h.Name, "+") {
			continue
		}
		out[path.Dir(h.Name)] = append(out[path.Dir(h.Name)], path.Base(h.Name))
	}
}

func (f *filesIndex) add(name, version string, by map[string][]string) {
	f.pkgs = append(f.pkgs, filesPkg{name, version, by})
}

func (f *filesIndex) render() []byte {
	set := map[string]bool{}
	for _, p := range f.pkgs {
		for d := range p.byDir {
			set[d] = true
		}
	}
	var dirs []string
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	idx := map[string]int{}
	var b bytes.Buffer
	prev := ""
	for i, d := range dirs {
		n := 0
		for n < len(prev) && n < len(d) && prev[n] == d[n] {
			n++
		}
		fmt.Fprintf(&b, "%d %s\n", n, d[n:])
		idx[d] = i
		prev = d
	}
	b.WriteByte('\n')
	for _, p := range f.pkgs {
		fmt.Fprintf(&b, "%s %s\n", p.name, p.version)
		var ds []string
		for d := range p.byDir {
			ds = append(ds, d)
		}
		sort.Slice(ds, func(i, j int) bool { return idx[ds[i]] < idx[ds[j]] })
		for _, d := range ds {
			fmt.Fprintf(&b, ">%d\n", idx[d])
			fl := p.byDir[d]
			sort.Strings(fl)
			for _, n := range fl {
				b.WriteString(n + "\n")
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// ---- verification ---------------------------------------------------------

// RepoCheck is the outcome of [VerifyRepository].
type RepoCheck struct {
	Packages  int
	Signed    bool
	Signature string // "pubkey", "fingerprints" or ""
}

// VerifyRepository checks one repository directory (the one holding
// meta.conf): the catalogs parse, every advertised package exists with the
// advertised size and checksum, and, if pubPEM is non-nil, the catalog
// signatures verify against that key. It is the offline equivalent of what a
// pkg client does on `pkg update`.
func VerifyRepository(dir string, pubPEM []byte) (*RepoCheck, error) {
	var pk *rsa.PublicKey
	if pubPEM != nil {
		k, err := ParseRSAPublicKey(pubPEM)
		if err != nil {
			return nil, err
		}
		pk = k
	}
	res := &RepoCheck{}
	var dataBytes, siteBytes []byte
	for _, c := range []struct{ archive, payload string }{{"data.pkg", "data"}, {"packagesite.pkg", "packagesite.yaml"}} {
		f, err := os.Open(filepath.Join(dir, c.archive))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", pkgerr.ErrNotFound, err)
		}
		tr, closeFn, err := pkgfile.OpenTar(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		ents := map[string][]byte{}
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				closeFn()
				f.Close()
				return nil, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidPackage, c.archive, err)
			}
			b, _ := io.ReadAll(io.LimitReader(tr, 256<<20))
			ents[h.Name] = b
		}
		closeFn()
		f.Close()
		content, ok := ents[c.payload]
		if !ok {
			return nil, fmt.Errorf("%w: %s lacks %s", pkgerr.ErrInvalidPackage, c.archive, c.payload)
		}
		sum := sha256.Sum256(content)
		hexsum := hex.EncodeToString(sum[:])
		var sig []byte
		switch {
		case ents["signature"] != nil:
			sig, res.Signature = ents["signature"], "pubkey"
		case ents[c.payload+".sig"] != nil:
			sig, res.Signature = ents[c.payload+".sig"], "fingerprints"
		}
		res.Signed = sig != nil
		if pk != nil {
			if sig == nil {
				return nil, fmt.Errorf("%w: %s is not signed", pkgerr.ErrSignature, c.archive)
			}
			if err := VerifyRSA(pk, hexsum, sig); err != nil {
				return nil, fmt.Errorf("%s: %w", c.archive, err)
			}
		}
		if c.payload == "data" {
			dataBytes = content
		} else {
			siteBytes = content
		}
	}
	type rec struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Sum     string `json:"sum"`
		Pkgsize int64  `json:"pkgsize"`
	}
	var cat struct {
		Packages []rec `json:"packages"`
	}
	if err := json.Unmarshal(dataBytes, &cat); err != nil {
		return nil, fmt.Errorf("%w: data: %v", pkgerr.ErrInvalidPackage, err)
	}
	var site []rec
	for _, line := range bytes.Split(siteBytes, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var p rec
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("%w: packagesite.yaml: %v", pkgerr.ErrInvalidPackage, err)
		}
		site = append(site, p)
	}
	// Every advertised package, in either catalog, must exist with the
	// advertised size and checksum.
	for _, p := range append(append([]rec(nil), cat.Packages...), site...) {
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("%w: suspicious path %q", pkgerr.ErrInvalidPackage, p.Path)
		}
		info, err := pkgfile.ReadInfo(filepath.Join(dir, filepath.FromSlash(p.Path)))
		if err != nil {
			return nil, err
		}
		if p.Sum != "1$"+info.SHA256 || p.Pkgsize != info.Size {
			return nil, fmt.Errorf("%w: catalog entry for %s does not match %s", pkgerr.ErrInvalidPackage, p.Name, p.Path)
		}
	}
	res.Packages = len(cat.Packages)
	return res, nil
}
