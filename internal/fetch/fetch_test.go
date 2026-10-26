package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

const abi = "FreeBSD:15:aarch64"

// site builds a repository with three versions and serves it over HTTP.
func site(t *testing.T) (root string, srv *httptest.Server, hits *[]string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "site")
	r := &repository.Repository{Root: root, Time: testutil.FixedTime}
	tmp := t.TempDir()
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		if _, err := r.Update(context.Background(), testutil.MustPkg(t, tmp, testutil.Fixture(v))); err != nil {
			t.Fatal(err)
		}
	}
	var h []string
	fs := http.FileServer(http.Dir(root))
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h = append(h, req.Header.Get("Range")+" "+req.URL.Path)
		fs.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	return root, srv, &h
}

func TestFetchRebuildsTheWholeBaselineAndVerifies(t *testing.T) {
	root, srv, _ := site(t)
	into := t.TempDir()
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: into})
	if err != nil {
		t.Fatal(err)
	}
	if res.Downloaded != 3 || res.Source != "pages" {
		t.Errorf("%+v", res)
	}
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		a, _ := os.ReadFile(filepath.Join(root, abi, "latest", "All", "hello-"+v+".pkg"))
		b, err := os.ReadFile(filepath.Join(into, abi, "latest", "All", "hello-"+v+".pkg"))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s differs: %v", v, err)
		}
	}
	ix, err := index.Read(into)
	if err != nil || len(ix.Packages) != 3 {
		t.Fatalf("index: %v %v", ix, err)
	}
	// a second run downloads nothing
	res, err = Pages(context.Background(), Options{From: srv.URL, Into: into})
	if err != nil || res.Downloaded != 0 || res.Reused != 3 {
		t.Errorf("rerun: %+v %v", res, err)
	}
	// the baseline is a valid base for the next build: new package + same index
	r := &repository.Repository{Root: into, Time: testutil.FixedTime}
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.3"))); err != nil {
		t.Fatal(err)
	}
	if ix, _ := index.Read(into); len(ix.Packages) != 4 {
		t.Errorf("history lost: %d", len(ix.Packages))
	}
}

func TestFetchResumesAPartialDownload(t *testing.T) {
	root, srv, hits := site(t)
	into := t.TempDir()
	rel := filepath.Join(abi, "latest", "All", "hello-0.1.1.pkg")
	full, _ := os.ReadFile(filepath.Join(root, rel))
	os.MkdirAll(filepath.Dir(filepath.Join(into, rel)), 0o755)
	os.WriteFile(filepath.Join(into, rel)+".part", full[:len(full)/2], 0o644)
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: into}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range *hits {
		if strings.HasPrefix(h, "bytes=") && strings.HasSuffix(h, "hello-0.1.1.pkg") {
			found = true
		}
	}
	if !found {
		t.Errorf("no Range request: %v", *hits)
	}
	got, _ := os.ReadFile(filepath.Join(into, rel))
	if string(got) != string(full) {
		t.Error("resumed file differs")
	}
}

func TestFetchFailsOnBadHashMissingFileAndNoIndex(t *testing.T) {
	root, srv, _ := site(t)
	rel := filepath.Join(root, abi, "latest", "All", "hello-0.1.1.pkg")
	good, _ := os.ReadFile(rel)
	bad := append([]byte(nil), good...)
	bad[len(bad)/2] ^= 0xff
	os.WriteFile(rel, bad, 0o644)
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into})
	if err == nil || !strings.Contains(err.Error(), "does not match the index") {
		t.Fatalf("bad hash: %v", err)
	}
	if _, e := os.Stat(filepath.Join(into, index.FileName)); !os.IsNotExist(e) {
		t.Error("the index must not be written for a failed baseline")
	}
	if _, e := os.Stat(filepath.Join(into, abi, "latest", "All", "hello-0.1.1.pkg")); !os.IsNotExist(e) {
		t.Error("the bad file must not be installed")
	}
	os.Remove(rel)
	if _, err = Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing file: %v", err)
	}
	empty := httptest.NewServer(http.NotFoundHandler())
	defer empty.Close()
	if _, err = Pages(context.Background(), Options{From: empty.URL, Into: t.TempDir()}); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("no index: %v", err)
	}
}

func TestFetchHonoursPruneManifestAndABIs(t *testing.T) {
	_, srv, hits := site(t)
	into := t.TempDir()
	m := &prune.Manifest{Schema: 1, Entries: []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.0", Reason: "old"}}}
	res, err := Pages(context.Background(), Options{From: srv.URL, Into: into, Prune: m})
	if err != nil {
		t.Fatal(err)
	}
	if res.Downloaded != 2 || res.Pruned != 1 {
		t.Errorf("%+v", res)
	}
	for _, h := range *hits {
		if strings.HasSuffix(h, "hello-0.1.0.pkg") {
			t.Error("a pruned file must not be requested")
		}
	}
	ix, _ := index.Read(into)
	if _, ok := ix.FindPruned(abi, "hello", "0.1.0"); !ok || len(ix.Packages) != 2 {
		t.Errorf("index %+v", ix)
	}
	// the newest cannot be pruned by accident
	m = &prune.Manifest{Schema: 1, Entries: []prune.Entry{{ABI: abi, Name: "hello", Version: "0.1.2"}}}
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), Prune: m}); !errors.Is(err, prune.ErrNewest) {
		t.Errorf("newest: %v", err)
	}
	// an ABI filter that matches nothing fetches nothing
	res, err = Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), ABIs: []string{"FreeBSD:15:amd64"}})
	if err != nil || res.Downloaded != 0 {
		t.Errorf("abi filter: %+v %v", res, err)
	}
}
