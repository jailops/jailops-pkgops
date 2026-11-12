package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree writes files (path -> content) below dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

const lintHead = "name: daemonpkg\nversion: \"1.0\"\ncomment: demo\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\n"

func lintOne(t *testing.T, yaml string, tree map[string]string) []LintFinding {
	t.Helper()
	src := t.TempDir()
	writeTree(t, src, tree)
	s, err := ParseSpec([]byte(lintHead + yaml))
	if err != nil {
		t.Fatal(err)
	}
	return Lint([]LintRecipe{{Dir: t.TempDir(), Spec: s}}, src)
}

func has(fs []LintFinding, rule, sub string) bool {
	for _, f := range fs {
		if f.Rule == rule && strings.Contains(f.Message, sub) {
			return true
		}
	}
	return false
}

// A source tree modelled on a daemon with a helper: the helper's script needs a
// third script that the recipe must install too.
var daemonTree = map[string]string{
	"packaging/rc.d/daemond":       "#!/bin/sh\n# PROVIDE: daemond\n# REQUIRE: LOGIN\n# KEYWORD: shutdown\n",
	"packaging/rc.d/daemon_aux":    "#!/bin/sh\n# PROVIDE: daemon_aux\n# REQUIRE: zfs daemon_revert syslogd\n# BEFORE: daemond\n",
	"packaging/rc.d/daemon_revert": "#!/bin/sh\n# PROVIDE: daemon_revert\n# REQUIRE: FILESYSTEMS\n",
	"packaging/newsyslog.conf":     "/var/log/daemon.log\n",
	"packaging/man/daemond.8":      ".Dd today\n",
}

const completeRecipe = `services:
  - {name: daemond, source: src/packaging/rc.d/daemond}
  - {name: daemon_aux, source: src/packaging/rc.d/daemon_aux}
  - {name: daemon_revert, source: src/packaging/rc.d/daemon_revert}
files:
  - {source: src/packaging/newsyslog.conf, destination: etc/newsyslog.conf.d/daemon.conf, config: true}
  - {source: src/packaging/man/daemond.8, destination: share/man/man8/daemond.8}
`

func TestLintCleanRecipe(t *testing.T) {
	if f := lintOne(t, completeRecipe, daemonTree); len(f) != 0 {
		t.Errorf("a complete recipe must lint clean: %+v", f)
	}
}

// The regression the lint exists for: the recipe installs the helper script
// that requires daemon_revert, but not daemon_revert itself nor the newsyslog file.
func TestLintCatchesAMissingRcScript(t *testing.T) {
	recipe := `services:
  - {name: daemond, source: src/packaging/rc.d/daemond}
  - {name: daemon_aux, source: src/packaging/rc.d/daemon_aux}
files:
  - {source: src/packaging/man/daemond.8, destination: share/man/man8/daemond.8}
`
	f := lintOne(t, recipe, daemonTree)
	if !has(f, "packaging-file", "packaging/rc.d/daemon_revert is in the source tree but no recipe installs it") ||
		!has(f, "packaging-file", "packaging/newsyslog.conf is in the source tree but no recipe installs it") ||
		!has(f, "rc-require", "etc/rc.d/daemon_aux: REQUIRE daemon_revert is provided neither by a script of this package nor by the base system") {
		t.Errorf("findings: %+v", f)
	}
	for _, x := range f {
		if x.Severity != LintWarning {
			t.Errorf("%+v must be a warning", x)
		}
	}
}

func TestLintTwoRecipesOfOneSource(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"packaging/rc.d/one": "# PROVIDE: one\n", "packaging/rc.d/two": "# PROVIDE: two\n",
	})
	a, _ := ParseSpec([]byte(lintHead + "services: [{name: one, source: src/packaging/rc.d/one}]\n"))
	b, _ := ParseSpec([]byte(strings.Replace(lintHead, "daemonpkg", "otherpkg", 1) + "services: [{name: two, source: src/packaging/rc.d/two}]\n"))
	if f := Lint([]LintRecipe{{Dir: t.TempDir(), Spec: a}, {Dir: t.TempDir(), Spec: b}}, src); len(f) != 0 {
		t.Errorf("a file installed by the other recipe of the group is fine: %+v", f)
	}
	if f := Lint([]LintRecipe{{Dir: t.TempDir(), Spec: a}}, src); !has(f, "packaging-file", "packaging/rc.d/two") {
		t.Errorf("alone, the first recipe misses two: %+v", f)
	}
}

func TestLintRcRules(t *testing.T) {
	f := lintOne(t, `services:
  - {name: other, destination: etc/rc.d/other, source: src/packaging/rc.d/x}
`, map[string]string{"packaging/rc.d/x": "# PROVIDE: something_else\n# REQUIRE: NOSUCH\n"})
	if !has(f, "rc-name", "etc/rc.d/other provides something_else") || !has(f, "rc-require", "REQUIRE NOSUCH") {
		t.Errorf("findings: %+v", f)
	}
	// without a source tree the scripts are not read, and the lint says so
	s, _ := ParseSpec([]byte(lintHead + "services: [{name: x, source: src/packaging/rc.d/x}]\n"))
	if f := Lint([]LintRecipe{{Dir: t.TempDir(), Spec: s}}, ""); !has(f, "rc-unread", "x: the rc.d script was not read") {
		t.Errorf("findings: %+v", f)
	}
	// a generated service provides its own name
	s, _ = ParseSpec([]byte(lintHead + "service: {name: daemonpkg, command: /usr/local/bin/x, require: [LOGIN, NOSUCH]}\n"))
	if f := Lint([]LintRecipe{{Dir: t.TempDir(), Spec: s}}, ""); len(f) != 1 || !has(f, "rc-require", "REQUIRE NOSUCH") {
		t.Errorf("generated service: %+v", f)
	}
}

func TestLintSystemDirsAndDeps(t *testing.T) {
	f := lintOne(t, `dirs:
  - {path: etc/rc.d}
  - {path: share/man/man8}
  - {path: /var/db/daemon}
deps:
  - {name: git-lite, origin: devel/git, version: "2.56.0"}
  - {name: curl, origin: ftp/curl, version: "8.0"}
`, nil)
	if !has(f, "system-dir", "dirs: /usr/local/etc/rc.d belongs to the system layout") || !has(f, "system-dir", "/usr/local/share/man/man8") ||
		has(f, "system-dir", "/var/db/daemon") || !has(f, "conflicting-dep", "git-lite (devel/git)") || has(f, "conflicting-dep", "curl") {
		t.Errorf("findings: %+v", f)
	}
	for _, x := range f {
		if x.Severity != LintError {
			t.Errorf("%+v must be an error", x)
		}
	}
}
