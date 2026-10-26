// Package config holds registry configuration that lives in files: API tokens
// and the roles they carry.
package config

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Scope is a permission carried by a token.
type Scope string

const (
	// ScopeUpload allows adding packages.
	ScopeUpload Scope = "upload"
	// ScopeDelete allows removing packages.
	ScopeDelete Scope = "delete"
)

// Role names a bundle of scopes, for humans.
type Role string

const (
	// RoleViewer can sign in to the web UI but change nothing.
	RoleViewer Role = "viewer"
	// RoleUploader can add packages.
	RoleUploader Role = "uploader"
	// RoleAdmin can add and delete packages.
	RoleAdmin Role = "admin"
)

// Token is an API credential. Only the SHA-256 of the secret is stored.
type Token struct {
	Name   string
	Hash   string // lowercase hex sha256 of the secret
	Scopes []Scope
}

// Has reports whether the token carries the scope.
func (t Token) Has(s Scope) bool {
	for _, x := range t.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// Role summarises the scopes: admin (delete), uploader (upload) or viewer.
func (t Token) Role() Role {
	switch {
	case t.Has(ScopeDelete):
		return RoleAdmin
	case t.Has(ScopeUpload):
		return RoleUploader
	}
	return RoleViewer
}

// HashToken returns the hex SHA-256 stored for a secret.
func HashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Authenticate returns the token matching secret, or nil. Every token is
// compared so timing does not reveal which one matched.
func Authenticate(tokens []Token, secret string) *Token {
	got := HashToken(secret)
	var found *Token
	for i := range tokens {
		if subtle.ConstantTimeCompare([]byte(got), []byte(tokens[i].Hash)) == 1 {
			found = &tokens[i]
		}
	}
	return found
}

// ParseScopes accepts a comma-separated list of scopes (upload, delete) and
// roles (viewer, uploader, admin).
func ParseScopes(spec string) ([]Scope, error) {
	var out []Scope
	for _, s := range strings.Split(spec, ",") {
		switch s {
		case "upload", string(RoleUploader):
			out = append(out, ScopeUpload)
		case "delete":
			out = append(out, ScopeDelete)
		case string(RoleAdmin):
			out = append(out, ScopeUpload, ScopeDelete)
		case string(RoleViewer):
		default:
			return nil, fmt.Errorf("unknown scope or role %q", s)
		}
	}
	return out, nil
}

// LoadTokens parses a token file: one token per line, "name scopes sha256hex",
// with '#' comments. scopes is a comma-separated list of upload, delete or the
// roles viewer, uploader, admin.
func LoadTokens(file string) ([]Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Token
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 || len(fs[2]) != 64 {
			return nil, fmt.Errorf("%s:%d: want \"name scopes sha256hex\"", file, n)
		}
		scopes, err := ParseScopes(fs[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", file, n, err)
		}
		out = append(out, Token{Name: fs[0], Hash: strings.ToLower(fs[2]), Scopes: scopes})
	}
	return out, sc.Err()
}
