package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/ghapp/ghfake"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

// ahead builds the situation heal is for: the archive holds hello 0.1.0 to
// 0.1.2, the baseline (the site as it was published) holds 0.1.0 and 0.1.1 only.
func ahead(t *testing.T) (a *Archive, f *ghfake.Fake, into string, ix *index.Index, recs []Record, rep *Report) {
	t.Helper()
	var r *repository.Repository
	a, f, r = setup(t)
	add(t, r, "0.1.0", "0.1.1", "0.1.2")
	full, _ := r.Index()
	ctx := context.Background()
	if _, err := a.Push(ctx, r.Root, full); err != nil {
		t.Fatal(err)
	}
	into = filepath.Join(t.TempDir(), "baseline")
	base := &repository.Repository{Root: into, Time: testutil.FixedTime}
	add(t, base, "0.1.0", "0.1.1")
	ix, err := base.Index()
	if err != nil {
		t.Fatal(err)
	}
	recs, err = a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rep = Verify(ix, recs, nil)
	if len(rep.ArchivedNotOnPages) != 1 || !strings.Contains(rep.ArchivedNotOnPages[0], "hello-0.1.2") || len(rep.Mismatched)+len(rep.OnPagesNotArchived) != 0 {
		t.Fatalf("setup: %+v", rep)
	}
	return
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func same(t *testing.T, a, b map[string]string) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("the baseline changed: %d files before, %d after", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
}

func TestHealRestoresWhatTheArchiveHoldsAheadOfThePages(t *testing.T) {
	a, _, into, ix, recs, rep := ahead(t)
	res, err := a.Heal(context.Background(), into, ix, recs, rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Healed) != 1 || !strings.Contains(res.Healed[0], "hello-0.1.2") || res.Bytes == 0 {
		t.Fatalf("%+v", res)
	}
	got, err := index.Read(into)
	if err != nil || len(got.Packages) != 3 {
		t.Fatalf("index %+v %v", got, err)
	}
	e, ok := got.Find(abi, "hello", "0.1.2")
	if !ok {
		t.Fatal("the healed version is not in the baseline index")
	}
	if _, err := os.Stat(filepath.Join(into, e.File)); err != nil {
		t.Errorf("file: %v", err)
	}
	if !fileHasSum(filepath.Join(into, e.File), e.SHA256, e.Size) {
		t.Error("the healed file does not match its index entry")
	}
	// the baseline and the archive agree now
	if rep2 := Verify(got, recs, nil); !rep2.OK() {
		t.Errorf("still disagree: %+v", rep2)
	}
	if left, _ := filepath.Glob(filepath.Join(into, "*", "*", "All", "*.part")); len(left) != 0 {
		t.Errorf("part files left: %v", left)
	}
	// healing again has nothing to do
	if res, err := a.Heal(context.Background(), into, got, recs, Verify(got, recs, nil)); err != nil || len(res.Healed) != 0 {
		t.Errorf("second heal: %+v %v", res, err)
	}
}

func TestHealRefusesBytesThatDoNotMatchTheManifest(t *testing.T) {
	a, f, into, ix, recs, rep := ahead(t)
	for _, rel := range f.Releases() {
		for _, as := range rel.Assets {
			if strings.Contains(as.Name, "0.1.2") && strings.HasSuffix(as.Name, ".pkg") {
				as.Data[len(as.Data)/2] ^= 0xff // same length, other bytes
			}
		}
	}
	before := snapshot(t, into)
	_, err := a.Heal(context.Background(), into, ix, recs, rep)
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "does not match its manifest") {
		t.Fatalf("err %v", err)
	}
	same(t, before, snapshot(t, into))
}

func TestHealRefusesAMixedReport(t *testing.T) {
	a, _, into, ix, recs, _ := ahead(t)
	before := snapshot(t, into)
	// the live index disagrees about the sha256 of 0.1.0: a different failure
	bad := *ix
	bad.Packages = append([]index.Entry(nil), ix.Packages...)
	bad.Packages[0].SHA256 = strings.Repeat("0", 64)
	rep := Verify(&bad, recs, nil)
	if len(rep.Mismatched) != 1 || len(rep.ArchivedNotOnPages) != 1 {
		t.Fatalf("setup: %+v", rep)
	}
	if _, err := a.Heal(context.Background(), into, &bad, recs, rep); !errors.Is(err, ErrNotHealable) {
		t.Fatalf("mismatch: %v", err)
	}
	// a live file the archive does not hold
	rep = Verify(ix, recs[:2], nil)
	rep.ArchivedNotOnPages = []string{recs[2].Key()}
	rep.OnPagesNotArchived = []string{"x y-1"}
	if _, err := a.Heal(context.Background(), into, ix, recs, rep); !errors.Is(err, ErrNotHealable) {
		t.Fatalf("not archived: %v", err)
	}
	// a pruned file that is gone for good
	rep = Verify(ix, recs, nil)
	rep.PrunedNotArchived = []string{"x y-1"}
	if _, err := a.Heal(context.Background(), into, ix, recs, rep); !errors.Is(err, ErrNotHealable) {
		t.Fatalf("gone for good: %v", err)
	}
	same(t, before, snapshot(t, into))
}

func TestHealNeverOverwritesADifferentFile(t *testing.T) {
	a, _, into, ix, recs, rep := ahead(t)
	dst := filepath.Join(into, abi, "latest", "All", "hello-0.1.2.pkg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("not the archived bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, into)
	_, err := a.Heal(context.Background(), into, ix, recs, rep)
	if !errors.Is(err, pkgerr.ErrInvalidPackage) || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("err %v", err)
	}
	same(t, before, snapshot(t, into))
}

func TestHealAcceptsAFileThatAlreadyHasTheRightBytes(t *testing.T) {
	a, _, into, ix, recs, rep := ahead(t)
	var rec Record
	for _, r := range recs {
		if r.Version == "0.1.2" {
			rec = r
		}
	}
	dst := filepath.Join(into, abi, "latest", "All", "hello-0.1.2.pkg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.download(context.Background(), rec, dst); err != nil {
		t.Fatal(err)
	}
	res, err := a.Heal(context.Background(), into, ix, recs, rep)
	if err != nil || res.Present != 1 || res.Bytes != 0 || len(res.Healed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := index.Read(into); len(got.Packages) != 3 {
		t.Errorf("index %+v", got)
	}
}

func TestHealRefusesAnUnreadableOrIncompleteArchiveRecord(t *testing.T) {
	a, f, into, ix, recs, rep := ahead(t)
	before := snapshot(t, into)
	ctx := context.Background()
	// a manifest entry without a sha256
	broken := append([]Record(nil), recs...)
	for i := range broken {
		if broken[i].Version == "0.1.2" {
			broken[i].SHA256 = ""
		}
	}
	if _, err := a.Heal(ctx, into, ix, broken, rep); !errors.Is(err, pkgerr.ErrInvalidPackage) {
		t.Fatalf("no sha256: %v", err)
	}
	// a manifest that lists an asset the release does not have
	missing := append([]Record(nil), recs...)
	for i := range missing {
		if missing[i].Version == "0.1.2" {
			missing[i].Asset = "hello-0.1.2-gone.pkg"
		}
	}
	if _, err := a.Heal(ctx, into, ix, missing, rep); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Fatalf("no asset: %v", err)
	}
	// a record that is not in the archive at all
	if _, err := a.Heal(ctx, into, ix, recs[:2], rep); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Fatalf("no record: %v", err)
	}
	same(t, before, snapshot(t, into))
	// an archive.json that cannot be read stops the listing itself: nothing to heal from
	for _, rel := range f.Releases() {
		for _, as := range rel.Assets {
			if rel.Tag == "pkg-hello-0.1.2" && as.Name == ManifestAsset {
				as.Data = []byte("{ not json")
			}
		}
	}
	if _, err := a.List(ctx); err == nil {
		t.Fatal("an unreadable manifest must fail the listing")
	}
}

func TestHealIsAllOrNothing(t *testing.T) {
	a, _, into, ix, recs, rep := ahead(t)
	// two files to heal; the second one fails
	second := recs[0]
	second.Version = "0.9.0"
	second.Asset = "hello-0.9.0-FreeBSD_15_aarch64.pkg"
	recs2 := append(append([]Record(nil), recs...), second)
	rep2 := *rep
	rep2.ArchivedNotOnPages = append(append([]string(nil), rep.ArchivedNotOnPages...), second.Key())
	before := snapshot(t, into)
	if _, err := a.Heal(context.Background(), into, ix, recs2, &rep2); err == nil {
		t.Fatal("the second file cannot be downloaded")
	}
	same(t, before, snapshot(t, into))
}

func TestHealWithNothingToDoChangesNothing(t *testing.T) {
	a, _, into, ix, recs, _ := ahead(t)
	before := snapshot(t, into)
	res, err := a.Heal(context.Background(), into, ix, recs, &Report{})
	if err != nil || len(res.Healed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	same(t, before, snapshot(t, into))
}

func TestHealNeedsABaseline(t *testing.T) {
	a, _, _, ix, recs, rep := ahead(t)
	empty := t.TempDir()
	if _, err := a.Heal(context.Background(), empty, ix, recs, rep); !errors.Is(err, pkgerr.ErrInvalidSpec) {
		t.Fatalf("err %v", err)
	}
}
