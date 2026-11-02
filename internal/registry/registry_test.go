package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

const abi = "FreeBSD:15:aarch64"

func signedRegistry(t *testing.T) (*Registry, *repository.Repository) {
	t.Helper()
	k, err := repository.GenerateRSAKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	repo := &repository.Repository{Root: t.TempDir(), Time: testutil.FixedTime, Signer: &repository.RSASigner{Key: k}}
	reg := New(repo, 0, false)
	if _, _, err := reg.Upload(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture("1.0.0"))); err != nil {
		t.Fatal(err)
	}
	return reg, repo
}

// swap replaces the published package with other readable bytes of the same
// name and version, keeping the catalog files untouched.
func swap(t *testing.T, repo *repository.Repository, change string) {
	t.Helper()
	evil := testutil.Fixture("1.0.0")
	evil.Files[1].Data = []byte(change)
	if err := os.WriteFile(filepath.Join(repo.Dir(abi), "All", "hello-1.0.0.pkg"), testutil.MustBytes(t, evil), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusSeesASwappedPackageAfterTheCacheIsWarm(t *testing.T) {
	reg, repo := signedRegistry(t)
	for i := 0; i < 2; i++ { // the second call answers from the cache
		if st, err := reg.Status(); err != nil || !st.Healthy {
			t.Fatalf("before: %+v %v", st, err)
		}
	}
	swap(t, repo, "#!/bin/sh\necho changed\n")
	_, verr := repository.VerifyRepository(repo.Dir(abi), nil)
	if verr == nil {
		t.Fatal("the swapped package should fail verification")
	}
	st, err := reg.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Healthy || len(st.ABIs) != 1 || st.ABIs[0].Consistent || !strings.Contains(st.ABIs[0].Problem, "does not match") {
		t.Errorf("the cached status hides the swap: %+v", st)
	}
}

func TestStatusReverifiesAfterVerifyEvery(t *testing.T) {
	reg, repo := signedRegistry(t)
	now := time.Unix(1_800_000_000, 0)
	reg.Now = func() time.Time { return now }
	reg.VerifyEvery = time.Minute
	if st, _ := reg.Status(); !st.Healthy {
		t.Fatal(st.Problems)
	}
	// a change the cache key cannot see: the catalog itself is damaged in
	// place with the same size and mtime
	f := filepath.Join(repo.Dir(abi), "data.pkg")
	fi, _ := os.Stat(f)
	b, _ := os.ReadFile(f)
	b[len(b)-5] ^= 0xff
	os.WriteFile(f, b, 0o644)
	os.Chtimes(f, fi.ModTime(), fi.ModTime())
	if st, _ := reg.Status(); !st.Healthy {
		t.Fatal("expected the cached result inside VerifyEvery")
	}
	now = now.Add(time.Minute)
	if st, _ := reg.Status(); st.Healthy {
		t.Error("a cached result outlived VerifyEvery")
	}
}
