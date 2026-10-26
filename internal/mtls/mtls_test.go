package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/pkgops/internal/pki"
)

type rig struct {
	t      *testing.T
	ca     *pki.CA
	dir    string
	srv    *http.Server
	addr   string
	pool   *x509.CertPool
	m      *Manager
	logbuf *syncBuf
	opts   Options
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T, mod func(*Options)) *rig {
	t.Helper()
	dir := t.TempDir()
	ca, err := pki.Init(dir, pki.InitOptions{Name: "test CA"})
	if err != nil {
		t.Fatal(err)
	}
	srvCert, err := ca.Issue(pki.IssueOptions{Kind: pki.KindServer, Name: "localhost", SANs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ca: ca, dir: dir, logbuf: &syncBuf{}, pool: x509.NewCertPool()}
	r.pool.AddCert(ca.Cert)
	o := Options{
		CertFile: srvCert.CertFile, KeyFile: srvCert.KeyFile,
		ClientCAFile: filepath.Join(dir, pki.CAFile), CRLFile: filepath.Join(dir, pki.CRLFile),
		CertInterval: -1, ClientInterval: -1, // check on every use
		Logger: slog.New(slog.NewTextHandler(r.logbuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	if mod != nil {
		mod(&o)
	}
	r.opts = o
	r.m, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.addr = ln.Addr().String()
	r.srv = &http.Server{
		Handler:   r.m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })),
		TLSConfig: r.m.TLSConfig(),
		ErrorLog:  slog.NewLogLogger(slog.NewTextHandler(r.logbuf, nil), slog.LevelInfo),
	}
	go r.srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { r.srv.Close() })
	return r
}

func (r *rig) client(kind, name string, cfg func(*tls.Config)) (*pki.Issued, *http.Client) {
	r.t.Helper()
	iss, err := r.ca.Issue(pki.IssueOptions{Kind: pki.KindClient, Name: name})
	if err != nil {
		r.t.Fatal(err)
	}
	return iss, r.clientFor(iss.CertFile, iss.KeyFile, r.pool, cfg)
}

func (r *rig) clientFor(certFile, keyFile string, roots *x509.CertPool, cfg func(*tls.Config)) *http.Client {
	tc := &tls.Config{RootCAs: roots}
	if certFile != "" {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			r.t.Fatal(err)
		}
		tc.Certificates = []tls.Certificate{c}
	}
	if cfg != nil {
		cfg(tc)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 5 * time.Second}
}

func (r *rig) get(c *http.Client) (int, error) {
	resp, err := c.Get("https://" + r.addr + "/x")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (r *rig) revoke(name string) {
	r.t.Helper()
	if _, err := r.ca.Revoke(name, 0); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.ca.WriteCRL(30); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) wantDenied(c *http.Client, what string) {
	r.t.Helper()
	code, err := r.get(c)
	if err == nil && code == 200 {
		r.t.Fatalf("%s: request was served", what)
	}
}

func TestValidClient(t *testing.T) {
	r := newRig(t, nil)
	_, c := r.client(pki.KindClient, "m1", nil)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatalf("%d %v", code, err)
	}
}

func TestTLSVersions(t *testing.T) {
	r := newRig(t, nil)
	iss, _ := r.client(pki.KindClient, "m1", nil)
	for _, v := range []uint16{tls.VersionTLS10, tls.VersionTLS11} {
		c := r.clientFor(iss.CertFile, iss.KeyFile, r.pool, func(c *tls.Config) { c.MinVersion, c.MaxVersion = v, v })
		if _, err := r.get(c); err == nil {
			t.Errorf("TLS %x accepted", v)
		}
	}
	c := r.clientFor(iss.CertFile, iss.KeyFile, r.pool, func(c *tls.Config) { c.MaxVersion = tls.VersionTLS12 })
	if code, err := r.get(c); err != nil || code != 200 {
		t.Errorf("TLS 1.2: %d %v", code, err)
	}
	conn, err := tls.Dial("tcp", r.addr, &tls.Config{RootCAs: r.pool, Certificates: c.Transport.(*http.Transport).TLSClientConfig.Certificates})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.ConnectionState().Version != tls.VersionTLS13 {
		t.Errorf("negotiated %x, want TLS 1.3", conn.ConnectionState().Version)
	}
}

func TestNoCertWrongCAExpired(t *testing.T) {
	r := newRig(t, nil)
	r.wantDenied(r.clientFor("", "", r.pool, nil), "no certificate")

	other, err := pki.Init(t.TempDir(), pki.InitOptions{Name: "other CA"})
	if err != nil {
		t.Fatal(err)
	}
	oc, err := other.Issue(pki.IssueOptions{Kind: pki.KindClient, Name: "evil"})
	if err != nil {
		t.Fatal(err)
	}
	r.wantDenied(r.clientFor(oc.CertFile, oc.KeyFile, r.pool, nil), "other CA")

	// a certificate issued in the past with a short life: expired now
	r.ca.Now = func() time.Time { return time.Now().AddDate(0, 0, -60) }
	ec, err := r.ca.Issue(pki.IssueOptions{Kind: pki.KindClient, Name: "old", Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	r.ca.Now = nil
	r.wantDenied(r.clientFor(ec.CertFile, ec.KeyFile, r.pool, nil), "expired")

	// a server certificate must not work as a client certificate (EKU)
	sc, err := r.ca.Issue(pki.IssueOptions{Kind: pki.KindServer, Name: "srv2"})
	if err != nil {
		t.Fatal(err)
	}
	r.wantDenied(r.clientFor(sc.CertFile, sc.KeyFile, r.pool, nil), "server EKU")
	if !strings.Contains(r.logbuf.String(), "client denied") && !strings.Contains(r.logbuf.String(), "TLS handshake error") {
		t.Errorf("denials not logged:\n%s", r.logbuf.String())
	}
}

func TestRevokedAtStartup(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.Init(dir, pki.InitOptions{})
	iss, _ := ca.Issue(pki.IssueOptions{Kind: pki.KindClient, Name: "gone"})
	ca.Revoke("gone", 0)
	ca.WriteCRL(30)
	srv, _ := ca.Issue(pki.IssueOptions{Kind: pki.KindServer, Name: "localhost", SANs: []string{"127.0.0.1"}})
	m, err := New(Options{CertFile: srv.CertFile, KeyFile: srv.KeyFile, ClientCAFile: filepath.Join(dir, pki.CAFile), CRLFile: filepath.Join(dir, pki.CRLFile), ClientInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := tls.LoadX509KeyPair(iss.CertFile, iss.KeyFile)
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	err = m.Check(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}})
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("got %v", err)
	}
}

// Revoking while the server runs: a new full handshake, a resumed session and
// a connection that is already open are all refused afterwards, without any
// restart.
func TestRevocationWithoutRestart(t *testing.T) {
	r := newRig(t, nil)
	iss, _ := r.client(pki.KindClient, "m1", nil)
	cache := tls.NewLRUClientSessionCache(8)
	cc, _ := tls.LoadX509KeyPair(iss.CertFile, iss.KeyFile)
	tc := &tls.Config{RootCAs: r.pool, Certificates: []tls.Certificate{cc}, ClientSessionCache: cache, MinVersion: tls.VersionTLS13}

	dial := func() (*tls.Conn, error) {
		c, err := tls.Dial("tcp", r.addr, tc)
		if err != nil {
			return nil, err
		}
		// TLS 1.3: a client-side rejection arrives with the first read
		c.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(c, "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		b, err := io.ReadAll(c)
		if err != nil || !strings.Contains(string(b), "200 OK") {
			c.Close()
			return nil, errors.New("not served")
		}
		return c, nil
	}
	c1, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2, err := dial() // prime: must really resume
	if err != nil {
		t.Fatal(err)
	}
	if !c2.ConnectionState().DidResume {
		t.Fatal("test setup: session was not resumed before the revocation")
	}
	c2.Close()

	// an established keep-alive connection
	ka := r.clientFor(iss.CertFile, iss.KeyFile, r.pool, nil)
	if code, err := r.get(ka); err != nil || code != 200 {
		t.Fatalf("%d %v", code, err)
	}

	r.revoke("m1")

	if c, err := dial(); err == nil { // resumption attempt (cache still holds the ticket)
		c.Close()
		t.Error("resumed session accepted after revocation")
	}
	if code, err := r.get(ka); err == nil && code == 200 {
		t.Error("open keep-alive connection still served after revocation")
	}
	tc.ClientSessionCache = nil // full handshake
	if c, err := dial(); err == nil {
		c.Close()
		t.Error("new handshake accepted after revocation")
	}
	// another machine is unaffected
	_, c := r.client(pki.KindClient, "m2", nil)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Errorf("m2: %d %v", code, err)
	}
	if !strings.Contains(r.logbuf.String(), "revoked") {
		t.Errorf("revocation not logged:\n%s", r.logbuf.String())
	}
	if strings.Contains(r.logbuf.String(), "PRIVATE KEY") {
		t.Error("log leaks key material")
	}
}

func TestReloadDelayIsBounded(t *testing.T) {
	// with a 200 ms interval the first rejection comes within ~interval
	r := newRig(t, func(o *Options) { o.ClientInterval = 200 * time.Millisecond })
	_, c := r.client(pki.KindClient, "m1", nil)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatal(code, err)
	}
	r.revoke("m1")
	start := time.Now()
	for {
		c.CloseIdleConnections()
		code, err := r.get(c)
		if err != nil || code != 200 {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("revocation not effective within 2 s with a 200 ms interval")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("took %s", d)
	}
}

func TestFailClosed(t *testing.T) {
	r := newRig(t, nil)
	_, c := r.client(pki.KindClient, "m1", nil)
	crl := filepath.Join(r.dir, pki.CRLFile)
	good, _ := os.ReadFile(crl)

	os.Remove(crl)
	r.wantDenied(c, "missing CRL")
	if err := os.WriteFile(crl, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.wantDenied(c, "invalid CRL")

	// CRL signed by another CA
	other, _ := pki.Init(t.TempDir(), pki.InitOptions{})
	other.WriteCRL(30)
	bad, _ := os.ReadFile(filepath.Join(other.Dir, pki.CRLFile))
	os.WriteFile(crl, bad, 0o644)
	r.wantDenied(c, "foreign CRL")

	os.WriteFile(crl, good, 0o644)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatalf("recovery: %d %v", code, err)
	}
	// broken client CA file
	caf := filepath.Join(r.dir, pki.CAFile)
	caData, _ := os.ReadFile(caf)
	os.WriteFile(caf, []byte("junk"), 0o644)
	r.wantDenied(c, "broken CA")
	os.WriteFile(caf, caData, 0o644)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatalf("recovery after CA: %d %v", code, err)
	}
}

func TestCRLExpiryGrace(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := newRig(t, func(o *Options) { o.Now = clock; o.CRLGrace = 24 * time.Hour })
	_, c := r.client(pki.KindClient, "m1", nil)
	r.ca.WriteCRL(1) // valid for one day
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatal(code, err)
	}
	// the client side verifies the server cert with the real clock; only the server clock moves
	mu.Lock()
	now = now.Add(24*time.Hour + 12*time.Hour) // past nextUpdate, inside grace
	mu.Unlock()
	if code, err := r.get(c); err != nil || code != 200 {
		t.Fatalf("inside grace: %d %v", code, err)
	}
	mu.Lock()
	now = now.Add(24 * time.Hour) // beyond grace
	mu.Unlock()
	r.wantDenied(c, "CRL expired beyond grace")
}

func TestServerCertReload(t *testing.T) {
	r := newRig(t, nil)
	iss, c := r.client(pki.KindClient, "m1", nil)
	_ = iss
	serial := func() string {
		conn, err := tls.Dial("tcp", r.addr, &tls.Config{RootCAs: r.pool, Certificates: c.Transport.(*http.Transport).TLSClientConfig.Certificates})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	}
	before := serial()
	n, err := r.ca.Issue(pki.IssueOptions{Kind: pki.KindServer, Name: "localhost", SANs: []string{"127.0.0.1"}, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{n.CertFile, r.opts.CertFile}, {n.KeyFile, r.opts.KeyFile}} {
		d, _ := os.ReadFile(p[0])
		os.Remove(p[1])
		os.WriteFile(p[1], d, 0o600)
	}
	if after := serial(); after == before {
		t.Error("new server certificate not picked up")
	}
	// a broken replacement keeps the old one serving
	os.Remove(r.opts.KeyFile)
	os.WriteFile(r.opts.KeyFile, []byte("junk"), 0o600)
	if code, err := r.get(c); err != nil || code != 200 {
		t.Errorf("after broken key: %d %v", code, err)
	}
}

func TestNoClientCAMeansNoClientAuth(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.Init(dir, pki.InitOptions{})
	srv, _ := ca.Issue(pki.IssueOptions{Kind: pki.KindServer, Name: "localhost", SANs: []string{"127.0.0.1"}})
	m, err := New(Options{CertFile: srv.CertFile, KeyFile: srv.KeyFile})
	if err != nil {
		t.Fatal(err)
	}
	if m.Required() || m.TLSConfig().ClientAuth != tls.NoClientCert {
		t.Error("client auth must be off without --client-ca")
	}
	if _, err := New(Options{CertFile: srv.CertFile, KeyFile: srv.KeyFile, CRLFile: "x"}); err == nil {
		t.Error("--crl without --client-ca accepted")
	}
	if _, err := New(Options{CertFile: srv.CertFile}); err == nil {
		t.Error("cert without key accepted")
	}
}

func TestStartupFailsWithoutCRL(t *testing.T) {
	r := newRig(t, nil)
	o := r.opts
	o.CRLFile = filepath.Join(r.dir, "nope.pem")
	if _, err := New(o); err == nil {
		t.Error("missing CRL at startup accepted")
	}
}
