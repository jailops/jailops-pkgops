package fetch

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

var testKey = func() *rsa.PrivateKey {
	k, err := repository.GenerateRSAKey(2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func pubPEM(t *testing.T, k *rsa.PrivateKey) []byte {
	t.Helper()
	b, err := repository.PublicKeyPEM(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// signedSite publishes the versions with testKey and serves the tree.
func signedSite(t *testing.T, versions ...string) (root string, repo *repository.Repository, srv *httptest.Server) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "site")
	repo = &repository.Repository{Root: root, Time: testutil.FixedTime, Signer: &repository.RSASigner{Key: testKey}}
	for _, v := range versions {
		publish(t, repo, v)
	}
	srv = httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(srv.Close)
	return root, repo, srv
}

func publish(t *testing.T, repo *repository.Repository, v string) {
	t.Helper()
	if _, err := repo.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture(v))); err != nil {
		t.Fatal(err)
	}
}

// tamper replaces the published file of version v with other bytes of the
// same name and version and rewrites the index to match, as someone who
// controls the delivery of the site but not the signing key could.
func tamper(t *testing.T, root, v string) []byte {
	t.Helper()
	evil := testutil.Fixture(v)
	evil.Files[1].Data = []byte("#!/bin/sh\necho changed\n")
	b := testutil.MustBytes(t, evil)
	rel := abi + "/latest/All/hello-" + v + ".pkg"
	if err := os.WriteFile(filepath.Join(root, rel), b, 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ix.Packages {
		if ix.Packages[i].File == rel {
			s := sha256.Sum256(b)
			ix.Packages[i].SHA256, ix.Packages[i].Size = hex.EncodeToString(s[:]), int64(len(b))
		}
	}
	if err := ix.Write(root); err != nil {
		t.Fatal(err)
	}
	return b
}

func assertNothingWritten(t *testing.T, into string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(into, index.FileName)); !os.IsNotExist(err) {
		t.Error("the index must not be written")
	}
	n := 0
	filepath.Walk(into, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	if n != 0 {
		t.Errorf("%d file(s) written before the signed state was verified", n)
	}
}

func TestVerifiedBaselineAcceptsTheSignedSite(t *testing.T) {
	root, _, srv := signedSite(t, "0.1.0", "0.1.1", "0.1.2")
	into := t.TempDir()
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: pubPEM(t, testKey)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signed != 1 || res.Unsigned != 2 || res.Downloaded != 3 {
		t.Errorf("%+v", res)
	}
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		a, _ := os.ReadFile(filepath.Join(root, abi, "latest", "All", "hello-"+v+".pkg"))
		b, _ := os.ReadFile(filepath.Join(into, abi, "latest", "All", "hello-"+v+".pkg"))
		if len(a) == 0 || string(a) != string(b) {
			t.Errorf("%s differs", v)
		}
	}
}

// The acceptance test: a package and its hash changed on the baseline site,
// without the signing key. The baseline stops before anything is written, so
// the next `pkgops repo --key` never sees it.
func TestVerifiedBaselineRefusesATamperedPackageAndHash(t *testing.T) {
	root, _, srv := signedSite(t, "0.1.0", "0.1.1")
	tamper(t, root, "0.1.1")
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: pubPEM(t, testKey)})
	if !errors.Is(err, pkgerr.ErrSignature) || !strings.Contains(err.Error(), "signed catalog says") {
		t.Fatalf("want a refusal, got %v", err)
	}
	assertNothingWritten(t, into)
	// without the key the old behaviour stays: the index alone decides
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir()}); err != nil {
		t.Errorf("no key: %v", err)
	}
}

func TestVerifiedBaselineRefusesAForgedCatalog(t *testing.T) {
	root, _, srv := signedSite(t, "0.1.0", "0.1.1")
	evilKey, _ := repository.GenerateRSAKey(2048)
	// re-sign the whole site with another key: catalogs, index and packages agree
	tamper(t, root, "0.1.1")
	forged := &repository.Repository{Root: root, Time: testutil.FixedTime, Signer: &repository.RSASigner{Key: evilKey}, Replace: true}
	if err := forged.Reindex(context.Background(), abi); err != nil {
		t.Fatal(err)
	}
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: pubPEM(t, testKey)})
	if !errors.Is(err, pkgerr.ErrSignature) || !strings.Contains(err.Error(), "do not verify against the trusted key") {
		t.Fatalf("forged: %v", err)
	}
	assertNothingWritten(t, into)
	// one damaged catalog is enough
	root2, _, srv2 := signedSite(t, "0.1.0")
	f := filepath.Join(root2, abi, "latest", "data.pkg")
	b, _ := os.ReadFile(f)
	b[len(b)-5] ^= 0xff
	os.WriteFile(f, b, 0o644)
	if _, err := Pages(context.Background(), Options{From: srv2.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)}); err == nil {
		t.Error("a damaged data.pkg was accepted")
	}
}

func TestVerifiedBaselineRefusesAnUnsignedSite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	r := &repository.Repository{Root: root, Time: testutil.FixedTime}
	publish(t, r, "0.1.0")
	srv := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer srv.Close()
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)}); !errors.Is(err, pkgerr.ErrSignature) || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("unsigned: %v", err)
	}
	// an index without any catalog
	os.Remove(filepath.Join(root, abi, "latest", "data.pkg"))
	os.Remove(filepath.Join(root, abi, "latest", "packagesite.pkg"))
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)}); err == nil || !strings.Contains(err.Error(), "no signed catalog") {
		t.Fatalf("no catalog: %v", err)
	}
}

// What the unsigned index may and may not add next to the signed catalog.
func TestVerifiedBaselineRefusesIndexEntriesTheCatalogDoesNotCover(t *testing.T) {
	addEntry := func(t *testing.T, root string, e index.Entry) {
		ix, _ := index.Read(root)
		ix.Packages = append(ix.Packages, e)
		if err := ix.Write(root); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a newer version", func(t *testing.T) {
		root, _, srv := signedSite(t, "0.1.0")
		b := testutil.MustBytes(t, testutil.Fixture("9.9.9"))
		rel := abi + "/latest/All/hello-9.9.9.pkg"
		os.WriteFile(filepath.Join(root, rel), b, 0o644)
		s := sha256.Sum256(b)
		addEntry(t, root, index.Entry{ABI: abi, Name: "hello", Version: "9.9.9", File: rel, SHA256: hex.EncodeToString(s[:]), Size: int64(len(b))})
		into := t.TempDir()
		_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: pubPEM(t, testKey)})
		if err == nil || !strings.Contains(err.Error(), "newer than hello-0.1.0") {
			t.Fatalf("newer: %v", err)
		}
		assertNothingWritten(t, into)
	})
	t.Run("a name the catalog does not advertise", func(t *testing.T) {
		root, _, srv := signedSite(t, "0.1.0")
		p := testutil.Fixture("1.0.0")
		p.Name = "other"
		b := testutil.MustBytes(t, p)
		rel := abi + "/latest/All/other-1.0.0.pkg"
		os.WriteFile(filepath.Join(root, rel), b, 0o644)
		s := sha256.Sum256(b)
		addEntry(t, root, index.Entry{ABI: abi, Name: "other", Version: "1.0.0", File: rel, SHA256: hex.EncodeToString(s[:]), Size: int64(len(b))})
		_, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)})
		if err == nil || !strings.Contains(err.Error(), "does not advertise") {
			t.Fatalf("other name: %v", err)
		}
	})
	t.Run("the advertised version left out of the index", func(t *testing.T) {
		root, _, srv := signedSite(t, "0.1.0", "0.1.1")
		ix, _ := index.Read(root)
		ix.Packages = ix.Packages[:1] // only 0.1.0
		ix.Write(root)
		_, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)})
		if err == nil || !strings.Contains(err.Error(), "advertises hello-0.1.1") {
			t.Fatalf("left out: %v", err)
		}
	})
	t.Run("a file path off the layout", func(t *testing.T) {
		root, _, srv := signedSite(t, "0.1.0")
		ix, _ := index.Read(root)
		ix.Packages[0].File = abi + "/latest/All/sub/hello-0.1.0.pkg"
		ix.Write(root)
		_, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey)})
		if err == nil || !strings.Contains(err.Error(), "ABI/CHANNEL/All") {
			t.Fatalf("layout: %v", err)
		}
	})
}

// Rollback: an older, genuinely signed catalog served next to the current
// index is refused. A complete older snapshot (catalog, index and files of an
// earlier day) verifies, because the catalog carries no serial; the limits are
// in docs/hosting.md ("Verified baseline").
func TestVerifiedBaselineRollback(t *testing.T) {
	root, repo, srv := signedSite(t, "0.1.0", "0.1.1")
	dir := filepath.Join(root, abi, "latest")
	snap := t.TempDir()
	for _, f := range []string{"data.pkg", "packagesite.pkg", "meta.conf", "meta"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		os.WriteFile(filepath.Join(snap, f), b, 0o644)
	}
	oldIndex, _ := os.ReadFile(filepath.Join(root, index.FileName))
	publish(t, repo, "0.1.2")
	key := pubPEM(t, testKey)
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: key}); err != nil {
		t.Fatalf("current state: %v", err)
	}
	for _, f := range []string{"data.pkg", "packagesite.pkg", "meta.conf", "meta"} {
		b, _ := os.ReadFile(filepath.Join(snap, f))
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: key})
	if err == nil || !strings.Contains(err.Error(), "newer than hello-0.1.1") {
		t.Fatalf("old catalog with the current index: %v", err)
	}
	assertNothingWritten(t, into)
	// the whole earlier snapshot: verifies, and its newest is the old 0.1.1
	os.WriteFile(filepath.Join(root, index.FileName), oldIndex, 0o644)
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: key})
	if err != nil || res.Signed != 1 {
		t.Fatalf("consistent snapshot: %+v %v", res, err)
	}
	if ix := res.Index; len(ix.Packages) != 2 {
		t.Errorf("snapshot index: %d entries", len(ix.Packages))
	}
}

// History is not covered by any signature: a changed older version passes
// (as before) but is reported as unsigned, and it can never become the
// advertised version through the prune manifest.
func TestVerifiedBaselineHistoryAndPrune(t *testing.T) {
	root, _, srv := signedSite(t, "0.1.0", "0.1.1")
	tamper(t, root, "0.1.0")
	key := pubPEM(t, testKey)
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: key})
	if err != nil || res.Signed != 1 || res.Unsigned != 1 {
		t.Fatalf("history: %+v %v", res, err)
	}
	m := &prune.Manifest{Schema: 1, Entries: []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.1", Force: true}}}
	into := t.TempDir()
	_, err = Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: key, Prune: m})
	if err == nil || !strings.Contains(err.Error(), "no signature covers it") {
		t.Fatalf("pruning the signed newest: %v", err)
	}
	assertNothingWritten(t, into)
	// pruning every version of the package is fine
	m.Entries = append(m.Entries, prune.Entry{ABI: abi, Name: "hello", Version: "0.1.0", Force: true})
	res, err = Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: key, Prune: m})
	if err != nil || res.Pruned != 2 {
		t.Fatalf("prune all: %+v %v", res, err)
	}
}

func TestVerifiedBaselineBadKeyAndABIFilter(t *testing.T) {
	_, _, srv := signedSite(t, "0.1.0")
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: []byte("not a key")}); err == nil || !strings.Contains(err.Error(), "trusted key") {
		t.Fatalf("bad key: %v", err)
	}
	// an ABI with neither catalog nor packages is fine
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey), ABIs: []string{"FreeBSD:15:amd64"}})
	if err != nil || res.Downloaded != 0 {
		t.Fatalf("other ABI: %+v %v", res, err)
	}
}
