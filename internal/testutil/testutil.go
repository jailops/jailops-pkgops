// Package testutil holds fixtures shared by the internal packages' tests.
package testutil

import (
	"archive/tar"
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"jailops.net/pkgops/internal/pkgfile"
)

var Update = flag.Bool("update", false, "rewrite golden files")

var FixedTime = time.Unix(1700000000, 0)

// fixture mirrors the package used for the real-pkg comparison in docs/format.md.
// Fixture returns the sample package used across tests.
func Fixture(version string) *pkgfile.Package {
	return &pkgfile.Package{
		Name: "hello", Version: version, Origin: "sysutils/hello", Comment: "hello test",
		Maintainer: "a@b.c", WWW: "https://example.com", ABI: "FreeBSD:15:aarch64", Prefix: "/usr/local",
		Desc: "hello desc\nline2", Licenses: []string{"MIT"}, Categories: []string{"sysutils"},
		Users: []string{"hello"}, Groups: []string{"hello"},
		Deps:    []pkgfile.Dep{{Name: "pcre2", Origin: "devel/pcre2", Version: "10.49"}},
		Scripts: map[string]string{"post-install": "echo hi"},
		Files: []pkgfile.File{
			{Path: "/usr/local/etc/rc.d/hello", Data: []byte("#!/bin/sh\necho rc\n"), Mode: 0o555},
			{Path: "/usr/local/bin/hello", Data: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
			{Path: "/usr/local/etc/hello/hello.conf", Data: []byte("a=1\n"), Mode: 0o644, Config: true},
		},
		Dirs: []pkgfile.Dir{{Path: "/var/db/hello", Mode: 0o750, Owner: "hello", Group: "hello"}},
		Time: FixedTime,
	}
}

func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name)
	if *Update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden:\n got: %s\nwant: %s", name, got, want)
	}
}

func BuildBytes(t *testing.T, p *pkgfile.Package) ([]byte, *pkgfile.Info) {
	t.Helper()
	var b bytes.Buffer
	info, err := p.Write(&b, pkgfile.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), info
}

type TarEntry struct {
	Hdr  tar.Header
	Data []byte
}

func Untar(t *testing.T, pkg []byte) []TarEntry {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(pkg))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var out []TarEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, TarEntry{*h, b})
	}
}

// ---- repository -----------------------------------------------------------

func MustPkg(t *testing.T, dir string, p *pkgfile.Package) string {
	t.Helper()
	f := filepath.Join(dir, p.Name+"-"+p.Version+".pkg")
	if _, err := p.WriteFile(f, pkgfile.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	return f
}

// MustBytes returns the bytes of the package built from p.
func MustBytes(t *testing.T, p *pkgfile.Package) []byte {
	t.Helper()
	b, _ := BuildBytes(t, p)
	return b
}

func ReadCatalog(t *testing.T, file, payload string) []byte {
	t.Helper()
	raw, _ := os.ReadFile(file)
	for _, e := range Untar(t, raw) {
		if e.Hdr.Name == payload {
			return e.Data
		}
	}
	t.Fatalf("%s not in %s", payload, file)
	return nil
}

var SumRe = regexp.MustCompile(`"sum":"1\$[0-9a-f]+"|"pkgsize":[0-9]+`)
