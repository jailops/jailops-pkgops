// Package sdk is the Go client for the pkgops registry HTTP API (`pkgops serve`)
// and the definition of its wire types.
//
// It covers what automation needs: upload a package, list packages, read a
// package or the repository status, delete a package, check a token. Reading
// packages as a pkg client does needs no SDK: the repository is a plain
// directory served over HTTP.
//
// The API is versioned by its path (/api/v1). Errors from the server are
// returned as *APIError, which matches the sentinels ErrUnauthorized,
// ErrForbidden, ErrNotFound, ErrConflict and ErrInvalid with errors.Is.
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Package is one package file in the repository.
type Package struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	ABI        string    `json:"abi"`
	Arch       string    `json:"arch"`
	Origin     string    `json:"origin"`
	Comment    string    `json:"comment"`
	Maintainer string    `json:"maintainer,omitempty"`
	WWW        string    `json:"www,omitempty"`
	Licenses   []string  `json:"licenses,omitempty"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	Flatsize   int64     `json:"flatsize"`
	Path       string    `json:"path"`   // URL path of the .pkg relative to the registry root
	Latest     bool      `json:"latest"` // advertised in the catalog (newest version of its name)
	Modified   time.Time `json:"modified"`
	// MinRelease is the oldest FreeBSD release the package supports ("15.1"),
	// empty when it declares none.
	MinRelease string `json:"min_release,omitempty"`
}

// Dep is a package dependency.
type Dep struct {
	Name    string `json:"name"`
	Origin  string `json:"origin"`
	Version string `json:"version"`
}

// FileEntry is one path installed by a package.
type FileEntry struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Owner   string `json:"owner"`
	Group   string `json:"group"`
	Symlink string `json:"symlink,omitempty"`
	Config  bool   `json:"config,omitempty"`
}

// Detail is a package with the content of its manifest.
type Detail struct {
	Package
	Desc        string      `json:"desc"`
	Prefix      string      `json:"prefix"`
	Categories  []string    `json:"categories,omitempty"`
	Deps        []Dep       `json:"deps,omitempty"`
	Users       []string    `json:"users,omitempty"`
	Groups      []string    `json:"groups,omitempty"`
	Files       []FileEntry `json:"files"`
	Directories []FileEntry `json:"directories,omitempty"`
	Scripts     []string    `json:"scripts,omitempty"` // phases present, e.g. post-install
}

// ABIStatus is the state of one ABI's repository.
type ABIStatus struct {
	ABI        string    `json:"abi"`
	Arch       string    `json:"arch"`
	Channel    string    `json:"channel"`
	Packages   int       `json:"packages"` // distinct names
	Versions   int       `json:"versions"` // package files
	Updated    time.Time `json:"updated"`  // catalog modification time
	Signature  string    `json:"signature"`
	Consistent bool      `json:"consistent"`
	Problem    string    `json:"problem,omitempty"`
}

// Status is the repository overview.
type Status struct {
	Healthy     bool        `json:"healthy"`
	Packages    int         `json:"packages"`
	Versions    int         `json:"versions"`
	Updated     time.Time   `json:"updated"`
	Signature   string      `json:"signature"`   // "pubkey", "fingerprints" or "unsigned"
	Fingerprint string      `json:"fingerprint"` // sha256 of the public key PEM, empty when unsigned
	ABIs        []ABIStatus `json:"abis"`
	Problems    []string    `json:"problems,omitempty"`
	Uploads     bool        `json:"uploads"` // the registry accepts authenticated writes
	Keep        int         `json:"keep"`    // retention (0 = keep all)
	Channel     string      `json:"channel"`
}

// Session describes the token a request was made with.
type Session struct {
	Name   string   `json:"name"`
	Role   string   `json:"role"` // viewer, uploader or admin
	Scopes []string `json:"scopes"`
	// Via is how the caller authenticated: "token" or "access" (Cloudflare Access).
	Via string `json:"via,omitempty"`
}

// Uploaded is the answer to an upload.
type Uploaded struct {
	Package Package  `json:"package"`
	Pruned  []string `json:"pruned"`
}

// Sentinel errors matched by *APIError.
var (
	ErrUnauthorized = errors.New("sdk: missing or invalid token")
	ErrForbidden    = errors.New("sdk: token lacks the required role")
	ErrNotFound     = errors.New("sdk: not found")
	ErrConflict     = errors.New("sdk: conflict")
	ErrInvalid      = errors.New("sdk: rejected as invalid")
)

// APIError is an error answer from the registry.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("registry answered %d: %s", e.Status, e.Message)
}

// Is maps HTTP statuses to the sentinel errors.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	case ErrInvalid:
		return e.Status == http.StatusUnprocessableEntity || e.Status == http.StatusBadRequest
	}
	return false
}

// Client talks to one registry. The zero HTTP client is replaced by one with a
// generous timeout, since uploads can be large.
type Client struct {
	BaseURL string // e.g. https://pkg.example.org
	Token   string // bearer token; only needed for writes and /session
	HTTP    *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, size int64, out any) error {
	return c.doCT(ctx, method, path, "application/octet-stream", body, size, out)
}

func (c *Client) doCT(ctx context.Context, method, path, contentType string, body io.Reader, size int64, out any) error {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(bytes.NewReader(data)).Decode(out)
}

// Health checks that the registry answers.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, "GET", "/api/v1/health", nil, 0, nil)
}

// Session validates the token and returns its role.
func (c *Client) Session(ctx context.Context) (*Session, error) {
	var s Session
	return &s, c.do(ctx, "GET", "/api/v1/session", nil, 0, &s)
}

// Status returns the repository overview.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	return &s, c.do(ctx, "GET", "/api/v1/status", nil, 0, &s)
}

// Packages lists every package file; abi filters when non-empty.
func (c *Client) Packages(ctx context.Context, abi string) ([]Package, error) {
	p := "/api/v1/packages"
	if abi != "" {
		p += "?abi=" + url.QueryEscape(abi)
	}
	var out struct {
		Packages []Package `json:"packages"`
	}
	err := c.do(ctx, "GET", p, nil, 0, &out)
	return out.Packages, err
}

// Package returns one package with its manifest content.
func (c *Client) Package(ctx context.Context, abi, name, version string) (*Detail, error) {
	var d Detail
	err := c.do(ctx, "GET", pkgPath(abi, name, version), nil, 0, &d)
	return &d, err
}

// Upload sends a .pkg file. Needs an uploader or admin token.
func (c *Client) Upload(ctx context.Context, file string) (*Uploaded, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var u Uploaded
	return &u, c.do(ctx, "PUT", "/api/v1/packages", f, st.Size(), &u)
}

// Delete removes one package version. Needs an admin token.
func (c *Client) Delete(ctx context.Context, abi, name, version string) error {
	return c.do(ctx, "DELETE", pkgPath(abi, name, version), nil, 0, nil)
}

func pkgPath(abi, name, version string) string {
	return "/api/v1/packages/" + url.PathEscape(abi) + "/" + url.PathEscape(name) + "/" + url.PathEscape(version)
}
