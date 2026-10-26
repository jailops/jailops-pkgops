package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/sdk"
)

// readAuth lets anyone read the overview when the server has no credentials
// configured at all (a read-only server), and otherwise needs a known caller.
func (s *Server) readAuth(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	if s.cfg.Access == nil && len(s.cfg.Tokens) == 0 {
		return nil, true
	}
	return s.auth(w, r, "")
}

// adminAuth requires an administrator. The Cloudflare Access identity only
// says who the caller is; the role comes from PkgOps' own allow-list, and
// every GitHub action is checked here first.
func (s *Server) adminAuth(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	return s.auth(w, r, config.ScopeDelete)
}

func (s *Server) adminError(w http.ResponseWriter, what string, err error) {
	var gh *ghapp.APIError
	switch {
	case errors.Is(err, pkgerr.ErrNotFound):
		fail(w, http.StatusNotFound, "%v", err)
	case errors.Is(err, pkgerr.ErrInvalidSpec), errors.Is(err, pkgerr.ErrUnsupportedABI):
		fail(w, http.StatusBadRequest, "%v", err)
	case errors.Is(err, pkgerr.ErrInvalidPackage), errors.Is(err, prune.ErrNewest):
		fail(w, http.StatusUnprocessableEntity, "%v", err)
	case errors.As(err, &gh):
		s.log.Error(what+" failed at GitHub", "status", gh.Status, "err", err)
		fail(w, http.StatusBadGateway, "GitHub answered %d: %s", gh.Status, gh.Message)
	default:
		s.log.Error(what+" failed", "err", err)
		fail(w, http.StatusInternalServerError, "%s failed: %v", what, err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "bad request body: %v", err)
		return false
	}
	return true
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readAuth(w, r)
	if !ok {
		return
	}
	o, err := s.adm.Overview(r.Context())
	if err != nil {
		s.adminError(w, "overview", err)
		return
	}
	if st, err := s.read.Status(); err == nil {
		o.Status = st
	}
	if p != nil {
		sess := s.session1(p)
		o.Identity = &sess
	}
	writeJSON(w, 200, o)
}

func (s *Server) prunePlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminAuth(w, r); !ok {
		return
	}
	var req sdk.PrunePlanRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.adm.Plan(r.Context(), req)
	if err != nil {
		s.adminError(w, "plan", err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) pruneRequest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminAuth(w, r)
	if !ok {
		return
	}
	var req sdk.PruneRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.adm.RequestPrune(r.Context(), p.Name, req)
	if err != nil {
		s.adminError(w, "cleanup request", err)
		return
	}
	if !req.DryRun {
		s.log.Info("cleanup requested", "by", p.Name, "via", p.Via, "mode", res.Mode, "entries", len(res.Entries), "commit", res.Commit)
		if s.cfg.Source != nil {
			s.cfg.Source.Refresh()
		}
	}
	writeJSON(w, 200, res)
}

func (s *Server) pruneCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminAuth(w, r)
	if !ok {
		return
	}
	var req struct {
		Refs []sdk.ArchiveRef `json:"refs"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.adm.CancelPrune(r.Context(), p.Name, req.Refs); err != nil {
		s.adminError(w, "cancel", err)
		return
	}
	s.log.Info("cleanup cancelled", "by", p.Name, "entries", len(req.Refs))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) republish(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminAuth(w, r)
	if !ok {
		return
	}
	res, err := s.adm.Republish(r.Context(), p.Name)
	if err != nil {
		s.adminError(w, "republish", err)
		return
	}
	s.log.Info("republish requested", "by", p.Name, "via", p.Via)
	writeJSON(w, http.StatusAccepted, res)
}

func (s *Server) archiveDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminAuth(w, r)
	if !ok {
		return
	}
	var req sdk.ArchiveDeleteRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.adm.ArchiveDelete(r.Context(), p.Name+" (via "+p.Via+")", req); err != nil {
		s.adminError(w, "archive delete", err)
		return
	}
	s.log.Warn("archive deletion", "by", p.Name, "via", p.Via, "files", len(req.Refs), "reason", req.Reason)
	w.WriteHeader(http.StatusNoContent)
}
