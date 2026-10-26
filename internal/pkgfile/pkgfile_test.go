package pkgfile_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"jailops.net/pkgops/internal/testutil"
)

func TestManifestGolden(t *testing.T) {
	raw, info := testutil.BuildBytes(t, testutil.Fixture("0.1.0"))
	ents := testutil.Untar(t, raw)
	var listing strings.Builder
	for _, e := range ents {
		listing.WriteString(e.Hdr.Name + " " + string(rune('0'+e.Hdr.Typeflag-'0')) + " " + e.Hdr.Uname + ":" + e.Hdr.Gname +
			" " + strings.TrimSpace(strings.Join([]string{permStr(uint32(e.Hdr.Mode)), e.Hdr.ModTime.UTC().Format(time.RFC3339)}, " ")) + "\n")
	}
	testutil.Golden(t, "tar-listing.txt", []byte(listing.String()))
	for _, e := range ents {
		switch e.Hdr.Name {
		case "+MANIFEST":
			testutil.Golden(t, "MANIFEST.json", e.Data)
		case "+COMPACT_MANIFEST":
			testutil.Golden(t, "COMPACT_MANIFEST.json", e.Data)
		}
	}
	if info.Name != "hello" || info.Arch != "freebsd:15:aarch64:64" || info.Flatsize != 18+18+4 {
		t.Errorf("unexpected info %+v", info)
	}
}

func TestEntryOrderAndMeta(t *testing.T) {
	raw, _ := testutil.BuildBytes(t, testutil.Fixture("0.1.0"))
	ents := testutil.Untar(t, raw)
	var names []string
	for _, e := range ents {
		names = append(names, e.Hdr.Name)
	}
	want := "+COMPACT_MANIFEST +MANIFEST /usr/local/bin/hello /usr/local/etc/hello/hello.conf /usr/local/etc/rc.d/hello /var/db/hello/"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("order:\n got %s\nwant %s", got, want)
	}
	// real pkg-create writes the manifests first with epoch mtime
	if ents[0].Hdr.ModTime.Unix() != 0 || ents[1].Hdr.ModTime.Unix() != 0 {
		t.Error("manifest entries must have mtime 0")
	}
}

func TestDeterministic(t *testing.T) {
	a, _ := testutil.BuildBytes(t, testutil.Fixture("0.1.0"))
	b, _ := testutil.BuildBytes(t, testutil.Fixture("0.1.0"))
	if !bytes.Equal(a, b) {
		t.Fatal("two builds of the same package differ")
	}
	// file order in the input must not matter
	p := testutil.Fixture("0.1.0")
	p.Files[0], p.Files[2] = p.Files[2], p.Files[0]
	c, _ := testutil.BuildBytes(t, p)
	if !bytes.Equal(a, c) {
		t.Fatal("input file order changed the output")
	}
	d, _ := testutil.BuildBytes(t, func() *pkgfile.Package {
		p := testutil.Fixture("0.1.0")
		p.Time = testutil.FixedTime.Add(time.Second)
		return p
	}())
	if bytes.Equal(a, d) {
		t.Fatal("epoch should influence the output")
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "hello-0.1.0.pkg")
	if _, err := testutil.Fixture("0.1.0").WriteFile(f, pkgfile.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pkgfile.Verify(f); err != nil {
		t.Fatalf("clean package failed: %v", err)
	}
	// Re-pack with a file whose content differs from the manifest sum.
	raw, _ := os.ReadFile(f)
	ents := testutil.Untar(t, raw)
	var out bytes.Buffer
	zw, _ := zstd.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for _, e := range ents {
		h := e.Hdr
		if h.Name == "/usr/local/bin/hello" {
			e.Data = []byte("#!/bin/sh\necho EVIL\n")
		}
		h.Size = int64(len(e.Data))
		tw.WriteHeader(&h)
		tw.Write(e.Data)
	}
	tw.Close()
	zw.Close()
	bad := filepath.Join(dir, "bad.pkg")
	os.WriteFile(bad, out.Bytes(), 0o644)
	if _, err := pkgfile.Verify(bad); !errors.Is(err, pkgerr.ErrInvalidPackage) {
		t.Fatalf("want pkgerr.ErrInvalidPackage, got %v", err)
	}
	if _, err := pkgfile.Verify(filepath.Join(dir, "missing.pkg")); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestABI(t *testing.T) {
	a, err := pkgfile.ParseABI("freebsd:15:aarch64")
	if err != nil || a.String() != "FreeBSD:15:aarch64" || a.PkgArch() != "freebsd:15:aarch64:64" || a.GOARCH() != "arm64" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := pkgfile.ParseABI("FreeBSD:15"); !errors.Is(err, pkgerr.ErrUnsupportedABI) {
		t.Fatal(err)
	}
}

func permStr(m uint32) string { return fmt.Sprintf("%04o", m&07777) }

func TestSymlinks(t *testing.T) {
	p := testutil.Fixture("0.1.0")
	p.Files = append(p.Files,
		pkgfile.File{Path: "/usr/local/bin/hello-rel", Symlink: "hello"},
		pkgfile.File{Path: "/usr/local/bin/hello-abs", Symlink: "/usr/local/bin/hello", Owner: "root", Group: "wheel"},
		pkgfile.File{Path: "/usr/local/lib/dangling", Symlink: "nowhere"})
	raw, info := testutil.BuildBytes(t, p)
	if info.Flatsize != 40+int64(len("hello")+len("/usr/local/bin/hello")+len("nowhere")) {
		t.Errorf("flatsize %d must count link targets like pkg-create", info.Flatsize)
	}
	var found int
	for _, e := range testutil.Untar(t, raw) {
		switch e.Hdr.Name {
		case "/usr/local/bin/hello-rel", "/usr/local/bin/hello-abs", "/usr/local/lib/dangling":
			found++
			if e.Hdr.Typeflag != tar.TypeSymlink || e.Hdr.Size != 0 || e.Hdr.Mode != 0o755 || len(e.Data) != 0 {
				t.Errorf("%s: %+v", e.Hdr.Name, e.Hdr)
			}
		case "+MANIFEST":
			if !strings.Contains(string(e.Data), `"perm":"0755","symlink_target":"hello","mtime"`) {
				t.Errorf("manifest lacks symlink_target in pkg's key order: %s", e.Data)
			}
		}
	}
	if found != 3 {
		t.Fatalf("found %d symlinks", found)
	}
	f := filepath.Join(t.TempDir(), "x.pkg")
	os.WriteFile(f, raw, 0o644)
	if _, err := pkgfile.Verify(f); err != nil {
		t.Fatal(err)
	}
	// retarget a link behind the manifest's back
	var out bytes.Buffer
	zw, _ := zstd.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for _, e := range testutil.Untar(t, raw) {
		h := e.Hdr
		if h.Name == "/usr/local/bin/hello-rel" {
			h.Linkname = "/etc/passwd"
		}
		tw.WriteHeader(&h)
		tw.Write(e.Data)
	}
	tw.Close()
	zw.Close()
	bad := filepath.Join(t.TempDir(), "bad.pkg")
	os.WriteFile(bad, out.Bytes(), 0o644)
	if _, err := pkgfile.Verify(bad); !errors.Is(err, pkgerr.ErrInvalidPackage) {
		t.Errorf("retargeted symlink accepted: %v", err)
	}
}
