package ghapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/ghapp/ghfake"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writeKey(t *testing.T, path string, k *rsa.PrivateKey, pkcs8 bool, mode os.FileMode) {
	t.Helper()
	var blk *pem.Block
	if pkcs8 {
		b, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		blk = &pem.Block{Type: "PRIVATE KEY", Bytes: b}
	} else {
		blk = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func newClient(t *testing.T) (*ghapp.Client, *ghfake.Fake) {
	t.Helper()
	f := ghfake.New()
	t.Cleanup(f.Close)
	return &ghapp.Client{Owner: "o", Repo: "r", Auth: ghapp.StaticToken("tok"), API: f.URL, UploadAPI: f.URL}, f
}

func TestAppAuthJWTAndCaching(t *testing.T) {
	f := ghfake.New()
	defer f.Close()
	key := newKey(t)
	f.SetApp(42, 7, &key.PublicKey)
	clk := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	f.Now = clk.Now
	kf := filepath.Join(t.TempDir(), "app.pem")
	writeKey(t, kf, key, true, 0o600)

	a, err := ghapp.NewAppAuth(ghapp.AppConfig{AppID: 42, InstallationID: 7, KeyFile: kf, API: f.URL, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t1, err := a.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.TokenRequests()
	if len(reqs) != 1 || reqs[0].Err != "" {
		t.Fatalf("token requests: %+v", reqs)
	}
	r := reqs[0]
	if r.Iss != "42" || r.Iat != clk.Now().Add(-60*time.Second).Unix() || r.Exp != clk.Now().Add(9*time.Minute).Unix() {
		t.Errorf("claims: %+v", r)
	}
	// Cached while more than 2 minutes remain.
	clk.Add(57 * time.Minute)
	t2, _ := a.Token(ctx)
	if t2 != t1 || len(f.TokenRequests()) != 1 {
		t.Fatalf("expected cached token")
	}
	// Within the 2 minute margin: refreshed.
	clk.Add(2 * time.Minute)
	t3, err := a.Token(ctx)
	if err != nil || t3 == t1 || len(f.TokenRequests()) != 2 {
		t.Fatalf("expected refresh: %v %q %q", err, t3, t1)
	}
	// Works as client auth.
	c := &ghapp.Client{Owner: "o", Repo: "r", Auth: a, API: f.URL}
	f.SeedFile("main", "x", []byte("hi"))
	if b, _, err := c.GetFile(ctx, "x", "main"); err != nil || string(b) != "hi" {
		t.Fatalf("GetFile: %v %q", err, b)
	}
}

func TestAppAuthKeyRereadOnRefresh(t *testing.T) {
	f := ghfake.New()
	defer f.Close()
	k1, k2 := newKey(t), newKey(t)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	f.Now = clk.Now
	kf := filepath.Join(t.TempDir(), "app.pem")
	writeKey(t, kf, k1, false, 0o600)
	f.SetApp(1, 2, &k1.PublicKey)
	a, err := ghapp.NewAppAuth(ghapp.AppConfig{AppID: 1, InstallationID: 2, KeyFile: kf, API: f.URL, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Rotate the key on disk and at "GitHub".
	writeKey(t, kf, k2, false, 0o600)
	f.SetApp(1, 2, &k2.PublicKey)
	clk.Add(2 * time.Hour)
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("rotated key not picked up: %v", err)
	}
	// A key file that becomes world-readable is refused at refresh time.
	_ = os.Chmod(kf, 0o644)
	clk.Add(2 * time.Hour)
	if _, err := a.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("want chmod error, got %v", err)
	}
}

func TestNewAppAuthValidation(t *testing.T) {
	for _, cfg := range []ghapp.AppConfig{
		{InstallationID: 1, KeyFile: "k"}, {AppID: 1, KeyFile: "k"}, {AppID: 1, InstallationID: 1},
	} {
		if _, err := ghapp.NewAppAuth(cfg); err == nil {
			t.Errorf("expected error for %+v", cfg)
		}
	}
}

func TestCheckKeyFile(t *testing.T) {
	dir := t.TempDir()
	k := newKey(t)
	good := filepath.Join(dir, "good.pem")
	writeKey(t, good, k, false, 0o600)
	if err := ghapp.CheckKeyFile(good); err != nil {
		t.Errorf("0600: %v", err)
	}
	p8 := filepath.Join(dir, "p8.pem")
	writeKey(t, p8, k, true, 0o400)
	if err := ghapp.CheckKeyFile(p8); err != nil {
		t.Errorf("pkcs8: %v", err)
	}
	bad := filepath.Join(dir, "bad.pem")
	writeKey(t, bad, k, false, 0o644)
	if err := ghapp.CheckKeyFile(bad); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("0644 should be refused with chmod hint: %v", err)
	}
	grp := filepath.Join(dir, "grp.pem")
	writeKey(t, grp, k, false, 0o640)
	if ghapp.CheckKeyFile(grp) == nil {
		t.Error("0640 should be refused")
	}
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(junk, []byte("not pem"), 0o600)
	if ghapp.CheckKeyFile(junk) == nil {
		t.Error("junk accepted")
	}
	if ghapp.CheckKeyFile(dir) == nil {
		t.Error("directory accepted")
	}
	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(good, link); err == nil {
		if err := ghapp.CheckKeyFile(link); err != nil {
			t.Errorf("symlink: %v", err)
		}
	}
	if err := ghapp.CheckKeyFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing accepted")
	}
}

func TestAppAuthErrorsHideSecrets(t *testing.T) {
	f := ghfake.New()
	defer f.Close()
	k := newKey(t)
	other := newKey(t)
	f.SetApp(1, 1, &other.PublicKey) // wrong key: signature rejected
	kf := filepath.Join(t.TempDir(), "k.pem")
	writeKey(t, kf, k, false, 0o600)
	a, _ := ghapp.NewAppAuth(ghapp.AppConfig{AppID: 1, InstallationID: 1, KeyFile: kf, API: f.URL})
	_, err := a.Token(context.Background())
	var ae *ghapp.APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("want 401 APIError, got %v", err)
	}
	if strings.Contains(err.Error(), "eyJ") {
		t.Errorf("error leaks JWT: %v", err)
	}
}

func TestContentsRoundTripAndConflict(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if _, _, err := c.GetFile(ctx, "repo/a b.txt", "main"); !errors.Is(err, ghapp.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	content := []byte(strings.Repeat("line of text\n", 20)) // multi-line base64
	commit, err := c.PutFile(ctx, "repo/a b.txt", "main", "create", content, "")
	if err != nil || commit == "" {
		t.Fatalf("create: %v", err)
	}
	got, sha, err := c.GetFile(ctx, "repo/a b.txt", "main")
	if err != nil || !bytes.Equal(got, content) || sha == "" {
		t.Fatalf("get: %v", err)
	}
	if _, err := c.PutFile(ctx, "repo/a b.txt", "main", "update", []byte("v2"), sha); err != nil {
		t.Fatal(err)
	}
	// Stale sha: 409.
	_, err = c.PutFile(ctx, "repo/a b.txt", "main", "stale", []byte("v3"), sha)
	if !errors.Is(err, ghapp.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	// Missing sha on existing file: 422.
	if _, err = c.PutFile(ctx, "repo/a b.txt", "main", "nosha", []byte("v3"), ""); !errors.Is(err, ghapp.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	cm := f.Commits()
	if len(cm) != 2 || cm[1].Message != "update" || string(cm[1].Content) != "v2" || cm[0].Path != "repo/a b.txt" {
		t.Errorf("commits: %+v", cm)
	}
}

func TestDispatchAndRuns(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if err := c.DispatchWorkflow(ctx, "build.yml", "main", map[string]string{"pkg": "foo"}); err != nil {
		t.Fatal(err)
	}
	d := f.Dispatches()
	if len(d) != 1 || d[0].Workflow != "build.yml" || d[0].Ref != "main" || d[0].Inputs["pkg"] != "foo" {
		t.Errorf("dispatches: %+v", d)
	}
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	f.SeedRun(ghfake.Run{Workflow: "build.yml", Branch: "main", Status: "completed", Conclusion: "success", Event: "workflow_dispatch", HeadSHA: "abc", CreatedAt: now})
	f.SeedRun(ghfake.Run{Workflow: "build.yml", Branch: "dev", Status: "in_progress", CreatedAt: now})
	f.SeedRun(ghfake.Run{Workflow: "build.yml", Branch: "main", Status: "in_progress", CreatedAt: now.Add(time.Hour)})
	runs, err := c.ListRuns(ctx, "build.yml", "main", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %v %+v", err, runs)
	}
	if runs[0].Status != "in_progress" || runs[0].Conclusion != "" || runs[1].Conclusion != "success" ||
		runs[1].HeadSHA != "abc" || !runs[1].CreatedAt.Equal(now) || runs[1].HeadBranch != "main" {
		t.Errorf("runs: %+v", runs)
	}
	if runs, _ = c.ListRuns(ctx, "build.yml", "main", 1); len(runs) != 1 {
		t.Errorf("limit not honoured: %d", len(runs))
	}
}

func TestReleasesLifecycle(t *testing.T) {
	c, f := newClient(t)
	f.PageSize = 2
	ctx := context.Background()
	if _, err := c.GetReleaseByTag(ctx, "nope"); !errors.Is(err, ghapp.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := c.CreateRelease(ctx, fmt.Sprintf("v%d", i), "n", "b", false); err != nil {
			t.Fatal(err)
		}
	}
	all, err := c.ListReleases(ctx)
	if err != nil || len(all) != 5 || all[4].TagName != "v4" {
		t.Fatalf("list: %v %d", err, len(all))
	}
	rel, err := c.GetReleaseByTag(ctx, "v1")
	if err != nil || rel.TagName != "v1" || rel.ID == 0 {
		t.Fatal(err)
	}
	if err := c.UpdateReleaseBody(ctx, rel.ID, "new body"); err != nil {
		t.Fatal(err)
	}
	if rel, _ = c.GetReleaseByTag(ctx, "v1"); rel.Body != "new body" {
		t.Errorf("body: %q", rel.Body)
	}

	data := []byte("package bytes")
	a, err := c.UploadAsset(ctx, rel, "p.pkg", bytes.NewReader(data), int64(len(data)), "application/x-tar")
	if err != nil || a.Size != int64(len(data)) || !strings.HasPrefix(a.Digest, "sha256:") {
		t.Fatalf("upload: %v %+v", err, a)
	}
	up := f.Uploads()
	if len(up) != 1 || up[0].Name != "p.pkg" || up[0].ContentType != "application/x-tar" || !bytes.Equal(up[0].Data, data) {
		t.Errorf("uploads: %+v", up)
	}
	// Empty asset works too.
	if _, err := c.UploadAsset(ctx, rel, "empty", strings.NewReader(""), 0, ""); err != nil {
		t.Errorf("empty upload: %v", err)
	}
	rel, _ = c.GetReleaseByTag(ctx, "v1")
	if len(rel.Assets) != 2 {
		t.Fatalf("assets: %+v", rel.Assets)
	}
	rc, err := c.DownloadAsset(ctx, rel.Assets[0])
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Errorf("download: %q", got)
	}
	if err := c.DeleteAsset(ctx, rel.Assets[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAsset(ctx, rel.Assets[0].ID); !errors.Is(err, ghapp.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := c.DeleteRelease(ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetReleaseByTag(ctx, "v1"); !errors.Is(err, ghapp.ErrNotFound) {
		t.Errorf("release still there: %v", err)
	}
	if !f.HasTag("v1") {
		t.Error("tag should survive release deletion")
	}
	if err := c.DeleteTag(ctx, "v1"); err != nil || f.HasTag("v1") {
		t.Errorf("DeleteTag: %v", err)
	}
}

func TestDownloadRedirectDoesNotLeakAuthorization(t *testing.T) {
	c, f := newClient(t)
	f.SeedRelease("v1", "n", "b")
	f.SeedAsset("v1", "x", "text/plain", []byte("secret payload"))
	rel, err := c.GetReleaseByTag(context.Background(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.DownloadAsset(context.Background(), rel.Assets[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "secret payload" {
		t.Fatalf("body %q", b)
	}
	auth := f.CDNAuth()
	if len(auth) != 1 || auth[0] != "" {
		t.Errorf("CDN received Authorization: %q", auth)
	}
	// An asset URL on a foreign host is refused outright.
	if _, err := c.DownloadAsset(context.Background(), ghapp.Asset{URL: f.CDN.URL + "/blob/1"}); err == nil {
		t.Error("foreign host accepted")
	}
}

func TestUploadTooLarge(t *testing.T) {
	c, f := newClient(t)
	rel := f.SeedRelease("v1", "n", "b")
	_, err := c.UploadAsset(context.Background(), &ghapp.Release{ID: rel.ID}, "big", strings.NewReader(""), 2<<30+1, "")
	if err == nil || !strings.Contains(err.Error(), "2 GiB") {
		t.Fatalf("want 2 GiB error, got %v", err)
	}
	if len(f.Uploads()) != 0 {
		t.Error("something was uploaded")
	}
}

func TestRateLimitMessage(t *testing.T) {
	c, f := newClient(t)
	reset := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	f.RateLimit(reset)
	_, _, err := c.GetFile(context.Background(), "x", "main")
	var ae *ghapp.APIError
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("want APIError 403, got %v", err)
	}
	if !strings.Contains(ae.Message, "rate limit") || !strings.Contains(ae.Message, "2026-05-06T07:08:09Z") {
		t.Errorf("message: %q", ae.Message)
	}
}

func TestRetryOn503(t *testing.T) {
	c, f := newClient(t)
	f.SeedFile("main", "x", []byte("ok"))
	f.FailNext(503, 1)
	if b, _, err := c.GetFile(context.Background(), "x", "main"); err != nil || string(b) != "ok" {
		t.Fatalf("GET should retry: %v", err)
	}
	f.FailNext(503, 1)
	if _, err := c.PutFile(context.Background(), "y", "main", "m", []byte("1"), ""); err == nil {
		t.Fatal("PUT must not be retried")
	}
	f.FailNext(503, 2)
	var ae *ghapp.APIError
	if _, _, err := c.GetFile(context.Background(), "x", "main"); !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("want 503 after one retry, got %v", err)
	}
}

func TestContextCancelled(t *testing.T) {
	c, _ := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.GetFile(ctx, "x", "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestBadCredentials(t *testing.T) {
	c, f := newClient(t)
	f.AllowToken("other")
	_, _, err := c.GetFile(context.Background(), "x", "main")
	var ae *ghapp.APIError
	if !errors.As(err, &ae) || ae.Status != 401 || strings.Contains(err.Error(), "tok") && strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("got %v", err)
	}
}
