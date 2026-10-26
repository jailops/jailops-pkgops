package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/testutil"
)

func TestSpecParse(t *testing.T) {
	good := `
name: demo
version: "1.2.3"
comment: demo
maintainer: a@b.c
www: https://example.com
abi: FreeBSD:15:amd64
deps: [{name: curl, origin: ftp/curl, version: "8.0"}, {name: pcre2, origin: devel/pcre2, version: "10.49"}]
files:
  - content: "x=1\n"
    destination: etc/demo.conf
    mode: 0640
    config: true
scripts:
  post-install: "echo ok"
  pre-deinstall: {file: scripts/pre}
`
	s, err := ParseSpec([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if *s.Files[0].Mode != 0o640 || s.Deps[0].Name != "curl" || s.Deps[1].Origin != "devel/pcre2" || s.Scripts["pre-deinstall"].File != "scripts/pre" {
		t.Errorf("parsed wrong: %+v", s)
	}
	for name, bad := range map[string]string{
		"typo key":       "name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://example.com\nabii: x\n",
		"no comment":     "name: a\nversion: 1\n",
		"bad abi":        "name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://example.com\nabi: Linux:6:amd64\n",
		"bad arch":       "name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:riscv64\n",
		"dotdot":         "name: a\nversion: 1\ncomment: c\nfiles: [{content: x, destination: ../x}]\n",
		"both sources":   "name: a\nversion: 1\ncomment: c\nfiles: [{source: a, content: b, destination: x}]\n",
		"bad script":     "name: a\nversion: 1\ncomment: c\nscripts: {post-upgrade: x}\n",
		"bad user":       "name: a\nversion: 1\ncomment: c\nusers: [{name: \"a b\"}]\n",
		"empty":          "",
		"dep w/o origin": "name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://example.com\ndeps: [{name: x}]\n",
		"no maintainer":  "name: a\nversion: 1\ncomment: c\n",
	} {
		if _, err := ParseSpec([]byte(bad)); !errors.Is(err, pkgerr.ErrInvalidSpec) && !errors.Is(err, pkgerr.ErrUnsupportedABI) {
			t.Errorf("%s: want pkgerr.ErrInvalidSpec, got %v", name, err)
		}
	}
}

func TestSpecPackageEndToEnd(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "share"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin"), []byte("BIN"), 0o755)
	os.WriteFile(filepath.Join(dir, "share", "a.txt"), []byte("A"), 0o600)
	s, err := ParseSpec([]byte(`
name: demo
version: "1.0"
comment: demo
maintainer: a@b.c
www: https://example.com
abi: FreeBSD:15:amd64
licenses: [MIT, BSD2CLAUSE]
license_logic: or
files:
  - {source: bin, destination: sbin/demo}
  - {source: share, destination: share/demo}
services:
  - {name: demo, content: "#!/bin/sh\n"}
users:
  - {name: demo, uid: 900}
groups:
  - {name: extra}
dirs:
  - {path: /var/db/demo, owner: demo, group: demo, mode: 0750}
scripts:
  pre-install: "echo mine"
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Package(BuildOptions{BaseDir: dir, Epoch: testutil.FixedTime})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]File{}
	for _, f := range p.Files {
		paths[f.Path] = f
	}
	for _, want := range []string{"/usr/local/sbin/demo", "/usr/local/share/demo/a.txt", "/usr/local/etc/rc.d/demo"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("missing %s in %v", want, paths)
		}
	}
	if paths["/usr/local/sbin/demo"].Mode != 0o755 || paths["/usr/local/share/demo/a.txt"].Mode != 0o644 || paths["/usr/local/etc/rc.d/demo"].Mode != 0o555 {
		t.Errorf("modes: %+v", paths)
	}
	dirs := map[string]bool{}
	for _, d := range p.Dirs {
		dirs[d.Path] = true
	}
	if !dirs["/usr/local/share/demo"] || !dirs["/var/db/demo"] || dirs["/usr/local/sbin"] || dirs["/usr/local/etc"] {
		t.Errorf("auto dirs wrong: %v", dirs)
	}
	sc := p.Scripts["pre-install"]
	if !strings.Contains(sc, "groupadd demo") || !strings.Contains(sc, "groupadd extra") || !strings.Contains(sc, "-u 900") ||
		!strings.Contains(sc, "-R ${PKG_ROOTDIR}") || !strings.HasSuffix(sc, "echo mine") {
		t.Errorf("pre-install script:\n%s", sc)
	}
	if strings.Join(p.Users, ",") != "demo" || strings.Join(p.Groups, ",") != "demo,extra" {
		t.Errorf("users/groups %v %v", p.Users, p.Groups)
	}
	if p.LicenseLogic != "or" {
		t.Error("license logic lost")
	}
	// missing source is an error
	s.Files[0].Source = "nope"
	if _, err := s.Package(BuildOptions{BaseDir: dir, Epoch: testutil.FixedTime}); err == nil {
		t.Error("missing source file should fail")
	}
}

func TestEpochDefaults(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x")
	os.WriteFile(f, []byte("x"), 0o644)
	old := time.Unix(1600000000, 0)
	os.Chtimes(f, old, old)
	s, _ := ParseSpec([]byte("name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\nfiles: [{source: x, destination: /x}]\n"))
	t.Setenv("SOURCE_DATE_EPOCH", "")
	os.Unsetenv("SOURCE_DATE_EPOCH")
	p, err := s.Package(BuildOptions{BaseDir: dir})
	if err != nil || !p.Time.Equal(old) {
		t.Fatalf("want newest source mtime, got %v %v", p.Time, err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1234567890")
	p, _ = s.Package(BuildOptions{BaseDir: dir})
	if p.Time.Unix() != 1234567890 {
		t.Fatalf("SOURCE_DATE_EPOCH ignored: %v", p.Time)
	}
}

func TestSpecSymlinks(t *testing.T) {
	s, err := ParseSpec([]byte(`
name: demo
version: "1"
comment: demo
maintainer: a@b.c
www: https://example.com
abi: FreeBSD:15:amd64
files:
  - {content: "x", destination: bin/demo-1.2, mode: 0755}
  - {symlink: demo-1.2, destination: bin/demo}
  - {symlink: /usr/local/bin/demo-1.2, destination: /usr/local/bin/demo-abs}
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Package(BuildOptions{Epoch: testutil.FixedTime})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range p.Files {
		got[f.Path] = f.Symlink
	}
	if got["/usr/local/bin/demo"] != "demo-1.2" || got["/usr/local/bin/demo-abs"] != "/usr/local/bin/demo-1.2" || got["/usr/local/bin/demo-1.2"] != "" {
		t.Errorf("%v", got)
	}
	for _, bad := range []string{
		"{symlink: a, content: b, destination: x}",
		"{symlink: a, destination: x, config: true}",
		"{symlink: a, destination: x, mode: 0644}",
	} {
		_, err := ParseSpec([]byte("name: a\nversion: 1\ncomment: c\nmaintainer: a@b.c\nwww: https://x\nfiles: [" + bad + "]\n"))
		if !errors.Is(err, pkgerr.ErrInvalidSpec) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

const multiSpec = `
name: demo
version: "1.0"
comment: demo
maintainer: a@b.c
www: https://example.com
abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]
files:
  - {source: "bin/${ARCH}/demo", destination: bin/demo, mode: 0755}
`

func multiDir(t *testing.T, arches ...string) string {
	dir := t.TempDir()
	for _, a := range arches {
		os.MkdirAll(filepath.Join(dir, "bin", a), 0o755)
		os.WriteFile(filepath.Join(dir, "bin", a, "demo"), []byte("binary for "+a), 0o755)
	}
	return dir
}

func TestBuildAllEveryABIDeterministic(t *testing.T) {
	dir := multiDir(t, "amd64", "aarch64")
	s, err := ParseSpec([]byte(multiSpec))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	var first [][]byte
	for run := 0; run < 2; run++ {
		out := t.TempDir()
		res, err := BuildAll(context.Background(), s, BuildOptions{BaseDir: dir, OutDir: out, Epoch: testutil.FixedTime, Log: &log})
		if err != nil || len(res) != 2 {
			t.Fatalf("%v %v", res, err)
		}
		if res[0].Info.ABI != "FreeBSD:15:amd64" || res[1].Info.ABI != "FreeBSD:15:aarch64" || res[1].Info.Arch != "freebsd:15:aarch64:64" {
			t.Errorf("order or arch: %s %s", res[0].Info.ABI, res[1].Info.ABI)
		}
		for i, r := range res {
			b, _ := os.ReadFile(r.Path)
			if !strings.Contains(r.Path, r.Info.ABI) {
				t.Errorf("not in its ABI dir: %s", r.Path)
			}
			if run == 0 {
				first = append(first, b)
			} else if !bytes.Equal(first[i], b) {
				t.Errorf("%s differs between runs", r.Info.ABI)
			}
		}
	}
	if !strings.Contains(log.String(), "[FreeBSD:15:aarch64] wrote ") || !strings.Contains(log.String(), "[FreeBSD:15:amd64] wrote ") {
		t.Errorf("per-ABI progress missing:\n%s", log.String())
	}
	if bytes.Equal(first[0], first[1]) {
		t.Error("amd64 and aarch64 packages must differ")
	}
}

func TestBuildAllSelectorsAndFailureNaming(t *testing.T) {
	s, _ := ParseSpec([]byte(multiSpec))
	dir := multiDir(t, "amd64") // no aarch64 binary
	res, err := BuildAll(context.Background(), s, BuildOptions{BaseDir: dir, OutDir: t.TempDir(), Epoch: testutil.FixedTime})
	if err == nil || !strings.Contains(err.Error(), "FreeBSD:15:aarch64") || strings.Contains(err.Error(), "FreeBSD:15:amd64:") {
		t.Fatalf("error must name the failing ABI only: %v", err)
	}
	if len(res) != 1 || res[0].Info.ABI != "FreeBSD:15:amd64" {
		t.Errorf("successful ABI should still be returned: %v", res)
	}
	res, err = BuildAll(context.Background(), s, BuildOptions{BaseDir: dir, OutDir: t.TempDir(), ABI: "FreeBSD:15:amd64,FreeBSD:15:amd64", Epoch: testutil.FixedTime})
	if err != nil || len(res) != 1 {
		t.Errorf("explicit list (deduplicated): %v %v", res, err)
	}
	if _, err := BuildAll(context.Background(), s, BuildOptions{BaseDir: dir, ABI: "FreeBSD:15:sparc"}); err == nil {
		t.Error("bad ABI accepted")
	}
	// the single-package API refuses to guess among several ABIs
	if _, err := s.Package(BuildOptions{BaseDir: dir}); err == nil {
		t.Error("Package with several ABIs and no selection should fail")
	}
	// abi: still works alone
	one, _ := ParseSpec([]byte(strings.Replace(multiSpec, "abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]", "abi: FreeBSD:15:amd64", 1)))
	if res, err := BuildAll(context.Background(), one, BuildOptions{BaseDir: dir, OutDir: t.TempDir(), Epoch: testutil.FixedTime}); err != nil || len(res) != 1 {
		t.Errorf("%v %v", res, err)
	}
}
