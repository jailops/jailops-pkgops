package sdk_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/server"
	"jailops.net/pkgops/internal/testutil"
	"jailops.net/pkgops/sdk"
)

func TestClientAgainstServer(t *testing.T) {
	repo := &repository.Repository{Root: filepath.Join(t.TempDir(), "repo")}
	s, err := server.New(server.Config{Repo: repo, Tokens: []config.Token{
		{Name: "up", Hash: config.HashToken("u"), Scopes: []config.Scope{config.ScopeUpload}},
		{Name: "adm", Hash: config.HashToken("a"), Scopes: []config.Scope{config.ScopeUpload, config.ScopeDelete}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx := context.Background()
	anon := &sdk.Client{BaseURL: ts.URL}
	up := &sdk.Client{BaseURL: ts.URL, Token: "u"}
	adm := &sdk.Client{BaseURL: ts.URL + "/", Token: "a"}

	if err := anon.Health(ctx); err != nil {
		t.Fatal(err)
	}
	f := testutil.MustPkg(t, t.TempDir(), testutil.Fixture("1.0"))
	if _, err := anon.Upload(ctx, f); !errors.Is(err, sdk.ErrUnauthorized) {
		t.Errorf("anonymous upload: %v", err)
	}
	u, err := up.Upload(ctx, f)
	if err != nil || u.Package.Name != "hello" || !u.Package.Latest {
		t.Fatalf("upload: %+v %v", u, err)
	}
	if _, err := up.Upload(ctx, f); err != nil {
		t.Errorf("identical re-upload should be idempotent: %v", err)
	}
	other := testutil.Fixture("1.0")
	other.Comment = "changed"
	if _, err := up.Upload(ctx, testutil.MustPkg(t, t.TempDir(), other)); !errors.Is(err, sdk.ErrConflict) {
		t.Errorf("conflict: %v", err)
	}
	if _, err := up.Upload(ctx, filepath.Join(t.TempDir(), "missing.pkg")); err == nil {
		t.Error("missing file")
	}
	if err := up.Delete(ctx, "FreeBSD:15:aarch64", "hello", "1.0"); !errors.Is(err, sdk.ErrForbidden) {
		t.Errorf("uploader delete: %v", err)
	}
	pk, err := anon.Packages(ctx, "")
	if err != nil || len(pk) != 1 {
		t.Fatalf("%v %v", pk, err)
	}
	d, err := anon.Package(ctx, pk[0].ABI, "hello", "1.0")
	if err != nil || len(d.Files) == 0 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := anon.Package(ctx, pk[0].ABI, "nope", "1.0"); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("%v", err)
	}
	st, err := anon.Status(ctx)
	if err != nil || !st.Healthy || st.Packages != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	if sess, err := adm.Session(ctx); err != nil || sess.Role != "admin" {
		t.Errorf("%+v %v", sess, err)
	}
	if err := adm.Delete(ctx, pk[0].ABI, "hello", "1.0"); err != nil {
		t.Fatal(err)
	}
	if err := adm.Delete(ctx, pk[0].ABI, "hello", "1.0"); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := (&sdk.Client{BaseURL: "http://127.0.0.1:1"}).Health(ctx); err == nil {
		t.Error("unreachable server must error")
	}
}
