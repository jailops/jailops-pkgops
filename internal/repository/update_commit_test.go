package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/testutil"
)

const abiB = "FreeBSD:15:amd64"

// A package file that a published catalog advertises is never removed by the
// rollback of a later failure of the same Update.
func TestUpdateKeepsFilesOfPublishedABIWhenALaterABIFails(t *testing.T) {
	r, pub, _ := signedRepo(t)
	tmp := t.TempDir()
	pa := testutil.Fixture("0.1.0")
	pb := testutil.Fixture("0.1.0")
	pb.ABI = abiB
	fa, fb := testutil.MustPkg(t, filepath.Join(tmp, "a"), pa), testutil.MustPkg(t, filepath.Join(tmp, "b"), pb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// the first ABI (aarch64) is published; the context ends before the second one is staged
	crashPoint = func(step string) {
		if step == "reindex:index" {
			cancel()
		}
	}
	defer func() { crashPoint = func(string) {} }()

	_, err := r.Update(ctx, fa, fb)
	if err == nil {
		t.Fatal("the second ABI cannot be published: want an error")
	}
	if !strings.Contains(err.Error(), "already published: "+abi) || !strings.Contains(err.Error(), "not published: "+abiB) {
		t.Errorf("the error does not say what was published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.0.pkg")); err != nil {
		t.Fatalf("the package file of the published ABI is gone: %v", err)
	}
	if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
		t.Fatalf("the published ABI does not verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(abiB), "All", "hello-0.1.0.pkg")); !os.IsNotExist(err) {
		t.Errorf("the file of the ABI that was not published is still there: %v", err)
	}
	ix, err := index.Read(r.Root)
	if err != nil || ix == nil || len(ix.Packages) != 1 || ix.Packages[0].ABI != abi {
		t.Fatalf("index after the failure: %+v %v", ix, err)
	}
	// the batch can simply be repeated
	crashPoint = func(string) {}
	if _, err := r.Update(context.Background(), fa, fb); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	for _, a := range []string{abi, abiB} {
		if _, err := VerifyRepository(r.Dir(a), pub); err != nil {
			t.Errorf("%s after the repeat: %v", a, err)
		}
	}
}

// The catalog is in place and the index write fails: the package file stays
// (the catalog lists it) and the next update finishes the work.
func TestUpdateKeepsTheFileWhenTheIndexWriteFails(t *testing.T) {
	r, pub, _ := signedRepo(t)
	populate(t, r, "0.1.0")
	newPkg := testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.1"))
	ixPath := filepath.Join(r.Root, index.FileName)
	saved, err := os.ReadFile(ixPath)
	if err != nil {
		t.Fatal(err)
	}
	// after the last catalog file is renamed, a directory takes the index's place: its atomic replace fails
	crashPoint = func(step string) {
		if step == "reindex:renamed:meta.conf" {
			os.Remove(ixPath)
			os.Mkdir(ixPath, 0o755)
		}
	}
	defer func() { crashPoint = func(string) {} }()
	if _, err := r.Update(context.Background(), newPkg); err == nil {
		t.Fatal("the index write must fail")
	}
	crashPoint = func(string) {}
	if _, err := os.Stat(filepath.Join(r.Dir(abi), "All", "hello-0.1.1.pkg")); err != nil {
		t.Fatalf("the file the catalog advertises was removed: %v", err)
	}
	if _, err := VerifyRepository(r.Dir(abi), pub); err != nil {
		t.Fatalf("the repository does not verify after the failure: %v", err)
	}
	// the index is restored (what the disk fault took away) and the same update completes
	if err := os.Remove(ixPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ixPath, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background(), newPkg); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	ix, err := index.Read(r.Root)
	if err != nil || ix == nil || len(ix.Packages) != 2 {
		t.Fatalf("index after the repeat: %+v %v", ix, err)
	}
}
