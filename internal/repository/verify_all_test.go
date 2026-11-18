package repository

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/testutil"
)

func twoABIRepo(t *testing.T) (*Repository, []byte) {
	t.Helper()
	r, pub, _ := signedRepo(t)
	tmp := t.TempDir()
	for i, v := range []string{"0.1.0", "0.1.1"} {
		a := testutil.Fixture(v)
		b := testutil.Fixture(v)
		b.ABI = abiB
		if _, err := r.Update(context.Background(),
			testutil.MustPkg(t, filepath.Join(tmp, "a", v), a), testutil.MustPkg(t, filepath.Join(tmp, "b", v), b)); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	return r, pub
}

func kinds(rep *Report) string {
	var k []string
	for _, p := range rep.Problems {
		k = append(k, p.Kind)
	}
	return strings.Join(k, ",")
}

func TestVerifyAllCleanRepository(t *testing.T) {
	r, pub := twoABIRepo(t)
	rep, err := VerifyAll(r.Root, pub)
	if err != nil || !rep.OK || len(rep.Problems) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if len(rep.Repos) != 2 || rep.Repos[0].Files != 2 || rep.Repos[0].Packages != 1 || rep.Index.Packages != 4 || !rep.Verified {
		t.Fatalf("report %+v", rep)
	}
	// unsigned check of the same tree is clean as well
	if rep, err := VerifyAll(r.Root, nil); err != nil || !rep.OK || rep.Verified {
		t.Fatalf("without a key: %+v %v", rep, err)
	}
	// nothing was changed by the check
	if rep2, _ := VerifyAll(r.Root, pub); !rep2.OK {
		t.Fatal("second check differs")
	}
	// the report is the document a workflow reads
	b, err := json.Marshal(rep)
	if err != nil || !strings.Contains(string(b), `"ok":true`) || !strings.Contains(string(b), `"problems":[]`) {
		t.Fatalf("json: %s %v", b, err)
	}
}

func TestVerifyAllFindsWhatIsWrong(t *testing.T) {
	type tc struct {
		name   string
		damage func(t *testing.T, r *Repository)
		want   string // kinds that must appear
	}
	all := func(r *Repository, a, f string) string { return filepath.Join(r.Dir(a), "All", f) }
	for _, c := range []tc{
		{"the newest file of one ABI is gone", func(t *testing.T, r *Repository) {
			os.Remove(all(r, abiB, "hello-0.1.1.pkg"))
		}, ProblemMissingFile},
		{"an older file the index lists is gone", func(t *testing.T, r *Repository) {
			os.Remove(all(r, abi, "hello-0.1.0.pkg"))
		}, ProblemMissingFile},
		{"a file has other bytes", func(t *testing.T, r *Repository) {
			other := testutil.Fixture("0.1.0")
			other.ABI = abiB
			other.Desc = "different"
			os.WriteFile(all(r, abiB, "hello-0.1.0.pkg"), testutil.MustBytes(t, other), 0o644)
		}, ProblemChecksum},
		{"a file the index does not know", func(t *testing.T, r *Repository) {
			p := testutil.Fixture("0.1.2")
			os.WriteFile(all(r, abi, "hello-0.1.2.pkg"), testutil.MustBytes(t, p), 0o644)
		}, ProblemNotInIndex},
		{"the index is gone", func(t *testing.T, r *Repository) {
			os.Remove(filepath.Join(r.Root, index.FileName))
		}, ProblemIndex},
		{"a pruned file is still there", func(t *testing.T, r *Repository) {
			ix, _ := index.Read(r.Root)
			e, _ := ix.Find(abi, "hello", "0.1.0")
			ix.Pruned = append(ix.Pruned, index.Pruned{ABI: abi, Name: "hello", Version: "0.1.0", SHA256: e.SHA256, PrunedAt: time.Unix(1700000000, 0)})
			if err := ix.Write(r.Root); err != nil {
				t.Fatal(err)
			}
		}, ProblemPrunedPresent},
		{"a catalog is damaged", func(t *testing.T, r *Repository) {
			os.WriteFile(filepath.Join(r.Dir(abi), "packagesite.pkg"), []byte("not a catalog"), 0o644)
		}, ProblemCatalog},
		{"a package file cannot be read", func(t *testing.T, r *Repository) {
			os.WriteFile(all(r, abi, "broken-1.pkg"), []byte("junk"), 0o644)
		}, ProblemUnreadableFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, pub := twoABIRepo(t)
			c.damage(t, r)
			rep, err := VerifyAll(r.Root, pub)
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK || !strings.Contains(kinds(rep), c.want) {
				t.Fatalf("want %s, got ok=%v %s: %+v", c.want, rep.OK, kinds(rep), rep.Problems)
			}
			for _, p := range rep.Problems {
				if p.Message == "" || p.Kind == "" {
					t.Errorf("incomplete finding %+v", p)
				}
			}
		})
	}
}

func TestVerifyAllNeedsARepository(t *testing.T) {
	rep, err := VerifyAll(t.TempDir(), nil)
	if err != nil || rep.OK || !strings.Contains(kinds(rep), ProblemNoRepositories) {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := VerifyAll(filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Fatal("a root that is not there cannot be checked")
	}
	if _, err := VerifyAll(t.TempDir(), []byte("not a key")); err == nil {
		t.Fatal("a bad key cannot be used")
	}
}
