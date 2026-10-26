package publish

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

func TestPublishLocal(t *testing.T) {
	tmp := t.TempDir()
	r := &repository.Repository{Root: filepath.Join(tmp, "site"), Time: testutil.FixedTime}
	r.Update(context.Background(), testutil.MustPkg(t, tmp, testutil.Fixture("0.1.0")))
	dest := filepath.Join(tmp, "www")
	var log bytes.Buffer
	if err := Publish(context.Background(), r.Root, dest, Options{Log: &log}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(log.String())
	// packages are copied before any catalog file
	if len(lines) < 4 || !strings.HasSuffix(lines[0], "hello-0.1.0.pkg") || !strings.HasSuffix(lines[len(lines)-1], "meta.conf") {
		t.Errorf("order: %v", lines)
	}
	log.Reset()
	if err := Publish(context.Background(), r.Root, dest, Options{Log: &log}); err != nil || log.Len() != 0 {
		t.Errorf("second publish should transfer nothing: %q %v", log.String(), err)
	}
	// stale file removal only with Delete
	stale := filepath.Join(dest, "FreeBSD:15:aarch64", "latest", "All", "old-1.pkg")
	os.WriteFile(stale, []byte("x"), 0o644)
	Publish(context.Background(), r.Root, dest, Options{})
	if _, err := os.Stat(stale); err != nil {
		t.Error("stale file removed without Delete")
	}
	Publish(context.Background(), r.Root, dest, Options{Delete: true})
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale file kept with Delete")
	}
	if _, err := repository.VerifyRepository(filepath.Join(dest, "FreeBSD:15:aarch64", "latest"), nil); err != nil {
		t.Error(err)
	}
	if err := Publish(context.Background(), filepath.Join(tmp, "nope"), dest, Options{}); !errors.Is(err, pkgerr.ErrNotFound) {
		t.Error(err)
	}
}

func TestIsRemote(t *testing.T) {
	for d, want := range map[string]bool{
		"user@host:/srv/pkg": true, "host:path": true, "/srv/x": false, "./x:y": false, "relative/dir": false,
		"/srv/FreeBSD:15:amd64": false,
	} {
		if IsRemote(d) != want {
			t.Errorf("IsRemote(%q) = %v", d, !want)
		}
	}
}
