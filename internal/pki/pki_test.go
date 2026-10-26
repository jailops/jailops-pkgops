package pki

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestInitModesAndRefuseOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	ca, err := Init(dir, InitOptions{Name: "my CA"})
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Join(dir, CAKeyFile)); m != 0o400 {
		t.Errorf("ca.key mode %o", m)
	}
	if m := mode(t, filepath.Join(dir, StateFile)); m != 0o600 {
		t.Errorf("state mode %o", m)
	}
	if !ca.Cert.IsCA || ca.Cert.Subject.CommonName != "my CA" || ca.Cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Errorf("bad CA cert: %+v", ca.Cert)
	}
	if _, ok := ca.key.(*ecdsa.PrivateKey); !ok {
		t.Errorf("default key is %T", ca.key)
	}
	before, _ := os.ReadFile(filepath.Join(dir, CAFile))
	if _, err := Init(dir, InitOptions{}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second init: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, CAFile))
	if string(before) != string(after) {
		t.Error("CA certificate was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, CRLFile)); err != nil {
		t.Error("init must write an initial CRL")
	}
}

func TestRSACA(t *testing.T) {
	ca, err := Init(t.TempDir(), InitOptions{KeyType: "rsa", RSABits: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ca.key.(*rsa.PrivateKey); !ok {
		t.Fatalf("%T", ca.key)
	}
	if _, err := Init(t.TempDir(), InitOptions{KeyType: "rsa", RSABits: 1024}); err == nil {
		t.Error("1024-bit RSA accepted")
	}
	if _, err := Init(t.TempDir(), InitOptions{KeyType: "dsa"}); err == nil {
		t.Error("unknown key type accepted")
	}
}

func TestIssueChainEKUModes(t *testing.T) {
	dir := t.TempDir()
	ca, _ := Init(dir, InitOptions{})
	s, err := ca.Issue(IssueOptions{Kind: KindServer, Name: "pkg.example.org", SANs: []string{"pkg", "10.1.2.3"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ca.Issue(IssueOptions{Kind: KindClient, Name: "web1", Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	if s.Entry.Serial != 2 || c.Entry.Serial != 3 {
		t.Errorf("serials %d %d", s.Entry.Serial, c.Entry.Serial)
	}
	for _, f := range []string{s.KeyFile, c.KeyFile} {
		if m := mode(t, f); m != 0o400 {
			t.Errorf("%s mode %o", f, m)
		}
	}
	read := func(f string) *x509.Certificate {
		d, _ := os.ReadFile(f)
		x, err := ParseCertPEM(d)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	sc, cc := read(s.CertFile), read(c.CertFile)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := sc.Verify(x509.VerifyOptions{Roots: pool, DNSName: "pkg.example.org", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("server chain: %v", err)
	}
	if _, err := sc.Verify(x509.VerifyOptions{Roots: pool, DNSName: "pkg", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("SAN pkg: %v", err)
	}
	if _, err := sc.Verify(x509.VerifyOptions{Roots: pool, DNSName: "10.1.2.3", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("IP SAN: %v", err)
	}
	if _, err := sc.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Error("server certificate accepted for client auth")
	}
	if _, err := cc.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("client chain: %v", err)
	}
	if _, err := cc.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Error("client certificate accepted for server auth")
	}
	if cc.Subject.CommonName != "web1" || cc.IsCA {
		t.Errorf("client cert: %v", cc.Subject)
	}
	if got := cc.NotAfter.Sub(cc.NotBefore); got < 29*24*time.Hour || got > 31*24*time.Hour {
		t.Errorf("validity %s", got)
	}
	// other CA must not verify it
	other, _ := Init(t.TempDir(), InitOptions{})
	op := x509.NewCertPool()
	op.AddCert(other.Cert)
	if _, err := cc.Verify(x509.VerifyOptions{Roots: op, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Error("verified against the wrong CA")
	}
	// state survives reopening and keeps counting
	ca2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := ca2.Issue(IssueOptions{Kind: KindClient, Name: "web2"})
	if err != nil || n.Entry.Serial != 4 {
		t.Fatalf("%v %v", n, err)
	}
}

func TestIssueRefusals(t *testing.T) {
	ca, _ := Init(t.TempDir(), InitOptions{Days: 100})
	if _, err := ca.Issue(IssueOptions{Kind: KindClient, Name: "a"}); err == nil {
		t.Error("365 day certificate from a 100 day CA accepted")
	}
	if _, err := ca.Issue(IssueOptions{Kind: KindClient, Name: "a", Days: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.Issue(IssueOptions{Kind: KindClient, Name: "a", Days: 10}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("overwrite: %v", err)
	}
	for _, bad := range []string{"", "../x", "a/b", ".hidden", "a b", "-x"} {
		if _, err := ca.Issue(IssueOptions{Kind: KindClient, Name: bad, Days: 10}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if n := ca.State.NextSerial; n != 3 {
		t.Errorf("failed issues consumed serials: next=%d", n)
	}
}

func TestRevokeAndCRL(t *testing.T) {
	dir := t.TempDir()
	ca, _ := Init(dir, InitOptions{})
	a, _ := ca.Issue(IssueOptions{Kind: KindClient, Name: "a"})
	b, _ := ca.Issue(IssueOptions{Kind: KindClient, Name: "b"})
	if _, err := ca.Revoke("nobody", 0); err == nil {
		t.Error("revoking an unknown name succeeded")
	}
	if _, err := ca.Revoke("", 999); err == nil {
		t.Error("revoking an unknown serial succeeded")
	}
	if _, err := ca.Revoke("a", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ca.Revoke("a", 0); err == nil {
		t.Error("double revoke succeeded")
	}
	next, err := ca.WriteCRL(10)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := ca.CRLStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Int64() != a.Entry.Serial {
		t.Errorf("crl entries: %+v", crl.RevokedCertificateEntries)
	}
	if !crl.NextUpdate.Equal(next.Truncate(time.Second)) && crl.NextUpdate.Sub(next).Abs() > time.Second {
		t.Errorf("nextUpdate %s vs %s", crl.NextUpdate, next)
	}
	if crl.Number.Int64() != 2 { // init wrote 1
		t.Errorf("crl number %d", crl.Number)
	}
	if mode(t, filepath.Join(dir, CRLFile)) != 0o644 {
		t.Error("crl mode")
	}
	// by serial, state reloaded from disk
	ca2, _ := Open(dir)
	if _, err := ca2.Revoke("", b.Entry.Serial); err != nil {
		t.Fatal(err)
	}
	ca2.WriteCRL(10)
	crl, _ = ca2.CRLStatus()
	if len(crl.RevokedCertificateEntries) != 2 {
		t.Errorf("want 2 revoked, got %d", len(crl.RevokedCertificateEntries))
	}
	if s := ca2.State.Issued[0].Status(time.Now()); s != "revoked" {
		t.Errorf("status %s", s)
	}
}

func TestParseSerial(t *testing.T) {
	for in, want := range map[string]int64{"7": 7, "0x10": 16} {
		if n, err := ParseSerial(in); err != nil || n != want {
			t.Errorf("%s: %d %v", in, n, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "x"} {
		if _, err := ParseSerial(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOpenNeedsKey(t *testing.T) {
	dir := t.TempDir()
	Init(dir, InitOptions{})
	os.Chmod(filepath.Join(dir, CAKeyFile), 0o600)
	os.Remove(filepath.Join(dir, CAKeyFile))
	if _, err := Open(dir); err == nil {
		t.Error("Open without key succeeded")
	}
	if _, err := OpenReadOnly(dir); err != nil {
		t.Error(err)
	}
}
