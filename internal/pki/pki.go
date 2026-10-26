// Package pki is a tiny private certificate authority for a PkgOps private
// registry: one CA, server and client certificates, a revocation list. It is
// pure Go (crypto/x509), keeps its state in one JSON file inside the CA
// directory and has no database.
//
// Layout of a CA directory:
//
//	ca.crt            CA certificate (public)
//	ca.key            CA private key, mode 0400; keep the directory offline
//	state.json        serial counter, issued certificates, revocations
//	crl.pem           current certificate revocation list
//	servers/NAME.crt|.key   server certificates (key mode 0400)
//	clients/NAME.crt|.key   client certificates (key mode 0400)
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// File names inside a CA directory.
const (
	CAFile    = "ca.crt"
	CAKeyFile = "ca.key"
	StateFile = "state.json"
	CRLFile   = "crl.pem"
)

// Defaults (documented in docs/private-registry.md).
const (
	DefaultCADays     = 5475 // 15 years
	DefaultServerDays = 1825 // 5 years: nobody wants to rotate certificates every few months
	DefaultClientDays = 1825
	// DefaultCRLDays is long on purpose: a revocation always writes a new CRL at once, so the validity only says
	// how long the server accepts an unchanged list. Past nextUpdate plus the grace the server denies everyone.
	DefaultCRLDays = 365
	// CRLWarnBefore is how close to nextUpdate a CRL is reported as near expiry.
	CRLWarnBefore = 60 * 24 * time.Hour
)

// Kinds of issued certificates.
const (
	KindServer = "server"
	KindClient = "client"
)

// Entry is one issued certificate in the state file.
type Entry struct {
	Serial    int64      `json:"serial"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	SANs      []string   `json:"sans,omitempty"`
	NotBefore time.Time  `json:"not_before"`
	NotAfter  time.Time  `json:"not_after"`
	Revoked   *time.Time `json:"revoked,omitempty"`
	File      string     `json:"file,omitempty"`
}

// State is the content of state.json.
type State struct {
	Version    int     `json:"version"`
	NextSerial int64   `json:"next_serial"`
	CRLNumber  int64   `json:"crl_number"`
	Issued     []Entry `json:"issued"`
}

// CA is an opened CA directory.
type CA struct {
	Dir   string
	Cert  *x509.Certificate
	State *State
	// Now is the clock (tests).
	Now func() time.Time

	key crypto.Signer
}

func (c *CA) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// InitOptions configure Init.
type InitOptions struct {
	Name    string // CA common name
	KeyType string // "ecdsa" (P-256, default) or "rsa"
	RSABits int    // default 4096
	Days    int    // default DefaultCADays
	Now     func() time.Time
}

func newKey(keyType string, bits int) (crypto.Signer, error) {
	switch strings.ToLower(keyType) {
	case "", "ecdsa", "ec", "p256":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "rsa":
		if bits == 0 {
			bits = 4096
		}
		if bits < 2048 {
			return nil, errors.New("RSA keys below 2048 bits are not allowed")
		}
		return rsa.GenerateKey(rand.Reader, bits)
	}
	return nil, fmt.Errorf("unknown key type %q (want ecdsa or rsa)", keyType)
}

// writeNew creates path exclusively with mode; it never overwrites.
func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; refusing to overwrite", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// writeAtomic replaces path via a temp file in the same directory.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func keyPEM(k crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Init creates a new CA in dir. It refuses to touch an existing CA.
func Init(dir string, o InitOptions) (*CA, error) {
	if o.Name == "" {
		o.Name = "PkgOps private CA"
	}
	if o.Days <= 0 {
		o.Days = DefaultCADays
	}
	for _, f := range []string{CAFile, CAKeyFile, StateFile} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return nil, fmt.Errorf("%s already exists; refusing to overwrite an existing CA", filepath.Join(dir, f))
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := newKey(o.KeyType, o.RSABits)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if o.Now != nil {
		now = o.Now()
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: o.Name},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(0, 0, o.Days),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	if err := writeNew(filepath.Join(dir, CAKeyFile), kp, 0o400); err != nil {
		return nil, err
	}
	if err := writeNew(filepath.Join(dir, CAFile), certPEM(der), 0o644); err != nil {
		return nil, err
	}
	ca := &CA{Dir: dir, Cert: cert, key: key, Now: o.Now,
		State: &State{Version: 1, NextSerial: 2, CRLNumber: 0}}
	if err := ca.save(true); err != nil {
		return nil, err
	}
	if _, err := ca.WriteCRL(DefaultCRLDays); err != nil {
		return nil, err
	}
	return ca, nil
}

// Open loads a CA directory (certificate, key, state).
func Open(dir string) (*CA, error) {
	certData, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, fmt.Errorf("no CA in %s (run: pkgops pki init --dir %s): %w", dir, dir, err)
	}
	cert, err := ParseCertPEM(certData)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", CAFile, err)
	}
	keyData, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, fmt.Errorf("the CA key is needed for this command (is the CA directory the offline one?): %w", err)
	}
	blk, _ := pem.Decode(keyData)
	if blk == nil {
		return nil, fmt.Errorf("%s: not PEM", CAKeyFile)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", CAKeyFile, err)
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: unsupported key", CAKeyFile)
	}
	sd, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(sd, st); err != nil {
		return nil, fmt.Errorf("%s: %w", StateFile, err)
	}
	return &CA{Dir: dir, Cert: cert, key: signer, State: st}, nil
}

// OpenReadOnly loads the CA certificate and state without the private key
// (for list on an admin machine that only holds the public part).
func OpenReadOnly(dir string) (*CA, error) {
	certData, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, err
	}
	cert, err := ParseCertPEM(certData)
	if err != nil {
		return nil, err
	}
	sd, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(sd, st); err != nil {
		return nil, fmt.Errorf("%s: %w", StateFile, err)
	}
	return &CA{Dir: dir, Cert: cert, State: st}, nil
}

func (c *CA) save(create bool) error {
	data, err := json.MarshalIndent(c.State, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(filepath.Join(c.Dir, StateFile), data, 0o600)
}

// IssueOptions configure Issue.
type IssueOptions struct {
	Kind    string   // KindServer or KindClient
	Name    string   // common name (the machine or host name)
	SANs    []string // extra DNS names or IP addresses (servers)
	Days    int
	KeyType string
	RSABits int
	OutDir  string // default: DIR/servers or DIR/clients
}

// Issued describes the files written by Issue.
type Issued struct {
	Entry    Entry
	CertFile string
	KeyFile  string
}

// ValidName reports whether s is usable as a certificate name and file name.
func ValidName(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		ok := r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !ok {
			return false
		}
	}
	return true
}

// Issue creates a key and a certificate signed by the CA and records it.
func (c *CA) Issue(o IssueOptions) (*Issued, error) {
	if c.key == nil {
		return nil, errors.New("CA key not loaded")
	}
	if o.Kind != KindServer && o.Kind != KindClient {
		return nil, fmt.Errorf("unknown kind %q", o.Kind)
	}
	if !ValidName(o.Name) {
		return nil, fmt.Errorf("name %q: use letters, digits, '.', '-', '_' (max 64)", o.Name)
	}
	if o.Days <= 0 {
		o.Days = DefaultClientDays
		if o.Kind == KindServer {
			o.Days = DefaultServerDays
		}
	}
	now := c.now()
	notAfter := now.AddDate(0, 0, o.Days)
	if notAfter.After(c.Cert.NotAfter) {
		return nil, fmt.Errorf("a %d day certificate would outlive the CA (expires %s); use fewer days", o.Days, c.Cert.NotAfter.Format("2006-01-02"))
	}
	out := o.OutDir
	if out == "" {
		out = filepath.Join(c.Dir, o.Kind+"s")
	}
	certPath := filepath.Join(out, o.Name+".crt")
	keyPath := filepath.Join(out, o.Name+".key")
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf("%s already exists; refusing to overwrite (revoke the old certificate and use --out DIR for the new one)", p)
		}
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	key, err := newKey(o.KeyType, o.RSABits)
	if err != nil {
		return nil, err
	}
	serial := c.State.NextSerial
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: o.Name},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	var sans []string
	if _, isRSA := key.(*rsa.PrivateKey); isRSA && o.Kind == KindServer {
		tmpl.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	switch o.Kind {
	case KindServer:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		names := append([]string{o.Name}, o.SANs...)
		seen := map[string]bool{}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			sans = append(sans, n)
			if ip := net.ParseIP(n); ip != nil {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			} else {
				tmpl.DNSNames = append(tmpl.DNSNames, n)
			}
		}
	case KindClient:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, key.Public(), c.key)
	if err != nil {
		return nil, err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	if err := writeNew(keyPath, kp, 0o400); err != nil {
		return nil, err
	}
	if err := writeNew(certPath, certPEM(der), 0o644); err != nil {
		os.Remove(keyPath)
		return nil, err
	}
	e := Entry{Serial: serial, Kind: o.Kind, Name: o.Name, SANs: sans, NotBefore: tmpl.NotBefore.UTC(), NotAfter: tmpl.NotAfter.UTC(), File: certPath}
	c.State.NextSerial++
	c.State.Issued = append(c.State.Issued, e)
	if err := c.save(false); err != nil {
		return nil, err
	}
	return &Issued{Entry: e, CertFile: certPath, KeyFile: keyPath}, nil
}

// Revoke marks certificates revoked by serial (serial > 0) or, with serial 0,
// every active certificate of that name. It does not write the CRL.
func (c *CA) Revoke(name string, serial int64) ([]Entry, error) {
	now := c.now().UTC()
	var done []Entry
	for i := range c.State.Issued {
		e := &c.State.Issued[i]
		match := (serial > 0 && e.Serial == serial) || (serial == 0 && name != "" && e.Name == name && e.Kind == KindClient)
		if !match || e.Revoked != nil {
			continue
		}
		e.Revoked = &now
		done = append(done, *e)
	}
	if len(done) == 0 {
		switch {
		case serial > 0:
			return nil, fmt.Errorf("no active certificate with serial %d", serial)
		default:
			return nil, fmt.Errorf("no active client certificate named %q", name)
		}
	}
	return done, c.save(false)
}

// WriteCRL regenerates crl.pem with the given validity and returns its nextUpdate.
func (c *CA) WriteCRL(days int) (time.Time, error) {
	if c.key == nil {
		return time.Time{}, errors.New("CA key not loaded")
	}
	if days <= 0 {
		days = DefaultCRLDays
	}
	now := c.now()
	var revoked []x509.RevocationListEntry
	for _, e := range c.State.Issued {
		if e.Revoked != nil {
			revoked = append(revoked, x509.RevocationListEntry{SerialNumber: big.NewInt(e.Serial), RevocationTime: *e.Revoked})
		}
	}
	sort.Slice(revoked, func(i, j int) bool { return revoked[i].SerialNumber.Cmp(revoked[j].SerialNumber) < 0 })
	c.State.CRLNumber++
	next := now.AddDate(0, 0, days)
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(c.State.CRLNumber),
		ThisUpdate:                now.Add(-5 * time.Minute),
		NextUpdate:                next,
		RevokedCertificateEntries: revoked,
	}, c.Cert, c.key)
	if err != nil {
		c.State.CRLNumber--
		return time.Time{}, err
	}
	if err := writeAtomic(filepath.Join(c.Dir, CRLFile), pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), 0o644); err != nil {
		return time.Time{}, err
	}
	return next, c.save(false)
}

// Status of an issued certificate at time t.
func (e Entry) Status(t time.Time) string {
	switch {
	case e.Revoked != nil:
		return "revoked"
	case t.After(e.NotAfter):
		return "expired"
	case t.Before(e.NotBefore):
		return "not yet valid"
	}
	return "active"
}

// ParseSerial accepts decimal, or hex with a 0x prefix.
func ParseSerial(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 0, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad serial %q", s)
	}
	return n, nil
}

// ParseCertPEM returns the first certificate in PEM data.
func ParseCertPEM(data []byte) (*x509.Certificate, error) {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no certificate found")
		}
		if b.Type == "CERTIFICATE" {
			return x509.ParseCertificate(b.Bytes)
		}
	}
}

// ParseCRLPEM parses a PEM or DER CRL.
func ParseCRLPEM(data []byte) (*x509.RevocationList, error) {
	if b, _ := pem.Decode(data); b != nil {
		if b.Type != "X509 CRL" {
			return nil, fmt.Errorf("PEM block is %q, not a CRL", b.Type)
		}
		data = b.Bytes
	}
	return x509.ParseRevocationList(data)
}

// CRLStatus loads the CRL of the CA directory and reports its nextUpdate.
func (c *CA) CRLStatus() (*x509.RevocationList, error) {
	data, err := os.ReadFile(filepath.Join(c.Dir, CRLFile))
	if err != nil {
		return nil, err
	}
	crl, err := ParseCRLPEM(data)
	if err != nil {
		return nil, err
	}
	return crl, crl.CheckSignatureFrom(c.Cert)
}
