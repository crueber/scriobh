package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// GET /files/{id} serves an attachment's bytes to a signed-in person who can
// read the project or Compendium it belongs to. It is the address a document's
// Markdown uses (![alt](/files/att_...)).
//
// Everything about the response is chosen here, from the stored row; nothing
// comes from the request except which attachment and whether to force a
// download:
//
//   - the type is the one detected at upload, never sniffed again (nosniff), and
//     text is always served as text/plain;
//   - only images and PDFs are shown inline; anything else is a download;
//   - the response carries a restrictive Content-Security-Policy: scripts, styles
//     from elsewhere and embedding are off, and everything but a PDF is also
//     sandboxed (a sandbox stops the browsers' PDF viewers);
//   - it is private and revalidated on every use (an ETag, so a repeat is a 304
//     that is still authorized), and ranges work for large PDFs;
//   - the path on disk is rebuilt from the row's owner, hash and extension.
//
// A missing attachment and one the caller cannot read are the same 404. The
// route takes sessions only: an API key reads attachments through the download
// ops.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	p := authFrom(r.Context()).P
	if !p.Authenticated() {
		writeJSONError(w, ops.Unauthenticated())
		return
	}
	id := r.PathValue("id")
	notFound := func() { writeJSONError(w, ops.NotFound("file")) }
	kind, owner, ok := attachments.Locate(r.Context(), s.DB, id)
	if !ok {
		notFound()
		return
	}
	op := "project.get"
	if kind == attachments.OwnerCompendium {
		op = "compendium.get"
	}
	if err := s.authorizeRead(r.Context(), p, op, owner); err != nil {
		notFound()
		return
	}
	att, path, err := attachments.Open(r.Context(), s.Reg.Env(), kind, owner, id)
	if err != nil {
		notFound()
		return
	}
	f, fi, err := attachments.OpenRegular(s.Cfg.DataDir, path)
	if err != nil {
		notFound() // missing, a link, or planted by hand: all the same
		return
	}
	defer f.Close()
	h := w.Header()
	inline := (att.Kind == "image" || att.Kind == "pdf") && r.URL.Query().Get("download") == ""
	switch att.Kind {
	case "text":
		h.Set("Content-Type", "text/plain; charset=utf-8")
	default:
		h.Set("Content-Type", att.Mime)
	}
	disp := ops.ContentDisposition(att.Filename)
	if inline {
		disp = strings.Replace(disp, "attachment;", "inline;", 1)
	}
	h.Set("Content-Disposition", disp)
	h.Set("X-Content-Type-Options", "nosniff")
	if att.Kind == "pdf" {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
		h.Set("X-Frame-Options", "SAMEORIGIN")
	} else {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", `"`+att.SHA256+`"`)
	mod := fi.ModTime()
	if t, err := db.Parse(att.CreatedAt); err == nil {
		mod = t
	}
	if mod.After(time.Now()) {
		mod = time.Now()
	}
	http.ServeContent(w, r, "", mod, f)
}
