package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/pwa"
)

// The installable shell. Four routes at the root, none of them behind sign-in
// and none of them carrying anything about a person:
//
//	/manifest.webmanifest  the web app manifest
//	/sw.js                 the service worker, at the root so its scope is the origin
//	/offline               the page shown when the network cannot be reached
//	/pwa/icon-*.png        install icons, drawn by internal/pwa
//
// The worker keeps /static and the offline page, and nothing else (see
// web/static/sw/worker.js and docs/OPERATIONS.md for why no page is cached).

// offlinePath is the one page the worker keeps.
const offlinePath = "/offline"

type pwaState struct {
	worker   []byte
	version  string
	offline  []byte
	manifest []byte
}

// precache lists the URLs the worker keeps: every static file by its
// content-hashed name, and the fonts also by the plain name the stylesheet asks
// for. The worker's own source is not among them.
func (a *assets) precache() []string {
	var logical []string
	for l := range a.hashed {
		if !strings.HasPrefix(l, "sw/") {
			logical = append(logical, l)
		}
	}
	sort.Strings(logical)
	var out []string
	for _, l := range logical {
		out = append(out, "/static/"+a.hashed[l])
		if strings.HasPrefix(l, "fonts/") {
			out = append(out, "/static/"+l)
		}
	}
	return out
}

// initPWA builds the worker, the manifest and the offline page once, after the
// assets and templates exist. The worker's version is a hash of everything it
// keeps (so any changed file, or a changed offline page, is a new cache and a
// new worker) and of its own source.
func (s *Server) initPWA() error {
	src, ok := s.assets.byName["sw/worker.js"]
	if !ok {
		return errMissingWorker
	}
	offline, err := s.renderBytes("offline", Page{Title: "Offline", Base: s.Cfg.BaseURLString(), Version: s.Cfg.Version})
	if err != nil {
		return err
	}
	list := s.assets.precache()
	h := sha256.New()
	h.Write(src.data)
	h.Write(offline)
	for _, u := range list {
		h.Write([]byte(u))
		h.Write([]byte{0})
	}
	version := hex.EncodeToString(h.Sum(nil))[:16]
	cfg, _ := json.Marshal(map[string]any{"version": version, "static": list, "offline": offlinePath})
	worker := append([]byte("var CONFIG = "+string(cfg)+";\n"), src.data...)

	manifest, _ := json.MarshalIndent(map[string]any{
		"id": "/", "name": "Scríobh", "short_name": "Scríobh",
		"description":      "A quiet room for the long thing you are writing.",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "any",
		"lang":             "en",
		"background_color": "#FBFAF7",
		"theme_color":      "#FBFAF7",
		"categories":       []string{"productivity"},
		"icons": []map[string]string{
			{"src": "/pwa/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/pwa/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/pwa/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
			{"src": "/static/icons/favicon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
		},
	}, "", "  ")
	s.pwa = &pwaState{worker: worker, version: version, offline: offline, manifest: append(manifest, '\n')}
	return nil
}

var errMissingWorker = &pwaError{"the service worker source web/static/sw/worker.js is not embedded"}

type pwaError struct{ msg string }

func (e *pwaError) Error() string { return e.msg }

func (s *Server) pwaRoutes() {
	m := s.mux
	m.HandleFunc("GET /sw.js", s.serviceWorker)
	m.HandleFunc("GET /manifest.webmanifest", s.webManifest)
	m.HandleFunc("GET "+offlinePath, s.offlinePage)
	m.HandleFunc("GET /pwa/{name}", s.pwaIcon)
}

// serviceWorker serves the worker. It must never be cached by the browser's
// HTTP cache (an update has to be seen at the next check), and its scope is the
// root, which is where it lives.
func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("Service-Worker-Allowed", "/")
	h.Set("Content-Length", strconv.Itoa(len(s.pwa.worker)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(s.pwa.worker)
	}
}

func (s *Server) webManifest(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/manifest+json")
	h.Set("Cache-Control", "public, no-cache")
	h.Set("Content-Length", strconv.Itoa(len(s.pwa.manifest)))
	_, _ = w.Write(s.pwa.manifest)
}

// offlinePage is the same bytes for everyone, whoever asks and whatever cookie
// they send: it is built once at start without a session.
func (s *Server) offlinePage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "public, no-cache")
	h.Set("Content-Length", strconv.Itoa(len(s.pwa.offline)))
	_, _ = w.Write(s.pwa.offline)
}

var icons = map[string]struct {
	size     int
	maskable bool
}{
	"icon-180.png":          {180, false},
	"icon-192.png":          {192, false},
	"icon-512.png":          {512, false},
	"icon-maskable-512.png": {512, true},
}

func (s *Server) pwaIcon(w http.ResponseWriter, r *http.Request) {
	ic, ok := icons[r.PathValue("name")]
	if !ok {
		s.notFound(w, r)
		return
	}
	b := pwa.Cached(ic.size, ic.maskable)
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
