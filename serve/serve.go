// Package serve implements the optional pkgops registry: an HTTP server for a
// pkg repository directory with a token-authenticated upload API and an
// optional web UI.
//
// The repository tree it serves is exactly the one produced by
// [pkgops.Repository], so a plain static web server (nginx, Caddy, S3) pointed
// at the same directory is an equivalent read path. The server adds only
// uploads, listing, deletion, retention and the status/UI conveniences. The
// implementation lives in internal packages; this package keeps the stable
// names. For a Go client of the HTTP API see jailops.net/pkgops/sdk.
package serve

import (
	"context"

	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/server"
	"jailops.net/pkgops/sdk"
)

// Scope is a permission carried by a token.
type Scope = config.Scope

// Token scopes.
const (
	ScopeUpload = config.ScopeUpload
	ScopeDelete = config.ScopeDelete
)

// Token is an API credential. Only the SHA-256 of the secret is stored.
type Token = config.Token

// Config configures a [Server].
type Config = server.Config

// Server is the HTTP handler of a registry.
type Server = server.Server

// HashToken returns the hex SHA-256 stored for a secret.
func HashToken(secret string) string { return config.HashToken(secret) }

// LoadTokens parses a token file: one token per line, "name scopes sha256hex"
// with '#' comments. Scopes are upload and delete, or the roles viewer,
// uploader and admin.
func LoadTokens(file string) ([]Token, error) { return config.LoadTokens(file) }

// New validates the configuration and builds a Server.
func New(cfg Config) (*Server, error) { return server.New(cfg) }

// Push uploads a .pkg to a running registry (PUT /api/v1/packages).
func Push(ctx context.Context, baseURL, token, pkgFile string) error {
	c := &sdk.Client{BaseURL: baseURL, Token: token}
	_, err := c.Upload(ctx, pkgFile)
	return err
}
