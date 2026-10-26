package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/pkgfile"
)

const specHead = `
name: hello
version: 1.0
comment: hello
maintainer: a@b.c
www: https://example.com
`

func TestMinReleaseBecomesTheFreeBSDVersionAnnotation(t *testing.T) {
	for _, c := range []struct {
		yaml string
		want int
	}{
		{"abi: FreeBSD:15:amd64\nmin_release: \"15.1\"\n", 1501000},
		{"abi: FreeBSD:15:amd64\nmin_release: \"15.0\"\n", 1500000},
		{"abi: FreeBSD:14:amd64\nmin_osversion: 1403000\n", 1403000},
		{"abi: FreeBSD:15:amd64\n", 0},
	} {
		s, err := ParseSpec([]byte(specHead + c.yaml))
		if err != nil {
			t.Fatalf("%q: %v", c.yaml, err)
		}
		p, err := s.Package(BuildOptions{BaseDir: t.TempDir(), Epoch: time.Unix(1700000000, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if p.MinOSVersion != c.want {
			t.Errorf("%q: MinOSVersion %d, want %d", c.yaml, p.MinOSVersion, c.want)
		}
		info, err := p.WriteFile(filepath.Join(t.TempDir(), "x.pkg"), pkgfile.WriteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if info.MinOSVersion() != c.want {
			t.Errorf("%q: annotation %d, want %d", c.yaml, info.MinOSVersion(), c.want)
		}
		if c.want == 0 && strings.Contains(string(info.CompactManifest()), "annotations") {
			t.Error("no minimum, no annotation")
		}
	}
}

func TestMinReleaseValidation(t *testing.T) {
	for yaml, want := range map[string]string{
		"abi: FreeBSD:15:amd64\nmin_release: \"15.1\"\nmin_osversion: 1501000\n": "not both",
		"abi: FreeBSD:15:amd64\nmin_release: \"15\"\n":                           "MAJOR.MINOR",
		"abi: FreeBSD:15:amd64\nmin_release: \"14.3\"\n":                         "does not belong to ABI",
		"abis: [FreeBSD:15:amd64, FreeBSD:14:amd64]\nmin_release: \"15.1\"\n":    "does not belong to ABI",
		"abi: FreeBSD:15:amd64\nmin_osversion: -5\n":                             "negative",
	} {
		_, err := ParseSpec([]byte(specHead + yaml))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", yaml, err, want)
		}
	}
}

func TestOutOfScopeInputsGetAClearError(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"abi: FreeBSD:15:amd64\nbuild: {type: rust, package: ./x}\n", "Go programs only"},
		{"abi: FreeBSD:15:amd64\nbuild: {type: cgo, package: ./x}\n", "use poudriere"},
		{"abi: FreeBSD:15:amd64\nlua: {pre-install: foo}\n", "use poudriere"},
		{"abi: FreeBSD:15:amd64\nscripts: {post-install.lua: {file: x}}\n", "Lua scripts are not supported"},
	} {
		_, err := ParseSpec([]byte(specHead + c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.yaml, err, c.want)
		}
	}
}

func TestBuildTargetsSingleAndMultiple(t *testing.T) {
	one := &BuildSpec{Type: "go", Package: "./cmd/a"}
	ts, err := one.Targets()
	if err != nil || len(ts) != 1 || ts[0].Output != "build/${ARCH}/${NAME}" {
		t.Fatalf("single: %v %v", ts, err)
	}
	many := &BuildSpec{Type: "go", Packages: []GoPackage{{Package: "./cmd/a"}, {Package: "./cmd/b", Output: "out/b"}}}
	ts, err = many.Targets()
	if err != nil || len(ts) != 2 || ts[0].Output != "build/${ARCH}/a" || ts[1].Output != "out/b" {
		t.Fatalf("multi: %v %v", ts, err)
	}
	for _, bad := range []*BuildSpec{
		{Type: "go"},
		{Type: "go", Package: "./a", Packages: []GoPackage{{Package: "./b"}}},
		{Type: "go", Packages: []GoPackage{{Package: "./a"}, {Package: "./x/a"}}}, // same default output
		{Type: "go", Packages: []GoPackage{{}}},
	} {
		if _, err := bad.Targets(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func writeTwoBinaryModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/two\n\ngo 1.21\n"), 0o644)
	for _, n := range []string{"alpha", "beta"} {
		os.MkdirAll(filepath.Join(dir, "cmd", n), 0o755)
		os.WriteFile(filepath.Join(dir, "cmd", n, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(\""+n+"\", version) }\n"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "pkgops.yaml"), []byte(specHead+`
abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]
min_release: "15.1"
build:
  type: go
  packages:
    - package: ./cmd/alpha
    - package: ./cmd/beta
      output: build/${ARCH}/beta-tool
  ldflags: -s -w -X main.version=${VERSION}
files:
  - source: build/${ARCH}/alpha
    destination: bin/alpha
  - source: build/${ARCH}/beta-tool
    destination: bin/beta
`), 0o644)
	return dir
}

// One package, two executables, built for every ABI, byte-identical on a rerun.
func TestMultiBinaryPackageIsBuiltForEveryABIAndReproducible(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := writeTwoBinaryModule(t)
	s, err := LoadSpec(filepath.Join(dir, "pkgops.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	opt := BuildOptions{BaseDir: dir, OutDir: filepath.Join(dir, "out1"), Epoch: time.Unix(1700000000, 0)}
	built, err := BuildAll(context.Background(), s, opt)
	if err != nil || len(built) != 2 {
		t.Fatalf("%v %v", built, err)
	}
	for _, b := range built {
		files, err := pkgfile.ReadManifest(b.Path)
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, f := range files.Files {
			have[f.Path] = true
		}
		if !have["/usr/local/bin/alpha"] || !have["/usr/local/bin/beta"] {
			t.Errorf("%s: files %v", b.Path, have)
		}
		if b.Info.MinOSVersion() != 1501000 {
			t.Errorf("%s: min osversion %d", b.Path, b.Info.MinOSVersion())
		}
	}
	opt.OutDir = filepath.Join(dir, "out2")
	again, err := BuildAll(context.Background(), s, opt)
	if err != nil || len(again) != 2 {
		t.Fatal(err)
	}
	for i := range built {
		if built[i].Info.SHA256 != again[i].Info.SHA256 {
			t.Errorf("%s is not reproducible", built[i].Path)
		}
	}
}
