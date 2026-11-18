package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/testutil"
)

func TestServable(t *testing.T) {
	yes := []string{
		"pkgops-index.json", "repo.pub", "pkgops.pub",
		"FreeBSD:15:aarch64/latest/meta", "FreeBSD:15:aarch64/latest/meta.conf", "FreeBSD:15:amd64/latest/packagesite.pkg",
		"FreeBSD:15:amd64/quarterly/data.pkg", "FreeBSD:15:amd64/latest/files.pkg",
		"FreeBSD:15:aarch64/latest/All/hello-0.1.0.pkg", "FreeBSD:15:aarch64/latest/All/hello-0.1.0_1,1.pkg",
	}
	no := []string{
		"", "repo.key", "repo.pem", "tokens", "tokens.txt", "token", ".env", ".git/config", "id_rsa", "pkgops-repo.yaml", "prune.yaml",
		"backup.tar.gz", "key.pub/x", "FreeBSD:15:aarch64", "FreeBSD:15:aarch64/latest", "FreeBSD:15:aarch64/latest/All",
		"FreeBSD:15:aarch64/latest/repo.key", "FreeBSD:15:aarch64/latest/.stage-1/data.pkg", "FreeBSD:15:aarch64/latest/All/.tmp-1.pkg",
		"FreeBSD:15:aarch64/latest/All/notes.txt", "FreeBSD:15:aarch64/latest/All/../../repo.key", "FreeBSD:15:aarch64/latest/All/a/b.pkg",
		"FreeBSD:15/latest/meta", "FreeBSD:15:arm64/latest/meta", "Linux:6:amd64/latest/meta", "FreeBSD:15:aarch64/.hidden/meta",
		"FreeBSD:15:aarch64/latest/secrets/meta.conf", "a\\b", "FreeBSD:15:aarch64/latest/meta\x00",
	}
	for _, p := range yes {
		if !servable(p) {
			t.Errorf("%q must be served", p)
		}
	}
	for _, p := range no {
		if servable(p) {
			t.Errorf("%q must not be served", p)
		}
	}
}

// A key, a token file and a backup that sit in the repository root are not
// downloadable, while the repository itself is.
func TestStaticServesOnlyDistributionFiles(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "0.1.0")
	secrets := map[string]string{
		"repo.key": "PRIVATE KEY", "tokens.txt": "ci upload 00ff", "backup.tar": "data", "notes.pem": "x",
		"FreeBSD:15:aarch64/latest/repo.key": "PRIVATE KEY",
	}
	for name, body := range secrets {
		if err := os.WriteFile(filepath.Join(e.repo.Root, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name := range secrets {
		resp, body := get(t, e.ts.URL+"/"+name)
		if resp.StatusCode != 404 || strings.Contains(body, "PRIVATE") || strings.Contains(body, "00ff") {
			t.Errorf("%s: status %d, body %q", name, resp.StatusCode, body)
		}
	}
	for _, name := range []string{"pkgops-index.json", abi + "/latest/meta.conf", abi + "/latest/packagesite.pkg", abi + "/latest/All/hello-0.1.0.pkg"} {
		if resp, _ := get(t, e.ts.URL+"/"+name); resp.StatusCode != 200 {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
}

// Two uploads at a time: a third is told to come back, and the first two are unharmed.
func TestUploadConcurrencyCap(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxUploads = 2 })
	hold := make(chan struct{})
	started := make(chan struct{}, 2)
	put := func(version string) (*http.Response, error) {
		f := testutil.MustPkg(t, t.TempDir(), testutil.Fixture(version))
		fi, _ := os.Stat(f)
		pr, pw := io.Pipe()
		go func() {
			b, _ := os.ReadFile(f)
			pw.Write(b[:1])
			started <- struct{}{}
			<-hold
			pw.Write(b[1:])
			pw.Close()
		}()
		req, _ := http.NewRequest("PUT", e.ts.URL+"/api/v1/packages", pr)
		req.ContentLength = fi.Size()
		req.Header.Set("Authorization", "Bearer a-secret")
		return http.DefaultClient.Do(req)
	}
	done := make(chan int, 2)
	for _, v := range []string{"0.1.0", "0.1.1"} {
		go func() {
			resp, err := put(v)
			if err != nil {
				done <- -1
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
	}
	<-started
	<-started
	third := testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.2"))
	_, err := e.client("a-secret").Upload(context.Background(), third)
	if !isStatus(err, 503) {
		t.Errorf("a third concurrent upload must be refused with 503: %v", err)
	}
	close(hold)
	for i := 0; i < 2; i++ {
		if code := <-done; code != 201 {
			t.Errorf("a held upload ended with %d", code)
		}
	}
	// the slots are free again
	if _, err := e.client("a-secret").Upload(context.Background(), third); err != nil {
		t.Errorf("after the others finished: %v", err)
	}
}

func TestUploadBudget(t *testing.T) {
	f := testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.0"))
	fi, _ := os.Stat(f)
	e := newEnv(t, func(c *Config) { c.UploadBudget = fi.Size() - 1 })
	if _, err := e.client("a-secret").Upload(context.Background(), f); !isStatus(err, 503) {
		t.Errorf("an upload larger than the whole budget must be refused with 503: %v", err)
	}
	e2 := newEnv(t, func(c *Config) { c.MaxUpload = fi.Size() - 1 })
	if _, err := e2.client("a-secret").Upload(context.Background(), f); !isStatus(err, 413) {
		t.Errorf("an upload larger than MaxUpload must be refused with 413 before it is read: %v", err)
	}
}

// A client that stops sending is cut off by the upload timeout.
func TestUploadTimeoutCutsOffAStalledClient(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.UploadTimeout = 300 * time.Millisecond })
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write([]byte("partial"))
	req, _ := http.NewRequest("PUT", e.ts.URL+"/api/v1/packages", pr)
	req.ContentLength = 1000
	req.Header.Set("Authorization", "Bearer a-secret")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 201 {
			t.Fatal("a stalled upload cannot succeed")
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the stalled upload was not cut off in time: %v", d)
	}
}
