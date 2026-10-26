// Package cfaccess verifies the Cf-Access-Jwt-Assertion JWT that Cloudflare
// Access adds to requests. It uses only the standard library.
package cfaccess

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrInvalid is wrapped by every rejection. Messages never contain the token.
var ErrInvalid = errors.New("cloudflare access: invalid token")

const (
	maxCertsBody = 1 << 20
	clockSkew    = time.Minute
	maxIATFuture = 5 * time.Minute
	maxKeyAge    = 24 * time.Hour
	minRSABits   = 2048
	headerName   = "Cf-Access-Jwt-Assertion"
)

// Config configures a Verifier.
type Config struct {
	// Team is "example" (issuer https://example.cloudflareaccess.com) or a
	// full host such as "example.cloudflareaccess.com".
	Team     string
	Audience string
	// CertsURL defaults to https://<team>.cloudflareaccess.com/cdn-cgi/access/certs.
	CertsURL string
	HTTP     *http.Client
	Now      func() time.Time
	// CacheTTL defaults to 1h.
	CacheTTL time.Duration
	// MinRefresh defaults to 1 minute: at most one fetch per interval.
	MinRefresh time.Duration
}

// Identity is the authenticated caller.
type Identity struct {
	Subject    string
	Email      string
	CommonName string
	Service    bool
	Issuer     string
	Expires    time.Time
}

// Name returns Email, else CommonName, else Subject.
func (i Identity) Name() string {
	switch {
	case i.Email != "":
		return i.Email
	case i.CommonName != "":
		return i.CommonName
	}
	return i.Subject
}

// Verifier verifies Cloudflare Access tokens.
type Verifier struct {
	issuer     string
	audience   string
	certsURL   string
	client     *http.Client
	now        func() time.Time
	cacheTTL   time.Duration
	minRefresh time.Duration

	refreshMu   sync.Mutex // serializes fetches (single flight)
	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time // last successful fetch
	lastAttempt time.Time // last fetch attempt
}

// New validates cfg and returns a Verifier.
func New(cfg Config) (*Verifier, error) {
	team := strings.TrimSpace(cfg.Team)
	team = strings.TrimPrefix(team, "https://")
	team = strings.TrimSuffix(team, "/")
	if team == "" {
		return nil, errors.New("cfaccess: team is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("cfaccess: audience is required")
	}
	host := team
	if !strings.Contains(host, ".") {
		host += ".cloudflareaccess.com"
	}
	v := &Verifier{
		issuer:     "https://" + host,
		audience:   cfg.Audience,
		certsURL:   cfg.CertsURL,
		client:     cfg.HTTP,
		now:        cfg.Now,
		cacheTTL:   cfg.CacheTTL,
		minRefresh: cfg.MinRefresh,
	}
	if v.certsURL == "" {
		v.certsURL = v.issuer + "/cdn-cgi/access/certs"
	}
	if v.client == nil {
		v.client = &http.Client{Timeout: 10 * time.Second}
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.cacheTTL <= 0 {
		v.cacheTTL = time.Hour
	}
	if v.minRefresh <= 0 {
		v.minRefresh = time.Minute
	}
	return v, nil
}

// Issuer returns the expected issuer.
func (v *Verifier) Issuer() string { return v.issuer }

// FromRequest returns the Cf-Access-Jwt-Assertion header value.
//
// The Cf-Access-Authenticated-User-Email header is deliberately ignored: it is
// not authenticated and can be forged by anything that reaches the origin.
func FromRequest(r *http.Request) (token string) {
	return strings.TrimSpace(r.Header.Get(headerName))
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type claims struct {
	Iss        string          `json:"iss"`
	Aud        json.RawMessage `json:"aud"`
	Sub        string          `json:"sub"`
	Email      string          `json:"email"`
	CommonName string          `json:"common_name"`
	Type       string          `json:"type"`
	Exp        *float64        `json:"exp"`
	Nbf        *float64        `json:"nbf"`
	Iat        *float64        `json:"iat"`
}

func decodeSeg(s string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func unixTime(f float64) time.Time {
	return time.Unix(int64(f), 0)
}

// Verify checks token and returns the identity it carries.
func (v *Verifier) Verify(ctx context.Context, token string) (*Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, invalid("malformed token")
	}
	var h header
	if err := decodeSeg(parts[0], &h); err != nil {
		return nil, invalid("bad header")
	}
	// Only RS256. jwk/jku/x5u/x5c headers are never consulted.
	if h.Alg != "RS256" {
		return nil, invalid("unsupported algorithm")
	}
	if h.Kid == "" {
		return nil, invalid("missing kid")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, invalid("bad signature encoding")
	}
	var c claims
	if err := decodeSeg(parts[1], &c); err != nil {
		return nil, invalid("bad claims")
	}

	key, err := v.keyFor(ctx, h.Kid)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return nil, invalid("signature mismatch")
	}

	now := v.now()
	if c.Iss != v.issuer {
		return nil, invalid("issuer mismatch")
	}
	if !audContains(c.Aud, v.audience) {
		return nil, invalid("audience mismatch")
	}
	if c.Exp == nil {
		return nil, invalid("missing exp")
	}
	exp := unixTime(*c.Exp)
	if !now.Before(exp.Add(clockSkew)) {
		return nil, invalid("token expired")
	}
	if c.Nbf != nil && unixTime(*c.Nbf).After(now.Add(clockSkew)) {
		return nil, invalid("token not yet valid")
	}
	if c.Iat != nil && unixTime(*c.Iat).After(now.Add(maxIATFuture+clockSkew)) {
		return nil, invalid("issued in the future")
	}

	id := &Identity{Subject: c.Sub, Email: c.Email, Issuer: c.Iss, Expires: exp}
	if c.Email == "" && (c.CommonName != "" || c.Type == "app") {
		id.Service = true
	}
	if id.Service || c.Email == "" {
		id.CommonName = c.CommonName
	}
	if id.Email == "" && id.CommonName == "" && id.Subject == "" {
		return nil, invalid("no identity claims")
	}
	return id, nil
}

func audContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func (v *Verifier) snapshot(kid string) (key *rsa.PublicKey, age time.Duration, have bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.fetchedAt.IsZero() {
		return nil, 0, false
	}
	return v.keys[kid], v.now().Sub(v.fetchedAt), true
}

func (v *Verifier) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	key, age, have := v.snapshot(kid)
	if have && key != nil && age < v.cacheTTL {
		return key, nil
	}
	// Unknown kid or stale cache: refetch (rate limited, single flight).
	ferr := v.refresh(ctx)
	key, age, have = v.snapshot(kid)
	if !have || age >= maxKeyAge {
		if ferr != nil {
			return nil, invalid("signing keys unavailable: %v", ferr)
		}
		return nil, invalid("signing keys unavailable")
	}
	if key == nil {
		return nil, invalid("unknown key id")
	}
	return key, nil
}

func (v *Verifier) refresh(ctx context.Context) error {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	v.mu.RLock()
	last := v.lastAttempt
	v.mu.RUnlock()
	if !last.IsZero() && v.now().Sub(last) < v.minRefresh {
		return nil // someone fetched recently; rate limited
	}
	v.mu.Lock()
	v.lastAttempt = v.now()
	v.mu.Unlock()

	keys, err := v.fetch(ctx)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = v.now()
	v.mu.Unlock()
	return nil
}

type certsDoc struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
	PublicCert  *certEntry  `json:"public_cert"`
	PublicCerts []certEntry `json:"public_certs"`
}

type certEntry struct {
	Kid  string `json:"kid"`
	Cert string `json:"cert"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch certs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch certs: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCertsBody+1))
	if err != nil {
		return nil, fmt.Errorf("read certs: %w", err)
	}
	if len(body) > maxCertsBody {
		return nil, errors.New("certs response too large")
	}
	var doc certsDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode certs: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kid == "" || k.Kty != "RSA" || (k.Alg != "" && k.Alg != "RS256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(nb) == 0 || len(eb) == 0 || len(eb) > 4 {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
		if e < 3 || pub.N.BitLen() < minRSABits {
			continue
		}
		keys[k.Kid] = pub
	}
	certs := append([]certEntry(nil), doc.PublicCerts...)
	if doc.PublicCert != nil {
		certs = append(certs, *doc.PublicCert)
	}
	for _, ce := range certs {
		if ce.Kid == "" || keys[ce.Kid] != nil {
			continue
		}
		blk, _ := pem.Decode([]byte(ce.Cert))
		if blk == nil {
			continue
		}
		crt, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		if pub, ok := crt.PublicKey.(*rsa.PublicKey); ok && pub.N.BitLen() >= minRSABits {
			keys[ce.Kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("certs response has no usable keys")
	}
	return keys, nil
}
