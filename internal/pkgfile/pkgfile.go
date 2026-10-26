package pkgfile

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"jailops.net/pkgops/internal/fsutil"
	"jailops.net/pkgops/internal/ordjson"
	"jailops.net/pkgops/internal/pkgerr"
)

// File is one regular file inside a package. Exactly one of Source (a path on
// the build machine) or Data is used; Data wins when non-nil.
type File struct {
	Path   string // absolute install path, e.g. /usr/local/bin/hello
	Source string
	Data   []byte
	Mode   uint32 // permission bits incl. setuid/setgid/sticky
	Owner  string // default root
	Group  string // default wheel
	// Symlink, when non-empty, makes this entry a symbolic link to that
	// target (relative or absolute, stored verbatim); Source and Data are
	// ignored and Mode defaults to 0755 like pkg-create writes it.
	Symlink string
	// Config marks the file as pkg "@config".
	Config bool
}

// Dir is a directory owned by the package (created on install, removed on
// deinstall when empty).
type Dir struct {
	Path  string
	Mode  uint32
	Owner string
	Group string
}

// Dep is a package dependency.
type Dep struct {
	Name    string
	Origin  string
	Version string
}

// Package is the complete, in-memory description of a FreeBSD package. It is
// the lowest-level build input of the SDK: fill it from Go, or obtain one from
// [Spec.Package].
type Package struct {
	Name         string
	Version      string
	Origin       string // default misc/<name>
	Comment      string
	Desc         string
	WWW          string
	Maintainer   string
	ABI          string // FreeBSD:15:amd64
	Prefix       string // default /usr/local
	Licenses     []string
	LicenseLogic string // single (default), and, or
	Categories   []string
	Users        []string // names recorded in the manifest "users" list
	Groups       []string // names recorded in the manifest "groups" list
	Deps         []Dep
	Files        []File
	Dirs         []Dir
	Scripts      map[string]string // phase -> script body

	// MinOSVersion is the lowest __FreeBSD_version (the number `uname -U`
	// prints, 1501000 for 15.1-RELEASE) the package may be installed on. It
	// is written as the FreeBSD_version annotation, which pkg(8) compares
	// with the running system and refuses to install on an older one
	// ("Newer FreeBSD version for package", unless IGNORE_OSVERSION is set).
	// Zero writes no annotation (any release of the ABI's major).
	MinOSVersion int

	// Time is the mtime stamped on every file and directory. It makes builds
	// reproducible; zero means the Unix epoch.
	Time time.Time
}

// WriteOptions tunes archive creation.
type WriteOptions struct {
	// Level is the zstd level; zero selects the best compression.
	Level zstd.EncoderLevel
}

// Info describes a package as recorded in its (compact) manifest. It is what
// repository tooling needs and what [ReadInfo] returns.
type Info struct {
	Name         string `json:"name"`
	Origin       string `json:"origin"`
	Version      string `json:"version"`
	Comment      string `json:"comment"`
	Maintainer   string `json:"maintainer"`
	WWW          string `json:"www"`
	ABI          string `json:"abi"`
	Arch         string `json:"arch"`
	Prefix       string `json:"prefix"`
	Flatsize     int64  `json:"flatsize"`
	LicenseLogic string `json:"licenselogic"`
	Licenses     []string
	Desc         string `json:"desc"`
	// Annotations are the manifest's annotations (FreeBSD_version, ...).
	Annotations map[string]string `json:"annotations"`

	// Size and SHA256 describe the .pkg file itself (filled by ReadInfo and
	// BuildPackage).
	Size   int64  `json:"-"`
	SHA256 string `json:"-"`

	compact []byte // raw +COMPACT_MANIFEST JSON, key order preserved
}

// CompactManifest returns the raw +COMPACT_MANIFEST JSON.
func (i *Info) CompactManifest() []byte { return append([]byte(nil), i.compact...) }

// MinOSVersion returns the FreeBSD_version annotation as a number, 0 when the
// package carries none.
func (i *Info) MinOSVersion() int {
	n, _ := strconv.Atoi(i.Annotations["FreeBSD_version"])
	return n
}

// Filename returns the canonical file name, name-version.pkg.
func (i *Info) Filename() string { return i.Name + "-" + i.Version + ".pkg" }

const (
	manifestName = "+MANIFEST"
	compactName  = "+COMPACT_MANIFEST"
)

// ---- writing --------------------------------------------------------------

func (p *Package) normalise() error {
	bad := func(f string, a ...any) error {
		return fmt.Errorf("%w: %s", pkgerr.ErrInvalidSpec, fmt.Sprintf(f, a...))
	}
	if !reName.MatchString(p.Name) {
		return bad("package name %q invalid", p.Name)
	}
	if p.Version == "" {
		return bad("package version is empty")
	}
	if p.Comment == "" {
		return bad("package comment is empty")
	}
	// pkg's repository database declares maintainer and www NOT NULL; a
	// catalog containing a package without them makes `pkg update` fail for
	// every client, so refuse to build such a package.
	for _, d := range p.Deps {
		if d.Origin == "" || d.Version == "" {
			return bad("dependency %q needs origin and version", d.Name)
		}
	}
	if p.Maintainer == "" || p.WWW == "" {
		return bad("maintainer and www are required (pkg cannot index a package without them)")
	}
	if _, err := ParseABI(p.ABI); err != nil {
		return err
	}
	return nil
}

func ownerOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func permString(m uint32) string { return fmt.Sprintf("%04o", m&07777) }

type fileEntry struct {
	File
	size int64
	sum  string
}

func (f *File) open() (io.ReadCloser, int64, error) {
	if f.Data != nil {
		return io.NopCloser(bytes.NewReader(f.Data)), int64(len(f.Data)), nil
	}
	fh, err := os.Open(f.Source)
	if err != nil {
		return nil, 0, err
	}
	st, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		fh.Close()
		return nil, 0, fmt.Errorf("%w: %s is not a regular file", pkgerr.ErrInvalidSpec, f.Source)
	}
	return fh, st.Size(), nil
}

// manifests builds +MANIFEST and +COMPACT_MANIFEST and the sorted file list.
func (p *Package) manifests() (manifest, compact []byte, files []fileEntry, dirs []Dir, err error) {
	if err = p.normalise(); err != nil {
		return
	}
	abi, _ := ParseABI(p.ABI)
	prefix := p.Prefix
	if prefix == "" {
		prefix = "/usr/local"
	}
	seen := map[string]bool{}
	var flat int64
	for _, f := range p.Files {
		if !path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path {
			err = fmt.Errorf("%w: file path %q must be absolute and clean", pkgerr.ErrInvalidSpec, f.Path)
			return
		}
		if seen[f.Path] {
			err = fmt.Errorf("%w: duplicate file %s", pkgerr.ErrInvalidSpec, f.Path)
			return
		}
		seen[f.Path] = true
		if f.Symlink != "" {
			flat += int64(len(f.Symlink))
			files = append(files, fileEntry{File: f, size: int64(len(f.Symlink)), sum: symlinkSum(f.Symlink)})
			continue
		}
		rc, size, oerr := f.open()
		if oerr != nil {
			err = oerr
			return
		}
		h := sha256.New()
		n, cerr := io.Copy(h, rc)
		rc.Close()
		if cerr != nil {
			err = cerr
			return
		}
		if n != size {
			err = fmt.Errorf("%s changed while reading", f.Path)
			return
		}
		flat += size
		files = append(files, fileEntry{File: f, size: size, sum: "1$" + hex.EncodeToString(h.Sum(nil))})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	dirs = append(dirs, p.Dirs...)
	dseen := map[string]bool{}
	for _, d := range dirs {
		if !path.IsAbs(d.Path) || path.Clean(d.Path) != d.Path {
			err = fmt.Errorf("%w: dir path %q must be absolute and clean", pkgerr.ErrInvalidSpec, d.Path)
			return
		}
		if dseen[d.Path] || seen[d.Path] {
			err = fmt.Errorf("%w: duplicate path %s", pkgerr.ErrInvalidSpec, d.Path)
			return
		}
		dseen[d.Path] = true
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Path < dirs[j].Path })

	mtime := p.Time.Unix()
	if p.Time.IsZero() {
		mtime = 0
	}

	origin := p.Origin
	if origin == "" {
		origin = "misc/" + p.Name
	}
	logic := p.LicenseLogic
	if logic == "" {
		logic = "single"
	}

	head := &ordjson.Obj{}
	head.Set("name", p.Name)
	head.Set("origin", origin)
	head.Set("version", p.Version)
	head.Set("comment", p.Comment)
	head.Set("maintainer", p.Maintainer)
	head.Set("www", p.WWW)
	head.Set("abi", abi.String())
	head.Set("arch", abi.PkgArch())
	head.Set("prefix", prefix)
	head.Set("flatsize", flat)
	head.Set("licenselogic", logic)
	if len(p.Licenses) > 0 {
		head.Set("licenses", p.Licenses)
	}
	// pkg's catalog database declares desc NOT NULL: an empty or missing
	// description makes `pkg update` fail for the whole repository, so fall
	// back to the one-line comment.
	desc := p.Desc
	if desc == "" {
		desc = p.Comment
	}
	head.Set("desc", desc)
	if len(p.Deps) > 0 {
		deps := map[string]map[string]string{}
		for _, d := range p.Deps {
			m := map[string]string{}
			if d.Origin != "" {
				m["origin"] = d.Origin
			}
			if d.Version != "" {
				m["version"] = d.Version
			}
			deps[d.Name] = m
		}
		head.Set("deps", deps)
	}
	if len(p.Categories) > 0 {
		head.Set("categories", p.Categories)
	}
	if len(p.Users) > 0 {
		head.Set("users", p.Users)
	}
	if len(p.Groups) > 0 {
		head.Set("groups", p.Groups)
	}
	if p.MinOSVersion < 0 {
		err = fmt.Errorf("%w: MinOSVersion %d is negative", pkgerr.ErrInvalidSpec, p.MinOSVersion)
		return
	}
	if p.MinOSVersion > 0 {
		head.Set("annotations", map[string]string{"FreeBSD_version": strconv.Itoa(p.MinOSVersion)})
	}
	compact = head.Bytes()

	full := head.Clone()
	var config []string
	// encoding/json sorts map keys but we need pkg's field order inside each
	// file entry (sum, uname, gname, perm, mtime), so build raw objects.
	var fb bytes.Buffer
	fb.WriteByte('{')
	for i, f := range files {
		if i > 0 {
			fb.WriteByte(',')
		}
		fe := &ordjson.Obj{}
		fe.Set("sum", f.sum)
		fe.Set("uname", ownerOr(f.Owner, "root"))
		fe.Set("gname", ownerOr(f.Group, "wheel"))
		perm := f.Mode
		if f.Symlink != "" && perm == 0 {
			perm = 0o755
		}
		fe.Set("perm", permString(perm))
		if f.Symlink != "" {
			fe.Set("symlink_target", f.Symlink)
		}
		fe.Set("mtime", mtime)
		fb.Write(ordjson.Marshal(f.Path))
		fb.WriteByte(':')
		fb.Write(fe.Bytes())
		if f.Config {
			config = append(config, f.Path)
		}
	}
	fb.WriteByte('}')
	if len(files) > 0 {
		full.Raw("files", fb.Bytes())
	}
	if len(config) > 0 {
		full.Set("config", config)
	}
	if len(dirs) > 0 {
		var db bytes.Buffer
		db.WriteByte('{')
		for i, d := range dirs {
			if i > 0 {
				db.WriteByte(',')
			}
			de := &ordjson.Obj{}
			de.Set("uname", ownerOr(d.Owner, "root"))
			de.Set("gname", ownerOr(d.Group, "wheel"))
			de.Set("perm", permString(d.Mode))
			db.Write(ordjson.Marshal(d.Path))
			db.WriteByte(':')
			db.Write(de.Bytes())
		}
		db.WriteByte('}')
		full.Raw("directories", db.Bytes())
	}
	if len(p.Scripts) > 0 {
		sc := map[string]string{}
		for k, v := range p.Scripts {
			if !contains(ScriptNames, k) {
				err = fmt.Errorf("%w: unknown script phase %q", pkgerr.ErrInvalidSpec, k)
				return
			}
			sc[k] = v
		}
		// pkg writes phases in a fixed order; keep ours stable too.
		so := &ordjson.Obj{}
		for _, k := range ScriptNames {
			if v, ok := sc[k]; ok {
				so.Set(k, v)
			}
		}
		full.Raw("scripts", so.Bytes())
	}
	manifest = full.Bytes()
	return
}

// Write creates the .pkg archive (tar + zstd) on w and returns its Info. The
// output is a pure function of the Package value and file contents.
func (p *Package) Write(w io.Writer, opt WriteOptions) (*Info, error) {
	manifest, compact, files, dirs, err := p.manifests()
	if err != nil {
		return nil, err
	}
	level := opt.Level
	if level == 0 {
		level = zstd.SpeedBestCompression
	}
	cw := &countHash{w: w, h: sha256.New()}
	zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	meta := func(name string, data []byte) error {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: 0o644,
			Uname: "root", Gname: "wheel", ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := meta(compactName, compact); err != nil {
		return nil, err
	}
	if err := meta(manifestName, manifest); err != nil {
		return nil, err
	}
	mt := p.Time
	if mt.IsZero() {
		mt = time.Unix(0, 0)
	}
	mt = time.Unix(mt.Unix(), 0)
	for _, f := range files {
		if f.Symlink != "" {
			mode := int64(f.Mode & 07777)
			if mode == 0 {
				mode = 0o755
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: f.Path, Linkname: f.Symlink, Mode: mode,
				Uname: ownerOr(f.Owner, "root"), Gname: ownerOr(f.Group, "wheel"), ModTime: mt}); err != nil {
				return nil, err
			}
			continue
		}
		h := &tar.Header{Typeflag: tar.TypeReg, Name: f.Path, Size: f.size, Mode: int64(f.Mode & 07777),
			Uname: ownerOr(f.Owner, "root"), Gname: ownerOr(f.Group, "wheel"), ModTime: mt}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		rc, _, err := f.open()
		if err != nil {
			return nil, err
		}
		h2 := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, h2), rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if n != f.size || "1$"+hex.EncodeToString(h2.Sum(nil)) != f.sum {
			return nil, fmt.Errorf("%s changed while building the package", f.Path)
		}
	}
	for _, d := range dirs {
		h := &tar.Header{Typeflag: tar.TypeDir, Name: d.Path + "/", Mode: int64(d.Mode & 07777),
			Uname: ownerOr(d.Owner, "root"), Gname: ownerOr(d.Group, "wheel"), ModTime: mt}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	info, err := parseInfo(compact)
	if err != nil {
		return nil, err
	}
	info.Size = cw.n
	info.SHA256 = hex.EncodeToString(cw.h.Sum(nil))
	return info, nil
}

type countHash struct {
	w io.Writer
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (c *countHash) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	c.h.Write(b[:n])
	return n, err
}

// WriteFile writes the package atomically to filename.
func (p *Package) WriteFile(filename string, opt WriteOptions) (*Info, error) {
	var info *Info
	err := fsutil.AtomicWrite(filename, 0o644, func(w io.Writer) error {
		var err error
		info, err = p.Write(w, opt)
		return err
	})
	return info, err
}

// ---- reading --------------------------------------------------------------

func parseInfo(compact []byte) (*Info, error) {
	var i Info
	if err := json.Unmarshal(compact, &i); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", pkgerr.ErrInvalidPackage, err)
	}
	var extra struct {
		Licenses []string `json:"licenses"`
	}
	_ = json.Unmarshal(compact, &extra)
	i.Licenses = extra.Licenses
	i.compact = append([]byte(nil), compact...)
	if i.Name == "" || i.Version == "" || i.ABI == "" {
		return nil, fmt.Errorf("%w: manifest lacks name, version or abi", pkgerr.ErrInvalidPackage)
	}
	return &i, nil
}

func OpenTar(r io.Reader) (*tar.Reader, func(), error) {
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", pkgerr.ErrInvalidPackage, err)
	}
	return tar.NewReader(zr), zr.Close, nil
}

// ReadInfo reads only the compact manifest of a .pkg file (it is the first
// entry of the archive, so this stops early) and fills Size and SHA256.
func ReadInfo(filename string) (*Info, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, closeFn, err := OpenTar(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	defer closeFn()
	var info *Info
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: no %s entry", filename, pkgerr.ErrInvalidPackage, compactName)
		}
		if h.Name != compactName {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 16<<20))
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %v", filename, pkgerr.ErrInvalidPackage, err)
		}
		info, err = parseInfo(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filename, err)
		}
		break
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, err
	}
	info.Size = n
	info.SHA256 = hex.EncodeToString(h.Sum(nil))
	return info, nil
}

// Verify fully reads a .pkg file and checks that every file listed in
// +MANIFEST is present with the recorded size-independent sha256 ("1$" sums),
// that no unlisted file is present, and that flatsize matches. Sums in other
// formats (written by FreeBSD's pkg-create) are reported as skipped, not as
// failures.
func Verify(filename string) (*Info, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, closeFn, err := OpenTar(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	defer closeFn()
	fail := func(format string, a ...any) error {
		return fmt.Errorf("%s: %w: %s", filename, pkgerr.ErrInvalidPackage, fmt.Sprintf(format, a...))
	}
	var manifest []byte
	type fe struct {
		Sum    string `json:"sum"`
		Perm   string `json:"perm"`
		Target string `json:"symlink_target"`
	}
	var mf struct {
		Flatsize int64         `json:"flatsize"`
		Files    map[string]fe `json:"files"`
		Dirs     map[string]any
	}
	var compact []byte
	got := map[string]bool{}
	var flat int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fail("%v", err)
		}
		switch {
		case h.Name == compactName || h.Name == manifestName:
			b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
			if err != nil {
				return nil, fail("%v", err)
			}
			if h.Name == compactName {
				compact = b
			} else {
				manifest = b
				var raw struct {
					Dirs map[string]any `json:"directories"`
				}
				if err := json.Unmarshal(b, &mf); err != nil {
					return nil, fail("bad %s: %v", manifestName, err)
				}
				_ = json.Unmarshal(b, &raw)
				mf.Dirs = raw.Dirs
			}
		case h.Typeflag == tar.TypeDir:
			p := strings.TrimSuffix(h.Name, "/")
			if _, ok := mf.Dirs[p]; !ok {
				return nil, fail("directory %s not in manifest", p)
			}
		default:
			if manifest == nil {
				return nil, fail("%s must precede payload files", manifestName)
			}
			e, ok := mf.Files[h.Name]
			if !ok {
				return nil, fail("file %s not in manifest", h.Name)
			}
			var hh = sha256.New()
			var n int64
			if h.Typeflag == tar.TypeSymlink {
				if e.Target != h.Linkname {
					return nil, fail("symlink target mismatch for %s: tar %q manifest %q", h.Name, h.Linkname, e.Target)
				}
				hh = nil
				n = int64(len(h.Linkname))
			} else {
				if e.Target != "" {
					return nil, fail("%s is a symlink in the manifest but a regular file in the archive", h.Name)
				}
				n, err = io.Copy(hh, tr)
				if err != nil {
					return nil, fail("%v", err)
				}
			}
			flat += n
			got[h.Name] = true
			if h.Typeflag == tar.TypeSymlink {
				if strings.HasPrefix(e.Sum, "1$") && e.Sum != symlinkSum(h.Linkname) {
					return nil, fail("checksum mismatch for symlink %s", h.Name)
				}
			} else if strings.HasPrefix(e.Sum, "1$") {
				if e.Sum != "1$"+hex.EncodeToString(hh.Sum(nil)) {
					return nil, fail("checksum mismatch for %s", h.Name)
				}
			}
			if want := e.Perm; want != "" && want != permString(uint32(h.Mode)) {
				return nil, fail("mode mismatch for %s: tar %s manifest %s", h.Name, permString(uint32(h.Mode)), want)
			}
		}
	}
	if compact == nil || manifest == nil {
		return nil, fail("missing %s or %s", compactName, manifestName)
	}
	for p := range mf.Files {
		if !got[p] {
			return nil, fail("file %s listed in manifest but absent", p)
		}
	}
	if flat != mf.Flatsize {
		return nil, fail("flatsize %d does not match content size %d", mf.Flatsize, flat)
	}
	info, err := parseInfo(compact)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		h := sha256.New()
		n, _ := io.Copy(h, f)
		info.Size, info.SHA256 = n, hex.EncodeToString(h.Sum(nil))
	}
	return info, nil
}

// symlinkSum is the manifest checksum of a symbolic link. pkg hashes the link
// target; for an absolute target it skips the leading slash and the hash
// includes the string's terminating NUL byte (found by checking pkgbase
// packages and `pkg check -s`; relative targets are hashed as they are).
func symlinkSum(target string) string {
	data := []byte(target)
	if len(target) > 0 && target[0] == '/' {
		data = append([]byte(target[1:]), 0)
	}
	sum := sha256.Sum256(data)
	return "1$" + hex.EncodeToString(sum[:])
}
