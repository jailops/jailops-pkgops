package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/index"
)

// recorded serves root and records every request URI.
func recorded(t *testing.T, root string) (*httptest.Server, *[]string) {
	t.Helper()
	var uris []string
	fs := http.FileServer(http.Dir(root))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.URL.RequestURI())
		fs.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &uris
}

func TestTheIndexAndTheCatalogsAreRequestedWithACacheBustingQuery(t *testing.T) {
	root, _, _ := signedSite(t, "0.1.0", "0.1.1")
	srv, uris := recorded(t, root)
	now := time.Unix(1_800_000_000, 0)
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, TrustedKey: pubPEM(t, testKey), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	var docs, files int
	for _, u := range *uris {
		path, query, _ := strings.Cut(u, "?")
		switch {
		case path == "/"+index.FileName, strings.HasSuffix(path, "/data.pkg"), strings.HasSuffix(path, "/packagesite.pkg"):
			docs++
			if query != "cb=1800000000" {
				t.Errorf("%s: query %q, want cb=1800000000", path, query)
			}
		case strings.Contains(path, "/All/"):
			files++
			if query != "" {
				t.Errorf("a package file is immutable and gets no query: %s", u)
			}
		}
	}
	if docs != 3 || files != 2 {
		t.Errorf("requests %v: %d documents, %d package files", *uris, docs, files)
	}
}

func TestNoCacheBustKeepsTheRequestsPlain(t *testing.T) {
	root, _, _ := signedSite(t, "0.1.0")
	srv, uris := recorded(t, root)
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: t.TempDir(), TrustedKey: pubPEM(t, testKey), NoCacheBust: true}); err != nil {
		t.Fatal(err)
	}
	for _, u := range *uris {
		if strings.Contains(u, "?") {
			t.Errorf("request with a query: %s", u)
		}
	}
}

// A static file server ignores the query: the baseline is the same with it.
func TestTheCacheBustingQueryIsHarmlessToAStaticServer(t *testing.T) {
	root, srv, _ := site(t)
	with, without := t.TempDir(), t.TempDir()
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: with}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: without, NoCacheBust: true}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(with, index.FileName))
	b, _ := os.ReadFile(filepath.Join(without, index.FileName))
	r, _ := os.ReadFile(filepath.Join(root, index.FileName))
	if len(a) == 0 || string(a) != string(b) || string(a) != string(r) {
		t.Errorf("the baseline index differs with and without the query")
	}
}

// The index of a URL that already has a query keeps it.
func TestTheCacheBustingQueryKeepsAnExistingQuery(t *testing.T) {
	o := &Options{Now: func() time.Time { return time.Unix(7, 0) }}
	if got := o.bust("https://x.example/a/b?token=1"); got != "https://x.example/a/b?cb=7&token=1" {
		t.Errorf("%s", got)
	}
	o.NoCacheBust = true
	if got := o.bust("https://x.example/a"); got != "https://x.example/a" {
		t.Errorf("%s", got)
	}
}
