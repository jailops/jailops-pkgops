package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePruneVia(t *testing.T) {
	repo, branch, file, err := parsePruneVia("github:jailops/jailops-packages@main2:conf/prune.yaml")
	if err != nil || repo != "jailops/jailops-packages" || branch != "main2" || file != "conf/prune.yaml" {
		t.Fatalf("%q %q %q %v", repo, branch, file, err)
	}
	if _, b, _, err := parsePruneVia("github:o/r:prune.yaml"); err != nil || b != "master" {
		t.Errorf("default branch: %q %v", b, err)
	}
	for _, bad := range []string{"", "o/r:prune.yaml", "github:o/r", "gitlab:o/r:x", "github:o:x"} {
		if _, _, _, err := parsePruneVia(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLoadAllow(t *testing.T) {
	f := filepath.Join(t.TempDir(), "allow")
	os.WriteFile(f, []byte("# who\nAlice@Example.com admin\nci.access uploader\n"), 0o600)
	m, err := loadAllow(f)
	if err != nil || len(m) != 2 || m["alice@example.com"] != "admin" {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"", "alice@example.com\n", "alice@example.com root\n", "a b c\n"} {
		os.WriteFile(f, []byte(bad), 0o600)
		if _, err := loadAllow(f); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestServeRefusesHalfConfiguredAccessAndMixedModes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--repo", t.TempDir(), "--access-team", "t"}, "refusing to start half configured"},
		{[]string{"--source", "https://x.example", "--key", "k"}, "read-only"},
		{[]string{"--source", "ftp://x"}, "http(s)"},
		{[]string{"--repo", t.TempDir(), "--workflow", "p.yml"}, "need --prune-via"},
	} {
		err := cmdServe(ctx, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: got %v, want %q", c.args, err, c.want)
		}
	}
}

func TestPruneNeverDeletesWithoutPlan(t *testing.T) {
	err := cmdPrune(context.Background(), []string{"--keep", "1"})
	if err == nil || !strings.Contains(err.Error(), "never deletes") {
		t.Errorf("%v", err)
	}
}

func TestArchivePruneNeedsTypedConfirmation(t *testing.T) {
	err := cmdArchive(context.Background(), []string{"prune", "--repo", "o/r", "--abi", "FreeBSD:15:amd64", "--name", "hello", "--version", "1", "--reason", "x"})
	if err == nil || !strings.Contains(err.Error(), `--confirm "delete from archive"`) {
		t.Errorf("%v", err)
	}
}

func TestFetchAndServeTrustFlags(t *testing.T) {
	ctx := context.Background()
	key := filepath.Join(t.TempDir(), "repo.pub")
	os.WriteFile(key, []byte("not a key"), 0o644)
	cases := map[string][]string{
		"needs --pubkey":   {"--from", "http://127.0.0.1:1", "--pubkey-sha256", "00"},
		"not the expected": {"--from", "http://127.0.0.1:1", "--pubkey", key, "--pubkey-sha256", strings.Repeat("0", 64)},
	}
	for want, args := range cases {
		if err := cmdFetch(ctx, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("fetch %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"--repo", t.TempDir(), "--pubkey", key},
		{"--repo", t.TempDir(), "--require-verified"},
		{"--source", "https://pkg.example.org", "--pubkey", key},
		{"--source", "https://pkg.example.org", "--download-url", "ftp://pkg.example.org"},
	} {
		if err := cmdServe(ctx, args); err == nil {
			t.Errorf("serve %v: accepted", args)
		}
	}
}
