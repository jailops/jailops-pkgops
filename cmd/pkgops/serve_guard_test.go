package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeRefusesTokensOverPlainHTTPOffLoopback(t *testing.T) {
	for _, tc := range []struct {
		cert, listen string
		tokens       int
		allow        bool
		refuse       bool
	}{
		{"", "0.0.0.0:39112", 1, false, true},
		{"", "192.0.2.10:39112", 1, false, true},
		{"", ":39112", 1, false, true},
		{"", "0.0.0.0:39112", 1, true, false},
		{"", "0.0.0.0:39112", 0, false, false}, // read-only: no secret to protect
		{"c.pem", "0.0.0.0:39112", 1, false, false},
		{"", "127.0.0.1:39112", 1, false, false},
		{"", "[::1]:39112", 1, false, false},
		{"", "localhost:39112", 1, false, false},
		{"", "unix:/run/pkgops.sock", 1, false, false},
	} {
		err := checkPlainHTTP(tc.cert, tc.listen, tc.tokens, tc.allow)
		if (err != nil) != tc.refuse {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestServeRefusesSecretsInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	in := filepath.Join(root, "repo.key")
	os.WriteFile(in, []byte("k"), 0o600)
	link := filepath.Join(outside, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip("symlinks not available")
	}
	for name, f := range map[string]string{"--key": in, "--token-file": filepath.Join(root, "sub", "tokens"), "--tls-key": filepath.Join(link, "repo.key")} {
		err := checkSecretsOutsideRoot(root, map[string]string{name: f})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s %s: %v", name, f, err)
		}
	}
	if err := checkSecretsOutsideRoot(root, map[string]string{"--key": filepath.Join(outside, "repo.key"), "--token-file": ""}); err != nil {
		t.Errorf("a key outside the root: %v", err)
	}
	if err := checkSecretsOutsideRoot("", map[string]string{"--key": in}); err != nil {
		t.Errorf("no root (read-through mode): %v", err)
	}
}
