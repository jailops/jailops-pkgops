// Package ghapp is a minimal GitHub REST client for a GitHub App
// installation, built on the standard library only. It covers what pkgops
// needs: repository contents, workflow dispatch and runs, and releases with
// their assets.
//
// Credentials are handled conservatively: the App private key is re-read from
// disk on every token refresh and never cached, installation tokens live in
// memory only, and no token, JWT or key material ever appears in an error.
package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Auth supplies a bearer token for GitHub API requests.
type Auth interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is an Auth for a plain, fixed token. It is meant for
// development and tests; a GitHub App (see NewAppAuth) is preferred because
// its tokens are short-lived and scoped to one installation.
type StaticToken string

// Token returns the fixed token, or an error when it is empty.
func (s StaticToken) Token(context.Context) (string, error) {
	if s == "" {
		return "", errors.New("github: empty static token")
	}
	return string(s), nil
}

// AppConfig configures NewAppAuth.
type AppConfig struct {
	AppID          int64
	InstallationID int64
	// KeyFile is the path of the App's PEM private key (PKCS#1 or PKCS#8
	// RSA). It must not be accessible by group or others.
	KeyFile string
	// API is the API base URL; default https://api.github.com.
	API string
	// HTTP is the client used for token requests; default has a 60s timeout.
	HTTP *http.Client
	// Now is the clock; default time.Now. Useful in tests.
	Now func() time.Time
}

// refreshMargin is how long before expiry a cached token is replaced.
const refreshMargin = 2 * time.Minute

// AppAuth is an Auth that exchanges a signed App JWT for an installation
// access token and caches that token in memory until shortly before it
// expires. It is safe for concurrent use; concurrent callers share a single
// refresh.
type AppAuth struct {
	cfg AppConfig

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewAppAuth validates cfg (AppID, InstallationID and KeyFile are required)
// and returns an AppAuth. The key file is not read here; it is read, and
// checked with CheckKeyFile, at every token refresh so a rotated key takes
// effect without a restart and the PEM is never kept in memory.
func NewAppAuth(cfg AppConfig) (*AppAuth, error) {
	switch {
	case cfg.AppID <= 0:
		return nil, errors.New("github app: AppID is required")
	case cfg.InstallationID <= 0:
		return nil, errors.New("github app: InstallationID is required")
	case cfg.KeyFile == "":
		return nil, errors.New("github app: KeyFile is required")
	}
	if cfg.API == "" {
		cfg.API = defaultAPI
	}
	cfg.API = strings.TrimRight(cfg.API, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &AppAuth{cfg: cfg}, nil
}

// Token returns a valid installation token, requesting a new one when the
// cached token is missing or within two minutes of expiry.
func (a *AppAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	if a.token != "" && now.Before(a.expires.Add(-refreshMargin)) {
		return a.token, nil
	}
	key, err := loadKey(a.cfg.KeyFile)
	if err != nil {
		return "", err
	}
	jwt, err := signJWT(key, a.cfg.AppID, now)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.cfg.API, a.cfg.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	setCommonHeaders(req)
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("github app: requesting installation token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", errorFromResponse(resp)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return "", fmt.Errorf("github app: decoding installation token: %w", err)
	}
	if out.Token == "" || out.ExpiresAt.IsZero() {
		return "", errors.New("github app: incomplete installation token response")
	}
	a.token, a.expires = out.Token, out.ExpiresAt
	return a.token, nil
}

// CheckKeyFile verifies that path is a regular file (symlinks are followed),
// that it is not accessible by group or others (no bits in mode 0o077), and
// that it holds a PEM-encoded PKCS#1 or PKCS#8 RSA private key.
func CheckKeyFile(path string) error {
	_, err := loadKey(path)
	return err
}

func loadKey(path string) (*rsa.PrivateKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("github app: key file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("github app: key file %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("github app: key file %s has mode %04o and is accessible by group or others; run: chmod 600 %s",
			path, fi.Mode().Perm(), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("github app: key file: %w", err)
	}
	blk, _ := pem.Decode(data)
	clear(data)
	if blk == nil {
		return nil, fmt.Errorf("github app: key file %s is not PEM encoded", path)
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app: key file %s is not a PKCS#1 or PKCS#8 private key", path)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app: key file %s is not an RSA key", path)
	}
	return rk, nil
}

// signJWT builds the RS256 App JWT: iss is the App ID, iat is 60s in the
// past to tolerate clock skew and exp is 9 minutes ahead.
func signJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": strconv.FormatInt(appID, 10),
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
	})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("github app: signing JWT failed")
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
