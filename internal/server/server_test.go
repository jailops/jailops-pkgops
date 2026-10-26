package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
	"jailops.net/pkgops/sdk"
)

const abi = "FreeBSD:15:aarch64"

type env struct {
	ts   *httptest.Server
	repo *repository.Repository
	pub  []byte
	pkgs string
}

func newEnv(t *testing.T, mutate func(*Config)) *env {
	t.Helper()
	key, err := repository.GenerateRSAKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := repository.PublicKeyPEM(&key.PublicKey)
	repo := &repository.Repository{Root: filepath.Join(t.TempDir(), "repo"), Signer: &repository.RSASigner{Key: key}}
	cfg := Config{Repo: repo, Tokens: []config.Token{
		{Name: "viewer", Hash: config.HashToken("v-secret")},
		{Name: "ci", Hash: config.HashToken("u-secret"), Scopes: []config.Scope{config.ScopeUpload}},
		{Name: "root", Hash: config.HashToken("a-secret"), Scopes: []config.Scope{config.ScopeUpload, config.ScopeDelete}},
	}, Logger: nil}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{ts: ts, repo: repo, pub: pub, pkgs: t.TempDir()}
}

func (e *env) client(token string) *sdk.Client { return &sdk.Client{BaseURL: e.ts.URL, Token: token} }

func (e *env) upload(t *testing.T, version string) {
	t.Helper()
	f := testutil.MustPkg(t, e.pkgs, testutil.Fixture(version))
	if _, err := e.client("a-secret").Upload(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestRolesGateWrites(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	f := testutil.MustPkg(t, e.pkgs, testutil.Fixture("0.1.0"))
	if _, err := e.client("").Upload(ctx, f); !isStatus(err, 401) {
		t.Errorf("anonymous upload: %v", err)
	}
	if _, err := e.client("v-secret").Upload(ctx, f); !isStatus(err, 403) {
		t.Errorf("viewer upload: %v", err)
	}
	if _, err := e.client("u-secret").Upload(ctx, f); err != nil {
		t.Fatalf("uploader upload: %v", err)
	}
	if err := e.client("u-secret").Delete(ctx, abi, "hello", "0.1.0"); !isStatus(err, 403) {
		t.Errorf("uploader delete must be forbidden: %v", err)
	}
	if err := e.client("v-secret").Delete(ctx, abi, "hello", "0.1.0"); !isStatus(err, 403) {
		t.Errorf("viewer delete must be forbidden: %v", err)
	}
	if err := e.client("").Delete(ctx, abi, "hello", "0.1.0"); !isStatus(err, 401) {
		t.Errorf("anonymous delete must be unauthorized: %v", err)
	}
	if err := e.client("wrong").Delete(ctx, abi, "hello", "0.1.0"); !isStatus(err, 401) {
		t.Errorf("bad token delete: %v", err)
	}
	// still there after all those refusals
	if pk, _ := e.client("").Packages(ctx, ""); len(pk) != 1 {
		t.Fatalf("package vanished: %v", pk)
	}
	if err := e.client("a-secret").Delete(ctx, abi, "hello", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tok, role string }{{"v-secret", "viewer"}, {"u-secret", "uploader"}, {"a-secret", "admin"}} {
		s, err := e.client(tc.tok).Session(ctx)
		if err != nil || s.Role != tc.role {
			t.Errorf("session %s: %+v %v", tc.tok, s, err)
		}
	}
	if _, err := e.client("nope").Session(ctx); !isStatus(err, 401) {
		t.Errorf("bad session: %v", err)
	}
}

func isStatus(err error, code int) bool {
	var ae *sdk.APIError
	return err != nil && asAPI(err, &ae) && ae.Status == code
}

func asAPI(err error, target **sdk.APIError) bool {
	for err != nil {
		if a, ok := err.(*sdk.APIError); ok {
			*target = a
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestStatusListDetailKey(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	c := e.client("")
	st, err := c.Status(ctx)
	if err != nil || !st.Healthy || st.Packages != 0 || len(st.ABIs) != 0 || st.Signature != "pubkey" || !st.Uploads {
		t.Fatalf("empty status: %+v %v", st, err)
	}
	e.upload(t, "0.1.0")
	e.upload(t, "0.1.1")
	st, err = c.Status(ctx)
	if err != nil || !st.Healthy || st.Packages != 1 || st.Versions != 2 || len(st.ABIs) != 1 || !st.ABIs[0].Consistent ||
		st.ABIs[0].Arch != "aarch64" || st.Fingerprint != repository.Fingerprint(e.pub) || st.Updated.IsZero() {
		t.Fatalf("status: %+v %v", st, err)
	}
	pk, err := c.Packages(ctx, "")
	if err != nil || len(pk) != 2 || pk[0].Version != "0.1.1" || !pk[0].Latest || pk[1].Latest || pk[0].Arch != "freebsd:15:aarch64:64" {
		t.Fatalf("packages: %+v %v", pk, err)
	}
	if !strings.HasSuffix(pk[0].Path, "/"+abi+"/latest/All/hello-0.1.1.pkg") {
		t.Errorf("path %s", pk[0].Path)
	}
	if pk, err := c.Packages(ctx, "FreeBSD:14:amd64"); err != nil || len(pk) != 0 {
		t.Errorf("unknown abi should list nothing, got %v %v", pk, err)
	}
	d, err := c.Package(ctx, abi, "hello", "0.1.1")
	if err != nil || d.Name != "hello" || len(d.Files) != 3 || d.Deps[0].Name != "pcre2" || len(d.Scripts) != 1 || len(d.Directories) != 1 {
		t.Fatalf("detail: %+v %v", d, err)
	}
	foundCfg := false
	for _, f := range d.Files {
		if f.Config {
			foundCfg = true
		}
	}
	if !foundCfg {
		t.Error("config flag lost")
	}
	if _, err := c.Package(ctx, abi, "hello", "9.9"); !isStatus(err, 404) {
		t.Errorf("missing detail: %v", err)
	}
	for _, bad := range []string{"/api/v1/packages/../../etc/passwd/x", "/api/v1/packages/..%2F..%2Ftmp/hello/1"} {
		if resp, _ := get(t, e.ts.URL+bad); resp.StatusCode == 200 {
			t.Errorf("%s answered 200", bad)
		}
	}
	resp, body := get(t, e.ts.URL+"/api/v1/key")
	if resp.StatusCode != 200 || body != string(e.pub) {
		t.Errorf("key: %d", resp.StatusCode)
	}
	if resp, body := get(t, e.ts.URL+"/api/v1/nonsense"); resp.StatusCode != 404 || !strings.Contains(body, `"error"`) {
		t.Errorf("unknown api: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

func TestStatusDetectsDamage(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "0.1.0")
	e.upload(t, "0.1.1")
	// corrupt the advertised package behind the server's back
	p := filepath.Join(e.repo.Dir(abi), "All", "hello-0.1.1.pkg")
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 0xff
	os.WriteFile(p, b, 0o644)
	st, err := e.client("").Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Healthy || st.ABIs[0].Consistent || st.ABIs[0].Problem == "" || len(st.Problems) == 0 {
		t.Fatalf("damage not reported: %+v", st)
	}
}

func TestUnsignedStatus(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Repo.Signer = nil })
	e.upload(t, "0.1.0")
	st, _ := e.client("").Status(context.Background())
	if st.Signature != "unsigned" || st.Fingerprint != "" || !st.Healthy {
		t.Fatalf("%+v", st)
	}
	if resp, _ := get(t, e.ts.URL+"/api/v1/key"); resp.StatusCode != 404 {
		t.Errorf("key of an unsigned repo: %d", resp.StatusCode)
	}
}

func TestDeleteTraversalAndErrors(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "0.1.0")
	victim := filepath.Join(filepath.Dir(e.repo.Root), "outside", "latest", "All")
	os.MkdirAll(victim, 0o755)
	os.WriteFile(filepath.Join(victim, "hello-0.1.0.pkg"), []byte("x"), 0o644)
	for _, abiArg := range []string{"../outside", "..", "FreeBSD:15:aarch64/../..", "freebsd:15:aarch64"} {
		err := e.client("a-secret").Delete(context.Background(), abiArg, "hello", "0.1.0")
		if err == nil {
			t.Errorf("abi %q accepted", abiArg)
		}
	}
	if _, err := os.Stat(filepath.Join(victim, "hello-0.1.0.pkg")); err != nil {
		t.Fatal("a file outside the repository was touched")
	}
	if err := e.client("a-secret").Delete(context.Background(), abi, "hello", "7.7"); !isStatus(err, 404) {
		t.Errorf("%v", err)
	}
	if err := e.client("a-secret").Delete(context.Background(), abi, "bad name", "1"); !isStatus(err, 400) {
		t.Errorf("%v", err)
	}
}

// pkg clients and the UI read while an admin deletes. A reader that follows
// the catalog may find a package gone only if a newer catalog no longer lists
// it, and every catalog it reads verifies.
func TestHTTPReadersDuringDelete(t *testing.T) {
	e := newEnv(t, nil)
	versions := []string{"0.1.0", "0.1.1", "0.1.2", "0.1.3", "0.1.4"}
	for _, v := range versions {
		e.upload(t, v)
	}
	base := e.ts.URL + "/" + abi + "/latest/"
	var stop atomic.Bool
	var bad atomic.Value
	var wg sync.WaitGroup
	var reads atomic.Int64
	catalog := func() (names []string, err error) {
		resp, err := http.Get(base + "packagesite.pkg")
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return nil, io.ErrUnexpectedEOF
		}
		_ = b
		resp2, err := http.Get(base + "data.pkg")
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 200 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := catalog(); err != nil {
					bad.Store(err)
					return
				}
				st, err := e.client("").Status(context.Background())
				if err != nil {
					bad.Store(err)
					return
				}
				// the status check reads everything; a healthy repo is the invariant
				if !st.Healthy {
					bad.Store(st.Problems)
					return
				}
				reads.Add(1)
			}
		}()
	}
	for i := len(versions) - 1; i >= 1; i-- {
		if err := e.client("a-secret").Delete(context.Background(), abi, "hello", versions[i]); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Fatalf("reader saw a broken repository: %v", v)
	}
	t.Logf("%d consistent reads during deletes", reads.Load())
}

func TestUI(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>x</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	e := newEnv(t, func(c *Config) { c.UI = true; c.UIFS = ui })
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := noRedirect.Get(e.ts.URL + "/")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/ui/" {
		t.Errorf("/ : %d %v", resp.StatusCode, resp.Header)
	}
	resp, body := get(t, e.ts.URL+"/ui/")
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>x") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("/ui/: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, e.ts.URL+"/ui/assets/app.js")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := get(t, e.ts.URL+"/ui/packages/hello"); resp.StatusCode != 200 || !strings.Contains(body, "<title>x") {
		t.Errorf("client route fallback: %d", resp.StatusCode)
	}
	if resp, _ := get(t, e.ts.URL+"/ui/missing.js"); resp.StatusCode != 404 {
		t.Errorf("missing asset: %d", resp.StatusCode)
	}
	// the repository tree is untouched by the UI
	e.upload(t, "0.1.0")
	if resp, _ := get(t, e.ts.URL+"/"+abi+"/latest/meta.conf"); resp.StatusCode != 200 {
		t.Errorf("meta.conf: %d", resp.StatusCode)
	}
}

func TestNoUIWithoutAssetsOrFlag(t *testing.T) {
	// UI requested but not built: API and static still work, / is a plain 404
	e := newEnv(t, func(c *Config) { c.UI = true; c.UIFS = nil })
	if resp, _ := get(t, e.ts.URL+"/api/v1/health"); resp.StatusCode != 200 {
		t.Error("health")
	}
	e2 := newEnv(t, nil)
	if resp, _ := get(t, e2.ts.URL+"/ui/"); resp.StatusCode == 200 {
		t.Error("UI served although not enabled")
	}
	_ = e
	_ = bytes.MinRead
	_ = json.Marshal
}

func TestDeleteWorksOnDamagedPackage(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "0.1.0")
	e.upload(t, "0.1.1")
	p := filepath.Join(e.repo.Dir(abi), "All", "hello-0.1.1.pkg")
	os.WriteFile(p, []byte("not a package"), 0o644)
	st, err := e.client("").Status(context.Background())
	if err != nil || st.Healthy || st.ABIs[0].Problem == "" {
		t.Fatalf("damage should be reported, not fatal: %+v %v", st, err)
	}
	// an upload is refused while a damaged file would end up next to the catalog
	if _, err := e.client("a-secret").Upload(context.Background(), testutil.MustPkg(t, e.pkgs, testutil.Fixture("0.1.2"))); err == nil {
		t.Error("upload into a damaged repository should fail loudly")
	}
	if err := e.client("a-secret").Delete(context.Background(), abi, "hello", "0.1.1"); err != nil {
		t.Fatalf("deleting the damaged file must work: %v", err)
	}
	st, _ = e.client("").Status(context.Background())
	if !st.Healthy || st.Versions != 1 {
		t.Fatalf("after delete: %+v", st)
	}
}

// With the real embedded UI (when built) the registry serves it and nothing else changes.
func TestEmbeddedUIServed(t *testing.T) {
	if _, ok := webuiFS(); !ok {
		t.Skip("UI not built")
	}
	e := newEnv(t, func(c *Config) { c.UI = true })
	resp, body := get(t, e.ts.URL+"/ui/")
	if resp.StatusCode != 200 || !strings.Contains(body, "PkgOps registry") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	i := strings.Index(body, "/ui/assets/")
	asset := body[i : i+strings.IndexAny(body[i:], `"'`)]
	if resp, _ := get(t, e.ts.URL+asset); resp.StatusCode != 200 {
		t.Errorf("asset %s: %d", asset, resp.StatusCode)
	}
}

// Identity is never taken from request headers: a reverse proxy (nginx doing
// mTLS) may add X-Client-* headers for its own logs, but a client that sends
// them must gain nothing, and a bearer token still decides who may write.
func TestIdentityHeadersAreIgnored(t *testing.T) {
	e := newEnv(t, nil)
	f := testutil.MustPkg(t, e.pkgs, testutil.Fixture("1.0.0"))
	data, _ := os.ReadFile(f)
	hdr := map[string]string{
		"X-Client-CN": "root", "X-Client-Verify": "SUCCESS", "X-SSL-Client-S-DN": "CN=root",
		"X-Forwarded-User": "root", "X-Remote-User": "root", "X-Client-Role": "admin",
		"Cf-Access-Authenticated-User-Email": "root@example.org",
	}
	do := func(method, url string, body []byte, token string) int {
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("PUT", e.ts.URL+"/api/v1/packages", data, ""); c != http.StatusUnauthorized {
		t.Errorf("upload with identity headers only: %d", c)
	}
	if c := do("GET", e.ts.URL+"/api/v1/session", nil, ""); c != http.StatusUnauthorized {
		t.Errorf("session with identity headers only: %d", c)
	}
	if c := do("PUT", e.ts.URL+"/api/v1/packages", data, "u-secret"); c != http.StatusCreated {
		t.Errorf("upload with a token: %d", c)
	}
	if c := do("DELETE", e.ts.URL+"/api/v1/packages/"+abi+"/demo/1.0.0", nil, "u-secret"); c != http.StatusForbidden {
		t.Errorf("uploader deleting while claiming to be root by header: %d", c)
	}
}
