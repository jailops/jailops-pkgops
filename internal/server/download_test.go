package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jailops.net/pkgops/internal/source"
	"jailops.net/pkgops/sdk"
)

// statusVia asks h for the status as a browser on host would.
func statusVia(t *testing.T, h http.Handler, host string) sdk.Status {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v1/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	var st sdk.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// In read-through mode the admin server does not serve the repository: the
// client configuration must point at the published site, not at the admin
// host the UI was loaded from.
func TestSourceModeAdvertisesThePublishedSite(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "1.0.0")
	os.WriteFile(filepath.Join(e.repo.Root, "pkgops.pub"), e.pub, 0o644)
	site := httptest.NewServer(http.FileServer(http.Dir(e.repo.Root)))
	defer site.Close()

	s, err := New(Config{Source: &source.Source{Base: site.URL + "/"}})
	if err != nil {
		t.Fatal(err)
	}
	st := statusVia(t, s.Handler(), "admin.example.org")
	if st.Mode != "source" || st.ServesRepository || st.DownloadURL != site.URL || st.KeyURL != site.URL+"/pkgops.pub" {
		t.Errorf("source default: mode=%s serves=%v download=%q key=%q", st.Mode, st.ServesRepository, st.DownloadURL, st.KeyURL)
	}
	// the admin server has no static route: the advertised URL is the only right one
	req := httptest.NewRequest(http.MethodGet, "http://admin.example.org/"+abi+"/latest/meta.conf", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the admin server answered %d for a repository file", rec.Code)
	}
	if resp, _ := get(t, st.DownloadURL+"/"+abi+"/latest/meta.conf"); resp.StatusCode != 200 {
		t.Errorf("download url: %d", resp.StatusCode)
	}

	s, _ = New(Config{Source: &source.Source{Base: site.URL}, DownloadURL: "https://pkg.example.org/", KeyURL: "https://pkg.example.org/repo.pub"})
	st = statusVia(t, s.Handler(), "admin.example.org")
	if st.DownloadURL != "https://pkg.example.org" || st.KeyURL != "https://pkg.example.org/repo.pub" {
		t.Errorf("configured: %q %q", st.DownloadURL, st.KeyURL)
	}
}

func TestLocalModeServesTheRepositoryItself(t *testing.T) {
	e := newEnv(t, nil)
	e.upload(t, "1.0.0")
	resp, body := get(t, e.ts.URL+"/api/v1/status")
	var st sdk.Status
	json.Unmarshal([]byte(body), &st)
	if resp.StatusCode != 200 || st.Mode != "local" || !st.ServesRepository || st.DownloadURL != "" || st.KeyURL != "/api/v1/key" ||
		st.Verification != "verified" || st.KeySource != "signer" {
		t.Errorf("local: %+v", st)
	}
	e2 := newEnv(t, func(c *Config) { c.DownloadURL = "https://pkg.example.org" })
	_, body = get(t, e2.ts.URL+"/api/v1/status")
	json.Unmarshal([]byte(body), &st)
	if st.DownloadURL != "https://pkg.example.org" {
		t.Errorf("configured local: %q", st.DownloadURL)
	}
}
