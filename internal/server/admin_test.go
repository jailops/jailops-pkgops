package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/admin"
	"jailops.net/pkgops/internal/archive"
	"jailops.net/pkgops/internal/cfaccess"
	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/ghapp/ghfake"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/source"
	"jailops.net/pkgops/internal/testutil"
	"jailops.net/pkgops/sdk"
)

const (
	cfAud  = "aud-1"
	cfTeam = "example"
)

var b64 = base64.RawURLEncoding

// access is a fake Cloudflare Access: a JWKS endpoint and a token signer.
type access struct {
	key *rsa.PrivateKey
	srv *httptest.Server
	v   *cfaccess.Verifier
}

func newAccess(t *testing.T) *access {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := &access{key: k}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "k1", "kty": "RSA", "alg": "RS256", "use": "sig",
			"e": b64.EncodeToString(big.NewInt(int64(k.E)).Bytes()), "n": b64.EncodeToString(k.N.Bytes())}}})
	}))
	t.Cleanup(a.srv.Close)
	a.v, err = cfaccess.New(cfaccess.Config{Team: cfTeam, Audience: cfAud, CertsURL: a.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (a *access) token(t *testing.T, claims map[string]any) string {
	t.Helper()
	c := map[string]any{"iss": "https://" + cfTeam + ".cloudflareaccess.com", "aud": []string{cfAud},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "sub": "u-1"}
	for k, v := range claims {
		c[k] = v
	}
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	cb, _ := json.Marshal(c)
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

type call struct {
	method, path string
	jwt          string
	bearer       string
	header       map[string]string
	body         any
}

func do(t *testing.T, base string, c call) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(c.method, base+c.path, rd)
	if c.jwt != "" {
		req.Header.Set("Cf-Access-Jwt-Assertion", c.jwt)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// stack: a published site (static server over a repository), a read-through
// pkgops serve with Cloudflare Access and a fake GitHub.
type stack struct {
	site  *httptest.Server
	repo  *repository.Repository
	gh    *ghfake.Fake
	acc   *access
	srv   *httptest.Server
	adm   *admin.Service
	tmp   string
	build func(versions ...string)
}

var allow = map[string]config.Role{
	"alice@example.com": config.RoleAdmin, "bob@example.com": config.RoleViewer,
	"carol@example.com": config.RoleUploader, "ci-client.access": config.RoleUploader,
}

func newStack(t *testing.T, withGitHub bool) *stack {
	t.Helper()
	st := &stack{tmp: t.TempDir()}
	st.repo = &repository.Repository{Root: filepath.Join(st.tmp, "site"), Time: testutil.FixedTime}
	st.build = func(versions ...string) {
		for _, v := range versions {
			if _, err := st.repo.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture(v))); err != nil {
				t.Fatal(err)
			}
		}
	}
	st.build("0.1.0", "0.1.1", "0.1.2")
	st.site = httptest.NewServer(http.FileServer(http.Dir(st.repo.Root)))
	t.Cleanup(st.site.Close)
	st.acc = newAccess(t)
	st.adm = &admin.Service{Workflow: "publish.yml"}
	if withGitHub {
		st.gh = ghfake.New()
		t.Cleanup(st.gh.Close)
		st.gh.AllowToken("tkn")
		st.adm.GH = &ghapp.Client{Owner: "o", Repo: "r", Auth: ghapp.StaticToken("tkn"), API: st.gh.URL, UploadAPI: st.gh.URL}
		st.adm.Archive = &archive.Archive{GH: st.adm.GH}
	}
	s, err := New(Config{Source: &source.Source{Base: st.site.URL, TTL: time.Millisecond}, Admin: st.adm, Access: st.acc.v, AccessAllow: allow,
		Tokens: []config.Token{{Name: "ci", Hash: config.HashToken("ci-secret"), Scopes: []config.Scope{config.ScopeUpload}}}})
	if err != nil {
		t.Fatal(err)
	}
	st.srv = httptest.NewServer(s.Handler())
	t.Cleanup(st.srv.Close)
	return st
}

func (st *stack) as(t *testing.T, email string) string {
	return st.acc.token(t, map[string]any{"email": email})
}

func TestAccessAuthentication(t *testing.T) {
	st := newStack(t, false)
	get := func(c call) int { code, _ := do(t, st.srv.URL, c); return code }
	if get(call{method: "GET", path: "/api/v1/health"}) != 200 {
		t.Error("health must stay open for probes")
	}
	if code := get(call{method: "GET", path: "/api/v1/overview"}); code != 401 {
		t.Errorf("no identity: %d", code)
	}
	// the unauthenticated header must never be believed
	if code := get(call{method: "GET", path: "/api/v1/overview", header: map[string]string{"Cf-Access-Authenticated-User-Email": "alice@example.com"}}); code != 401 {
		t.Errorf("plain email header: %d", code)
	}
	if code := get(call{method: "GET", path: "/api/v1/overview", jwt: "a.b.c"}); code != 401 {
		t.Errorf("garbage jwt: %d", code)
	}
	if code := get(call{method: "GET", path: "/api/v1/overview", jwt: st.acc.token(t, map[string]any{"email": "alice@example.com", "aud": []string{"other"}})}); code != 401 {
		t.Errorf("wrong audience: %d", code)
	}
	if code := get(call{method: "GET", path: "/api/v1/overview", jwt: st.as(t, "mallory@example.com")}); code != 403 {
		t.Errorf("unknown identity: %d", code)
	}
	if code := get(call{method: "GET", path: "/api/v1/overview", jwt: st.as(t, "Bob@Example.com")}); code != 200 {
		t.Errorf("viewer (case-insensitive): %d", code)
	}
	svc := st.acc.token(t, map[string]any{"common_name": "ci-client.access", "sub": "svc"})
	code, body := do(t, st.srv.URL, call{method: "GET", path: "/api/v1/session", jwt: svc})
	var sess sdk.Session
	json.Unmarshal(body, &sess)
	if code != 200 || sess.Name != "ci-client.access" || sess.Role != "uploader" || sess.Via != "access" {
		t.Errorf("service token session: %d %+v", code, sess)
	}
	// bearer tokens keep working next to Access (CI, the CLI)
	if code := get(call{method: "GET", path: "/api/v1/session", bearer: "ci-secret"}); code != 200 {
		t.Errorf("bearer: %d", code)
	}
	if code := get(call{method: "GET", path: "/api/v1/session", bearer: "wrong"}); code != 401 {
		t.Errorf("bad bearer: %d", code)
	}
}

func TestNewRefusesAccessWithoutAllowList(t *testing.T) {
	acc := newAccess(t)
	if _, err := New(Config{Source: &source.Source{Base: "http://x"}, Access: acc.v}); err == nil {
		t.Fatal("Access without an allow-list must not start")
	}
	if _, err := New(Config{Source: &source.Source{Base: "http://x"}, Repo: &repository.Repository{Root: t.TempDir()}}); err == nil {
		t.Fatal("Source and Repo together must not start")
	}
}

// Cloudflare authenticates the caller; PkgOps decides what the caller may do.
func TestOnlyAdminsReachGitHubActions(t *testing.T) {
	st := newStack(t, true)
	req := sdk.PruneRequest{Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "0.1.0"}}, Confirm: "prune"}
	endpoints := []call{
		{method: "POST", path: "/api/v1/prune/plan", body: sdk.PrunePlanRequest{Keep: 1}},
		{method: "POST", path: "/api/v1/prune", body: req},
		{method: "POST", path: "/api/v1/prune/cancel", body: map[string]any{"refs": []any{}}},
		{method: "POST", path: "/api/v1/republish", body: struct{}{}},
		{method: "POST", path: "/api/v1/archive/delete", body: sdk.ArchiveDeleteRequest{Refs: []sdk.ArchiveRef{{ABI: abi, Name: "hello", Version: "0.1.0"}}, Reason: "x", Confirm: "delete from archive"}},
	}
	for _, who := range []string{"bob@example.com", "carol@example.com"} {
		for _, e := range endpoints {
			e.jwt = st.as(t, who)
			if code, body := do(t, st.srv.URL, e); code != 403 {
				t.Errorf("%s %s as %s: %d %s", e.method, e.path, who, code, body)
			}
		}
		// an upload-scoped bearer token is not an admin either
	}
	for _, e := range endpoints[:4] {
		e.bearer = "ci-secret"
		if code, _ := do(t, st.srv.URL, e); code != 403 {
			t.Errorf("uploader token on %s: %d", e.path, code)
		}
	}
	if len(st.gh.Commits()) != 0 || len(st.gh.Dispatches()) != 0 || len(st.gh.Uploads()) != 0 {
		t.Fatalf("GitHub was touched on behalf of a non-admin: %d commits, %d dispatches", len(st.gh.Commits()), len(st.gh.Dispatches()))
	}
	// the same call as an admin works
	code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/prune", jwt: st.as(t, "alice@example.com"), body: req})
	if code != 200 {
		t.Fatalf("admin: %d %s", code, body)
	}
	if len(st.gh.Commits()) != 1 {
		t.Errorf("commits: %d", len(st.gh.Commits()))
	}
}

func TestCleanupRequestCommitsTheManifestAndShowsThePhases(t *testing.T) {
	st := newStack(t, true)
	admin1 := st.as(t, "alice@example.com")
	ov := func() *sdk.Overview {
		code, body := do(t, st.srv.URL, call{method: "GET", path: "/api/v1/overview", jwt: admin1})
		if code != 200 {
			t.Fatalf("overview %d %s", code, body)
		}
		var o sdk.Overview
		if err := json.Unmarshal(body, &o); err != nil {
			t.Fatal(err)
		}
		return &o
	}
	o := ov()
	if o.Mode != "source" || len(o.Versions) != 3 || o.Prune.Mode != "github" || o.Prune.Repo != "o/r" || o.Identity == nil || o.Identity.Name != "alice@example.com" {
		t.Fatalf("overview %+v", o)
	}
	if o.Bytes == 0 || len(o.ABIs) != 1 || o.ABIs[0].Versions != 3 || o.Status == nil {
		t.Errorf("totals: %+v", o.ABIs)
	}
	newest := 0
	for _, v := range o.Versions {
		if v.Newest {
			newest++
		}
		if v.State != "active" || v.Archive == "" {
			t.Errorf("version %+v", v)
		}
	}
	if newest != 1 {
		t.Errorf("newest flags: %d", newest)
	}

	req := sdk.PruneRequest{Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "0.1.0"}}, Reason: "superseded", Confirm: "prune"}
	pr := func(r sdk.PruneRequest) (int, sdk.PruneResult, string) {
		code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/prune", jwt: admin1, body: r})
		var res sdk.PruneResult
		json.Unmarshal(body, &res)
		return code, res, string(body)
	}
	// the typed confirmation is mandatory
	noConfirm := req
	noConfirm.Confirm = ""
	if code, _, _ := pr(noConfirm); code != 400 {
		t.Errorf("no confirmation: %d", code)
	}
	// dry run changes nothing
	dry := req
	dry.DryRun = true
	code, res, body := pr(dry)
	if code != 200 || !res.DryRun || res.Commit != "" || len(st.gh.Commits()) != 0 || !strings.Contains(res.Diff, "version: \"0.1.0\"") {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if !strings.Contains(res.Effect, "next deploy") || res.Bytes == 0 {
		t.Errorf("effect/bytes: %+v", res)
	}
	// the real request commits prune.yaml with the identity in the message
	code, res, body = pr(req)
	if code != 200 || res.Mode != "github" || res.Commit == "" || len(st.gh.Commits()) != 1 {
		t.Fatalf("request: %d %s", code, body)
	}
	c := st.gh.Commits()[0]
	if c.Path != "prune.yaml" || c.Branch != "master" || c.Message != "Prune: remove hello 0.1.0 (FreeBSD:15:aarch64) requested by alice@example.com\n\nsuperseded" {
		t.Errorf("commit: %+v", c)
	}
	m, err := prune.Parse(c.Content)
	if err != nil || len(m.Entries) != 1 || m.Entries[0].RequestedBy != "alice@example.com" || m.Entries[0].Reason != "superseded" {
		t.Errorf("manifest: %v %+v", err, m)
	}
	// asking again is idempotent: skipped, no second commit
	code, res, _ = pr(req)
	if code != 200 || len(res.Skipped) != 1 || len(res.Entries) != 0 || len(st.gh.Commits()) != 1 {
		t.Errorf("again: %d %+v", code, res)
	}
	// requested -> committed: the file is still served until the next deploy
	o = ov()
	for _, v := range o.Versions {
		if v.Version == "0.1.0" && (v.State != "pending-prune" || v.Phase != "committed") {
			t.Errorf("pending: %+v", v)
		}
	}
	// a running workflow makes the phase "workflow-running"
	st.gh.SeedRun(ghfake.Run{Workflow: "publish.yml", Branch: "master", Status: "in_progress", CreatedAt: time.Now()})
	o = ov()
	if !o.Publish.Available || len(o.Publish.Runs) != 1 {
		t.Fatalf("publish: %+v", o.Publish)
	}
	for _, v := range o.Versions {
		if v.Version == "0.1.0" && v.Phase != "workflow-running" {
			t.Errorf("phase: %+v", v)
		}
	}
	// the deploy happens: the site is rebuilt with the manifest; the index confirms it
	st.repo.PruneManifest = m
	st.build("0.1.3")
	o = ov()
	pruned := false
	for _, v := range o.Versions {
		if v.Version == "0.1.0" {
			pruned = v.State == "pruned" && v.Phase == "published" && v.Reason == "superseded"
		}
	}
	if !pruned || o.ABIs[0].Pruned != 1 {
		t.Errorf("after deploy: %+v", o.Versions)
	}
	// the newest version is refused unless forced
	nw := sdk.PruneRequest{Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "0.1.3"}}, Confirm: "prune"}
	if code, _, body := pr(nw); code != 422 || !strings.Contains(body, "newest") {
		t.Errorf("newest: %d %s", code, body)
	}
	nw.Entries[0].Force = true
	if code, _, body := pr(nw); code != 200 {
		t.Errorf("forced newest: %d %s", code, body)
	}
	// unknown files
	if code, _, _ := pr(sdk.PruneRequest{Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "9"}}, Confirm: "prune"}); code != 404 {
		t.Errorf("unknown: %d", code)
	}
}

func TestPlanKeepNewestPreviewAndRepublish(t *testing.T) {
	st := newStack(t, true)
	jwt := st.as(t, "alice@example.com")
	code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/prune/plan", jwt: jwt, body: sdk.PrunePlanRequest{ABI: abi, Keep: 1}})
	var res sdk.PruneResult
	json.Unmarshal(body, &res)
	if code != 200 || len(res.Entries) != 2 || res.Entries[0].Version != "0.1.0" || len(st.gh.Commits()) != 0 {
		t.Fatalf("plan: %d %s", code, body)
	}
	code, body = do(t, st.srv.URL, call{method: "POST", path: "/api/v1/republish", jwt: jwt, body: struct{}{}})
	if code != 202 || len(st.gh.Dispatches()) != 1 || st.gh.Dispatches()[0].Workflow != "publish.yml" || st.gh.Dispatches()[0].Ref != "master" {
		t.Fatalf("republish: %d %s %+v", code, body, st.gh.Dispatches())
	}
}

func TestManualModeShowsTheStepsAndSourceModeIsReadOnly(t *testing.T) {
	st := newStack(t, false)
	jwt := st.as(t, "alice@example.com")
	req := sdk.PruneRequest{Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "0.1.0"}, {ABI: abi, Name: "hello", Version: "0.1.1"}}, Confirm: "prune"}
	code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/prune", jwt: jwt, body: req})
	var res sdk.PruneResult
	json.Unmarshal(body, &res)
	if code != 200 || res.Mode != "manual" || res.Manual == nil || res.Commit != "" {
		t.Fatalf("manual: %d %s", code, body)
	}
	if !strings.Contains(res.Manual.Content, "0.1.0") || !strings.Contains(res.Manual.Diff, "+  - abi: "+abi) || len(res.Manual.Commands) < 2 {
		t.Errorf("manual steps: %+v", res.Manual)
	}
	code, _ = do(t, st.srv.URL, call{method: "POST", path: "/api/v1/republish", jwt: jwt, body: struct{}{}})
	if code != 400 {
		t.Errorf("republish without a workflow: %d", code)
	}
	code, _ = do(t, st.srv.URL, call{method: "DELETE", path: "/api/v1/packages/" + abi + "/hello/0.1.0", jwt: jwt})
	if code != 405 {
		t.Errorf("delete in source mode: %d", code)
	}
	code, _ = do(t, st.srv.URL, call{method: "PUT", path: "/api/v1/packages", jwt: jwt})
	if code != 405 {
		t.Errorf("upload in source mode: %d", code)
	}
	// the read API works against the site
	c := &sdk.Client{BaseURL: st.srv.URL, Token: "ci-secret"}
	pk, err := c.Packages(context.Background(), abi)
	if err != nil || len(pk) != 3 || !pk[0].Latest {
		t.Errorf("packages: %v %v", pk, err)
	}
	stt, err := c.Status(context.Background())
	if err != nil || stt.Versions != 3 || len(stt.ABIs) != 1 {
		t.Errorf("status: %+v %v", stt, err)
	}
}

func TestArchiveStatesAndSeparateDeletion(t *testing.T) {
	st := newStack(t, true)
	ctx := context.Background()
	ix, _ := st.repo.Index()
	if _, err := st.adm.Archive.Push(ctx, st.repo.Root, ix); err != nil {
		t.Fatal(err)
	}
	jwt := st.as(t, "alice@example.com")
	_, body := do(t, st.srv.URL, call{method: "GET", path: "/api/v1/overview", jwt: jwt})
	var o sdk.Overview
	json.Unmarshal(body, &o)
	if !o.Archive.Available || o.Archive.Files != 3 || o.Archive.Report == nil || !o.Archive.Report.OK {
		t.Fatalf("archive: %+v", o.Archive)
	}
	for _, v := range o.Versions {
		if v.Archive != "archived" || v.ArchiveURL == "" {
			t.Errorf("version %+v", v)
		}
	}
	del := sdk.ArchiveDeleteRequest{Refs: []sdk.ArchiveRef{{ABI: abi, Name: "hello", Version: "0.1.0"}}, Reason: "licence problem", Confirm: "delete from archive"}
	bad := del
	bad.Confirm = "yes"
	if code, _ := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/archive/delete", jwt: jwt, body: bad}); code != 400 {
		t.Errorf("wrong confirmation: %d", code)
	}
	noReason := del
	noReason.Reason = ""
	if code, _ := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/archive/delete", jwt: jwt, body: noReason}); code != 400 {
		t.Errorf("no reason: %d", code)
	}
	if code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/archive/delete", jwt: jwt, body: del}); code != 204 {
		t.Fatalf("delete: %d %s", code, body)
	}
	recs, _ := st.adm.Archive.List(ctx)
	if len(recs) != 2 {
		t.Errorf("archive after delete: %d", len(recs))
	}
	// the Pages site still has the file: now the report says so
	_, body = do(t, st.srv.URL, call{method: "GET", path: "/api/v1/overview", jwt: jwt})
	json.Unmarshal(body, &o)
	if o.Archive.Report.OK || len(o.Archive.Report.OnPagesNotArchived) != 1 {
		t.Errorf("report: %+v", o.Archive.Report)
	}
	// a Pages prune never touches the archive
	before := len(st.gh.Releases())
	if code, body := do(t, st.srv.URL, call{method: "POST", path: "/api/v1/prune", jwt: jwt, body: sdk.PruneRequest{
		Entries: []sdk.PruneEntry{{ABI: abi, Name: "hello", Version: "0.1.1"}}, Confirm: "prune"}}); code != 200 {
		t.Fatalf("prune: %d %s", code, body)
	}
	if len(st.gh.Releases()) != before {
		t.Error("a Pages prune changed the archive")
	}
}
