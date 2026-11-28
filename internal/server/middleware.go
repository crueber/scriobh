package server

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// csp is the Content-Security-Policy. No inline scripts, no inline styles, no
// eval, nothing from outside. If an exception is ever needed it is a hash
// with a comment, not a loosened directive.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// maxBody is the default request body limit. Uploads and imports get explicit
// per-op overrides later.
const maxBody = 2 << 20

type ctxKey int

const (
	keyAuth ctxKey = iota
	keyReqID
	keyInfo
	keyGateMark
)

// reqInfo is filled in by inner layers so the outer request log can report
// the matched route and the principal (inner layers see copies of *http.Request).
type reqInfo struct {
	route     string
	principal string
}

// reqCache caches per-request read-only user lookups (prefs, language, timezone,
// compendium addresses) so handlers and s.tr() never hit SQLite repeatedly for
// the same user during a single request.
type reqCache struct {
	mu          sync.Mutex
	prefsDone   bool
	prefs       map[string]string
	langDone    bool
	lang, uiDir string
	zoneDone    bool
	zone        *time.Location
	compAddrs   map[string]string
	compDone    bool
}

// reqAuth is what the session middleware learned about the caller.
type reqAuth struct {
	P         ops.Principal
	SessionID string
	CSRF      string
	// Set when the caller signed in with an API key.
	KeyID, KeyPrefix, KeyName, KeyExpires string
	cache                                 *reqCache
}

func authFrom(ctx context.Context) reqAuth {
	a, _ := ctx.Value(keyAuth).(reqAuth)
	return a
}

// statusWriter records the status for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = 200, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// chain wraps the mux with every cross-cutting concern, outermost first.
func (s *Server) chain(next http.Handler) http.Handler {
	h := next
	h = s.crossOrigin().Handler(h)
	h = s.limits(h)
	h = s.sessions(h) // before limits: buckets are per principal when signed in
	h = s.securityHeaders(h)
	h = s.desktopHost(h) // desktop mode only: nothing but this instance's own Host gets further
	h = s.mcpCORS(h)     // outside the key check, so a preflight (which carries no key) and a refusal can be answered to the page
	h = s.logging(h)
	h = s.recoverer(h)
	return h
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("panic", "panic", v, "stack", string(debug.Stack()), "route", s.routeOf(r))
				s.fail(w, r, ops.Internal(nil))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logging writes one structured line per request: method, route, status,
// duration and principal. Never bodies, queries, prompts or manuscript text.
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		id := db.NewID("req")
		w.Header().Set("X-Request-Id", id)
		info := &reqInfo{}
		ctx := context.WithValue(context.WithValue(r.Context(), keyReqID, id), keyInfo, info)
		next.ServeHTTP(sw, r.WithContext(ctx))
		if r.URL.Path == "/healthz" && sw.status == 200 {
			return
		}
		route := info.route
		if route == "" {
			route = "unmatched"
		}
		s.Log.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("method", r.Method), slog.String("route", route), slog.Int("status", sw.status),
			slog.String("dur", time.Since(start).Round(time.Microsecond).String()),
			slog.String("principal", info.principal), slog.String("req", id))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if isSharePath(r.URL.Path) {
			// The token is in the address: it must not leave in a Referer, and a link
			// must not be indexed.
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		}
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if s.Cfg.Secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			// Nothing but the static assets may be kept by a browser or a shared
			// cache: pages carry manuscript text, and Back must not show an old copy.
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimit is the request body limit for r: the default, or the larger limit
// an upload op declares for its own route. Nothing is read here; the REST
// handler authorizes the caller before it touches the body.
func (s *Server) bodyLimit(r *http.Request) int64 {
	if isSharePath(r.URL.Path) {
		return 32 << 10 // a guest writes a comment, not a manuscript
	}
	if r.Method == http.MethodPost && isCoverForm(r.URL.Path) && isMultipart(r) {
		// the cover page's one form carries up to three pictures (front, back, wrap), each up to the upload limit; the
		// body is read a part at a time after the caller is authorized (uiRouteStream), never held whole
		return 3*int64(s.Cfg.UploadMaxMB)<<20 + 1<<20
	}
	if r.Method == http.MethodPost && isImportStage(r.URL.Path) && isMultipart(r) {
		// the import chooser's form carries one file (or a folder's files), read a part at a time and streamed to disk
		return importBodyLimit
	}
	for _, e := range s.bigBody {
		if e.Op.Method == r.Method && pathMatches(e.Op.Path, r.URL.Path) {
			return e.Op.MaxBody
		}
	}
	return maxBody
}

// limits caps request bodies and applies per-principal token buckets, with a
// stricter bucket for sign-in.
func (s *Server) limits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			r.Body = http.MaxBytesReader(w, r.Body, s.bodyLimit(r))
		}
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ip := auth.ClientIP(r, s.Cfg.TrustProxy)
		bucket, per := "ip:"+ip, s.Cfg.RateLimit
		if a := authFrom(r.Context()); a.KeyID != "" {
			// A key spends its own allowance and its owner's, so a person who
			// holds many keys has one allowance between them, not one each.
			bucket = "key:" + a.KeyID
			if s.Cfg.RateLimit > 0 {
				if ok, wait := s.limiter.allow("user:"+a.P.UserID, s.Cfg.RateLimit); !ok {
					s.fail(w, r, ops.RateLimited(wait))
					return
				}
			}
		} else if a.P.UserID != "" {
			bucket = "user:" + a.P.UserID
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") && !s.Cfg.DesktopEmbedded() {
			// The stricter sign-in bucket is per address, against guessing at a provider. A desktop instance on its
			// embedded issuer has one address (this computer) and nothing to guess: the launch cookie decides who signs
			// in, and the ordinary bucket above still applies.
			bucket, per = "auth:"+ip, 30
		}
		if per > 0 {
			if ok, wait := s.limiter.allow(bucket, per); !ok {
				s.fail(w, r, ops.RateLimited(wait))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sessions attaches the signed-in principal, if any.
func (s *Server) sessions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Embedded static assets and health probes never use session auth; skipping
		// avoids per-asset SQLite session lookups and writer-lock touch stampedes.
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if isSharePath(r.URL.Path) {
			// A share link is not a person: a session cookie or an API key sent to /s/ is
			// not looked at, so nothing can be learned from it and nothing is set.
			next.ServeHTTP(w, r)
			return
		}
		if kr, ok, handled := s.keyAuth(w, r); handled {
			return
		} else if ok {
			next.ServeHTTP(w, kr)
			return
		}
		if isMCPPath(r.URL.Path) {
			// The MCP endpoint is for a key and nothing else: a cookie is not read.
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(auth.SessionCookie); err == nil {
			lookup := s.Sess.Lookup
			if isPresencePath(r.URL.Path) {
				lookup = s.Sess.Peek // a presence beat is not activity: see presence.go
			}
			p, info, err := lookup(r.Context(), c.Value)
			switch err {
			case nil:
				ra := reqAuth{P: p, SessionID: info.ID, CSRF: s.Sess.CSRFToken(info.ID), cache: &reqCache{}}
				if ri, ok := r.Context().Value(keyInfo).(*reqInfo); ok {
					ri.principal = p.UserID
				}
				r = r.WithContext(context.WithValue(r.Context(), keyAuth, ra))
			case auth.ErrNoSession:
				s.Sess.ClearCookie(w)
			default:
				s.Log.Error("session lookup", "err", err)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limiter is a per-key token bucket with a periodic sweep.
type limiter struct {
	mu    sync.Mutex
	m     map[string]*entry
	nextS time.Time
	// now is the clock; tests inject one.
	now func() time.Time
}

type entry struct {
	l    *rate.Limiter
	seen time.Time
	per  int
}

func newLimiter() *limiter { return &limiter{m: map[string]*entry{}, now: time.Now} }

// allow takes one token from key's bucket of `per` requests per minute (burst
// of per/4, at least 10). It returns the seconds to wait when refused.
func (l *limiter) allow(key string, per int) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.After(l.nextS) {
		for k, e := range l.m {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.m, k)
			}
		}
		l.nextS = now.Add(time.Minute)
	}
	e := l.m[key]
	if e == nil || e.per != per {
		burst := per / 4
		if burst < 10 {
			burst = 10
		}
		e = &entry{l: rate.NewLimiter(rate.Limit(float64(per)/60), burst), per: per}
		l.m[key] = e
	}
	e.seen = now
	res := e.l.ReserveN(now, 1)
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		secs := int(d.Seconds()) + 1
		return false, secs
	}
	return true, 0
}

// crossOrigin is the standard library's Sec-Fetch-Site / Origin check for
// every mutation. It answers in the app's own JSON error shape. When the base
// URL is explicit it is also a trusted origin, so a proxy that rewrites Host
// does not turn every browser POST into a 403.
func (s *Server) crossOrigin() *http.CrossOriginProtection {
	cop := http.NewCrossOriginProtection()
	// The MCP endpoint answers for its own origins: it takes a key and no cookie, and
	// checks Origin against an allow-list of its own (mcp.go), so a browser origin that
	// may call it is not thereby trusted with the pages.
	cop.AddInsecureBypassPattern(mcpPath)
	if s.Cfg.BaseURL != "" {
		_ = cop.AddTrustedOrigin(s.Cfg.BaseURL)
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Log.Warn("cross-origin request refused", "method", r.Method, "path", logPath(r), "site", r.Header.Get("Sec-Fetch-Site"))
		if isMCPPath(r.URL.Path) {
			writeMCPFail(w, ops.Forbidden("Cross-origin requests are not allowed."))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSONError(w, ops.Forbidden("Cross-origin requests are not allowed."))
			return
		}
		s.fail(w, r, ops.Forbidden("Cross-origin requests are not allowed."))
	}))
	return cop
}

// isSharePath reports whether a request path is under /s/, the public share routes, or /card/, the private progress
// cards: both are a link and not a person, with a token in the address.
func isSharePath(p string) bool { return strings.HasPrefix(p, "/s/") || strings.HasPrefix(p, "/card/") }

// routeOf is the route pattern a request matched ("GET /p/{slug}/read"), never its path: a path holds slugs, ids and,
// for a share link, the token. A request no route matched is "unmatched".
func (s *Server) routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	if _, p := s.mux.Handler(r); p != "" {
		return p
	}
	return "unmatched"
}

// logPath is the path of a request as it may appear in a log line: a share link's
// token is in its address, so the address of a /s/ route is never logged.
func logPath(r *http.Request) string {
	if isSharePath(r.URL.Path) {
		if strings.HasPrefix(r.URL.Path, "/card/") {
			return "/card/..."
		}
		return "/s/..."
	}
	return r.URL.Path
}

// isMultipart says whether a request's body is multipart/form-data.
func isMultipart(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "multipart/form-data"
}
