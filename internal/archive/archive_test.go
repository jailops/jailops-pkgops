package archive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/fetch"
	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/ghapp/ghfake"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

const abi = "FreeBSD:15:aarch64"

func setup(t *testing.T) (*Archive, *ghfake.Fake, *repository.Repository) {
	t.Helper()
	f := ghfake.New()
	t.Cleanup(f.Close)
	f.AllowToken("tkn")
	gh := &ghapp.Client{Owner: "o", Repo: "r", Auth: ghapp.StaticToken("tkn"), API: f.URL, UploadAPI: f.URL}
	r := &repository.Repository{Root: filepath.Join(t.TempDir(), "site"), Time: testutil.FixedTime}
	return &Archive{GH: gh}, f, r
}

func add(t *testing.T, r *repository.Repository, versions ...string) {
	t.Helper()
	for _, v := range versions {
		if _, err := r.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture(v))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPushIsIdempotentAndRefusesChangedBytes(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0", "0.1.1")
	ix, _ := r.Index()
	ctx := context.Background()
	res, err := a.Push(ctx, r.Root, ix)
	if err != nil || res.Uploaded != 2 {
		t.Fatalf("push: %+v %v", res, err)
	}
	rels := f.Releases()
	if len(rels) != 2 || rels[0].Tag != "pkg-hello-0.1.0" {
		t.Fatalf("releases: %+v", rels)
	}
	names := map[string]bool{}
	for _, as := range rels[0].Assets {
		names[as.Name] = true
	}
	if !names["hello-0.1.0-FreeBSD_15_aarch64.pkg"] || !names[ManifestAsset] {
		t.Errorf("assets: %v", names)
	}
	if !strings.Contains(rels[0].Body, ix.Packages[0].SHA256) {
		t.Errorf("release notes lack the sha256:\n%s", rels[0].Body)
	}
	before := len(f.Uploads())
	res, err = a.Push(ctx, r.Root, ix)
	if err != nil || res.Uploaded != 0 || res.Skipped != 2 || len(f.Uploads()) != before {
		t.Fatalf("second push: %+v %v (uploads %d -> %d)", res, err, before, len(f.Uploads()))
	}
	// same name-version, different bytes: an error, nothing overwritten
	other := testutil.Fixture("0.1.0")
	other.Desc = "different"
	tmp := t.TempDir()
	r2 := &repository.Repository{Root: filepath.Join(t.TempDir(), "other"), Time: testutil.FixedTime}
	if _, err := r2.Update(ctx, testutil.MustPkg(t, tmp, other)); err != nil {
		t.Fatal(err)
	}
	ix2, _ := r2.Index()
	_, err = a.Push(ctx, r2.Root, ix2)
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "already archived") {
		t.Fatalf("changed bytes: %v", err)
	}
}

func TestPushCompletesAnInterruptedUpload(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0")
	ix, _ := r.Index()
	e := ix.Packages[0]
	data, _ := os.ReadFile(filepath.Join(r.Root, e.File))
	f.SeedRelease("pkg-hello-0.1.0", "hello 0.1.0", "")
	f.SeedAsset("pkg-hello-0.1.0", "hello-0.1.0-FreeBSD_15_aarch64.pkg", "application/octet-stream", data) // no archive.json yet
	res, err := a.Push(context.Background(), r.Root, ix)
	if err != nil || res.Uploaded != 0 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	recs, err := a.List(context.Background())
	if err != nil || len(recs) != 1 || recs[0].SHA256 != e.SHA256 {
		t.Fatalf("list: %v %v", recs, err)
	}
}

func TestFetchFromArchiveRestoresTheBaselineAndHonoursPrune(t *testing.T) {
	a, _, r := setup(t)
	add(t, r, "0.1.0", "0.1.1", "0.1.2")
	ix, _ := r.Index()
	if _, err := a.Push(context.Background(), r.Root, ix); err != nil {
		t.Fatal(err)
	}
	into := t.TempDir()
	m := &prune.Manifest{Schema: 1, Entries: []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.0"}}}
	res, err := a.Fetch(context.Background(), fetch.Options{Into: into, Prune: m})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "releases" || res.Downloaded != 2 || res.Pruned != 1 {
		t.Errorf("%+v", res)
	}
	got, _ := index.Read(into)
	if len(got.Packages) != 2 {
		t.Fatalf("index %+v", got)
	}
	for _, e := range got.Packages {
		want, _ := os.ReadFile(filepath.Join(r.Root, e.File))
		have, err := os.ReadFile(filepath.Join(into, e.File))
		if err != nil || !bytes.Equal(want, have) {
			t.Errorf("%s: %v", e.File, err)
		}
	}
	// a restored baseline is a valid base for the next build
	r2 := &repository.Repository{Root: into, Time: testutil.FixedTime, PruneManifest: m}
	if _, err := r2.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.3"))); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReportsEveryDifference(t *testing.T) {
	a, _, r := setup(t)
	add(t, r, "0.1.0", "0.1.1", "0.1.2")
	ctx := context.Background()
	ix, _ := r.Index()
	// archive only 0.1.0 and 0.1.1
	partial := index.New()
	partial.Packages = ix.Packages[:2]
	if _, err := a.Push(ctx, r.Root, partial); err != nil {
		t.Fatal(err)
	}
	recs, _ := a.List(ctx)
	rep := Verify(ix, recs, nil)
	if len(rep.OnPagesNotArchived) != 1 || !strings.Contains(rep.OnPagesNotArchived[0], "hello-0.1.2") || rep.OK() {
		t.Errorf("not archived: %+v", rep)
	}
	// Pages drops 0.1.0 without a prune record: a problem; with a record it is normal
	live := index.New()
	live.Packages = ix.Packages[1:]
	rep = Verify(live, recs, nil)
	if len(rep.ArchivedNotOnPages) != 1 || rep.OK() {
		t.Errorf("dropped silently: %+v", rep)
	}
	live.Pruned = []index.Pruned{{ABI: abi, Name: "hello", Version: "0.1.0", SHA256: ix.Packages[0].SHA256}}
	rep = Verify(live, recs, nil)
	if len(rep.ArchivedNotOnPages) != 0 || len(rep.PrunedStillArchived) != 1 {
		t.Errorf("pruned: %+v", rep)
	}
	if rep2 := Verify(&index.Index{Packages: ix.Packages[1:2]}, recs, map[string]bool{ix.Packages[0].Key(): true}); len(rep2.ArchivedNotOnPages) != 0 {
		t.Errorf("listed in the prune manifest: %+v", rep2)
	}
	// a differing sha256
	bad := *ix
	bad.Packages = append([]index.Entry(nil), ix.Packages[:2]...)
	bad.Packages[0].SHA256 = strings.Repeat("0", 64)
	if rep := Verify(&bad, recs, nil); len(rep.Mismatched) != 1 {
		t.Errorf("mismatch: %+v", rep)
	}
	// pruned and not archived is reported, not hidden
	gone := index.New()
	gone.Pruned = []index.Pruned{{ABI: abi, Name: "hello", Version: "9.9"}}
	if rep := Verify(gone, recs, nil); len(rep.PrunedNotArchived) != 1 {
		t.Errorf("%+v", rep)
	}
}

func TestDeleteNeedsReasonWritesAuditAndKeepsTheRestOfTheRelease(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0", "0.1.1")
	ix, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	ref := Ref{ABI: abi, Name: "hello", Version: "0.1.0"}
	if err := a.Delete(ctx, []Ref{ref}, "alice", " "); err == nil {
		t.Fatal("a reason is required")
	}
	if err := a.Delete(ctx, []Ref{{ABI: abi, Name: "hello", Version: "7"}}, "alice", "x"); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}
	if err := a.Delete(ctx, []Ref{ref}, "alice@example.com (via access)", "bad build\nsecond line"); err != nil {
		t.Fatal(err)
	}
	recs, _ := a.List(ctx)
	if len(recs) != 1 || recs[0].Version != "0.1.1" {
		t.Fatalf("remaining: %+v", recs)
	}
	if f.HasTag("pkg-hello-0.1.0") {
		t.Error("an empty release must be deleted with its tag")
	}
	var audit string
	for _, rel := range f.Releases() {
		if rel.Tag == AuditTag {
			audit = rel.Body
		}
	}
	if !strings.Contains(audit, "alice@example.com") || !strings.Contains(audit, "hello-0.1.0") || !strings.Contains(audit, ix.Packages[0].SHA256) || len(strings.Split(strings.TrimSpace(audit), "\n")) != 2 {
		t.Errorf("audit:\n%s", audit)
	}
	// the audit release is not mistaken for a package
	if recs, _ := a.List(ctx); len(recs) != 1 {
		t.Errorf("list: %+v", recs)
	}
}
