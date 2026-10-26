package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/testutil"
)

const abi = "FreeBSD:15:aarch64"

func signedRepo(t *testing.T) (*Repository, []byte, string) {
	t.Helper()
	key, err := GenerateRSAKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := PrivateKeyPEM(key)
	pub, _ := PublicKeyPEM(&key.PublicKey)
	keyFile := filepath.Join(t.TempDir(), "k.pem")
	os.WriteFile(keyFile, priv, 0o600)
	return &Repository{Root: filepath.Join(t.TempDir(), "site"), Signer: &RSASigner{Key: key}, Time: testutil.FixedTime}, pub, keyFile
}

func populate(t *testing.T, r *Repository, versions ...string) {
	t.Helper()
	tmp := t.TempDir()
	for _, v := range versions {
		if _, err := r.Update(context.Background(), testutil.MustPkg(t, tmp, testutil.Fixture(v))); err != nil {
			t.Fatal(err)
		}
	}
}

// The helper process: performs an operation and dies (os.Exit, no deferred
// cleanup, like kill -9) when it reaches the step named in PKGOPS_CRASH_AT.
func TestCrashHelper(t *testing.T) {
	if os.Getenv("PKGOPS_CRASH_HELPER") != "1" {
		t.Skip("helper process")
	}
	at := os.Getenv("PKGOPS_CRASH_AT")
	crashPoint = func(step string) {
		if step == at {
			os.Exit(42)
		}
	}
	signer, err := LoadRSASigner(os.Getenv("PKGOPS_KEY"), SignPubkey)
	if err != nil {
		t.Fatal(err)
	}
	r := &Repository{Root: os.Getenv("PKGOPS_ROOT"), Signer: signer, Time: testutil.FixedTime}
	ctx := context.Background()
	switch os.Getenv("PKGOPS_OP") {
	case "delete":
		err = r.Delete(ctx, abi, "hello", os.Getenv("PKGOPS_VERSION"))
	case "add":
		_, err = r.Update(ctx, os.Getenv("PKGOPS_PKG"))
	}
	if err != nil {
		t.Fatal(err)
	}
	// reached the end without crashing: signal it
	os.Exit(0)
}

func runHelper(t *testing.T, at string, env ...string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), append(env, "PKGOPS_CRASH_HELPER=1", "PKGOPS_CRASH_AT="+at)...)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

// Whatever step a deleting process is killed at, the repository must verify
// (both catalogs signed and every advertised package present), and it must
// stay operable.
func TestCrashDuringDeleteLeavesConsistentCatalog(t *testing.T) {
	steps := []string{"reindex:staged", "reindex:renamed:packagesite.pkg", "reindex:renamed:data.pkg",
		"reindex:renamed:meta", "reindex:renamed:meta.conf", "delete:catalog", "delete:removed:hello-0.1.1.pkg"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			r, pub, keyFile := signedRepo(t)
			populate(t, r, "0.1.0", "0.1.1")
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatal(err)
			}
			code := runHelper(t, step, "PKGOPS_OP=delete", "PKGOPS_VERSION=0.1.1", "PKGOPS_ROOT="+r.Root, "PKGOPS_KEY="+keyFile)
			if code != 42 {
				t.Fatalf("helper did not crash at %s (exit %d)", step, code)
			}
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatalf("inconsistent after crash at %s: %v", step, err)
			}
			// recovery: stale staging is cleaned by the next operation and the
			// delete can simply be repeated (or is already done)
			if err := r.Delete(context.Background(), abi, "hello", "0.1.1"); err != nil && !errors.Is(err, pkgerr.ErrNotFound) {
				t.Fatalf("retry: %v", err)
			}
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatalf("after retry: %v", err)
			}
			infos, _ := r.List(abi)
			if len(infos) != 1 || infos[0].Version != "0.1.0" {
				t.Errorf("final state %v", infos)
			}
			ents, _ := os.ReadDir(r.Dir(abi))
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".stage-") {
					t.Errorf("stale staging dir %s left behind", e.Name())
				}
			}
		})
	}
}

func TestCrashDuringAddLeavesConsistentCatalog(t *testing.T) {
	for _, step := range []string{"add:copied", "reindex:staged", "reindex:renamed:data.pkg", "reindex:renamed:meta.conf"} {
		t.Run(step, func(t *testing.T) {
			r, pub, keyFile := signedRepo(t)
			populate(t, r, "0.1.0")
			newPkg := testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.1"))
			code := runHelper(t, step, "PKGOPS_OP=add", "PKGOPS_PKG="+newPkg, "PKGOPS_ROOT="+r.Root, "PKGOPS_KEY="+keyFile)
			if code != 42 {
				t.Fatalf("helper did not crash at %s (exit %d)", step, code)
			}
			if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
				t.Fatalf("inconsistent after crash at %s: %v", step, err)
			}
			if _, err := r.Update(context.Background(), newPkg); err != nil {
				t.Fatalf("retry add: %v", err)
			}
			if res, err := VerifyRepository(r.Dir(abi), pub); err != nil || res.Packages != 1 {
				t.Fatalf("after retry: %v %v", res, err)
			}
		})
	}
}

// Readers that behave like pkg (read the catalog, then fetch what it lists)
// while versions are deleted: a package named by a catalog may vanish only
// after a newer catalog stopped advertising it, so a re-read always recovers.
func TestConcurrentReadersDuringDelete(t *testing.T) {
	r, pub, _ := signedRepo(t)
	versions := []string{"0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4", "0.1.5"}
	populate(t, r, versions...)
	dir := r.Dir(abi)
	var stop atomic.Bool
	var wg sync.WaitGroup
	var bad atomic.Value
	var reads, recovered atomic.Int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				for attempt := 0; ; attempt++ {
					_, err := VerifyRepository(dir, pub)
					reads.Add(1)
					if err == nil {
						break
					}
					if attempt < 4 && (errors.Is(err, pkgerr.ErrNotFound) || os.IsNotExist(errors.Unwrap(err)) || strings.Contains(err.Error(), "no such file")) {
						recovered.Add(1) // stale snapshot; like pkg, refresh and retry
						continue
					}
					bad.Store(err)
					return
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond) // let the readers start
	for i := len(versions) - 1; i >= 1; i-- {
		if err := r.Delete(context.Background(), abi, "hello", versions[i]); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Fatalf("reader saw an inconsistent repository: %v", v)
	}
	if reads.Load() == 0 {
		t.Fatal("no reads happened")
	}
	t.Logf("%d reads, %d stale-snapshot retries", reads.Load(), recovered.Load())
	infos, _ := r.List(abi)
	if len(infos) != 1 || infos[0].Version != "0.1.0" {
		t.Errorf("final: %v", infos)
	}
}

func TestDeleteMissingAndInvalid(t *testing.T) {
	r, _, _ := signedRepo(t)
	populate(t, r, "0.1.0")
	ctx := context.Background()
	if err := r.Delete(ctx, abi, "hello", "9.9"); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	for _, bad := range [][2]string{{"../x", "1"}, {"hello", "../1"}, {"hello", ""}} {
		if err := r.Delete(ctx, abi, bad[0], bad[1]); !errors.Is(err, pkgerr.ErrInvalidSpec) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// deleting the last version leaves a valid, empty catalog
	if err := r.Delete(ctx, abi, "hello", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if res, err := VerifyRepository(r.Dir(abi), nil); err != nil || res.Packages != 0 {
		t.Errorf("empty catalog: %v %v", res, err)
	}
}
