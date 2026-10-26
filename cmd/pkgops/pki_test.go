package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pkiRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var o, e bytes.Buffer
	err := runPKI(&o, &e, args)
	return o.String(), e.String(), err
}

func TestPKICommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	if _, _, err := pkiRun(t, "init", "--dir", dir, "--name", "Test CA"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pkiRun(t, "init", "--dir", dir); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second init: %v", err)
	}
	if _, _, err := pkiRun(t, "issue", "server", "--dir", dir, "--name", "pkg.lan,10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	out, _, err := pkiRun(t, "issue", "client", "--dir", dir, "--name", "web1")
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "clients", "web1.key")
	pemKey, _ := os.ReadFile(key)
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o400 {
		t.Errorf("key mode %v", st.Mode())
	}
	if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, string(pemKey[30:60])) {
		t.Error("issue printed key material")
	}
	if _, _, err := pkiRun(t, "issue", "client", "--dir", dir, "--name", "web1"); err == nil {
		t.Error("overwrote a client certificate")
	}
	// show: certificate details, never the key
	show, _, err := pkiRun(t, "show", filepath.Join(dir, "clients", "web1.crt"))
	if err != nil || !strings.Contains(show, "clientAuth") || !strings.Contains(show, "CN=web1") {
		t.Errorf("show cert: %v\n%s", err, show)
	}
	show, _, err = pkiRun(t, "show", key)
	if err != nil || !strings.Contains(show, "not shown") || strings.Contains(show, string(pemKey[30:60])) {
		t.Errorf("show key: %v\n%s", err, show)
	}
	show, _, _ = pkiRun(t, "show", filepath.Join(dir, "servers", "pkg.lan.crt"))
	if !strings.Contains(show, "serverAuth") || !strings.Contains(show, "10.0.0.5") {
		t.Errorf("show server:\n%s", show)
	}
	// revoke + list + crl
	out, _, err = pkiRun(t, "revoke", "--dir", dir, "--name", "web1")
	if err != nil || !strings.Contains(out, "revoked client \"web1\" serial 3") {
		t.Fatalf("revoke: %v\n%s", err, out)
	}
	if _, _, err := pkiRun(t, "revoke", "--dir", dir, "--name", "web1", "--serial", "3"); err == nil {
		t.Error("--name and --serial together accepted")
	}
	list, _, _ := pkiRun(t, "list", "--dir", dir)
	if !strings.Contains(list, "revoked") || !strings.Contains(list, "web1") || !strings.Contains(list, "pkg.lan") {
		t.Errorf("list:\n%s", list)
	}
	show, _, _ = pkiRun(t, "show", filepath.Join(dir, "crl.pem"))
	if !strings.Contains(show, "serial 3") {
		t.Errorf("crl:\n%s", show)
	}
	if _, _, err := pkiRun(t, "crl", "--dir", dir, "--check"); err != nil {
		t.Errorf("check on a fresh CRL: %v", err)
	}
	_, warn, err := pkiRun(t, "crl", "--dir", dir, "--days", "3")
	if err != nil || !strings.Contains(warn, "warning") {
		t.Errorf("short CRL must warn: %v %q", err, warn)
	}
	if _, _, err := pkiRun(t, "crl", "--dir", dir, "--check"); err == nil {
		t.Error("--check must fail for a CRL near expiry")
	}
	if _, _, err := pkiRun(t, "bogus"); err == nil {
		t.Error("unknown subcommand accepted")
	}
}

func TestCheckTransport(t *testing.T) {
	cases := []struct {
		cert, key, ca, crl string
		want               string
	}{
		{"", "", "ca.pem", "", "--client-ca requires"},
		{"c", "", "", "", "together"},
		{"", "", "", "crl.pem", "--crl needs"},
		{"c", "k", "ca.pem", "crl.pem", ""},
		{"c", "k", "", "", ""},
		{"", "", "", "", ""},
	}
	for _, c := range cases {
		err := checkTransport("0.0.0.0:443", c.cert, c.key, c.ca, c.crl)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, "unix:/x": true, ":8080": false, "0.0.0.0:1": false, "10.0.0.1:1": false} {
		if isLoopbackListen(addr) != want {
			t.Errorf("isLoopbackListen(%s) != %v", addr, want)
		}
	}
}

func TestListenUnix(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := listenOn("unix:"+sock, "0660")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(sock); st.Mode().Perm() != 0o660 {
		t.Errorf("socket mode %v", st.Mode())
	}
	if _, err := listenOn("unix:"+sock, "0660"); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("live socket reused: %v", err)
	}
	ln.Close()
	// stale socket file is replaced; mode 0600 honoured
	ua, _ := net.ResolveUnixAddr("unix", sock)
	stale, err := net.ListenUnix("unix", ua)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close() // leaves a stale socket file behind
	ln, err = listenOn("unix:"+sock, "0600")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if st, _ := os.Stat(sock); st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", st.Mode())
	}
	plain := filepath.Join(t.TempDir(), "file")
	os.WriteFile(plain, nil, 0o600)
	if _, err := listenOn("unix:"+plain, "0660"); err == nil {
		t.Error("replaced a regular file")
	}
	if _, err := listenOn("unix:"+sock+"2", "9"); err == nil {
		t.Error("bad socket mode accepted")
	}
}

func TestConfGolden(t *testing.T) {
	var out, errw bytes.Buffer
	err := writeConf(&out, &errw, "private", "https://pkg.example.org/${ABI}/latest",
		"/usr/local/etc/pkg/keys/private.pub", "",
		"/usr/local/etc/pkg/mtls/client.crt", "/usr/local/etc/pkg/mtls/client.key", "/usr/local/etc/pkg/mtls/ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/conf_mtls.golden")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	// without mTLS flags the output is unchanged (no env block)
	out.Reset()
	writeConf(&out, &errw, "r", "https://x/${ABI}/latest", "/k.pub", "", "", "", "")
	if strings.Contains(out.String(), "env") {
		t.Errorf("unexpected env block:\n%s", out.String())
	}
	if err := writeConf(&out, &errw, "r", "u", "/k", "", "/c", "", ""); err == nil {
		t.Error("cert without key accepted")
	}
}
