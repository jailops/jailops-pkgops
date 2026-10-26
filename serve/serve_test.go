package serve

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops"
)

func buildPkg(t *testing.T, dir, version string) string {
	t.Helper()
	p := &pkgops.Package{Name: "demo", Version: version, Comment: "demo", Maintainer: "a@b.c", WWW: "https://example.com", ABI: "FreeBSD:15:amd64",
		Files: []pkgops.File{{Path: "/usr/local/bin/demo", Data: []byte("bin " + version), Mode: 0o755}},
		Time:  time.Unix(1700000000, 0)}
	f := filepath.Join(dir, "demo-"+version+".pkg")
	if _, err := p.WriteFile(f, pkgops.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	return f
}

func newServer(t *testing.T, keep int) (*httptest.Server, *pkgops.Repository, []byte) {
	t.Helper()
	key, err := pkgops.GenerateRSAKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := pkgops.PublicKeyPEM(&key.PublicKey)
	repo := &pkgops.Repository{Root: filepath.Join(t.TempDir(), "repo"), Signer: &pkgops.RSASigner{Key: key}}
	s, err := New(Config{Repo: repo, Keep: keep, Tokens: []Token{
		{Name: "ci", Hash: HashToken("up-secret"), Scopes: []Scope{ScopeUpload}},
		{Name: "admin", Hash: HashToken("adm-secret"), Scopes: []Scope{ScopeUpload, ScopeDelete}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, repo, pub
}

func do(t *testing.T, method, url, token string, body io.Reader) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestUploadListServeDelete(t *testing.T) {
	ts, repo, pub := newServer(t, 0)
	tmp := t.TempDir()
	f1 := buildPkg(t, tmp, "1.0")
	data, _ := os.ReadFile(f1)

	if resp, _ := do(t, "PUT", ts.URL+"/api/v1/packages", "", bytes.NewReader(data)); resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "PUT", ts.URL+"/api/v1/packages", "wrong", bytes.NewReader(data)); resp.StatusCode != 401 {
		t.Errorf("bad token: %d", resp.StatusCode)
	}
	if resp, body := do(t, "PUT", ts.URL+"/api/v1/packages", "up-secret", strings.NewReader("garbage")); resp.StatusCode != 422 {
		t.Errorf("garbage upload: %d %s", resp.StatusCode, body)
	}
	if err := Push(context.Background(), ts.URL, "up-secret", f1); err != nil {
		t.Fatal(err)
	}
	// re-uploading identical bytes is fine, different bytes under the same version conflict
	if err := Push(context.Background(), ts.URL, "up-secret", f1); err != nil {
		t.Fatalf("idempotent push failed: %v", err)
	}
	p := &pkgops.Package{Name: "demo", Version: "1.0", Comment: "other", Maintainer: "a@b.c", WWW: "https://example.com", ABI: "FreeBSD:15:amd64"}
	other := filepath.Join(t.TempDir(), "demo-1.0.pkg")
	p.WriteFile(other, pkgops.WriteOptions{})
	if err := Push(context.Background(), ts.URL, "up-secret", other); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("want 409 conflict, got %v", err)
	}
	// the repository is signed and consistent right after the upload
	if _, err := pkgops.VerifyRepository(repo.Dir("FreeBSD:15:amd64"), pub); err != nil {
		t.Fatal(err)
	}

	resp, body := do(t, "GET", ts.URL+"/api/v1/packages", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"name":"demo"`) || !strings.Contains(body, "/FreeBSD:15:amd64/latest/All/demo-1.0.pkg") {
		t.Errorf("list: %d %s", resp.StatusCode, body)
	}

	// static side
	base := ts.URL + "/FreeBSD:15:amd64/latest/"
	resp, body = do(t, "GET", base+"meta.conf", "", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || resp.Header.Get("Cache-Control") != "no-cache" || !strings.Contains(body, "version = 2") {
		t.Errorf("meta.conf: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = do(t, "GET", base+"All/demo-1.0.pkg", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("package headers: %v", resp.Header)
	}
	req, _ := http.NewRequest("GET", base+"All/demo-1.0.pkg", nil)
	req.Header.Set("Range", "bytes=0-3")
	rr, _ := http.DefaultClient.Do(req)
	rr.Body.Close()
	if rr.StatusCode != http.StatusPartialContent {
		t.Errorf("range requests unsupported: %d", rr.StatusCode)
	}
	resp, _ = do(t, "GET", base+"data.pkg", "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Last-Modified") == "" {
		t.Errorf("data.pkg: %v", resp.Header)
	}
	for _, p := range []string{"", "All/", "..%2f..%2fetc/passwd", "All/.tmp-x"} {
		if resp, _ := do(t, "GET", base+p, "", nil); resp.StatusCode == 200 {
			t.Errorf("GET %q should not be 200", p)
		}
	}

	// delete needs the delete scope
	del := ts.URL + "/api/v1/packages/FreeBSD:15:amd64/demo/1.0"
	if resp, _ := do(t, "DELETE", del, "up-secret", nil); resp.StatusCode != 403 {
		t.Errorf("delete without scope: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", del, "adm-secret", nil); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", del, "adm-secret", nil); resp.StatusCode != 404 {
		t.Errorf("delete again: %d", resp.StatusCode)
	}
}

func TestRetentionAndConcurrentClientsNeverSeeBrokenRepo(t *testing.T) {
	ts, repo, pub := newServer(t, 2)
	tmp := t.TempDir()
	if err := Push(context.Background(), ts.URL, "up-secret", buildPkg(t, tmp, "1.0")); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errs := make(chan error, 1)
	go func() { // a "client" verifying the repository while uploads happen
		for {
			select {
			case <-stop:
				errs <- nil
				return
			default:
			}
			if _, err := pkgops.VerifyRepository(repo.Dir("FreeBSD:15:amd64"), pub); err != nil {
				errs <- err
				return
			}
		}
	}()
	for _, v := range []string{"1.1", "1.2", "1.3"} {
		if err := Push(context.Background(), ts.URL, "up-secret", buildPkg(t, tmp, v)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-errs; err != nil {
		t.Fatalf("reader saw an inconsistent repository: %v", err)
	}
	list, _ := repo.List("FreeBSD:15:amd64")
	if len(list) != 2 || list[0].Version != "1.2" || list[1].Version != "1.3" {
		t.Errorf("retention kept %v", list)
	}
}

func TestLoadTokens(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tokens")
	os.WriteFile(f, []byte("# c\nci upload "+HashToken("x")+"\nadmin upload,delete "+HashToken("y")+"\n"), 0o600)
	toks, err := LoadTokens(f)
	if err != nil || len(toks) != 2 || !toks[1].Has(ScopeDelete) || toks[0].Has(ScopeDelete) {
		t.Fatalf("%+v %v", toks, err)
	}
	os.WriteFile(f, []byte("ci bogus "+HashToken("x")+"\n"), 0o600)
	if _, err := LoadTokens(f); err == nil {
		t.Error("unknown scope accepted")
	}
	os.WriteFile(f, []byte("ci upload short\n"), 0o600)
	if _, err := LoadTokens(f); err == nil {
		t.Error("short hash accepted")
	}
}

func TestReadOnlyWithoutTokens(t *testing.T) {
	s, _ := New(Config{Repo: &pkgops.Repository{Root: t.TempDir()}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if resp, _ := do(t, "PUT", ts.URL+"/api/v1/packages", "anything", strings.NewReader("x")); resp.StatusCode != 401 {
		t.Errorf("uploads must be refused without tokens: %d", resp.StatusCode)
	}
}
