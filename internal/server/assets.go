package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
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
	data   []byte
	gz     []byte
	etag   string
	gzEtag string
	ctyp   string
	imm    bool
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
		compress := false
		switch ext {
		case ".woff2":
			ctyp = "font/woff2"
		case ".js", ".mjs":
			ctyp = "text/javascript; charset=utf-8"
			compress = true
		case ".css":
			ctyp = "text/css; charset=utf-8"
			compress = true
		case ".svg":
			ctyp = "image/svg+xml"
			compress = true
		case ".webmanifest":
			ctyp = "application/manifest+json"
			compress = true
		}
		var gz []byte
		if compress && len(b) >= 256 {
			var buf bytes.Buffer
			if zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression); err == nil {
				_, werr := zw.Write(b)
				cerr := zw.Close()
				if werr == nil && cerr == nil && buf.Len() < len(b) {
					gz = buf.Bytes()
				}
			}
		}
		hashedName := strings.TrimSuffix(p, ext) + "." + h + ext
		a.hashed[p] = hashedName
		etag, gzEtag := `"`+h+`"`, `"`+h+`-gz"`
		a.byName[hashedName] = file{data: b, gz: gz, etag: etag, gzEtag: gzEtag, ctyp: ctyp, imm: true}
		a.byName[p] = file{data: b, gz: gz, etag: etag, gzEtag: gzEtag, ctyp: ctyp}
		return nil
	})
	return a, err
}

func acceptsGzip(ae string) bool {
	for _, part := range strings.Split(ae, ",") {
		enc, params, hasParams := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		if !hasParams {
			return true
		}
		for _, param := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(param, "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				return err == nil && q > 0
			}
		}
		return true
	}
	return false
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
	if f.imm {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	useGzip := len(f.gz) > 0 && acceptsGzip(r.Header.Get("Accept-Encoding"))
	if len(f.gz) > 0 {
		w.Header().Add("Vary", "Accept-Encoding")
	}
	body, etag := f.data, f.etag
	if useGzip {
		body, etag = f.gz, f.gzEtag
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
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
