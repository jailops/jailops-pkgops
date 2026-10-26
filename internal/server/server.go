// Package server is the HTTP face of the registry: static serving of the
// repository directory, the /api/v1 JSON API and, optionally, the embedded web
// UI. Everything is stdlib net/http.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"jailops.net/pkgops/internal/admin"
	"jailops.net/pkgops/internal/cfaccess"
	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/registry"
	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/source"
	"jailops.net/pkgops/internal/webui"
	"jailops.net/pkgops/sdk"
)

// Config configures a Server.
type Config struct {
	// Repo is the repository served and updated. Its Signer re-signs the
	// catalog after every change.
	Repo *repository.Repository
	// Tokens authorise the write API. With none, the server is read-only.
	Tokens []config.Token
	// Keep, when > 0, retains only the newest Keep versions per package after
	// each upload.
	Keep int
	// MaxUpload bounds a single upload in bytes (default 512 MiB).
	MaxUpload int64
	// Logger receives one line per write operation (default slog.Default()).
	Logger *slog.Logger
	// UI serves the embedded web UI at /ui/ (and redirects / there) when it
	// was built into the binary. Without it nothing changes for the API or the
	// static repository.
	UI bool
	// UIFS replaces the embedded UI files (tests).
	UIFS fs.FS

	// Source, instead of Repo, serves a read-through view of a published site:
	// nothing is stored, uploads and deletes are refused, and cleanup goes
	// through Admin (a prune manifest commit). Repo must be nil.
	Source *source.Source
	// Admin carries the GitHub, archive and workflow settings of the admin
	// API. Nil is fine: the overview works and cleanup requests are manual.
	Admin *admin.Service

	// Access, when set, authenticates requests by their Cloudflare Access
	// JWT (Cf-Access-Jwt-Assertion). AccessAllow maps a verified identity (an
	// email or a service token's common name, lower case) to a role; an
	// identity that is not listed is refused with 403. Bearer tokens keep
	// working next to it (CI, the CLI). The server must only be reachable
	// through Access (or on loopback behind a tunnel): the header is trusted
	// only after its signature, issuer and audience check out.
	Access      *cfaccess.Verifier
	AccessAllow map[string]config.Role
}

// Server is the HTTP handler of a registry.
type Server struct {
	cfg  Config
	reg  *registry.Registry // nil in source (read-through) mode
	read reader
	adm  *admin.Service
	mux  *http.ServeMux
	log  *slog.Logger
	ui   fs.FS
}

// reader is the read side shared by the local registry and a published site.
type reader interface {
	Status() (*sdk.Status, error)
	Packages(abi string) ([]sdk.Package, error)
	Detail(abi, name, version string) (*sdk.Detail, error)
	PublicKeyPEM() []byte
}

// New validates the configuration and builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Source != nil && cfg.Repo != nil {
		return nil, errors.New("serve: Source and Repo are mutually exclusive")
	}
	if cfg.Source == nil && (cfg.Repo == nil || cfg.Repo.Root == "") {
		return nil, errors.New("serve: Config.Repo with a Root (or a Source) is required")
	}
	if cfg.Access != nil && len(cfg.AccessAllow) == 0 {
		return nil, errors.New("serve: Cloudflare Access needs an allow-list of identities")
	}
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = 512 << 20
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), log: cfg.Logger}
	if s.log == nil {
		s.log = slog.Default()
	}
	if cfg.Source != nil {
		s.read = cfg.Source
	} else {
		if err := os.MkdirAll(cfg.Repo.Root, 0o755); err != nil {
			return nil, err
		}
		s.reg = registry.New(cfg.Repo, cfg.Keep, len(cfg.Tokens) > 0)
		s.read = s.reg
	}
	s.adm = cfg.Admin
	if s.adm == nil {
		s.adm = &admin.Service{}
	}
	if s.adm.Index == nil {
		if cfg.Source != nil {
			s.adm.Index = cfg.Source.Index
			s.adm.Mode, s.adm.Source = "source", cfg.Source.Base
		} else {
			s.adm.Index = func(context.Context) (*index.Index, error) { return cfg.Repo.Index() }
			s.adm.Mode = "local"
			if s.adm.Policy == nil {
				s.adm.Policy = cfg.Repo.Policy
			}
		}
	}
	if cfg.UI {
		if cfg.UIFS != nil {
			s.ui = cfg.UIFS
		} else if f, ok := webui.FS(); ok {
			s.ui = f
		}
	}
	s.mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	s.mux.HandleFunc("GET /api/v1/status", s.status)
	s.mux.HandleFunc("GET /api/v1/session", s.session)
	s.mux.HandleFunc("GET /api/v1/key", s.key)
	s.mux.HandleFunc("GET /api/v1/overview", s.overview)
	s.mux.HandleFunc("POST /api/v1/prune/plan", s.prunePlan)
	s.mux.HandleFunc("POST /api/v1/prune", s.pruneRequest)
	s.mux.HandleFunc("POST /api/v1/prune/cancel", s.pruneCancel)
	s.mux.HandleFunc("POST /api/v1/republish", s.republish)
	s.mux.HandleFunc("POST /api/v1/archive/delete", s.archiveDelete)
	s.mux.HandleFunc("GET /api/v1/packages", s.list)
	s.mux.HandleFunc("GET /api/v1/packages/{abi}/{name}/{version}", s.detail)
	s.mux.HandleFunc("PUT /api/v1/packages", s.upload)
	s.mux.HandleFunc("DELETE /api/v1/packages/{abi}/{name}/{version}", s.remove)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "no such API endpoint") })
	if s.ui != nil {
		s.mux.Handle("/ui/", s.uiHandler())
		s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
	if s.reg != nil {
		s.mux.Handle("/", s.static())
	}
	return s, nil
}

// Handler returns the http.Handler to mount or pass to http.Server.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			// With Cloudflare Access every API call needs a verified identity
			// (or a bearer token), except the health probe.
			if s.cfg.Access != nil && r.URL.Path != "/api/v1/health" {
				if _, ok := s.principal(w, r); !ok {
					return
				}
			}
		}
		s.mux.ServeHTTP(w, r)
	})
}

// ---- static repository ----------------------------------------------------

type noDirFS struct{ http.FileSystem }

func (n noDirFS) Open(name string) (http.File, error) {
	for _, el := range strings.Split(name, "/") {
		if strings.HasPrefix(el, ".") && el != "" {
			return nil, os.ErrNotExist // hides temp files, staging dirs and anything dotted
		}
	}
	f, err := n.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist // no directory listings
	}
	return f, nil
}

func (s *Server) static() http.Handler {
	fsrv := http.FileServer(noDirFS{http.Dir(s.cfg.Repo.Root)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		switch base := path.Base(r.URL.Path); {
		case strings.HasSuffix(base, ".pkg") && strings.Contains(r.URL.Path, "/All/"):
			// name-version.pkg is immutable once published.
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		case base == "meta.conf" || base == "meta":
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Cache-Control", "no-cache")
		case strings.HasSuffix(base, ".pkg"):
			// catalogs: clients revalidate with If-Modified-Since / ETag
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Cache-Control", "no-cache")
		default:
			h.Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}

// ---- web UI ---------------------------------------------------------------

func (s *Server) uiHandler() http.Handler {
	files := http.FileServerFS(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		rel := strings.TrimPrefix(r.URL.Path, "/ui/")
		if rel == "" || strings.HasSuffix(rel, ".html") {
			h.Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(rel, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if _, err := fs.Stat(s.ui, rel); rel != "" && err != nil && path.Ext(rel) == "" {
			rel = "" // client-side route: serve the app shell
			h.Set("Cache-Control", "no-cache")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rel
		files.ServeHTTP(w, r2)
	})
}

// ---- API ------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, a...)})
}

// principal is an authenticated caller: a bearer token or a verified
// Cloudflare Access identity.
type principal struct {
	Name   string
	Via    string // "token" or "access"
	Scopes []config.Scope
}

func (p *principal) has(sc config.Scope) bool {
	for _, x := range p.Scopes {
		if x == sc {
			return true
		}
	}
	return false
}

func (p *principal) role() config.Role {
	switch {
	case p.has(config.ScopeDelete):
		return config.RoleAdmin
	case p.has(config.ScopeUpload):
		return config.RoleUploader
	}
	return config.RoleViewer
}

// principal authenticates the request: a bearer token first (CI and the CLI),
// then, when configured, the Cloudflare Access JWT. It answers 401 or 403 and
// returns false when the caller is not known. The plain
// Cf-Access-Authenticated-User-Email header is never read.
func (s *Server) principal(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	if secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && secret != "" {
		tok := config.Authenticate(s.cfg.Tokens, secret)
		if tok == nil {
			fail(w, http.StatusUnauthorized, "invalid token")
			return nil, false
		}
		return &principal{Name: tok.Name, Via: "token", Scopes: tok.Scopes}, true
	}
	if s.cfg.Access != nil {
		raw := cfaccess.FromRequest(r)
		if raw == "" {
			fail(w, http.StatusUnauthorized, "no Cloudflare Access identity: reach this server through Access")
			return nil, false
		}
		id, err := s.cfg.Access.Verify(r.Context(), raw)
		if err != nil {
			s.log.Warn("access token rejected", "err", err)
			fail(w, http.StatusUnauthorized, "invalid Cloudflare Access token")
			return nil, false
		}
		role, ok := s.cfg.AccessAllow[strings.ToLower(id.Name())]
		if !ok {
			s.log.Warn("access identity not allowed", "identity", id.Name())
			fail(w, http.StatusForbidden, "%s is not allowed to use this server", id.Name())
			return nil, false
		}
		scopes, _ := config.ParseScopes(string(role))
		return &principal{Name: id.Name(), Via: "access", Scopes: scopes}, true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="pkgops"`)
	fail(w, http.StatusUnauthorized, "missing bearer token")
	return nil, false
}

// auth authenticates and, when need is not empty, requires that scope.
// need == "" accepts any authenticated caller (session check).
func (s *Server) auth(w http.ResponseWriter, r *http.Request, need config.Scope) (*principal, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return nil, false
	}
	if need != "" && !p.has(need) {
		fail(w, http.StatusForbidden, "%s lacks scope %q (role %s)", p.Name, need, p.role())
		return nil, false
	}
	return p, true
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	p, ok := s.auth(w, r, "")
	if !ok {
		return
	}
	writeJSON(w, 200, s.session1(p))
}

func (s *Server) session1(p *principal) sdk.Session {
	scopes := []string{}
	for _, sc := range p.Scopes {
		scopes = append(scopes, string(sc))
	}
	return sdk.Session{Name: p.Name, Role: string(p.role()), Scopes: scopes, Via: p.Via}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.read.Status()
	if err != nil {
		s.log.Error("status failed", "err", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) key(w http.ResponseWriter, r *http.Request) {
	pem := s.read.PublicKeyPEM()
	if pem == nil {
		fail(w, 404, "the repository is not signed")
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="repo.pub"`)
	_, _ = w.Write(pem)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	pk, err := s.read.Packages(r.URL.Query().Get("abi"))
	switch {
	case errors.Is(err, pkgerr.ErrNotFound):
		pk = nil
	case err != nil:
		s.log.Error("list failed", "err", err)
		fail(w, 500, "internal error")
		return
	}
	if pk == nil {
		pk = []sdk.Package{}
	}
	writeJSON(w, 200, map[string]any{"packages": pk})
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	d, err := s.read.Detail(r.PathValue("abi"), r.PathValue("name"), r.PathValue("version"))
	switch {
	case errors.Is(err, pkgerr.ErrNotFound):
		fail(w, 404, "%v", err)
	case errors.Is(err, pkgerr.ErrInvalidSpec), errors.Is(err, pkgerr.ErrUnsupportedABI):
		fail(w, 400, "%v", err)
	case err != nil:
		s.log.Error("detail failed", "err", err)
		fail(w, 500, "internal error")
	default:
		writeJSON(w, 200, d)
	}
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if s.reg == nil {
		fail(w, http.StatusMethodNotAllowed, "this server is a read-through view of a published site; upload through the packages repository's workflow")
		return
	}
	tok, ok := s.auth(w, r, config.ScopeUpload)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	tmp, err := os.CreateTemp("", "pkgops-upload-*.pkg")
	if err != nil {
		fail(w, 500, "%v", err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, r.Body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, http.StatusRequestEntityTooLarge, "upload exceeds %d bytes", s.cfg.MaxUpload)
			return
		}
		fail(w, 400, "reading upload: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		fail(w, 500, "%v", err)
		return
	}
	info, pruned, err := s.reg.Upload(r.Context(), tmp.Name())
	switch {
	case errors.Is(err, pkgerr.ErrInvalidPackage), errors.Is(err, pkgerr.ErrUnsupportedABI):
		code := 422
		if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		fail(w, code, "%v", err)
		return
	case err != nil && info == nil:
		s.log.Error("upload failed", "token", tok.Name, "err", err)
		fail(w, 500, "internal error")
		return
	case err != nil:
		s.log.Error("retention failed", "err", err) // the upload itself succeeded
	}
	s.log.Info("package uploaded", "token", tok.Name, "pkg", info.Name+"-"+info.Version, "abi", info.ABI, "pruned", len(pruned))
	pk, _ := s.reg.Packages(info.ABI)
	var out sdk.Package
	for _, p := range pk {
		if p.Name == info.Name && p.Version == info.Version {
			out = p
		}
	}
	if pruned == nil {
		pruned = []string{}
	}
	writeJSON(w, http.StatusCreated, sdk.Uploaded{Package: out, Pruned: pruned})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	if s.reg == nil {
		fail(w, http.StatusMethodNotAllowed, "this server is a read-through view of a published site; request a cleanup (POST /api/v1/prune) instead")
		return
	}
	tok, ok := s.auth(w, r, config.ScopeDelete)
	if !ok {
		return
	}
	abi, name, ver := r.PathValue("abi"), r.PathValue("name"), r.PathValue("version")
	err := s.reg.Delete(r.Context(), abi, name, ver)
	switch {
	case errors.Is(err, pkgerr.ErrNotFound):
		fail(w, 404, "%v", err)
	case errors.Is(err, pkgerr.ErrInvalidSpec), errors.Is(err, pkgerr.ErrUnsupportedABI):
		fail(w, 400, "%v", err)
	case err != nil:
		s.log.Error("delete failed", "token", tok.Name, "err", err)
		fail(w, 500, "internal error")
	default:
		s.log.Info("package deleted", "token", tok.Name, "pkg", name+"-"+ver, "abi", abi)
		w.WriteHeader(http.StatusNoContent)
	}
}
