package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// assets serves the embedded static files. Every file is also reachable under
// a content-hashed name (css/app.1a2b3c4d.css) with an immutable cache; the
// templates ask for the hashed name through the asset function, so a deploy
// never serves a stale stylesheet. Unhashed names (fonts referenced from CSS,
// later the manifest) get an ETag and a day of caching.
type assets struct {
	fsys   fs.FS
	hashed map[string]string // logical -> hashed, e.g. css/app.css -> css/app.1a2b.css
	byName map[string]file   // served name -> file
}

type file struct {
	data []byte
	etag string
	ctyp string
	imm  bool
}

func newAssets(static fs.FS) (*assets, error) {
	a := &assets{fsys: static, hashed: map[string]string{}, byName: map[string]file{}}
	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])[:8]
		ext := path.Ext(p)
		ctyp := mime.TypeByExtension(ext)
		switch ext {
		case ".woff2":
			ctyp = "font/woff2"
		case ".js", ".mjs":
			ctyp = "text/javascript; charset=utf-8"
		case ".css":
			ctyp = "text/css; charset=utf-8"
		case ".svg":
			ctyp = "image/svg+xml"
		case ".webmanifest":
			ctyp = "application/manifest+json"
		}
		hashedName := strings.TrimSuffix(p, ext) + "." + h + ext
		a.hashed[p] = hashedName
		a.byName[hashedName] = file{data: b, etag: `"` + h + `"`, ctyp: ctyp, imm: true}
		a.byName[p] = file{data: b, etag: `"` + h + `"`, ctyp: ctyp}
		return nil
	})
	return a, err
}

// URL returns the hashed URL for a logical asset name, or the plain one when
// the file does not exist (a template bug the template test catches).
func (a *assets) URL(logical string) string {
	if h, ok := a.hashed[logical]; ok {
		return "/static/" + h
	}
	return "/static/" + logical
}

// Has reports whether a logical asset exists.
func (a *assets) Has(logical string) bool { _, ok := a.hashed[logical]; return ok }

// ServeHTTP serves /static/*.
func (a *assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	f, ok := a.byName[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.ctyp)
	w.Header().Set("ETag", f.etag)
	if f.imm {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", itoa(len(f.data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(f.data)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
