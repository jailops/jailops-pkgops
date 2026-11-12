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

func TestLintCommand(t *testing.T) {
	recipe := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(recipe, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o755)
	}
	write("pkgops.yaml", "name: demo\nversion: \"1.0\"\ncomment: demo\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\n"+
		"services:\n  - {name: demo, source: src/packaging/rc.d/demo}\n")
	write("src/packaging/rc.d/demo", "# PROVIDE: demo\n# REQUIRE: DAEMON\n")
	write("src/packaging/rc.d/demo_revert", "# PROVIDE: demo_revert\n")

	var out strings.Builder
	// a warning only: exit 0 by default, 1 with --fail-on warning
	if err := cmdLint([]string{"--recipe", recipe}, &out); err != nil {
		t.Errorf("a warning must not fail by default: %v", err)
	}
	if !strings.Contains(out.String(), "packaging/rc.d/demo_revert is in the source tree but no recipe installs it") {
		t.Errorf("output: %s", out.String())
	}
	out.Reset()
	if err := cmdLint([]string{"--recipe", recipe, "--fail-on", "warning"}, &out); err != exitCode(1) {
		t.Errorf("--fail-on warning: %v", err)
	}
	out.Reset()
	if err := cmdLint([]string{"--recipe", recipe, "-o", "json"}, &out); err != nil || !strings.Contains(out.String(), `"rule": "packaging-file"`) || !strings.Contains(out.String(), `"warnings": 1`) {
		t.Errorf("json: %v %s", err, out.String())
	}
	// fixed: nothing left to say
	write("pkgops.yaml", "name: demo\nversion: \"1.0\"\ncomment: demo\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\n"+
		"services:\n  - {name: demo, source: src/packaging/rc.d/demo}\n  - {name: demo_revert, source: src/packaging/rc.d/demo_revert}\n")
	out.Reset()
	if err := cmdLint([]string{"--recipe", recipe, "--fail-on", "warning"}, &out); err != nil || !strings.Contains(out.String(), "no findings") {
		t.Errorf("clean: %v %s", err, out.String())
	}
	// an error-level finding and a manifest that does not load both exit 1; bad flags exit 2
	write("pkgops.yaml", "name: demo\nversion: \"1.0-rc1\"\n")
	out.Reset()
	if err := cmdLint([]string{"--recipe", recipe}, &out); err != exitCode(1) || !strings.Contains(out.String(), "[manifest]") {
		t.Errorf("bad manifest: %v %s", err, out.String())
	}
	for _, bad := range [][]string{{"--fail-on", "never"}, {"-o", "yaml"}, {"extra"}, {"--nope"}} {
		out.Reset()
		if err := cmdLint(bad, &out); err != exitCode(2) {
			t.Errorf("%v must be a usage error, got %v", bad, err)
		}
	}
}

// The example recipe is what the README tells people to copy: it must lint clean.
func TestExampleRecipeLintsClean(t *testing.T) {
	var out strings.Builder
	if err := cmdLint([]string{"--recipe", "../../examples/hello", "--fail-on", "warning"}, &out); err != nil {
		t.Errorf("examples/hello: %v\n%s", err, out.String())
	}
}
