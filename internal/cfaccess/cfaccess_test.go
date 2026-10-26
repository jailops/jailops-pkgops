package cfaccess

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAud = "aud-tag-123"
	testISS = "https://example.cloudflareaccess.com"
)

var b64 = base64.RawURLEncoding

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fakeCerts struct {
	mu     sync.Mutex
	keys   map[string]*rsa.PrivateKey
	fail   bool
	hits   atomic.Int32
	asCert bool
}

func (f *fakeCerts) set(kid string, k *rsa.PrivateKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys = map[string]*rsa.PrivateKey{}
	}
	f.keys[kid] = k
}

func (f *fakeCerts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	doc := map[string]any{}
	var jwks []map[string]string
	var certs []map[string]string
	for kid, k := range f.keys {
		if f.asCert {
			tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
			der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
			if err != nil {
				panic(err)
			}
			certs = append(certs, map[string]string{"kid": kid,
				"cert": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))})
			continue
		}
		jwks = append(jwks, map[string]string{"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig",
			"e": b64.EncodeToString(big.NewInt(int64(k.E)).Bytes()), "n": b64.EncodeToString(k.N.Bytes())})
	}
	doc["keys"] = jwks
	doc["public_certs"] = certs
	_ = json.NewEncoder(w).Encode(doc)
}

type env struct {
	f   *fakeCerts
	key *rsa.PrivateKey
	v   *Verifier
	now time.Time
	mu  sync.Mutex
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func newEnv(t *testing.T, mut func(*fakeCerts)) *env {
	t.Helper()
	e := &env{f: &fakeCerts{}, key: genKey(t), now: time.Unix(1_800_000_000, 0)}
	e.f.set("k1", e.key)
	if mut != nil {
		mut(e.f)
	}
	srv := httptest.NewServer(e.f)
	t.Cleanup(srv.Close)
	v, err := New(Config{Team: "example", Audience: testAud, CertsURL: srv.URL, Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	e.v = v
	return e
}

func (e *env) claims(m map[string]any) map[string]any {
	c := map[string]any{"iss": testISS, "aud": []string{testAud}, "sub": "user-1", "email": "a@example.com",
		"iat": e.now.Unix(), "exp": e.now.Add(time.Hour).Unix()}
	for k, v := range m {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func sign(t *testing.T, k *rsa.PrivateKey, h map[string]any, c map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(h)
	cb, _ := json.Marshal(c)
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

func (e *env) token(t *testing.T, m map[string]any) string {
	return sign(t, e.key, map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"}, e.claims(m))
}

func (e *env) reject(t *testing.T, tok string) {
	t.Helper()
	_, err := e.v.Verify(context.Background(), tok)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if tok != "" && strings.Contains(err.Error(), tok) {
		t.Fatal("error contains token")
	}
}

func TestValidEmail(t *testing.T) {
	e := newEnv(t, nil)
	id, err := e.v.Verify(context.Background(), e.token(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "a@example.com" || id.Service || id.Subject != "user-1" || id.Name() != "a@example.com" || id.Issuer != testISS {
		t.Fatalf("%+v", id)
	}
	if e.v.Issuer() != testISS {
		t.Fatal(e.v.Issuer())
	}
}

func TestValidCertOnly(t *testing.T) {
	e := newEnv(t, func(f *fakeCerts) { f.asCert = true })
	if _, err := e.v.Verify(context.Background(), e.token(t, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceToken(t *testing.T) {
	e := newEnv(t, nil)
	id, err := e.v.Verify(context.Background(), e.token(t, map[string]any{"email": nil, "sub": nil, "common_name": "client.access"}))
	if err != nil {
		t.Fatal(err)
	}
	if !id.Service || id.CommonName != "client.access" || id.Name() != "client.access" {
		t.Fatalf("%+v", id)
	}
	id, err = e.v.Verify(context.Background(), e.token(t, map[string]any{"email": nil, "type": "app"}))
	if err != nil || !id.Service || id.Name() != "user-1" {
		t.Fatalf("%+v %v", id, err)
	}
	e.reject(t, e.token(t, map[string]any{"email": nil, "sub": nil}))
}

func TestAudArrayAndString(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.v.Verify(context.Background(), e.token(t, map[string]any{"aud": []string{"x", testAud}})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.v.Verify(context.Background(), e.token(t, map[string]any{"aud": testAud})); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRejections(t *testing.T) {
	e := newEnv(t, nil)
	n := e.now
	cases := map[string]map[string]any{
		"wrong aud":  {"aud": []string{"other"}},
		"no aud":     {"aud": nil},
		"wrong iss":  {"iss": "https://evil.cloudflareaccess.com"},
		"expired":    {"exp": n.Add(-2 * time.Minute).Unix()},
		"no exp":     {"exp": nil},
		"nbf future": {"nbf": n.Add(10 * time.Minute).Unix()},
		"iat future": {"iat": n.Add(10 * time.Minute).Unix()},
		"exp as str": {"exp": "x"},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) { e.reject(t, e.token(t, m)) })
	}
	// within skew is fine
	ok := map[string]any{"exp": n.Add(-30 * time.Second).Unix(), "nbf": n.Add(30 * time.Second).Unix()}
	if _, err := e.v.Verify(context.Background(), e.token(t, ok)); err != nil {
		t.Fatal(err)
	}
}

func TestAlgAttacks(t *testing.T) {
	e := newEnv(t, nil)
	c := e.claims(nil)
	// none
	hb, _ := json.Marshal(map[string]any{"alg": "none", "kid": "k1"})
	cb, _ := json.Marshal(c)
	e.reject(t, b64.EncodeToString(hb)+"."+b64.EncodeToString(cb)+".")
	e.reject(t, b64.EncodeToString(hb)+"."+b64.EncodeToString(cb)+".AAAA")
	// HS256 keyed with the public key material
	hb, _ = json.Marshal(map[string]any{"alg": "HS256", "kid": "k1"})
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	m := hmac.New(sha256.New, e.key.N.Bytes())
	m.Write([]byte(in))
	e.reject(t, in+"."+b64.EncodeToString(m.Sum(nil)))
	// RS384 label
	e.reject(t, sign(t, e.key, map[string]any{"alg": "RS384", "kid": "k1"}, c))
	// embedded jwk/jku/x5u are ignored: signed by attacker key with own jwk
	atk := genKey(t)
	e.reject(t, sign(t, atk, map[string]any{"alg": "RS256", "kid": "k1",
		"jwk": map[string]string{"kty": "RSA", "n": b64.EncodeToString(atk.N.Bytes()), "e": "AQAB"},
		"jku": "http://evil/", "x5u": "http://evil/"}, c))
}

func TestMalformedAndTampered(t *testing.T) {
	e := newEnv(t, nil)
	for _, s := range []string{"", "a.b", "a.b.c.d", "..", "a.b.c", "!!.!!.!!"} {
		e.reject(t, s)
	}
	tok := e.token(t, nil)
	p := strings.Split(tok, ".")
	cb, _ := json.Marshal(e.claims(map[string]any{"email": "admin@example.com"}))
	e.reject(t, p[0]+"."+b64.EncodeToString(cb)+"."+p[2])
	// signature by a different key under same kid
	e.reject(t, sign(t, genKey(t), map[string]any{"alg": "RS256", "kid": "k1"}, e.claims(nil)))
}

func TestUnknownKidRotation(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	if _, err := e.v.Verify(ctx, e.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	if e.f.hits.Load() != 1 {
		t.Fatal("hits", e.f.hits.Load())
	}
	k2 := genKey(t)
	tok2 := sign(t, k2, map[string]any{"alg": "RS256", "kid": "k2"}, e.claims(nil))
	// Not yet published: refetch is rate limited right after the first fetch.
	e.advance(5 * time.Second)
	e.reject(t, tok2)
	if e.f.hits.Load() != 1 {
		t.Fatal("refetch not rate limited", e.f.hits.Load())
	}
	// Published, after the rate limit window: one refetch picks it up.
	e.f.set("k2", k2)
	e.advance(time.Minute)
	if _, err := e.v.Verify(ctx, tok2); err != nil {
		t.Fatal(err)
	}
	if e.f.hits.Load() != 2 {
		t.Fatal("hits", e.f.hits.Load())
	}
	// Unknown kid again within window: no further fetch.
	k3 := genKey(t)
	e.reject(t, sign(t, k3, map[string]any{"alg": "RS256", "kid": "k3"}, e.claims(nil)))
	e.reject(t, sign(t, k3, map[string]any{"alg": "RS256", "kid": "k3"}, e.claims(nil)))
	if e.f.hits.Load() != 2 {
		t.Fatal("hits", e.f.hits.Load())
	}
}

func TestConcurrentSingleFlight(t *testing.T) {
	e := newEnv(t, nil)
	tok := e.token(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.v.Verify(context.Background(), tok); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if h := e.f.hits.Load(); h != 1 {
		t.Fatal("hits", h)
	}
}

func TestEndpointFailureWarmCache(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	tok := e.token(t, map[string]any{"exp": e.now.Add(48 * time.Hour).Unix()})
	if _, err := e.v.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	e.f.fail = true
	e.f.mu.Unlock()
	// cache TTL expired, refetch fails, cached keys still served
	e.advance(2 * time.Hour)
	if _, err := e.v.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	// older than 24h: fail closed
	e.advance(23 * time.Hour)
	e.reject(t, tok)
}

func TestColdEndpointFailure(t *testing.T) {
	e := newEnv(t, func(f *fakeCerts) { f.fail = true })
	e.reject(t, e.token(t, nil))
}

func TestNew(t *testing.T) {
	if _, err := New(Config{Audience: "a"}); err == nil {
		t.Fatal("want error without team")
	}
	if _, err := New(Config{Team: "x"}); err == nil {
		t.Fatal("want error without audience")
	}
	v, err := New(Config{Team: "example.cloudflareaccess.com", Audience: "a"})
	if err != nil || v.Issuer() != testISS || v.certsURL != testISS+"/cdn-cgi/access/certs" {
		t.Fatal(v, err)
	}
}

func TestFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Cf-Access-Authenticated-User-Email", "evil@example.com")
	if FromRequest(r) != "" {
		t.Fatal("must ignore unauthenticated email header")
	}
	r.Header.Set("Cf-Access-Jwt-Assertion", "tok")
	if FromRequest(r) != "tok" {
		t.Fatal()
	}
}
