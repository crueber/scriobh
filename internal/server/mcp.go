package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
)

// The Model Context Protocol endpoint, POST /mcp (docs/OPEN-QUESTIONS.md,
// "Phase 6 integration decisions").
//
//   - Authentication is an API key, in Authorization: Bearer (or X-API-Key, as
//     on /api/). A browser session cookie is never read here, so a page in another
//     tab can neither use nor ride on a signed-in person.
//   - A tool call is run as the op's own REST handler, in process, with the key's
//     principal: the same authorization, dry run, idempotency and body limits as a
//     request to /api/v1, and one code path for the sweeps to cover.
//   - It is stateless. No Mcp-Session-Id is ever issued (one that arrives is
//     ignored), GET and DELETE answer 405, and every POST is answered with one
//     application/json document. Nothing here streams, so a reverse proxy needs no
//     special setting for it.
//   - Origin is checked against the instance's own origin and SCRIOBH_MCP_ALLOWED_ORIGINS.
//     A request without an Origin header (every non-browser client) passes the
//     check and still needs a key; a browser's cross-origin request is refused
//     with 403 before the body is read (DNS rebinding makes the browser's Origin
//     and Host agree, so this is an allow-list, not a comparison with Host).

const mcpPath = "/mcp"

// isMCPPath reports whether a request is for the MCP endpoint.
func isMCPPath(p string) bool { return p == mcpPath }

// mcpState is the protocol engine and what the HTTP side needs around it.
type mcpState struct {
	srv     *mcp.Server
	catalog *mcp.Catalog
	origins map[string]bool
	groups  map[string]bool
}

// mountMCP builds the tool catalog from the op schema and registers the route.
// It must run after bindOps (the schema and every REST route exist by then).
func (s *Server) mountMCP() error {
	if s.Cfg.MCP.Off {
		return nil
	}
	cat, err := mcp.BuildCatalog(s.schema)
	if err != nil {
		return err
	}
	cfg := mcp.Config{
		Version: s.Cfg.Version, AdminTools: s.Cfg.MCP.AdminTools, PermanentTools: s.Cfg.MCP.PermanentTools,
		MaxResultBytes: s.Cfg.MCP.MaxResultKB << 10, MaxBatch: s.Cfg.MCP.MaxBatch, MaxBody: maxBody,
		CallTimeout: time.Duration(s.Cfg.MCP.CallTimeoutS) * time.Second,
		Allow:       s.mcpAllow,
		Observe: func(e mcp.Event) {
			s.Log.Debug("mcp", "method", e.Method, "tool", e.Tool, "op", e.Op, "status", e.Status, "dur", e.Dur.Round(time.Microsecond).String(), "key", e.KeyID)
		},
	}
	st := &mcpState{catalog: cat, origins: map[string]bool{}, groups: map[string]bool{}}
	st.srv = mcp.New(cfg, cat, &mcpInvoker{s: s, max: cfg.MaxResultBytes})
	for _, o := range s.Cfg.MCP.AllowedOrigins {
		st.origins[o] = true
	}
	for i := range cat.Tools {
		st.groups[cat.Tools[i].Group()] = true
	}
	s.mcp = st
	s.mux.HandleFunc("POST "+mcpPath, s.mcpPost)
	// Any other method: 405 with what is allowed. GET would open the legacy
	// server-to-client stream and DELETE would end a session; neither exists.
	s.mux.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeMCPError(w, http.StatusMethodNotAllowed, mcp.CodeInvalidRequest, "This endpoint takes POST only: it keeps no sessions and opens no streams.")
	})
	return nil
}

// MCPCatalog returns the tool catalog (nil when MCP is off), for tests and tools.
func (s *Server) MCPCatalog() *mcp.Catalog {
	if s.mcp == nil {
		return nil
	}
	return s.mcp.catalog
}

func writeMCPError(w http.ResponseWriter, status, code int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": code, "message": msg}})
	_, _ = w.Write(b)
}

// writeMCPFail renders a registry error (a refused key, a rate limit) as a JSON-RPC error with the HTTP status the op rules give it.
func writeMCPFail(w http.ResponseWriter, e *ops.Error) {
	if e.Code == ops.CodeRateLimited && e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	if e.Code == ops.CodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", `Bearer realm="scriobh"`)
	}
	code := mcp.CodeInvalidRequest
	if e.Code == ops.CodeInternal {
		code = mcp.CodeInternal
	}
	writeMCPError(w, e.Code.HTTPStatus(), code, e.Message)
}

// origin is scheme://host[:port], lower-cased, with the default port dropped.
func normalizeOrigin(o string) string {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") {
		return ""
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "http" && strings.HasSuffix(host, ":80")) || (u.Scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return u.Scheme + "://" + host
}

// mcpOriginOK applies the DNS-rebinding defence.
func (s *Server) mcpOriginOK(r *http.Request) bool {
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return true
	}
	if len(vals) > 1 {
		return false
	}
	o := normalizeOrigin(vals[0])
	if o == "" { // "null", a file page, junk
		return false
	}
	if o == normalizeOrigin(s.Cfg.BaseURLString()) || s.mcp.origins[o] {
		return true
	}
	return false
}

// mcpCORS lets a browser-based MCP client at one of the listed origins call /mcp:
// the preflight is answered for it (204, the origin echoed back, the headers a call
// needs, a short Max-Age), and every answer to it, errors included, names the origin
// that may read it. No wildcard and no credentials flag: the key travels in a header
// the page sets itself, and a cookie is never read here. Any other origin gets no
// CORS headers, and its preflight is a 403.
func (s *Server) mcpCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMCPPath(r.URL.Path) || s.mcp == nil {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		allowed := origin != "" && s.mcpOriginOK(r)
		if allowed {
			h.Set("Access-Control-Allow-Origin", normalizeOrigin(origin))
		}
		if r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed {
				writeMCPError(w, http.StatusForbidden, mcp.CodeInvalidRequest, "This origin may not call the MCP endpoint.")
				return
			}
			h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Session-Id, Last-Event-ID")
			h.Set("Access-Control-Max-Age", "300")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mcpAllow charges a batch's further messages to the key and its owner, the
// way the request middleware charges the request.
func (s *Server) mcpAllow(_ context.Context, id mcp.Identity) (bool, int) {
	if s.Cfg.RateLimit <= 0 {
		return true, 0
	}
	if ok, wait := s.limiter.allow("user:"+id.UserID, s.Cfg.RateLimit); !ok {
		return false, wait
	}
	return s.limiter.allow("key:"+id.KeyID, s.Cfg.RateLimit)
}

func (s *Server) mcpPost(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r.Context())
	if !s.mcpOriginOK(r) {
		s.Log.Warn("mcp: cross-origin request refused", "origin_present", true)
		writeMCPError(w, http.StatusForbidden, mcp.CodeInvalidRequest, "This origin may not call the MCP endpoint.")
		return
	}
	if a.KeyID == "" || !a.P.Authenticated() {
		writeMCPFail(w, ops.Unauthenticated())
		return
	}
	if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
		writeMCPError(w, http.StatusUnsupportedMediaType, mcp.CodeInvalidRequest, "Send the message as application/json.")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if asMaxBytes(err, &mbe) {
			writeMCPError(w, http.StatusRequestEntityTooLarge, mcp.CodeInvalidRequest, "The message is larger than this server accepts.")
			return
		}
		writeMCPError(w, http.StatusBadRequest, mcp.CodeParse, "The message could not be read.")
		return
	}
	filters, ferr := s.mcpFilters(r)
	if ferr != "" {
		writeMCPError(w, http.StatusBadRequest, mcp.CodeInvalidParams, ferr)
		return
	}
	out := s.mcp.srv.Handle(r.Context(), mcp.Input{
		Body:    body,
		Header:  r.Header.Get,
		Client:  auth.ClientIP(r, s.Cfg.TrustProxy),
		ID:      mcp.Identity{KeyID: a.KeyID, UserID: a.P.UserID, Scope: a.P.Scope, ProjectID: a.P.ProjectID, CompendiumID: a.P.CompendiumID},
		Filters: filters,
	})
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	if out.Body == nil {
		w.WriteHeader(out.Status)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}

// mcpPresets name sets of groups, so a client can ask for "the manuscript" or
// "the bible" without knowing every group.
var mcpPresets = map[string][]string{
	"core":  {"project", "doc", "outline", "search", "comment", "suggestion", "revision", "review", "goal", "stats"},
	"bible": {"compendium", "entity", "timeline", "mention", "scene"},
}

// mcpFilters reads ?groups=a,b and ?readonly=1 from the connection address.
func (s *Server) mcpFilters(r *http.Request) (mcp.Filters, string) {
	q := r.URL.Query()
	var f mcp.Filters
	switch strings.ToLower(q.Get("permanent")) {
	case "", "0", "false", "no", "off":
	case "1", "true", "yes", "on":
		f.Permanent = true
	default:
		return f, "permanent is 1 or 0."
	}
	switch strings.ToLower(q.Get("readonly")) {
	case "", "0", "false", "no", "off":
	case "1", "true", "yes", "on":
		f.ReadOnly = true
	default:
		return f, "readonly is 1 or 0."
	}
	if g := q.Get("groups"); g != "" {
		if len(g) > 300 {
			return f, "groups is too long."
		}
		seen := map[string]bool{}
		for _, name := range strings.Split(g, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			names := []string{name}
			if p, ok := mcpPresets[name]; ok {
				names = p
			} else if name == "all" {
				seen = nil
				f.Groups = nil
				return f, ""
			} else if !s.mcp.groups[name] {
				return f, "Unknown tool group " + strconv.Quote(safeLog(name)) + "."
			}
			for _, n := range names {
				if !seen[n] {
					seen[n] = true
					f.Groups = append(f.Groups, n)
				}
			}
		}
	}
	return f, ""
}

func safeLog(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// mcpInvoker runs a tool's request through the op's REST binding.
type mcpInvoker struct {
	s   *Server
	max int
}

// capRecorder is a response writer that keeps the status, the headers and at
// most max bytes of body.
type capRecorder struct {
	h        http.Header
	status   int
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (c *capRecorder) Header() http.Header { return c.h }

func (c *capRecorder) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *capRecorder) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if room := c.max + 1 - c.buf.Len(); room > 0 {
		if len(b) > room {
			c.buf.Write(b[:room])
		} else {
			c.buf.Write(b)
		}
	}
	if c.buf.Len() > c.max {
		c.overflow = true
	}
	return len(b), nil // a full buffer is not the handler's error: the result is refused afterwards
}

func (m *mcpInvoker) Invoke(ctx context.Context, id mcp.Identity, r mcp.Request) (resp mcp.Response) {
	u := r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		return mcp.Response{Status: http.StatusBadRequest, Body: []byte(`{"error":{"code":"invalid","message":"The request could not be built."}}`)}
	}
	req.RequestURI = u
	req.RemoteAddr = "127.0.0.1:0"
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(r.Body))
	}
	if r.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	}
	if r.DryRun {
		req.Header.Set("X-Scriobh-Dry-Run", "1")
	}
	rec := &capRecorder{h: http.Header{}, max: m.max}
	req.Body = http.MaxBytesReader(rec, req.Body, m.s.bodyLimit(req))
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			m.s.Log.Error("mcp: panic in a tool call", "op", r.Op, "panic", v)
			resp = mcp.Response{Status: http.StatusInternalServerError, Body: []byte(`{"error":{"code":"internal","message":"Something went wrong."}}`)}
		}
	}()
	m.s.mux.ServeHTTP(rec, req)
	ra, _ := strconv.Atoi(rec.h.Get("Retry-After"))
	return mcp.Response{
		Status: rec.status, ContentType: rec.h.Get("Content-Type"), Disposition: rec.h.Get("Content-Disposition"),
		Body: rec.buf.Bytes(), Truncated: rec.overflow, RetryAfter: ra,
	}
}
