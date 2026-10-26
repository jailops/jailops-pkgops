package build

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the in-memory form of a pkgops.yaml manifest. It is what the CLI
// loads; SDK users may also fill it from Go and call [Spec.Package], or skip
// it entirely and construct a [Package] directly.
type Spec struct {
	Name         string   `yaml:"name"`
	Version      string   `yaml:"version"`
	Origin       string   `yaml:"origin"`
	Comment      string   `yaml:"comment"`
	Desc         string   `yaml:"desc"`
	WWW          string   `yaml:"www"`
	Maintainer   string   `yaml:"maintainer"`
	License      string   `yaml:"license"`
	Licenses     []string `yaml:"licenses"`
	LicenseLogic string   `yaml:"license_logic"` // single, and, or
	Categories   []string `yaml:"categories"`
	// ABI is the target pkg ABI, e.g. "FreeBSD:15:amd64". It can be
	// overridden per build (BuildOptions.ABI / --abi).
	ABI string `yaml:"abi"`
	// ABIs lists every target built by one `pkgops build` run (and by
	// BuildAll). It may be combined with, or replace, abi.
	ABIs   []string `yaml:"abis"`
	Prefix string   `yaml:"prefix"` // default /usr/local

	// MinRelease is the oldest FreeBSD release the package supports, as
	// MAJOR.MINOR ("15.1"). The ABI only names the major version, so this is
	// what keeps a package built on 15.1 away from 15.0: it is written as the
	// FreeBSD_version annotation and pkg(8) refuses the install on an older
	// system. MinOSVersion is the same thing as a __FreeBSD_version number
	// (1501000); give at most one of them.
	MinRelease   string `yaml:"min_release"`
	MinOSVersion int    `yaml:"min_osversion"`

	Deps     []DepSpec             `yaml:"deps"`
	Build    *BuildSpec            `yaml:"build"`
	Files    []FileSpec            `yaml:"files"`
	Dirs     []DirSpec             `yaml:"dirs"`
	Services []ServiceSpec         `yaml:"services"`
	Users    []UserSpec            `yaml:"users"`
	Groups   []GroupSpec           `yaml:"groups"`
	Scripts  map[string]ScriptSpec `yaml:"scripts"`

	// Service generates an rc.d script, the service user and an optional
	// newsyslog fragment (see ServiceDef); services: still installs hand-written scripts.
	Service *ServiceDef `yaml:"service"`
}

// DepSpec is a dependency on another package. pkg skips dependencies that
// lack origin or version, so all three fields are required; version is the
// version of the dependency you built against.
type DepSpec struct {
	Name    string `yaml:"name"`
	Origin  string `yaml:"origin"`
	Version string `yaml:"version"`
}

// GoPackage is one executable of a Go build: a main package and where its
// binary is written.
type GoPackage struct {
	Package string `yaml:"package"` // e.g. ./cmd/jailops
	Output  string `yaml:"output"`  // default build/${ARCH}/<last element of package>
}

// BuildSpec describes the optional Go build step.
type BuildSpec struct {
	Type    string `yaml:"type"`    // only "go"
	Package string `yaml:"package"` // e.g. ./cmd/hello (single executable; or use Packages)
	Output  string `yaml:"output"`  // default build/${ARCH}/${NAME}
	// Packages builds several executables into the same package, each for
	// every ABI. Mutually exclusive with package/output.
	Packages []GoPackage `yaml:"packages"`
	Dir      string      `yaml:"dir"`     // module directory, default "."
	LDFlags  string      `yaml:"ldflags"` // default "-s -w"
	Tags     []string    `yaml:"tags"`
}

// Mode is a Unix permission value written in octal in YAML ("0755" or 0755).
type Mode uint32

// UnmarshalYAML parses the scalar text as octal.
func (m *Mode) UnmarshalYAML(n *yaml.Node) error {
	v, err := strconv.ParseUint(strings.TrimPrefix(n.Value, "0o"), 8, 32)
	if err != nil || v > 07777 {
		return fmt.Errorf("line %d: invalid octal mode %q", n.Line, n.Value)
	}
	*m = Mode(v)
	return nil
}

// FileSpec places one file, or a whole directory tree, into the package.
type FileSpec struct {
	Source      string `yaml:"source"`      // file or directory on disk (relative to the manifest)
	Content     string `yaml:"content"`     // inline content instead of source
	Symlink     string `yaml:"symlink"`     // make destination a symbolic link to this target
	Destination string `yaml:"destination"` // absolute, or relative to prefix
	Mode        *Mode  `yaml:"mode"`
	Owner       string `yaml:"owner"`
	Group       string `yaml:"group"`
	// Config marks the file as pkg "@config": a locally modified copy is kept
	// on upgrade and the new version is written next to it as <file>.pkgnew.
	Config bool `yaml:"config"`
}

// DirSpec declares a directory owned by the package.
type DirSpec struct {
	Path  string `yaml:"path"`
	Mode  *Mode  `yaml:"mode"`
	Owner string `yaml:"owner"`
	Group string `yaml:"group"`
}

// ServiceSpec installs an rc.d script.
type ServiceSpec struct {
	Name        string `yaml:"name"`
	Source      string `yaml:"source"`
	Content     string `yaml:"content"`
	Destination string `yaml:"destination"` // default etc/rc.d/<name>
}

// UserSpec creates a system user in the generated pre-install script.
type UserSpec struct {
	Name    string `yaml:"name"`
	UID     int    `yaml:"uid"`
	Group   string `yaml:"group"` // primary group, default = name
	Home    string `yaml:"home"`  // default /nonexistent
	Shell   string `yaml:"shell"` // default /usr/sbin/nologin
	Comment string `yaml:"comment"`
}

// GroupSpec creates a group in the generated pre-install script.
type GroupSpec struct {
	Name string `yaml:"name"`
	GID  int    `yaml:"gid"`
}

// ScriptSpec is a pre/post install/deinstall script: either an inline
// string or {file: path}.
type ScriptSpec struct {
	Inline string
	File   string
	// Gen is generated text placed before the script (not from YAML).
	Gen string
}

// UnmarshalYAML accepts a scalar (inline script) or {file: path}.
func (s *ScriptSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s.Inline = n.Value
		return nil
	}
	var m struct {
		File string `yaml:"file"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	s.File = m.File
	return nil
}

var (
	reName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	reIdent  = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	reOrigin = regexp.MustCompile(`^[A-Za-z0-9._+-]+/[A-Za-z0-9._+-]+$`)
)

// ParseSpec decodes pkgops.yaml content. Unknown keys are an error so typos do
// not silently produce a wrong package.
func ParseSpec(data []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: empty manifest", pkgerr.ErrInvalidSpec)
		}
		msg := err.Error()
		if strings.Contains(msg, "not found in type") {
			msg += " (PkgOps reads only the documented keys: Go builds, files, users, dirs, simple shell scripts and rc.d services; Lua scripts, other build systems and ports options are out of scope, use poudriere)"
		}
		return nil, fmt.Errorf("%w: %s", pkgerr.ErrInvalidSpec, msg)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadSpec reads and parses a manifest file.
func LoadSpec(file string) (*Spec, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	s, err := ParseSpec(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return s, nil
}

func specErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", pkgerr.ErrInvalidSpec, fmt.Sprintf(format, a...))
}

// Validate checks the static consistency of the manifest.
func (s *Spec) Validate() error {
	if s == nil {
		return specErr("nil spec")
	}
	if !pkgfile.ValidName(s.Name) {
		return specErr("name %q is missing or invalid", s.Name)
	}
	if s.Version == "" || strings.ContainsAny(s.Version, " \t/:") {
		return specErr("version %q is missing or invalid", s.Version)
	}
	if s.Origin != "" && !reOrigin.MatchString(s.Origin) {
		return specErr("origin %q must look like category/name", s.Origin)
	}
	if s.Comment == "" {
		return specErr("comment is required")
	}
	if s.Maintainer == "" || s.WWW == "" {
		return specErr("maintainer and www are required: pkg's repository database cannot index a package without them")
	}
	if s.ABI != "" {
		if _, err := ParseABI(s.ABI); err != nil {
			return err
		}
	}
	for _, a := range s.ABIs {
		if _, err := ParseABI(a); err != nil {
			return err
		}
	}
	if s.MinRelease != "" && s.MinOSVersion != 0 {
		return specErr("give min_release or min_osversion, not both")
	}
	if _, err := s.MinOS(); err != nil {
		return err
	}
	switch s.LicenseLogic {
	case "", "single", "and", "or":
	default:
		return specErr("license_logic %q must be single, and or or", s.LicenseLogic)
	}
	if s.Prefix != "" && !path.IsAbs(s.Prefix) {
		return specErr("prefix %q must be absolute", s.Prefix)
	}
	if s.Build != nil {
		if s.Build.Type != "go" {
			return specErr("%s", errBuildType(s.Build.Type))
		}
		if _, err := s.Build.Targets(); err != nil {
			return err
		}
	}
	for i, d := range s.Deps {
		if !pkgfile.ValidName(d.Name) {
			return specErr("deps[%d]: invalid name %q", i, d.Name)
		}
		if !reOrigin.MatchString(d.Origin) || d.Version == "" {
			return specErr("deps[%d] (%s): origin (category/name) and version are required; pkg skips dependencies without them", i, d.Name)
		}
	}
	for i, f := range s.Files {
		if f.Destination == "" {
			return specErr("files[%d]: destination is required", i)
		}
		n := 0
		for _, v := range []string{f.Source, f.Content, f.Symlink} {
			if v != "" {
				n++
			}
		}
		if n != 1 {
			return specErr("files[%d] (%s): exactly one of source, content or symlink is required", i, f.Destination)
		}
		if f.Symlink != "" && (f.Config || f.Mode != nil) {
			return specErr("files[%d] (%s): a symlink cannot be config or have a mode", i, f.Destination)
		}
		if strings.ContainsAny(f.Symlink, "\n\x00") {
			return specErr("files[%d] (%s): invalid symlink target", i, f.Destination)
		}
		if err := checkDest(f.Destination); err != nil {
			return specErr("files[%d]: %v", i, err)
		}
	}
	for i, d := range s.Dirs {
		if d.Path == "" {
			return specErr("dirs[%d]: path is required", i)
		}
		if err := checkDest(d.Path); err != nil {
			return specErr("dirs[%d]: %v", i, err)
		}
	}
	if err := s.validateService(); err != nil {
		return err
	}
	for i, sv := range s.Services {
		if !reIdent.MatchString(sv.Name) {
			return specErr("services[%d]: invalid name %q", i, sv.Name)
		}
		if (sv.Source == "") == (sv.Content == "") {
			return specErr("services[%d] (%s): exactly one of source or content is required", i, sv.Name)
		}
	}
	for i, u := range s.Users {
		if !reIdent.MatchString(u.Name) {
			return specErr("users[%d]: invalid name %q", i, u.Name)
		}
		if u.Group != "" && !reIdent.MatchString(u.Group) {
			return specErr("users[%d]: invalid group %q", i, u.Group)
		}
		for _, v := range []string{u.Home, u.Shell} {
			if strings.ContainsAny(v, "'\"\n$`\\") {
				return specErr("users[%d]: unsafe characters in home/shell", i)
			}
		}
		if strings.ContainsAny(u.Comment, "'\"\n$`\\") {
			return specErr("users[%d]: unsafe characters in comment", i)
		}
	}
	for i, g := range s.Groups {
		if !reIdent.MatchString(g.Name) {
			return specErr("groups[%d]: invalid name %q", i, g.Name)
		}
	}
	for k := range s.Scripts {
		if !contains(pkgfile.ScriptNames, k) {
			hint := ""
			if strings.Contains(k, "lua") {
				hint = "; Lua scripts are not supported, write a plain shell script"
			}
			return specErr("unknown script %q (valid: %s)%s", k, strings.Join(pkgfile.ScriptNames, ", "), hint)
		}
	}
	return nil
}

// MinOS returns the minimum __FreeBSD_version the manifest asks for, 0 for
// none, and checks that it belongs to the major version of every ABI listed.
func (s *Spec) MinOS() (int, error) {
	v := s.MinOSVersion
	if s.MinRelease != "" {
		var err error
		if v, err = pkgfile.ReleaseOSVersion(s.MinRelease); err != nil {
			return 0, err
		}
	}
	if v < 0 {
		return 0, specErr("min_osversion %d is negative", v)
	}
	if v == 0 {
		return 0, nil
	}
	list := append(append([]string(nil), s.ABIs...), s.ABI)
	for _, a := range list {
		if a == "" {
			continue
		}
		abi, err := ParseABI(a)
		if err != nil {
			continue // reported by the abi checks
		}
		if abi.Major != pkgfile.OSVersionMajor(v) {
			return 0, specErr("minimum release %s (FreeBSD_version %d) does not belong to ABI %s", pkgfile.OSVersionRelease(v), v, abi)
		}
	}
	return v, nil
}

// Target is one resolved Go executable of a build.
type Target struct {
	Package string
	Output  string
}

// Targets resolves the build section into its executables (the single
// package/output form, or the packages list) and checks them.
func (b *BuildSpec) Targets() ([]Target, error) {
	if b.Type != "go" {
		return nil, specErr("%s", errBuildType(b.Type))
	}
	var out []Target
	switch {
	case len(b.Packages) > 0:
		if b.Package != "" || b.Output != "" {
			return nil, specErr("build.packages cannot be combined with build.package or build.output")
		}
		for i, p := range b.Packages {
			if p.Package == "" {
				return nil, specErr("build.packages[%d]: package is required", i)
			}
			o := p.Output
			if o == "" {
				o = "build/${ARCH}/" + path.Base(p.Package)
			}
			out = append(out, Target{p.Package, o})
		}
	case b.Package != "":
		o := b.Output
		if o == "" {
			o = "build/${ARCH}/${NAME}"
		}
		out = append(out, Target{b.Package, o})
	default:
		return nil, specErr("build.package (or build.packages) is required")
	}
	seen := map[string]bool{}
	for _, t := range out {
		if seen[t.Output] {
			return nil, specErr("build: two executables write to %q", t.Output)
		}
		seen[t.Output] = true
	}
	return out, nil
}

func errBuildType(t string) string {
	if t == "" {
		return "build.type is required and must be \"go\""
	}
	return fmt.Sprintf("build.type %q is not supported: PkgOps builds Go programs only (type: go); for Rust, C, cgo or other build systems use poudriere", t)
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func checkDest(p string) error {
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return fmt.Errorf("path %q must not contain ..", p)
		}
	}
	return nil
}

// expandVars substitutes ${NAME}, ${VERSION}, ${ABI}, ${ARCH} and ${GOARCH}.
// Unknown variables are an error.
func expandVars(s string, vars map[string]string) (string, error) {
	var bad []string
	out := os.Expand(s, func(k string) string {
		v, ok := vars[k]
		if !ok {
			bad = append(bad, k)
		}
		return v
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return "", specErr("unknown variable ${%s} in %q", bad[0], s)
	}
	return out, nil
}
