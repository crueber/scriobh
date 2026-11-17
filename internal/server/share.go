package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/ops"
)

// The public share routes, /s/{token}/...: read-only and comment-only access with
// no account. They are deliberately not registry ops (see internal/domain/share):
// a caller here is a link, not a person, and none of the machinery that is about
// people (sessions, roles, API-key scopes and pins) applies. What replaces it is
// small and is tested by enumeration (share_test.go walks shareEndpoints):
//
//   - One entry point, shareHandler, in front of every endpoint: the per-address
//     rate limit first (before the token is looked at), then the token, then the
//     per-link rate limit, then the passphrase proof, then the endpoint. Nothing
//     can be added to the table without passing through it.
//   - The session and API-key middleware are skipped for /s/ (middleware.go), so a
//     session cookie or a key sent along is ignored and nothing is ever set: there
//     is no Set-Cookie on any /s/ response.
//   - Every response carries no-referrer and noindex (securityHeaders), no
//     caching, and the same strict CSP as every page. The token is in the path and
//     in no log line: the request log records the route pattern, never the path.
//   - Every token that does not open a link, and every path under /s/ that is not an
//     endpoint, gets the same 404 with the same body.
//
// The endpoints answer JSON; the screens that read it are the UI's.

// shareEndpoint is one public route.
type shareEndpoint struct {
	Method string
	Path   string // the mux pattern, always /s/{token}...
	// Open marks the endpoint a passphrase-protected link answers without a proof
	// (the one that asks for it).
	Open bool
	Fn   func(s *Server, w http.ResponseWriter, r *http.Request, l share.Link)
}

// shareEndpoints is every route under /s/. The sweep test walks it, and checks
// that the mux has no other /s/ route.
var shareEndpoints = []shareEndpoint{
	{Method: "GET", Path: "/s/{token}", Fn: (*Server).shareManifest},
	{Method: "GET", Path: "/s/{token}/api/manifest", Fn: (*Server).shareManifest},
	{Method: "POST", Path: "/s/{token}/api/unlock", Open: true, Fn: (*Server).shareUnlock},
	{Method: "GET", Path: "/s/{token}/api/docs/{docid}", Fn: (*Server).shareDoc},
	{Method: "GET", Path: "/s/{token}/api/comments", Fn: (*Server).shareThreads},
	{Method: "POST", Path: "/s/{token}/api/comments", Fn: (*Server).shareComment},
	{Method: "GET", Path: "/s/{token}/api/download", Fn: (*Server).shareDownload},
}

// shareState is what the public routes keep: the use counters and the proof key.
type shareState struct {
	meter     *share.Meter
	downloads *share.Downloads
	key       []byte
}

// shareClient is the part of a client address the per-address limits and the passphrase counts
// are kept by: an IPv4 address whole, an IPv6 address by its /64 (one household or one machine
// owns a /64, and a rotating suffix must not be a way round a limit).
func shareClient(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}

func (s *Server) registerShare(m *http.ServeMux) {
	byPath := map[string][]shareEndpoint{}
	var paths []string
	for _, e := range shareEndpoints {
		if _, seen := byPath[e.Path]; !seen {
			paths = append(paths, e.Path)
		}
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	for _, p := range paths {
		// No method in the pattern: the mux would answer another method with a 405
		// of its own, and every wrong request here gets the same 404.
		m.HandleFunc(p, s.shareHandler(byPath[p]))
	}
	m.HandleFunc("/s/", s.shareGone) // every other path under /s/
}

// shareGone is the one answer for a token that opens nothing, a path that is not an
// endpoint and a method an endpoint does not take.
func (s *Server) shareGone(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) { // a browser that navigated here: the page that says nothing about why
		s.shareGonePage(w, r)
		return
	}
	writeJSONError(w, ops.NotFound("link"))
}

func shareError(w http.ResponseWriter, e *ops.Error) { writeJSONError(w, e) }

func (s *Server) shareHandler(eps []shareEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var e *shareEndpoint
		for i := range eps {
			if eps[i].Method == r.Method {
				e = &eps[i]
			}
		}
		if e == nil {
			s.shareGone(w, r)
			return
		}
		ip := shareClient(auth.ClientIP(r, s.Cfg.TrustProxy))
		if per := s.Cfg.ShareRateLimit; per > 0 {
			if ok, wait := s.limiter.allow("shareip:"+ip, per); !ok {
				shareError(w, ops.RateLimited(wait))
				return
			}
		}
		l, err := share.Authenticate(r.Context(), s.DB, s.shr.meter, r.PathValue("token"))
		if err != nil {
			if errors.Is(err, share.ErrNotFound) {
				s.shareGone(w, r)
				return
			}
			s.Log.Error("share lookup", "err", err)
			shareError(w, ops.Internal(err))
			return
		}
		// A navigation to the link itself gets the page, which holds nothing of the project and so is not locked.
		if wantsHTML(r) && e.Path == "/s/{token}" {
			s.shareShell(w, r, l)
			return
		}
		// A passphrase link's own allowance is spent only by callers who hold the proof: someone who
		// has the token but not the passphrase can spend their own address's allowance and nothing
		// else, so they cannot use up the readers'.
		if l.HasPassphrase && !e.Open && !share.CheckProof(s.shr.key, l, r.Header.Get("X-Share-Unlock"), db.NowTime()) {
			shareError(w, &ops.Error{Code: ops.CodeUnauthenticated, Message: "This link needs its passphrase: send it to /api/unlock, then send the proof in X-Share-Unlock."})
			return
		}
		if per := s.Cfg.ShareLinkRate; per > 0 && !(l.HasPassphrase && e.Open) {
			if ok, wait := s.limiter.allow("sharelink:"+l.ID, per); !ok {
				shareError(w, ops.RateLimited(wait))
				return
			}
		}
		s.shr.meter.Used(l.ID, false)
		s.shr.meter.FlushSoon()
		e.Fn(s, w, r, l)
	}
}

func shareJSON(w http.ResponseWriter, status int, v any) {
	writeJSONBody(w, status, encodeJSON(v), false)
}

func (s *Server) shareManifest(w http.ResponseWriter, r *http.Request, l share.Link) {
	out, err := share.Manifest(r.Context(), s.DB, l)
	if err != nil {
		shareError(w, ops.AsError(err))
		return
	}
	s.shr.meter.Used(l.ID, true) // opening the contents is a view
	shareJSON(w, http.StatusOK, out)
}

// UnlockIn is the passphrase.
type shareUnlockIn struct {
	Passphrase string `json:"passphrase"`
}

type shareUnlockOut struct {
	Proof string `json:"proof,omitempty" desc:"Send this in the X-Share-Unlock header of every later request. Valid eight hours; void if the passphrase is changed or the link revoked."`
}

func readJSON(r *http.Request, v any) *ops.Error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if asMaxBytes(err, &mbe) {
			return ops.TooLarge()
		}
		return ops.Invalid("", "Could not read the request body.")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ops.Invalid("", "Malformed JSON.")
	}
	if _, err := dec.Token(); err != io.EOF {
		return ops.Invalid("", "Unexpected data after the JSON body.")
	}
	return nil
}

func (s *Server) shareUnlock(w http.ResponseWriter, r *http.Request, l share.Link) {
	// Wrong passphrases are the one thing a stranger can do that costs memory: a
	// tighter bucket per address, on top of the lockout per link.
	ip := shareClient(auth.ClientIP(r, s.Cfg.TrustProxy))
	if ok, wait := s.limiter.allow("shareunlock:"+ip, 12); !ok {
		shareError(w, ops.RateLimited(wait))
		return
	}
	var in shareUnlockIn
	if e := readJSON(r, &in); e != nil {
		shareError(w, e)
		return
	}
	proof, err := share.Unlock(r.Context(), s.DB, s.shr.key, l, s.Sess.IPHash(ip), in.Passphrase)
	var locked *share.LockedError
	switch {
	case errors.As(err, &locked):
		shareError(w, ops.RateLimited(int(locked.RetryAfter.Seconds())+1))
	case errors.Is(err, share.ErrBusy):
		shareError(w, ops.RateLimited(2))
	case errors.Is(err, share.ErrBadPassphrase):
		shareError(w, &ops.Error{Code: ops.CodeUnauthenticated, Message: "That passphrase is not right."})
	case errors.Is(err, share.ErrNotFound):
		s.shareGone(w, r)
	case err != nil:
		s.Log.Error("share unlock", "err", err)
		shareError(w, ops.Internal(err))
	default:
		shareJSON(w, http.StatusOK, shareUnlockOut{Proof: proof})
	}
}

func (s *Server) shareDoc(w http.ResponseWriter, r *http.Request, l share.Link) {
	id := r.PathValue("docid")
	if !db.ValidID(id) {
		s.shareGone(w, r)
		return
	}
	out, err := share.Document(r.Context(), s.DB, l, id)
	switch {
	case errors.Is(err, share.ErrNoDocument):
		s.shareGone(w, r)
	case err != nil:
		s.Log.Error("share document", "err", err)
		shareError(w, ops.Internal(err))
	default:
		shareJSON(w, http.StatusOK, out)
	}
}

func (s *Server) shareThreads(w http.ResponseWriter, r *http.Request, l share.Link) {
	if !l.CanComment() {
		shareError(w, ops.Forbidden("This link is for reading only."))
		return
	}
	id := r.URL.Query().Get("document_id")
	if !db.ValidID(id) {
		s.shareGone(w, r)
		return
	}
	out, err := share.ThreadsOf(r.Context(), s.DB, l, id)
	switch {
	case errors.Is(err, share.ErrNoDocument):
		s.shareGone(w, r)
	case err != nil:
		s.Log.Error("share threads", "err", err)
		shareError(w, ops.Internal(err))
	default:
		shareJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

func (s *Server) shareComment(w http.ResponseWriter, r *http.Request, l share.Link) {
	if !l.CanComment() {
		shareError(w, ops.Forbidden("This link is for reading only."))
		return
	}
	if ok, wait := s.limiter.allow("sharepost:"+l.ID, 20); !ok {
		shareError(w, ops.RateLimited(wait))
		return
	}
	var in share.GuestIn
	if e := readJSON(r, &in); e != nil {
		shareError(w, e)
		return
	}
	out, err := share.AddComment(r.Context(), s.DB, s.shr.meter, s.Events, l, in, s.Cfg.ShareCommentsPerHour)
	var rate *share.RateError
	switch {
	case errors.As(err, &rate):
		shareError(w, ops.RateLimited(int(rate.RetryAfter.Seconds())+1))
	case err != nil:
		ae := ops.AsError(err)
		if ae.Code == ops.CodeInternal {
			s.Log.Error("share comment", "err", ae.Cause())
		}
		shareError(w, ae)
	case out.Duplicate:
		shareJSON(w, http.StatusOK, out)
	default:
		shareJSON(w, http.StatusCreated, out)
	}
}

func (s *Server) shareDownload(w http.ResponseWriter, r *http.Request, l share.Link) {
	if !l.AllowDownload {
		s.shareGone(w, r)
		return
	}
	name, body, err := s.shr.downloads.Get(r.Context(), s.DB, l)
	if errors.Is(err, share.ErrBusy) {
		shareError(w, &ops.Error{Code: ops.CodeRateLimited, Message: "The download is being prepared for others; try again in a moment.", RetryAfterSeconds: 3})
		return
	}
	if err != nil {
		s.Log.Error("share download", "err", err)
		shareError(w, ops.Internal(err))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("Content-Disposition", ops.ContentDisposition(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}
