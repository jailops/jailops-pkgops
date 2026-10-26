package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BuildOptions controls [Spec.Package] and [BuildPackage].
type BuildOptions struct {
	// BaseDir is the directory relative paths in the manifest refer to
	// (normally the directory holding pkgops.yaml). Default ".".
	BaseDir string
	// ABI overrides Spec.ABI.
	ABI string
	// OutDir receives <OutDir>/<ABI>/<name>-<version>.pkg. Default "dist".
	OutDir string
	// Epoch is the timestamp stamped on all package content. When zero,
	// SOURCE_DATE_EPOCH is used, and if that is unset too, the newest mtime
	// among the input files (so identical inputs give identical packages).
	Epoch time.Time
	// SkipGoBuild disables the build step even if the manifest has one.
	SkipGoBuild bool
	// Level is the zstd level (zero = best).
	Write WriteOptions
	// Log, if non-nil, receives progress lines and go build output.
	Log io.Writer
}

func (o *BuildOptions) baseDir() string {
	if o.BaseDir == "" {
		return "."
	}
	return o.BaseDir
}

func (o *BuildOptions) logf(format string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", a...)
	}
}

func (s *Spec) abi(opt *BuildOptions) (ABI, error) {
	v := opt.ABI
	if v == "" {
		list, err := s.ABIList("")
		if err != nil {
			return ABI{}, err
		}
		if len(list) != 1 {
			return ABI{}, fmt.Errorf("%w: the manifest lists %d ABIs; use BuildAll (or --abi to pick one)", pkgerr.ErrInvalidSpec, len(list))
		}
		v = list[0]
	}
	return ParseABI(v)
}

// ABIList resolves a selector to canonical ABI strings, in a stable order
// without duplicates. "" and "all" mean every ABI the manifest lists (abis,
// then abi); otherwise sel is a comma-separated list of ABIs.
func (s *Spec) ABIList(sel string) ([]string, error) {
	var raw []string
	if sel == "" || sel == "all" {
		raw = append(raw, s.ABIs...)
		if s.ABI != "" {
			raw = append(raw, s.ABI)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("%w: no abi or abis in the manifest and none given", pkgerr.ErrInvalidSpec)
		}
	} else {
		raw = strings.Split(sel, ",")
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		a, err := ParseABI(strings.TrimSpace(r))
		if err != nil {
			return nil, err
		}
		if !seen[a.String()] {
			seen[a.String()] = true
			out = append(out, a.String())
		}
	}
	return out, nil
}

func (s *Spec) vars(abi ABI) map[string]string {
	return map[string]string{
		"NAME": s.Name, "VERSION": s.Version, "ABI": abi.String(),
		"ARCH": abi.Arch, "GOARCH": abi.GOARCH(),
	}
}

func (s *Spec) prefix() string {
	if s.Prefix == "" {
		return "/usr/local"
	}
	return path.Clean(s.Prefix)
}

func (s *Spec) dest(d string) string {
	if path.IsAbs(d) {
		return path.Clean(d)
	}
	return path.Join(s.prefix(), d)
}

// GoBuild runs `go build` for the manifest's build section, cross-compiling
// for FreeBSD with CGO disabled, -trimpath and a fixed build id. It is a no-op
// when the manifest has no build section.
func (s *Spec) GoBuild(ctx context.Context, opt BuildOptions) error {
	if s.Build == nil || opt.SkipGoBuild {
		return nil
	}
	abi, err := s.abi(&opt)
	if err != nil {
		return err
	}
	vars := s.vars(abi)
	b := s.Build
	targets, err := b.Targets()
	if err != nil {
		return err
	}
	exp := func(v string) (string, error) { return expandVars(v, vars) }
	ld := b.LDFlags
	if ld == "" {
		ld = "-s -w"
	}
	if ld, err = exp(ld); err != nil {
		return err
	}
	dir := filepath.Join(opt.baseDir(), b.Dir)
	versionLogged := false
	for _, t := range targets {
		out, err := exp(t.Output)
		if err != nil {
			return err
		}
		pkgArg, err := exp(t.Package)
		if err != nil {
			return err
		}
		absOut := out
		if !filepath.IsAbs(absOut) {
			absOut = filepath.Join(opt.baseDir(), out)
		}
		if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
			return err
		}
		args := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags", ld + " -buildid="}
		if len(b.Tags) > 0 {
			args = append(args, "-tags", strings.Join(b.Tags, ","))
		}
		absOutArg, err := filepath.Abs(absOut)
		if err != nil {
			return err
		}
		args = append(args, "-o", absOutArg, pkgArg)
		env := goEnv(os.Environ(), abi)
		if !versionLogged {
			versionLogged = true
			if v := goVersion(ctx, dir, env); v != "" {
				opt.logf("building with %s", v)
			}
		}
		cmd := newGoCmd(ctx, dir, env, args...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		opt.logf("go %s", strings.Join(args, " "))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: %v\n%s", pkgerr.ErrBuild, err, strings.TrimSpace(buf.String()))
		}
	}
	return nil
}

// Package resolves the manifest into a fully populated [Package] for one ABI:
// variables are expanded, source files located, user/group scripts generated
// and parent directories declared. Files must already exist (run GoBuild first).
func (s *Spec) Package(opt BuildOptions) (*Package, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s = s.withService() // generated rc.d script, newsyslog fragment and service user
	abi, err := s.abi(&opt)
	if err != nil {
		return nil, err
	}
	vars := s.vars(abi)
	base := opt.baseDir()
	p := &Package{
		Name: s.Name, Version: s.Version, Origin: s.Origin, Comment: s.Comment,
		Desc: strings.TrimRight(s.Desc, "\n"), WWW: s.WWW, Maintainer: s.Maintainer,
		ABI: abi.String(), Prefix: s.prefix(), LicenseLogic: s.LicenseLogic,
		Categories: s.Categories, Scripts: map[string]string{},
	}
	if p.MinOSVersion, err = s.MinOS(); err != nil {
		return nil, err
	}
	p.Licenses = append(p.Licenses, s.Licenses...)
	if s.License != "" {
		p.Licenses = append([]string{s.License}, p.Licenses...)
	}
	if len(p.Licenses) > 1 && p.LicenseLogic == "" {
		return nil, specErr("several licenses need license_logic: and|or")
	}
	for _, d := range s.Deps {
		p.Deps = append(p.Deps, Dep(d))
	}
	newest := time.Time{}
	note := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	addFile := func(f File, src os.FileInfo) {
		if src != nil {
			note(src.ModTime())
		}
		p.Files = append(p.Files, f)
	}
	resolve := func(src string) (string, error) {
		src, err := expandVars(src, vars)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(src) {
			src = filepath.Join(base, src)
		}
		return src, nil
	}
	defMode := func(m *Mode, st os.FileInfo, fallback uint32) uint32 {
		if m != nil {
			return uint32(*m)
		}
		if st != nil {
			if st.Mode()&0o111 != 0 {
				return 0o755
			}
			return 0o644
		}
		return fallback
	}
	for i, fs_ := range s.Files {
		dst := s.dest(fs_.Destination)
		if fs_.Symlink != "" {
			addFile(File{Path: dst, Symlink: fs_.Symlink, Owner: fs_.Owner, Group: fs_.Group}, nil)
			continue
		}
		if fs_.Content != "" {
			addFile(File{Path: dst, Data: []byte(fs_.Content), Mode: defMode(fs_.Mode, nil, 0o644),
				Owner: fs_.Owner, Group: fs_.Group, Config: fs_.Config}, nil)
			continue
		}
		src, err := resolve(fs_.Source)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(src)
		if err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		if !st.IsDir() {
			addFile(File{Path: dst, Source: src, Mode: defMode(fs_.Mode, st, 0o644),
				Owner: fs_.Owner, Group: fs_.Group, Config: fs_.Config}, st)
			continue
		}
		err = filepath.WalkDir(src, func(fp string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				return nil
			}
			fst, err := d.Info()
			if err != nil {
				return err
			}
			if !fst.Mode().IsRegular() {
				return fmt.Errorf("files[%d]: %s is not a regular file (symlinks are not supported)", i, fp)
			}
			rel, _ := filepath.Rel(src, fp)
			addFile(File{Path: path.Join(dst, filepath.ToSlash(rel)), Source: fp, Mode: defMode(fs_.Mode, fst, 0o644),
				Owner: fs_.Owner, Group: fs_.Group, Config: fs_.Config}, fst)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for i, sv := range s.Services {
		dst := sv.Destination
		if dst == "" {
			dst = "etc/rc.d/" + sv.Name
		}
		f := File{Path: s.dest(dst), Mode: 0o555}
		if sv.Content != "" {
			f.Data = []byte(sv.Content)
			addFile(f, nil)
			continue
		}
		src, err := resolve(sv.Source)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(src)
		if err != nil {
			return nil, fmt.Errorf("services[%d]: %w", i, err)
		}
		f.Source = src
		addFile(f, st)
	}
	for _, d := range s.Dirs {
		dm := uint32(0o755)
		if d.Mode != nil {
			dm = uint32(*d.Mode)
		}
		p.Dirs = append(p.Dirs, Dir{Path: s.dest(d.Path), Mode: dm, Owner: d.Owner, Group: d.Group})
	}
	p.Dirs = append(p.Dirs, s.autoDirs(p)...)

	// users and groups
	gen := userGroupScript(s)
	for _, g := range s.Groups {
		p.Groups = append(p.Groups, g.Name)
	}
	for _, u := range s.Users {
		p.Users = append(p.Users, u.Name)
		g := u.Group
		if g == "" {
			g = u.Name
		}
		if !contains(p.Groups, g) {
			p.Groups = append(p.Groups, g)
		}
	}
	sort.Strings(p.Users)
	sort.Strings(p.Groups)

	for _, name := range pkgfile.ScriptNames {
		var body string
		sc, ok := s.Scripts[name]
		if ok {
			body = sc.Inline
			if sc.File != "" {
				src, err := resolve(sc.File)
				if err != nil {
					return nil, err
				}
				st, err := os.Stat(src)
				if err != nil {
					return nil, fmt.Errorf("scripts.%s: %w", name, err)
				}
				note(st.ModTime())
				b, err := os.ReadFile(src)
				if err != nil {
					return nil, err
				}
				body = string(b)
			}
		}
		if name == "pre-install" && gen != "" {
			body = gen + body
		}
		body = sc.Gen + body
		if body != "" {
			p.Scripts[name] = strings.TrimRight(body, "\n")
		}
	}
	if len(p.Scripts) == 0 {
		p.Scripts = nil
	}

	switch {
	case !opt.Epoch.IsZero():
		p.Time = opt.Epoch
	default:
		if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("SOURCE_DATE_EPOCH %q: %w", v, err)
			}
			p.Time = time.Unix(n, 0)
		} else {
			p.Time = newest
		}
	}
	return p, nil
}

// wellKnownDirs are never claimed by a package: they belong to the base
// system or the ports tree layout.
var wellKnownDirs = []string{
	"", "bin", "sbin", "lib", "libexec", "libdata", "libdata/pkgconfig", "include", "etc", "etc/rc.d",
	"etc/periodic", "etc/pkg", "share", "share/doc", "share/examples", "share/man", "man", "www",
	"share/licenses", "etc/newsyslog.conf.d", "etc/syslog.d", "etc/devd", "etc/sysctl.conf.d",
}

// autoDirs claims every directory below the prefix that holds package files
// (and is not a well-known shared directory) so deinstall removes it when
// empty. Directories outside the prefix must be declared with dirs:.
func (s *Spec) autoDirs(p *Package) []Dir {
	prefix := s.prefix()
	have := map[string]bool{}
	for _, d := range p.Dirs {
		have[d.Path] = true
	}
	var out []Dir
	for _, f := range p.Files {
		for dir := path.Dir(f.Path); strings.HasPrefix(dir, prefix+"/"); dir = path.Dir(dir) {
			rel := strings.TrimPrefix(dir, prefix+"/")
			if contains(wellKnownDirs, rel) || have[dir] {
				continue
			}
			have[dir] = true
			out = append(out, Dir{Path: dir, Mode: 0o755})
		}
	}
	return out
}

func userGroupScript(s *Spec) string {
	if len(s.Users) == 0 && len(s.Groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# generated by pkgops: users and groups\n")
	b.WriteString("if [ -n \"${PKG_ROOTDIR}\" ] && [ \"${PKG_ROOTDIR}\" != \"/\" ]; then\n  PW=\"/usr/sbin/pw -R ${PKG_ROOTDIR}\"\nelse\n  PW=/usr/sbin/pw\nfi\n")
	done := map[string]bool{}
	group := func(name string, gid int) {
		if done[name] {
			return
		}
		done[name] = true
		add := "${PW} groupadd " + name
		if gid > 0 {
			add += " -g " + strconv.Itoa(gid)
		}
		fmt.Fprintf(&b, "if ! ${PW} groupshow %s >/dev/null 2>&1; then\n  %s || exit $?\nfi\n", name, add)
	}
	for _, g := range s.Groups {
		group(g.Name, g.GID)
	}
	for _, u := range s.Users {
		g := u.Group
		if g == "" {
			g = u.Name
		}
		group(g, 0)
	}
	for _, u := range s.Users {
		g := u.Group
		if g == "" {
			g = u.Name
		}
		home, shell, comment := u.Home, u.Shell, u.Comment
		if home == "" {
			home = "/nonexistent"
		}
		if shell == "" {
			shell = "/usr/sbin/nologin"
		}
		if comment == "" {
			comment = u.Name + " user"
		}
		add := fmt.Sprintf("${PW} useradd %s -g %s -d %s -s %s -c '%s'", u.Name, g, home, shell, comment)
		if u.UID > 0 {
			add += " -u " + strconv.Itoa(u.UID)
		}
		fmt.Fprintf(&b, "if ! ${PW} usershow %s >/dev/null 2>&1; then\n  %s || exit $?\nfi\n", u.Name, add)
	}
	return b.String()
}

// Built is the result of [BuildPackage].
type Built struct {
	Path string // <OutDir>/<ABI>/<name>-<version>.pkg
	Info *Info
}

// BuildPackage is the one-call pipeline used by `pkgops build`: go build (if
// configured), resolve the manifest, write the .pkg.
func BuildPackage(ctx context.Context, s *Spec, opt BuildOptions) (*Built, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := s.GoBuild(ctx, opt); err != nil {
		return nil, err
	}
	p, err := s.Package(opt)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := opt.OutDir
	if out == "" {
		out = "dist"
	}
	file := filepath.Join(out, p.ABI, p.Name+"-"+p.Version+".pkg")
	info, err := p.WriteFile(file, opt.Write)
	if err != nil {
		return nil, err
	}
	opt.logf("wrote %s (%d bytes, %d files)", file, info.Size, len(p.Files))
	return &Built{Path: file, Info: info}, nil
}

// prefixWriter tags each line of progress output with the ABI it belongs to and
// serialises writes from the parallel builds.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range strings.SplitAfter(strings.TrimRight(string(b), "\n"), "\n") {
		fmt.Fprintf(p.w, "%s%s\n", p.prefix, strings.TrimRight(line, "\n"))
	}
	return len(b), nil
}

// BuildAll builds the package for every ABI selected by opt.ABI ("" or "all" =
// every ABI in the manifest, or a comma-separated list). Architectures build in
// parallel; ABIs sharing an architecture build one after another because they
// share the go build output path. Results are in selector order, one per
// successful ABI. Any failure fails the run: the error names each failing ABI
// and the packages that did build are still returned.
func BuildAll(ctx context.Context, s *Spec, opt BuildOptions) ([]*Built, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	abis, err := s.ABIList(opt.ABI)
	if err != nil {
		return nil, err
	}
	groups := map[string][]int{} // GOARCH -> indexes into abis
	for i, a := range abis {
		p, _ := ParseABI(a)
		groups[p.Arch] = append(groups[p.Arch], i)
	}
	results := make([]*Built, len(abis))
	errs := make([]error, len(abis))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, idx := range groups {
		wg.Add(1)
		go func(idx []int) {
			defer wg.Done()
			for _, i := range idx {
				o := opt
				o.ABI = abis[i]
				if opt.Log != nil {
					o.Log = prefixWriter{&mu, opt.Log, "[" + abis[i] + "] "}
				}
				b, err := BuildPackage(ctx, s, o)
				if err != nil {
					errs[i] = fmt.Errorf("%s: %w", abis[i], err)
					o.logf("FAILED")
					continue
				}
				results[i] = b
			}
		}(idx)
	}
	wg.Wait()
	var out []*Built
	for _, r := range results {
		if r != nil {
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}
