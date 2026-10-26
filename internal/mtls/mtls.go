// Package mtls builds the TLS side of "pkgops serve": a server certificate
// that is reloaded when its files change and, optionally, mandatory client
// certificates verified against a CA file and a CRL file.
//
// Nothing here needs a restart. The certificate, key, client CA and CRL files
// are re-checked lazily (a stat call, at most once per interval, on the next
// connection or request) and re-read when the file's inode, size or mtime
// changed. The revocation check runs in tls.Config.VerifyConnection, so it
// also covers resumed sessions, and again on every HTTP request, so an open
// keep-alive connection of a revoked machine stops working at the next request.
//
// Revocation is fail closed: when the client CA file or the CRL file is
// missing, unreadable, not signed by the CA, or expired for longer than the
// grace period, every client is denied (and the reason is logged) until a
// good file is back. The previous list is not kept.
package mtls

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Default check intervals and grace.
const (
	// DefaultCertInterval is the minimum time between two checks of the
	// server certificate and key files.
	DefaultCertInterval = 15 * time.Second
	// DefaultClientInterval is the minimum time between two checks of the
	// client CA and CRL files. It is the maximum delay between regenerating
	// the CRL and the first rejected connection.
	DefaultClientInterval = 5 * time.Second
	// DefaultCRLGrace is how long a CRL may be past its nextUpdate before
	// everything is denied.
	DefaultCRLGrace = 24 * time.Hour
)

// Options configure a Manager.
type Options struct {
	CertFile, KeyFile string // server certificate and key (PEM)
	ClientCAFile      string // when set, client certificates are required
	CRLFile           string // optional, only with ClientCAFile
	CRLGrace          time.Duration
	CertInterval      time.Duration // default DefaultCertInterval
	ClientInterval    time.Duration // default DefaultClientInterval
	Logger            *slog.Logger
	Now               func() time.Time
}

// Manager holds the live TLS state.
type Manager struct {
	o   Options
	log *slog.Logger

	mu       sync.Mutex
	certW    watch
	keyW     watch
	cert     *tls.Certificate
	caW      watch
	crlW     watch
	pool     *x509.CertPool
	cas      []*x509.Certificate
	caErr    error
	crl      *crlState
	crlErr   error
	ticketKs [][32]byte
}

type crlState struct {
	signer  *x509.Certificate // CA that signed the list
	next    time.Time
	revoked map[string]bool // serial (decimal) -> true
}

// New loads everything once; any error is fatal at startup.
func New(o Options) (*Manager, error) {
	if o.CertFile == "" || o.KeyFile == "" {
		return nil, errors.New("--tls-cert and --tls-key are required together")
	}
	if o.ClientCAFile == "" && o.CRLFile != "" {
		return nil, errors.New("--crl needs --client-ca")
	}
	if o.CertInterval == 0 {
		o.CertInterval = DefaultCertInterval
	}
	if o.ClientInterval == 0 {
		o.ClientInterval = DefaultClientInterval
	}
	if o.CertInterval < 0 {
		o.CertInterval = 0
	}
	if o.ClientInterval < 0 {
		o.ClientInterval = 0
	}
	if o.CRLGrace == 0 {
		o.CRLGrace = DefaultCRLGrace
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	m := &Manager{o: o, log: o.Logger}
	m.certW = watch{path: o.CertFile, every: o.CertInterval}
	m.keyW = watch{path: o.KeyFile, every: o.CertInterval}
	m.caW = watch{path: o.ClientCAFile, every: o.ClientInterval}
	m.crlW = watch{path: o.CRLFile, every: o.ClientInterval}
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return nil, err
	}
	m.ticketKs = [][32]byte{k}

	c, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("server certificate: %w", err)
	}
	m.cert = &c
	m.certW.poll(m.now())
	m.keyW.poll(m.now())
	if o.ClientCAFile != "" {
		m.caW.poll(m.now())
		if m.pool, m.cas, err = loadCA(o.ClientCAFile); err != nil {
			return nil, err
		}
		if o.CRLFile != "" {
			m.crlW.poll(m.now())
			if m.crl, err = loadCRL(o.CRLFile, m.cas); err != nil {
				return nil, err
			}
			if err := m.crlUsable(m.crl, m.now()); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

func (m *Manager) now() time.Time {
	if m.o.Now != nil {
		return m.o.Now()
	}
	return time.Now()
}

// Required reports whether client certificates are required.
func (m *Manager) Required() bool { return m.o.ClientCAFile != "" }

// watch detects a replaced or rewritten file, checking at most every `every`.
type watch struct {
	path  string
	every time.Duration
	last  time.Time
	fi    os.FileInfo
}

// poll reports whether the file needs to be (re)loaded now.
func (w *watch) poll(now time.Time) bool {
	if !w.last.IsZero() && now.Sub(w.last) < w.every {
		return false
	}
	w.last = now
	fi, err := os.Stat(w.path)
	if err != nil {
		w.fi = nil
		return true
	}
	if w.fi != nil && os.SameFile(w.fi, fi) && w.fi.ModTime().Equal(fi.ModTime()) && w.fi.Size() == fi.Size() {
		return false
	}
	w.fi = fi
	return true
}

func loadCA(file string) (*x509.CertPool, []*x509.Certificate, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, fmt.Errorf("client CA: %w", err)
	}
	var cas []*x509.Certificate
	pool := x509.NewCertPool()
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("client CA %s: %w", file, err)
		}
		pool.AddCert(c)
		cas = append(cas, c)
	}
	if len(cas) == 0 {
		return nil, nil, fmt.Errorf("client CA %s: no certificate found", file)
	}
	return pool, cas, nil
}

func loadCRL(file string, cas []*x509.Certificate) (*crlState, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("CRL: %w", err)
	}
	if b, _ := pem.Decode(data); b != nil {
		if b.Type != "X509 CRL" {
			return nil, fmt.Errorf("CRL %s: PEM block is %q", file, b.Type)
		}
		data = b.Bytes
	}
	rl, err := x509.ParseRevocationList(data)
	if err != nil {
		return nil, fmt.Errorf("CRL %s: %w", file, err)
	}
	st := &crlState{next: rl.NextUpdate, revoked: map[string]bool{}}
	for _, ca := range cas {
		if rl.CheckSignatureFrom(ca) == nil {
			st.signer = ca
			break
		}
	}
	if st.signer == nil {
		return nil, fmt.Errorf("CRL %s: not signed by any certificate in the client CA file", file)
	}
	if rl.NextUpdate.IsZero() {
		return nil, fmt.Errorf("CRL %s: no nextUpdate", file)
	}
	for _, e := range rl.RevokedCertificateEntries {
		st.revoked[e.SerialNumber.String()] = true
	}
	return st, nil
}

func (m *Manager) crlUsable(c *crlState, now time.Time) error {
	if now.After(c.next.Add(m.o.CRLGrace)) {
		return fmt.Errorf("CRL expired at %s (grace %s): regenerate it with 'pkgops pki crl'", c.next.Format(time.RFC3339), m.o.CRLGrace)
	}
	return nil
}

// refresh re-reads changed files. Caller holds m.mu.
func (m *Manager) refresh() {
	now := m.now()
	pc, pk := m.certW.poll(now), m.keyW.poll(now)
	if pc || pk {
		c, err := tls.LoadX509KeyPair(m.o.CertFile, m.o.KeyFile)
		if err != nil {
			// keep serving the old certificate: a half-copied pair is common
			m.log.Warn("server certificate not reloaded; keeping the previous one", "err", err)
			// look again at the next interval
			m.certW.fi, m.keyW.fi = nil, nil
		} else {
			m.cert = &c
			m.log.Info("server certificate reloaded", "file", m.o.CertFile)
		}
	}
	if m.o.ClientCAFile == "" {
		return
	}
	caChanged := m.caW.poll(now)
	if caChanged {
		pool, cas, err := loadCA(m.o.ClientCAFile)
		if err != nil {
			m.pool, m.cas, m.caErr = nil, nil, err
			m.log.Error("client CA unusable: denying all clients", "err", err)
		} else {
			m.pool, m.cas, m.caErr = pool, cas, nil
			m.log.Info("client CA reloaded", "file", m.o.ClientCAFile, "certs", len(cas))
		}
	}
	if m.o.CRLFile == "" {
		return
	}
	if crlChanged := m.crlW.poll(now); crlChanged || caChanged {
		if m.caErr != nil {
			m.crl, m.crlErr = nil, m.caErr
			return
		}
		c, err := loadCRL(m.o.CRLFile, m.cas)
		if err != nil {
			m.crl, m.crlErr = nil, err
			m.log.Error("CRL unusable: denying all clients", "err", err)
			return
		}
		m.crl, m.crlErr = c, nil
		m.log.Info("CRL reloaded", "file", m.o.CRLFile, "revoked", len(c.revoked), "next_update", c.next.Format(time.RFC3339))
	}
}

// snapshot refreshes and returns the current client policy.
func (m *Manager) snapshot() (pool *x509.CertPool, crl *crlState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh()
	if m.caErr != nil {
		return nil, nil, m.caErr
	}
	if m.o.CRLFile != "" {
		if m.crlErr != nil {
			return nil, nil, m.crlErr
		}
		if err := m.crlUsable(m.crl, m.now()); err != nil {
			return nil, nil, err
		}
	}
	return m.pool, m.crl, nil
}

func (m *Manager) serverCert() *tls.Certificate {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh()
	return m.cert
}

// TLSConfig returns the server configuration: TLS 1.2 minimum (1.3 is
// negotiated with every client that supports it), the reloading certificate
// and, with a client CA, mandatory verified client certificates.
func (m *Manager) TLSConfig() *tls.Config {
	base := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.serverCert(), nil
		},
		NextProtos: []string{"h2", "http/1.1"},
	}
	base.SetSessionTicketKeys(m.ticketKs)
	if !m.Required() {
		return base
	}
	base.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		pool, _, err := m.snapshot()
		if err != nil {
			m.log.Info("client denied", "reason", err.Error())
			return nil, err
		}
		c := base.Clone()
		c.GetConfigForClient = nil
		c.ClientAuth = tls.RequireAndVerifyClientCert
		c.ClientCAs = pool
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			err := m.Check(&cs)
			if err != nil {
				m.deny("tls", cs.PeerCertificates, err)
			}
			return err
		}
		return c, nil
	}
	return base
}

// Check applies the current CA and CRL to a connection's client certificate.
// It runs on full and on resumed handshakes and on every HTTP request.
func (m *Manager) Check(cs *tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no client certificate")
	}
	pool, crl, err := m.snapshot()
	if err != nil {
		return err
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, Intermediates: inter, CurrentTime: m.now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return fmt.Errorf("certificate not accepted: %w", err)
	}
	if crl == nil {
		return nil
	}
	chain := chains[0]
	if len(chain) < 2 {
		return errors.New("the client certificate must be issued by the client CA, not be the CA itself")
	}
	for i := 0; i < len(chain)-1; i++ { // every certificate but the root
		if crl.revoked[chain[i].SerialNumber.String()] {
			// only valid if this CRL is the issuer's
			if bytes.Equal(chain[i+1].Raw, crl.signer.Raw) {
				return fmt.Errorf("certificate revoked (serial %s)", chain[i].SerialNumber)
			}
		}
	}
	// the CRL must belong to the CA that issued the leaf
	if !bytes.Equal(chain[1].Raw, crl.signer.Raw) {
		return errors.New("no CRL covers the issuer of this certificate")
	}
	return nil
}

func (m *Manager) deny(where string, peers []*x509.Certificate, err error) {
	attrs := []any{"via", where, "reason", err.Error()}
	if len(peers) > 0 {
		attrs = append(attrs, "cn", peers[0].Subject.CommonName, "serial", peers[0].SerialNumber.String())
	}
	m.log.Info("client denied", attrs...)
}

// Middleware refuses requests whose connection has no acceptable client
// certificate at this moment. It makes revocation effective on keep-alive
// connections that were opened before the CRL changed. It also closes the
// connection after refusing. No header supplied by the client is ever used
// to identify it.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	if !m.Required() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			http.Error(w, "client certificate required", http.StatusForbidden)
			return
		}
		if err := m.Check(r.TLS); err != nil {
			m.deny("http", r.TLS.PeerCertificates, err)
			w.Header().Set("Connection", "close")
			http.Error(w, "client certificate not accepted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
