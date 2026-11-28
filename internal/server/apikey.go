package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/ops"
)

// badKeyPerMinute is how many unrecognised keys one client address may offer
// per minute before it is answered 429 instead of 401.
const badKeyPerMinute = 30

// keyAuth authenticates an API request that carries a key (Authorization:
// Bearer or X-API-Key). A key is honoured on /api/ only: the pages and their
// forms are for people with a browser session, so a key sent there is ignored.
//
// A request that offers a key is judged by it alone. An unknown, revoked or
// expired key is a 401 and never falls back to a cookie, so a bad key cannot
// quietly become a session.
//
// handled is true when the response has been written.
func (s *Server) keyAuth(w http.ResponseWriter, r *http.Request) (out *http.Request, ok, handled bool) {
	if !strings.HasPrefix(r.URL.Path, "/api/") && !isMCPPath(r.URL.Path) {
		return r, false, false
	}
	secret, present := auth.KeyFromRequest(r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
	if !present {
		return r, false, false
	}
	p, info, err := s.Keys.Lookup(r.Context(), secret)
	if err != nil {
		if !errors.Is(err, auth.ErrNoKey) {
			s.Log.Error("api key lookup", "err", err)
			s.writeKeyError(w, r, ops.Internal(err))
			return r, false, true
		}
		// A bad key reaches no per-key or per-user bucket, so guessing is held
		// to a fixed allowance per client address instead.
		if ok, wait := s.limiter.allow("badkey:"+auth.ClientIP(r, s.Cfg.TrustProxy), badKeyPerMinute); !ok {
			s.writeKeyError(w, r, ops.RateLimited(wait))
			return r, false, true
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="scriobh"`)
		s.writeKeyError(w, r, &ops.Error{Code: ops.CodeUnauthenticated, Message: "That API key is not valid. It may be mistyped, revoked or expired."})
		return r, false, true
	}
	if ri, found := r.Context().Value(keyInfo).(*reqInfo); found {
		ri.principal = p.UserID + " key:" + info.ID
	}
	ctx := context.WithValue(r.Context(), keyAuth, reqAuth{P: p, KeyID: info.ID, KeyPrefix: info.Prefix, KeyName: info.Name, KeyExpires: info.ExpiresAt, cache: &reqCache{}})
	return r.WithContext(ctx), true, false
}

// writeKeyError answers a refused key in the shape of the endpoint it was sent to.
func (s *Server) writeKeyError(w http.ResponseWriter, r *http.Request, e *ops.Error) {
	if isMCPPath(r.URL.Path) {
		writeMCPFail(w, e)
		return
	}
	writeJSONError(w, e)
}

// whoami says who the caller is and what the caller may do. It is not an op:
// it must answer for a key pinned to one project, which the op rules would keep
// away from instance-wide ops, and the CLI uses it to know its own limits.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r.Context())
	p := a.P
	if !p.Authenticated() {
		writeJSONError(w, ops.Unauthenticated())
		return
	}
	type project struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Slug  string `json:"slug"`
	}
	type compendium struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type key struct {
		ID        string `json:"id"`
		Prefix    string `json:"prefix"`
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}
	out := struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Name  string `json:"name"`
			Role  string `json:"role"`
		} `json:"user"`
		Via          string   `json:"via"`
		Agent        bool     `json:"agent"`
		Scope        string   `json:"scope,omitempty"`
		Project      *project `json:"project,omitempty"`
		CompendiumID string   `json:"compendium_id,omitempty"`
		// Compendium is the one a pinned key works in: the pinned Compendium, or
		// the primary Compendium of the pinned project (absent when it has none).
		Compendium *compendium `json:"compendium,omitempty"`
		Key        *key        `json:"key,omitempty"`
		SchemaHash string      `json:"schema_hash"`
		Version    string      `json:"version"`
	}{Via: p.Via, Agent: p.IsAgent, Scope: p.Scope, CompendiumID: p.CompendiumID, SchemaHash: s.schema.Hash, Version: s.Cfg.Version}
	out.User.ID, out.User.Email, out.User.Name, out.User.Role = p.UserID, p.Email, p.Name, p.Role
	if p.ProjectID != "" {
		pr := project{ID: p.ProjectID}
		// The title and slug only while the key's owner can still open the project:
		// a person removed from it keeps a key pinned to it.
		if _, ok := s.Reg.Env().ProjectRole(r.Context(), p.UserID, p.ProjectID); ok {
			_ = s.DB.Read().QueryRowContext(r.Context(), `SELECT title, slug FROM projects WHERE id = ? AND deleted_at IS NULL`, p.ProjectID).Scan(&pr.Title, &pr.Slug)
		}
		out.Project = &pr
	}
	cid := p.CompendiumID
	if cid == "" && p.ProjectID != "" && s.Reg.Env().ProjectPrimary != nil {
		cid = s.Reg.Env().ProjectPrimary(r.Context(), p.ProjectID)
	}
	if _, member := s.Reg.Env().CompendiumRole(r.Context(), p.UserID, cid); cid != "" && member {
		cm := compendium{ID: cid}
		if err := s.DB.Read().QueryRowContext(r.Context(), `SELECT name FROM compendiums WHERE id = ? AND deleted_at IS NULL`, cid).Scan(&cm.Name); err == nil {
			out.Compendium = &cm
		}
	}
	if a.KeyID != "" {
		out.Key = &key{ID: a.KeyID, Prefix: a.KeyPrefix, Name: a.KeyName, ExpiresAt: a.KeyExpires}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
