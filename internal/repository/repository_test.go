package repository

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/testutil"
)

// The package below was produced by FreeBSD's own `pkg create` (pkg 2.8.4,
// FreeBSD 15.1). We must be able to read it, and our repo tooling must index it.
func TestReadsRealPkgCreateOutput(t *testing.T) {
	info, err := pkgfile.Verify("../../testdata/real/hello-0.1.0.pkg")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "hello" || info.ABI != "FreeBSD:15:aarch64" || info.Flatsize != 164 {
		t.Errorf("unexpected: %+v", info)
	}
	r := &Repository{Root: t.TempDir(), Time: testutil.FixedTime}
	if _, err := r.Update(context.Background(), "../../testdata/real/hello-0.1.0.pkg"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRepository(r.Dir("FreeBSD:15:aarch64"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.1", -1}, {"0.1.10", "0.1.9", 1}, {"1.0", "1.0", 0}, {"1.0_1", "1.0", 1},
		{"1.0,1", "2.0", 1}, {"1.0rc1", "1.0", -1}, {"1.0a", "1.0", 1}, {"1.0.1", "1.0", 1},
		{"2.0_3", "2.0_10", -1}, {"1.0.alpha", "1.0.beta", -1},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("antisymmetry %q %q", c.a, c.b)
		}
	}
}

func TestRepositoryCatalogGolden(t *testing.T) {
	tmp := t.TempDir()
	f := testutil.MustPkg(t, tmp, testutil.Fixture("0.1.0"))
	r := &Repository{Root: filepath.Join(tmp, "site"), Time: testutil.FixedTime, FileList: true}
	if _, err := r.Update(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	d := r.Dir("FreeBSD:15:aarch64")
	norm := func(b []byte) []byte { return testutil.SumRe.ReplaceAll(b, []byte(`"X":0`)) }
	testutil.Golden(t, "catalog-data.json", norm(testutil.ReadCatalog(t, filepath.Join(d, "data.pkg"), "data")))
	testutil.Golden(t, "catalog-packagesite.yaml", norm(testutil.ReadCatalog(t, filepath.Join(d, "packagesite.pkg"), "packagesite.yaml")))
	meta, _ := os.ReadFile(filepath.Join(d, "meta.conf"))
	want, _ := os.ReadFile("../../testdata/real/meta.conf")
	if !bytes.Equal(meta, want) {
		t.Errorf("meta.conf differs from the one written by FreeBSD pkg repo:\n%s", meta)
	}
	meta2, _ := os.ReadFile(filepath.Join(d, "meta"))
	if !bytes.Equal(meta, meta2) {
		t.Error("meta and meta.conf should be identical")
	}
	if _, err := os.Stat(filepath.Join(d, "All", "hello-0.1.0.pkg")); err != nil {
		t.Error(err)
	}
}

// Same file set as the real-pkg fixture: output must equal what `pkg repo -l` wrote.
func TestFilesListMatchesRealPkgRepo(t *testing.T) {
	p := &pkgfile.Package{Name: "hello", Version: "0.1.0", Comment: "c", Maintainer: "a@b.c", WWW: "https://example.com", ABI: "FreeBSD:15:aarch64",
		Files: []pkgfile.File{
			{Path: "/usr/local/bin/hello", Data: []byte("x"), Mode: 0o755},
			{Path: "/usr/local/etc/hello/hello.conf.sample", Data: []byte("x"), Mode: 0o644},
			{Path: "/usr/local/etc/rc.d/hello", Data: []byte("x"), Mode: 0o555},
		}}
	tmp := t.TempDir()
	f := testutil.MustPkg(t, tmp, p)
	r := &Repository{Root: filepath.Join(tmp, "site"), Time: testutil.FixedTime, FileList: true}
	if _, err := r.Update(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	got := testutil.ReadCatalog(t, filepath.Join(r.Dir("FreeBSD:15:aarch64"), "files.pkg"), "files")
	want, _ := os.ReadFile("../../testdata/real/files.txt")
	if !bytes.Equal(got, want) {
		t.Errorf("files list:\n%q\nwant\n%q", got, want)
	}
}

func TestSignAndVerify(t *testing.T) {
	key, err := GenerateRSAKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := GenerateRSAKey(2048)
	pub, _ := PublicKeyPEM(&key.PublicKey)
	otherPub, _ := PublicKeyPEM(&other.PublicKey)
	tmp := t.TempDir()
	f := testutil.MustPkg(t, tmp, testutil.Fixture("0.1.0"))

	for _, mode := range []SignMode{SignPubkey, SignFingerprints} {
		r := &Repository{Root: filepath.Join(tmp, "site", string(rune('a'+mode))), Signer: &RSASigner{Key: key, Mode: mode}, Time: testutil.FixedTime}
		if _, err := r.Update(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		d := r.Dir("FreeBSD:15:aarch64")
		res, err := VerifyRepository(d, pub)
		if err != nil || !res.Signed || res.Packages != 1 {
			t.Fatalf("mode %d: %+v %v", mode, res, err)
		}
		if _, err := VerifyRepository(d, otherPub); !errors.Is(err, pkgerr.ErrSignature) {
			t.Fatalf("mode %d: wrong key accepted: %v", mode, err)
		}
		// tamper: change the catalog, keep the old signature
		raw, _ := os.ReadFile(filepath.Join(d, "data.pkg"))
		ents := testutil.Untar(t, raw)
		var out bytes.Buffer
		zw, _ := zstd.NewWriter(&out)
		tw := tar.NewWriter(zw)
		for _, e := range ents {
			h := e.Hdr
			if h.Name == "data" {
				e.Data = bytes.Replace(e.Data, []byte("hello test"), []byte("evil test"), 1)
			}
			h.Size = int64(len(e.Data))
			tw.WriteHeader(&h)
			tw.Write(e.Data)
		}
		tw.Close()
		zw.Close()
		os.WriteFile(filepath.Join(d, "data.pkg"), out.Bytes(), 0o644)
		if _, err := VerifyRepository(d, pub); !errors.Is(err, pkgerr.ErrSignature) {
			t.Fatalf("mode %d: tampered catalog accepted: %v", mode, err)
		}
	}
	// unsigned repo is rejected when a key is demanded
	r := &Repository{Root: filepath.Join(tmp, "unsigned"), Time: testutil.FixedTime}
	r.Update(context.Background(), f)
	if _, err := VerifyRepository(r.Dir("FreeBSD:15:aarch64"), pub); !errors.Is(err, pkgerr.ErrSignature) {
		t.Fatalf("unsigned repo accepted: %v", err)
	}
}

// pkg-repo(8) convention, checked independently of our verifier: the signature
// is PKCS#1 v1.5 over SHA-256 of the lowercase hex SHA-256 of the catalog file,
// followed by one NUL byte (pubkey mode).
func TestSignatureConvention(t *testing.T) {
	key, _ := GenerateRSAKey(2048)
	ents, err := (&RSASigner{Key: key}).SignCatalog("data", "00ff")
	if err != nil || len(ents) != 1 || ents[0].Name != "signature" {
		t.Fatalf("%v %v", ents, err)
	}
	if len(ents[0].Data) != 257 || ents[0].Data[256] != 0 {
		t.Errorf("signature entry should be 256 bytes + NUL, got %d", len(ents[0].Data))
	}
	if err := VerifyRSA(&key.PublicKey, "00ff", ents[0].Data); err != nil {
		t.Error(err)
	}
	if err := VerifyRSA(&key.PublicKey, "00fe", ents[0].Data); err == nil {
		t.Error("wrong digest accepted")
	}
	fp, _ := (&RSASigner{Key: key, Mode: SignFingerprints}).SignCatalog("packagesite.yaml", "00ff")
	if fp[0].Name != "packagesite.yaml.sig" || fp[1].Name != "packagesite.yaml.pub" || len(fp[0].Data) != 256 {
		t.Errorf("fingerprint entries: %v", fp)
	}
	pub, _ := PublicKeyPEM(&key.PublicKey)
	sum := sha256.Sum256(pub)
	if Fingerprint(pub) != hex.EncodeToString(sum[:]) || !strings.Contains(string(FingerprintFile(pub)), "function: sha256") {
		t.Error("fingerprint")
	}
	priv, _ := PrivateKeyPEM(key)
	if k2, err := ParseRSAPrivateKey(priv); err != nil || k2.N.Cmp(key.N) != 0 {
		t.Errorf("key round trip: %v", err)
	}
}

func TestRepositoryVersionsAndPrune(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	r := &Repository{Root: filepath.Join(tmp, "site"), Time: testutil.FixedTime}
	for _, v := range []string{"0.1.0", "0.1.10", "0.1.9"} {
		if _, err := r.Update(ctx, testutil.MustPkg(t, tmp, testutil.Fixture(v))); err != nil {
			t.Fatal(err)
		}
	}
	d := r.Dir("FreeBSD:15:aarch64")
	data := string(testutil.ReadCatalog(t, filepath.Join(d, "data.pkg"), "data"))
	if strings.Count(data, `"name":"hello"`) != 1 || !strings.Contains(data, `"version":"0.1.10"`) {
		t.Errorf("catalog should advertise only the newest version: %s", data)
	}
	list, _ := r.List("FreeBSD:15:aarch64")
	if len(list) != 3 || list[2].Version != "0.1.10" {
		t.Errorf("list: %+v", list)
	}
	// same version, different content: refused; identical content: idempotent
	p := testutil.Fixture("0.1.9")
	p.Comment = "changed"
	if _, err := r.Update(ctx, testutil.MustPkg(t, t.TempDir(), p)); !errors.Is(err, pkgerr.ErrInvalidPackage) {
		t.Errorf("republishing different bytes should fail, got %v", err)
	}
	if _, err := r.Update(ctx, testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.9"))); err != nil {
		t.Errorf("identical republish should be a no-op: %v", err)
	}
	rm, err := r.Prune(ctx, "FreeBSD:15:aarch64", 2)
	if err != nil || len(rm) != 1 || rm[0] != "hello-0.1.0.pkg" {
		t.Errorf("prune: %v %v", rm, err)
	}
	if err := r.Remove("FreeBSD:15:aarch64", "hello", "9.9"); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Errorf("remove missing: %v", err)
	}
	if err := r.Remove("FreeBSD:15:aarch64", "../x", "1"); err == nil {
		t.Error("path traversal in name accepted")
	}
	if _, err := r.List("FreeBSD:14:amd64"); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Errorf("list unknown abi: %v", err)
	}
}

func TestRepositoryTwoABIs(t *testing.T) {
	tmp := t.TempDir()
	a := testutil.Fixture("1.0")
	b := testutil.Fixture("1.0")
	b.ABI = "FreeBSD:15:amd64"
	r := &Repository{Root: filepath.Join(tmp, "site"), Time: testutil.FixedTime}
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, filepath.Join(tmp, "a"), a), testutil.MustPkg(t, filepath.Join(tmp, "b"), b)); err != nil {
		t.Fatal(err)
	}
	abis, _ := r.ABIs()
	if strings.Join(abis, " ") != "FreeBSD:15:aarch64 FreeBSD:15:amd64" {
		t.Errorf("abis %v", abis)
	}
	// an empty Update re-indexes everything
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryDeterministicCatalog(t *testing.T) {
	tmp := t.TempDir()
	f := testutil.MustPkg(t, tmp, testutil.Fixture("0.1.0"))
	var first []byte
	for i := 0; i < 2; i++ {
		r := &Repository{Root: filepath.Join(tmp, "s", string(rune('a'+i))), Time: testutil.FixedTime, FileList: true}
		r.Update(context.Background(), f)
		b, _ := os.ReadFile(filepath.Join(r.Dir("FreeBSD:15:aarch64"), "data.pkg"))
		if i == 0 {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Error("catalog not reproducible")
		}
	}
}
