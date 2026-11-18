package archive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/ghapp/ghfake"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/testutil"
)

func releaseOf(f *ghfake.Fake, tag string) *ghfake.Release {
	for _, r := range f.Releases() {
		if r.Tag == tag {
			return &r
		}
	}
	return nil
}

func dropManifest(t *testing.T, f *ghfake.Fake, a *Archive, tag, name string) []byte {
	t.Helper()
	rel, err := a.GH.GetReleaseByTag(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	as := findAsset(rel, name)
	if as == nil {
		t.Fatalf("%s has no %s", tag, name)
	}
	var data []byte
	for _, x := range releaseOf(f, tag).Assets {
		if x.Name == name {
			data = x.Data
		}
	}
	if err := a.GH.DeleteAsset(context.Background(), as.ID); err != nil {
		t.Fatal(err)
	}
	return data
}

// A release whose archive.json is gone (an interrupted replacement) is still
// listed: its package assets are the truth, and the manifest is rebuilt from them.
func TestListRebuildsAReleaseWhoseManifestIsGone(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0", "0.1.1")
	ix, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	dropManifest(t, f, a, "pkg-hello-0.1.0", ManifestAsset)
	recs, err := a.List(ctx)
	if err != nil || len(recs) != 2 {
		t.Fatalf("list: %v %v", recs, err)
	}
	for _, rec := range recs {
		if rec.Name != "hello" || rec.ABI != abi || len(rec.SHA256) != 64 {
			t.Errorf("record %+v", rec)
		}
	}
	// the next push writes the manifest back
	if res, err := a.Push(ctx, r.Root, ix); err != nil || res.Uploaded != 0 {
		t.Fatalf("push: %+v %v", res, err)
	}
	if releaseOf(f, "pkg-hello-0.1.0") == nil {
		t.Fatal("release gone")
	}
}

// The complete copy that a failed replacement leaves is read, with everything
// the manifest says.
func TestListReadsTheCompleteCopyOfAFailedReplacement(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0")
	ix, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	data := dropManifest(t, f, a, "pkg-hello-0.1.0", ManifestAsset)
	var man Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatal(err)
	}
	man.Packages[0].Comment = "kept in the copy"
	next, _ := json.Marshal(man)
	f.SeedAsset("pkg-hello-0.1.0", ManifestNext, "application/json", next)
	recs, err := a.List(ctx)
	if err != nil || len(recs) != 1 || recs[0].Comment != "kept in the copy" {
		t.Fatalf("list: %+v %v", recs, err)
	}
}

// Replacing a manifest leaves exactly the package assets and one archive.json.
func TestWriteManifestLeavesOneManifestAndNoCopy(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0")
	ix, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	// a second ABI in the same release replaces the manifest
	b := testutil.Fixture("0.1.0")
	b.ABI = "FreeBSD:15:amd64"
	if _, err := r.Update(ctx, testutil.MustPkg(t, t.TempDir(), b)); err != nil {
		t.Fatal(err)
	}
	ix, _ = r.Index()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, as := range releaseOf(f, "pkg-hello-0.1.0").Assets {
		names[as.Name]++
	}
	if names[ManifestAsset] != 1 || names[ManifestNext] != 0 || len(names) != 3 {
		t.Fatalf("assets: %v", names)
	}
	if recs, err := a.List(ctx); err != nil || len(recs) != 2 {
		t.Fatalf("list: %v %v", recs, err)
	}
}

// An unreadable manifest stops a deletion before anything is removed; it is
// never taken for "no other package is left" (which deletes the whole release).
func TestDeleteStopsOnAnUnreadableManifest(t *testing.T) {
	a, f, r := setup(t)
	add(t, r, "0.1.0")
	b := testutil.Fixture("0.1.0")
	b.ABI = "FreeBSD:15:amd64"
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), b)); err != nil {
		t.Fatal(err)
	}
	ix, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, ix); err != nil {
		t.Fatal(err)
	}
	for _, x := range releaseOf(f, "pkg-hello-0.1.0").Assets {
		if x.Name == ManifestAsset {
			x.Data = []byte("{not json")
		}
	}
	err := a.Delete(ctx, []Ref{{ABI: abi, Name: "hello", Version: "0.1.0"}}, "alice", "test")
	if !strings.Contains(errString(err), ManifestAsset) {
		t.Fatalf("want an error about %s, got %v", ManifestAsset, err)
	}
	if !f.HasTag("pkg-hello-0.1.0") || len(releaseOf(f, "pkg-hello-0.1.0").Assets) != 3 {
		t.Fatalf("something was deleted: %+v", releaseOf(f, "pkg-hello-0.1.0"))
	}
	if rel := releaseOf(f, AuditTag); rel != nil && strings.Contains(rel.Body, "deleted") {
		t.Errorf("an audit record was written for a deletion that did not happen:\n%s", rel.Body)
	}
	_ = pkgerr.ErrInvalidPackage
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
