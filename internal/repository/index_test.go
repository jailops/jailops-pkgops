package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/testutil"
)

func fixture(version string, minOS int) *pkgfile.Package {
	p := testutil.Fixture(version)
	p.MinOSVersion = minOS
	return p
}

func TestIndexListsEveryVersionCatalogOnlyTheNewest(t *testing.T) {
	r, pub, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1", "0.1.2")
	ix, err := index.Read(r.Root)
	if err != nil || ix == nil {
		t.Fatalf("index: %v %v", ix, err)
	}
	if len(ix.Packages) != 3 || ix.Schema != index.SchemaVersion {
		t.Fatalf("index has %d packages, schema %d", len(ix.Packages), ix.Schema)
	}
	e := ix.Packages[2]
	if e.File != abi+"/latest/All/hello-0.1.2.pkg" || len(e.SHA256) != 64 || e.Size == 0 || e.Built.IsZero() {
		t.Errorf("entry %+v", e)
	}
	res, err := VerifyRepository(r.Dir(abi), pub)
	if err != nil || res.Packages != 1 {
		t.Fatalf("catalog must advertise only the newest: %v %v", res, err)
	}
	// built survives re-indexing
	before := ix.Packages[0].Built
	populate(t, r, "0.1.3")
	ix, _ = index.Read(r.Root)
	if !ix.Packages[0].Built.Equal(before) || len(ix.Packages) != 4 {
		t.Errorf("built changed or entries lost: %v", ix.Packages)
	}
}

func TestPolicyRefusesUnsupportedABIAndLowMinRelease(t *testing.T) {
	pol, err := policy.Parse([]byte("abis: [FreeBSD:15:aarch64]\nmin_release: \"15.1\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Repository{Root: t.TempDir(), Time: testutil.FixedTime, Policy: pol}
	tmp := t.TempDir()
	ctx := context.Background()
	if _, err := r.Update(ctx, testutil.MustPkg(t, tmp, fixture("1", 1501000))); err != nil {
		t.Fatalf("a 15.1 package must pass: %v", err)
	}
	if _, err := r.Update(ctx, testutil.MustPkg(t, tmp, fixture("2", 1502000))); err != nil {
		t.Fatalf("a package that needs more than the minimum is fine: %v", err)
	}
	err = errOf(r.Update(ctx, testutil.MustPkg(t, tmp, fixture("3", 1500000))))
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "below the repository minimum 15.1") {
		t.Errorf("min 15.0: %v", err)
	}
	err = errOf(r.Update(ctx, testutil.MustPkg(t, tmp, fixture("4", 0))))
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "declares no minimum release") {
		t.Errorf("no min: %v", err)
	}
	p14 := fixture("5", 1401000)
	p14.ABI = "FreeBSD:14:aarch64"
	err = errOf(r.Update(ctx, testutil.MustPkg(t, tmp, p14)))
	if !errors.Is(err, pkgerr.ErrUnsupportedABI) || !strings.Contains(err.Error(), "supports only FreeBSD:15:aarch64") {
		t.Errorf("abi 14: %v", err)
	}
	if infos, _ := r.List(abi); len(infos) != 2 {
		t.Errorf("refused packages must not be in the repository: %d", len(infos))
	}
}

func errOf[T any](_ T, err error) error { return err }

func TestPruneManifestRemovesFromCatalogIndexAndDisk(t *testing.T) {
	r, pub, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1", "0.1.2")
	m := prune.New()
	m.Entries = []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.0", Reason: "superseded"}}
	r.PruneManifest = m
	populate(t, r, "0.1.3")
	if _, err := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.0.pkg")); !os.IsNotExist(err) {
		t.Errorf("listed file must be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.1.pkg")); err != nil {
		t.Errorf("unlisted file must stay: %v", err)
	}
	ix, _ := index.Read(r.Root)
	if _, ok := ix.Find(abi, "hello", "0.1.0"); ok {
		t.Error("pruned file still in the index")
	}
	p, ok := ix.FindPruned(abi, "hello", "0.1.0")
	if !ok || p.Reason != "superseded" || p.SHA256 == "" || p.PrunedAt.IsZero() || p.Built.IsZero() {
		t.Errorf("pruned history: %+v", p)
	}
	if len(ix.Packages) != 3 {
		t.Errorf("remaining: %d", len(ix.Packages))
	}
	if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
		t.Fatal(err)
	}
	// a pruned version is never reused
	tmp := t.TempDir()
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, tmp, testutil.Fixture("0.1.0"))); err == nil {
		t.Error("re-adding a pruned version must be refused")
	}
}

func TestPruneManifestRefusesTheNewestUnlessForced(t *testing.T) {
	r, _, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1")
	r.PruneManifest = &prune.Manifest{Schema: 1, Entries: []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.1"}}}
	err := r.Reindex(context.Background(), abi)
	if !errors.Is(err, prune.ErrNewest) {
		t.Fatalf("want ErrNewest, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.1.pkg")); e != nil {
		t.Error("nothing may be removed on refusal")
	}
	r.PruneManifest.Entries[0].Force = true
	if err := r.Reindex(context.Background(), abi); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if _, e := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.1.pkg")); !os.IsNotExist(e) {
		t.Error("forced entry must be removed")
	}
}

func TestIndexedFileMissingOrChangedFailsTheRun(t *testing.T) {
	r, _, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1")
	f := filepath.Join(r.Dir(abi), "All", "hello-0.1.0.pkg")
	data, _ := os.ReadFile(f)

	os.Remove(f)
	err := r.Reindex(context.Background(), abi)
	if !errors.Is(err, pkgerr.ErrNotFound) || !strings.Contains(err.Error(), "listed in "+index.FileName+" but missing") {
		t.Fatalf("missing file: %v", err)
	}
	// the damaged baseline must not have been published over
	ix, _ := index.Read(r.Root)
	if len(ix.Packages) != 2 {
		t.Errorf("index changed on failure: %d", len(ix.Packages))
	}

	// a different file with the same name and version
	other := fixture("0.1.0", 0)
	other.Desc = "changed"
	os.WriteFile(f, testutil.MustBytes(t, other), 0o644)
	err = r.Reindex(context.Background(), abi)
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "changed since it was published") {
		t.Fatalf("changed file: %v", err)
	}
	os.WriteFile(f, data, 0o644)
	if err := r.Reindex(context.Background(), abi); err != nil {
		t.Fatalf("restored baseline: %v", err)
	}
}

func TestDeleteAndRetentionKeepTheIndexTruthful(t *testing.T) {
	r, _, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1", "0.1.2")
	ctx := context.Background()
	if err := r.Delete(ctx, abi, "hello", "0.1.1"); err != nil {
		t.Fatal(err)
	}
	if rm, err := r.Prune(ctx, abi, 1); err != nil || len(rm) != 1 {
		t.Fatalf("retention: %v %v", rm, err)
	}
	ix, _ := index.Read(r.Root)
	if len(ix.Packages) != 1 || ix.Packages[0].Version != "0.1.2" || len(ix.Pruned) != 2 {
		t.Fatalf("index %+v", ix)
	}
	if err := r.Reindex(ctx, abi); err != nil {
		t.Fatalf("reindex after deletes: %v", err)
	}
}

// A process killed at any step of a prune leaves a site whose catalog verifies
// and whose index never lists a file that is not there.
func TestCrashDuringPruneKeepsIndexAndCatalogConsistent(t *testing.T) {
	steps := []string{"reindex:staged", "reindex:renamed:data.pkg", "reindex:renamed:meta.conf", "reindex:index", "delete:catalog"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			r, pub, keyFile := signedRepo(t)
			populate(t, r, "0.1.0", "0.1.1")
			code := runHelper(t, step, "PKGOPS_OP=delete", "PKGOPS_VERSION=0.1.1", "PKGOPS_ROOT="+r.Root, "PKGOPS_KEY="+keyFile)
			if code != 42 {
				t.Fatalf("no crash at %s (exit %d)", step, code)
			}
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatalf("catalog inconsistent: %v", err)
			}
			ix, err := index.Read(r.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range ix.Packages {
				if _, err := os.Stat(filepath.Join(r.Root, filepath.FromSlash(e.File))); err != nil {
					t.Fatalf("index lists %s which is not there: %v", e.File, err)
				}
			}
			// the retry (or the next build) converges
			if err := r.Delete(context.Background(), abi, "hello", "0.1.1"); err != nil && !errors.Is(err, pkgerr.ErrNotFound) {
				t.Fatalf("retry: %v", err)
			}
			if err := r.Reindex(context.Background(), abi); err != nil {
				t.Fatalf("reindex: %v", err)
			}
			ix, _ = index.Read(r.Root)
			if len(ix.Packages) != 1 || ix.Packages[0].Version != "0.1.0" {
				t.Errorf("final index %+v", ix.Packages)
			}
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLeftoverOfInterruptedPruneIsRemoved(t *testing.T) {
	r, _, _ := signedRepo(t)
	populate(t, r, "0.1.0", "0.1.1")
	f := filepath.Join(r.Dir(abi), "All", "hello-0.1.0.pkg")
	data, _ := os.ReadFile(f)
	if err := r.Delete(context.Background(), abi, "hello", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, data, 0o644) // the file reappears (crash between index and removal)
	if err := r.Reindex(context.Background(), abi); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Error("leftover must be removed, not re-published")
	}
	ix, _ := index.Read(r.Root)
	if len(ix.Packages) != 1 {
		t.Errorf("leftover re-entered the index: %+v", ix.Packages)
	}
}
