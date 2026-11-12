package build

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Lint severities.
const (
	LintError   = "error"
	LintWarning = "warning"
)

// LintFinding is one result of [Lint].
type LintFinding struct {
	Package  string `json:"package,omitempty"` // recipe name; empty for the source tree as a whole
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// LintRecipe is one recipe handed to [Lint]: its name, the directory its
// relative paths refer to, and its parsed manifest.
type LintRecipe struct {
	Dir  string
	Spec *Spec
}

var reRCKey = regexp.MustCompile(`^#\s*(PROVIDE|REQUIRE|BEFORE|KEYWORD):\s*(.*)$`)

type rcInfo struct {
	file     string // file name the script is installed under
	provide  []string
	require  []string
	before   []string
	readable bool
}

func parseRC(file, text string) rcInfo {
	r := rcInfo{file: file, readable: true}
	for _, line := range strings.Split(text, "\n") {
		m := reRCKey.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		words := strings.Fields(m[2])
		switch m[1] {
		case "PROVIDE":
			r.provide = append(r.provide, words...)
		case "REQUIRE":
			r.require = append(r.require, words...)
		case "BEFORE":
			r.before = append(r.before, words...)
		}
	}
	return r
}

// sourceRel maps a `source:` of a recipe to the path below the source tree when
// it points into it (`src/packaging/rc.d/x` gives `packaging/rc.d/x`); ok is
// false for anything else.
func sourceRel(source string) (string, bool) {
	p := path.Clean(filepath.ToSlash(source))
	if strings.HasPrefix(p, "src/") {
		return strings.TrimPrefix(p, "src/"), true
	}
	return "", false
}

// Lint checks recipes against what the source tree they package ships and
// against FreeBSD packaging practice, the way stage-qa does for a port. srcDir
// is the fetched source tree (what `src/` means in a recipe); with "" the
// source-tree rules are skipped. Several recipes may be given together when
// they package one source (a file under packaging/ may belong to any of them).
//
// Rules:
//   - packaging-file: a file under packaging/{rc.d,man,completion} or a
//     packaging/newsyslog* file that no given recipe installs (warning)
//   - rc-require: a REQUIRE or BEFORE name of an installed rc.d script that no
//     script of the same recipe and no base rc.d script provides (warning)
//   - rc-name: the file name of an installed rc.d script is not among its
//     PROVIDE names (warning)
//   - system-dir: a dirs: entry claims a directory of the system layout (error)
//   - conflicting-dep: a dependency on one flavour of a package whose flavours
//     conflict (error)
//   - rc-unread: an rc.d script whose text could not be read, so its keywords
//     were not checked (warning)
func Lint(recipes []LintRecipe, srcDir string) []LintFinding {
	var out []LintFinding
	add := func(pkg, rule, sev, format string, a ...any) {
		out = append(out, LintFinding{Package: pkg, Rule: rule, Severity: sev, Message: specMsg(format, a...)})
	}
	installed := map[string]bool{} // source-tree relative files and directories installed by any recipe
	for _, r := range recipes {
		s := r.Spec
		for _, f := range s.Files {
			if rel, ok := sourceRel(f.Source); ok {
				installed[rel] = true
			}
		}
		for _, sv := range s.Services {
			if rel, ok := sourceRel(sv.Source); ok {
				installed[rel] = true
			}
		}
	}
	covered := func(rel string) bool {
		for p := rel; p != "." && p != "/" && p != ""; p = path.Dir(p) {
			if installed[p] {
				return true
			}
		}
		return false
	}
	if srcDir != "" {
		root := filepath.Join(srcDir, "packaging")
		var missing []string
		scan := func(dir string, recursive bool) {
			base := filepath.Join(root, dir)
			_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if p != base && !recursive {
						return fs.SkipDir
					}
					return nil
				}
				rel, _ := filepath.Rel(srcDir, p)
				if !covered(filepath.ToSlash(rel)) {
					missing = append(missing, filepath.ToSlash(rel))
				}
				return nil
			})
		}
		scan("rc.d", false)
		scan("man", true)
		scan("completion", true)
		if ents, err := os.ReadDir(root); err == nil {
			for _, e := range ents {
				if !e.IsDir() && strings.HasPrefix(e.Name(), "newsyslog") && !covered("packaging/"+e.Name()) {
					missing = append(missing, "packaging/"+e.Name())
				}
			}
		}
		sort.Strings(missing)
		for _, m := range missing {
			add("", "packaging-file", LintWarning, "%s is in the source tree but no recipe installs it", m)
		}
	}
	for _, r := range recipes {
		s := r.Spec
		name := s.Name
		var scripts []rcInfo
		read := func(file, source, content string) {
			if content != "" {
				scripts = append(scripts, parseRC(file, content))
				return
			}
			var p string
			if rel, ok := sourceRel(source); ok {
				if srcDir == "" {
					scripts = append(scripts, rcInfo{file: file})
					return
				}
				p = filepath.Join(srcDir, filepath.FromSlash(rel))
			} else {
				p = filepath.Join(r.Dir, filepath.FromSlash(source))
			}
			b, err := os.ReadFile(p)
			if err != nil {
				scripts = append(scripts, rcInfo{file: file})
				return
			}
			scripts = append(scripts, parseRC(file, string(b)))
		}
		for _, sv := range s.Services {
			dst := sv.Destination
			if dst == "" {
				dst = "etc/rc.d/" + sv.Name
			}
			read(path.Base(dst), sv.Source, sv.Content)
		}
		if s.Service != nil {
			read(s.svcName(), "", s.rcScript())
		}
		provided := map[string]bool{}
		for _, sc := range scripts {
			for _, p := range sc.provide {
				provided[p] = true
			}
		}
		for _, sc := range scripts {
			if !sc.readable {
				add(name, "rc-unread", LintWarning, "%s: the rc.d script was not read (no source tree), its keywords were not checked", sc.file)
				continue
			}
			if len(sc.provide) == 0 || !contains(sc.provide, sc.file) {
				add(name, "rc-name", LintWarning, "etc/rc.d/%s provides %s: the file name must be one of the PROVIDE names", sc.file, strings.Join(sc.provide, " "))
			}
			for _, kw := range []struct {
				name  string
				words []string
			}{{"REQUIRE", sc.require}, {"BEFORE", sc.before}} {
				for _, w := range kw.words {
					if !provided[w] && !baseRCNames[w] {
						add(name, "rc-require", LintWarning, "etc/rc.d/%s: %s %s is provided neither by a script of this package nor by the base system", sc.file, kw.name, w)
					}
				}
			}
		}
		for _, d := range s.Dirs {
			rel := strings.TrimPrefix(s.dest(d.Path), s.prefix()+"/")
			if s.dest(d.Path) == s.prefix() {
				rel = ""
			}
			if strings.HasPrefix(s.dest(d.Path), s.prefix()) && IsSystemDir(rel) {
				add(name, "system-dir", LintError, "dirs: %s belongs to the system layout: pkg must not own it", s.dest(d.Path))
			}
		}
		for _, d := range s.Deps {
			if fam, bad := conflictingDeps[d.Origin]; bad {
				add(name, "conflicting-dep", LintError, "depends on %s (%s): flavours of %s conflict, so a host with another flavour is offered a replacement; check for the program at run time instead", d.Name, d.Origin, fam)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return false
	})
	return out
}

func specMsg(format string, a ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, a...))
}
