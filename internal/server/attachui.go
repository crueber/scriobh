package server

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/ops"
)

// The attachment manager: /p/{slug}/files and /c/{slug}/files list what a project or a Compendium holds, and
// /p/{slug}/files/{id} is one file: its preview, its name and alt text, where it is used, the Markdown to copy and
// the delete. Everything is read through the attachment ops and every change goes through them; uploading is done by
// js/attach.js straight to the upload op, because the file is the request body. The pages work without script for
// everything but uploading.

// cspFrames is the page policy of the manager pages: the same as every page's, plus frame-src 'self', so that a PDF
// can be shown in a panel on request. Nothing else on this origin can be framed (every other response forbids it with
// frame-ancestors 'none' and X-Frame-Options DENY; the file route allows only 'self', and only for a PDF), and the
// policy of the rest of the app is unchanged.
var cspFrames = strings.Replace(csp, "frame-ancestors 'none'", "frame-src 'self'; frame-ancestors 'none'", 1)

const attPageSize = 48

// attOwner is the project or Compendium whose files a page shows.
type attOwner struct {
	Kind     string // "project" or "compendium"
	Word     string // the same, as a person says it: project or Compendium
	ID       string
	Slug     string
	Name     string
	Base     string // /p/{slug} or /c/{slug}
	Prefix   string // the op names: project.attachment. or compendium.attachment.
	UI       string // /ui/projects/{id} or /ui/compendiums/{id}
	API      string // the upload route
	CanEdit  bool
	IsOwner  bool
	Compend  bool
	ListPath string // the fragment route
}

func newAttOwner(kind, id, slug, name string, canEdit, isOwner bool) attOwner {
	o := attOwner{Kind: kind, Word: "project", ID: id, Slug: slug, Name: name, CanEdit: canEdit, IsOwner: isOwner}
	if kind == attachments.OwnerCompendium {
		o.Compend, o.Word = true, "Compendium"
		o.Base, o.Prefix, o.UI = "/c/"+slug, "compendium.attachment.", "/ui/compendiums/"+id
		o.API = "/api/v1/compendiums/" + id + "/attachments"
	} else {
		o.Base, o.Prefix, o.UI = "/p/"+slug, "project.attachment.", "/ui/projects/"+id
		o.API = "/api/v1/projects/" + id + "/attachments"
	}
	o.ListPath = o.UI + "/files/list"
	return o
}

type attRow struct {
	attachments.Attachment
	KindLabel string
	Size      string
	When      string
	Markdown  string
	Shown     template.HTML // the Markdown as drawn: its own "!" is written as an entity, since it is syntax and not copy
	IsImage   bool
}

type attUsage struct {
	Used, Limit   string
	Files         int
	Pct           int
	HasLimit      bool
	You, YouLimit string
	YouHasLimit   bool
	YouPct        int
	MaxFile       string
	MaxFileBytes  int64
	Near          bool
	Full          bool
}

type attPageData struct {
	Owner     attOwner
	Project   any // the project, for the page's own crumb and tabs (nil for a Compendium)
	Compend   any // the Compendium, for the crumb (nil for a project)
	Slug      string
	CSRF      string
	Rows      []attRow
	Next      string
	Cursor    string
	Usage     attUsage
	Saved     string
	Error     string
	Poll      bool
	Kind      string
	ListOnly  bool
	TooMany   bool
	MaxAccept string
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func mdEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

func attKindLabel(k string) string {
	switch k {
	case "image":
		return "Image"
	case "pdf":
		return "PDF"
	}
	return "Text"
}

// attMarkdown is what to paste to use a file in a chapter: an image is drawn, anything else is a link.
func attMarkdown(a attachments.Attachment) string {
	name := a.Alt
	if name == "" {
		name = a.Filename
	}
	if a.Kind == "image" {
		return "![" + mdEscape(name) + "](" + a.URL + ")"
	}
	return "[" + mdEscape(a.Filename) + "](" + a.URL + ")"
}

func attRowOf(a attachments.Attachment, loc *time.Location) attRow {
	md := attMarkdown(a)
	shown := html.EscapeString(md)
	if strings.HasPrefix(md, "!") {
		shown = "&excl;" + html.EscapeString(md[1:])
	}
	return attRow{Attachment: a, KindLabel: attKindLabel(a.Kind), Size: fmtBytes(a.Bytes), When: whenIn(a.CreatedAt, loc), Markdown: md, Shown: template.HTML(shown), IsImage: a.Kind == "image"}
}

func pct(used, limit int64) int {
	if limit <= 0 {
		return 0
	}
	p := int(used * 100 / limit)
	if p > 100 {
		p = 100
	}
	return p
}

func attUsageOf(u attachments.UsageOut) attUsage {
	v := attUsage{Used: fmtBytes(u.Owner.Bytes), Files: int(u.Owner.Files), HasLimit: u.Owner.Limit > 0, Pct: pct(u.Owner.Bytes, u.Owner.Limit),
		You: fmtBytes(u.You.Bytes), YouHasLimit: u.You.Limit > 0, YouPct: pct(u.You.Bytes, u.You.Limit), MaxFile: fmtBytes(u.MaxFile), MaxFileBytes: u.MaxFile}
	if u.Owner.Limit > 0 {
		v.Limit = fmtBytes(u.Owner.Limit)
		v.Near = v.Pct >= 90
		v.Full = u.Owner.Bytes >= u.Owner.Limit
	}
	if u.You.Limit > 0 {
		v.YouLimit = fmtBytes(u.You.Limit)
	}
	return v
}

// ---- finding the owner ----------------------------------------------------------------------

func (s *Server) attProject(w http.ResponseWriter, r *http.Request) (attOwner, any, bool) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return attOwner{}, nil, false
	}
	owner := s.gitOwner(r, proj)
	return newAttOwner(attachments.OwnerProject, proj.ID, proj.Slug, proj.Title, owner || proj.Role == ops.ProjectEditor, owner), proj, true
}

func (s *Server) attProjectByID(w http.ResponseWriter, r *http.Request) (attOwner, bool) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return attOwner{}, false
	}
	owner := s.gitOwner(r, proj)
	return newAttOwner(attachments.OwnerProject, proj.ID, proj.Slug, proj.Title, owner || proj.Role == ops.ProjectEditor, owner), true
}

func (s *Server) attCompendium(w http.ResponseWriter, r *http.Request) (attOwner, any, bool) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return attOwner{}, nil, false
	}
	return newAttOwner(attachments.OwnerCompendium, c.ID, c.Slug, c.Name, compEditable(c), c.Role == "owner" && !c.Trashed), c, true
}

func (s *Server) attCompendiumByID(w http.ResponseWriter, r *http.Request) (attOwner, bool) {
	c, ok := s.attCompendiumGet(w, r, r.PathValue("id"))
	if !ok {
		return attOwner{}, false
	}
	return newAttOwner(attachments.OwnerCompendium, c.ID, c.Slug, c.Name, compEditable(c), c.Role == "owner" && !c.Trashed), true
}

// attCompendiumGet reads one Compendium through compendium.get, which answers 404 for a stranger.
func (s *Server) attCompendiumGet(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	return s.compendiumByID(w, r, id)
}

// ---- reading --------------------------------------------------------------------------------

func (s *Server) attList(r *http.Request, o attOwner, cursor, kind string) (attPageData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := attPageData{Owner: o, Slug: o.Slug, CSRF: authFrom(ctx).CSRF, Cursor: cursor, Kind: kind}
	lo, err := s.Reg.Call(ctx, p, o.Prefix+"list", attachments.ListIn{ID: o.ID, Limit: attPageSize, Cursor: cursor, Kind: kind})
	if err != nil {
		return d, err
	}
	list := lo.(attachments.ListOut)
	loc := s.userZone(r)
	for _, a := range list.Items {
		d.Rows = append(d.Rows, attRowOf(a, loc))
	}
	d.Next = list.NextCursor
	uo, err := s.Reg.Call(ctx, p, o.Prefix+"usage", attachments.OwnerIn{ID: o.ID})
	if err == nil {
		d.Usage = attUsageOf(uo.(attachments.UsageOut))
	}
	d.MaxAccept = "image/png,image/jpeg,image/gif,image/webp,application/pdf,text/plain,text/markdown,.md,.markdown,.txt"
	return d, nil
}

func (s *Server) attPageFor(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var o attOwner
		var proj, comp any
		var ok bool
		if kind == attachments.OwnerCompendium {
			o, comp, ok = s.attCompendium(w, r)
		} else {
			o, proj, ok = s.attProject(w, r)
		}
		if !ok {
			return
		}
		kd := r.URL.Query().Get("kind")
		if kd != "image" && kd != "pdf" && kd != "text" {
			kd = ""
		}
		d, err := s.attList(r, o, r.URL.Query().Get("cursor"), kd)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Project, d.Compend = proj, comp
		d.Saved = attSaved[r.URL.Query().Get("saved")]
		w.Header().Set("Content-Security-Policy", cspFrames)
		s.render(w, r, http.StatusOK, "files", s.page(r, "Attachments · "+o.Name, d))
	}
}

// attFragment is the list alone, for the page's script to ask for after an upload or an event.
func (s *Server) attFragmentFor(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var o attOwner
		var ok bool
		if kind == attachments.OwnerCompendium {
			o, ok = s.attCompendiumByID(w, r)
		} else {
			o, ok = s.attProjectByID(w, r)
		}
		if !ok {
			return
		}
		kd := r.URL.Query().Get("kind")
		if kd != "image" && kd != "pdf" && kd != "text" {
			kd = ""
		}
		d, err := s.attList(r, o, r.URL.Query().Get("cursor"), kd)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.fragment(w, r, http.StatusOK, "attach-list", "attach-list", d)
	}
}

type attRefRow struct {
	attachments.Ref
	URL   string
	Label string
}

type attDetailData struct {
	Owner      attOwner
	Project    any
	Compend    any
	Slug       string
	CSRF       string
	Att        attRow
	Refs       []attRefRow
	Saved      string
	Error      string
	Field      string
	Refused    bool // the delete was refused because places still link to it
	Typed      string
	Values     map[string]string
	CanPreview bool
}

func (s *Server) attDetail(r *http.Request, o attOwner, id string) (attDetailData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := attDetailData{Owner: o, Slug: o.Slug, CSRF: authFrom(ctx).CSRF, Values: map[string]string{}}
	out, err := s.Reg.Call(ctx, p, o.Prefix+"get", attachments.AttIn{ID: o.ID, AttID: id})
	if err != nil {
		return d, err
	}
	d.Att = attRowOf(out.(attachments.Item).Item, s.userZone(r))
	d.CanPreview = d.Att.Kind == "pdf"
	ro, err := s.Reg.Call(ctx, p, o.Prefix+"refs", attachments.AttIn{ID: o.ID, AttID: id})
	if err == nil {
		for _, ref := range ro.(attachments.RefsOut).Items {
			row := attRefRow{Ref: ref, Label: ref.Title}
			if row.Label == "" {
				row.Label = "Untitled"
			}
			switch ref.Kind {
			case "research":
				row.URL = o.Base + "/research/" + url.PathEscape(ref.ID)
			default:
				row.URL = o.Base + "/d/" + url.PathEscape(ref.ID)
			}
			d.Refs = append(d.Refs, row)
		}
	}
	return d, nil
}

func (s *Server) attDetailPageFor(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var o attOwner
		var proj, comp any
		var ok bool
		if kind == attachments.OwnerCompendium {
			o, comp, ok = s.attCompendium(w, r)
		} else {
			o, proj, ok = s.attProject(w, r)
		}
		if !ok {
			return
		}
		d, err := s.attDetail(r, o, r.PathValue("att"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Project, d.Compend = proj, comp
		d.Saved = attSaved[r.URL.Query().Get("saved")]
		w.Header().Set("Content-Security-Policy", cspFrames)
		s.render(w, r, http.StatusOK, "file", s.page(r, d.Att.Filename, d))
	}
}

// ---- changes --------------------------------------------------------------------------------

var attSaved = map[string]string{
	"updated": "Saved.",
	"deleted": "The file is deleted.",
}

func (s *Server) attFail(w http.ResponseWriter, r *http.Request, o attOwner, id string, err error, refused bool, typed string) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge:
	default:
		s.fail(w, r, err)
		return
	}
	d, derr := s.attDetail(r, o, id)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	if o.Compend {
		if c, ok := s.attCompendiumGet(w, r, o.ID); ok {
			d.Compend = c
		} else {
			return
		}
	} else if proj, ok := s.goalsProjectByID(w, r, o.ID); ok {
		d.Project = proj
	} else {
		return
	}
	d.Error, d.Field, d.Refused, d.Typed = e.Message, e.Field, refused, typed
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	w.Header().Set("Content-Security-Policy", cspFrames)
	s.render(w, r, http.StatusUnprocessableEntity, "file", s.page(r, d.Att.Filename, d))
}

func (s *Server) uiAttachFor(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var o attOwner
		var ok bool
		if kind == attachments.OwnerCompendium {
			o, ok = s.attCompendiumByID(w, r)
		} else {
			o, ok = s.attProjectByID(w, r)
		}
		if !ok {
			return
		}
		if !o.CanEdit {
			s.fail(w, r, ops.Forbidden("Only editors can change files."))
			return
		}
		id, action := r.PathValue("att"), r.PathValue("action")
		p, ctx := s.principal(r), r.Context()
		back := o.Base + "/files/" + url.PathEscape(id)
		switch action {
		case "update":
			name, alt := strings.TrimSpace(r.PostFormValue("filename")), strings.TrimSpace(r.PostFormValue("alt"))
			if _, err := s.Reg.Call(ctx, p, o.Prefix+"update", attachments.UpdateIn{ID: o.ID, AttID: id, Filename: &name, Alt: &alt}); err != nil {
				s.attFail(w, r, o, id, err, false, "")
				return
			}
			http.Redirect(w, r, back+"?saved=updated", http.StatusSeeOther)
		case "delete":
			force := r.PostFormValue("force") == "1"
			if force {
				// Deleting a file that places still use breaks their images: the file's name must be typed.
				out, err := s.Reg.Call(ctx, p, o.Prefix+"get", attachments.AttIn{ID: o.ID, AttID: id})
				if err != nil {
					s.fail(w, r, err)
					return
				}
				if typed := r.PostFormValue("confirm"); typed != out.(attachments.Item).Item.Filename {
					s.attFail(w, r, o, id, ops.Invalid("confirm", "Type the file's name exactly to confirm."), true, typed)
					return
				}
			}
			if _, err := s.Reg.Call(ctx, p, o.Prefix+"delete", attachments.DeleteIn{ID: o.ID, AttID: id, Force: force}); err != nil {
				refused := ops.AsError(err).Code == ops.CodeConflict
				s.attFail(w, r, o, id, err, refused, "")
				return
			}
			http.Redirect(w, r, o.Base+"/files?saved=deleted", http.StatusSeeOther)
		default:
			s.fail(w, r, ops.Invalid("", "That form was not recognised."))
		}
	}
}

// registerAttachments adds the manager pages for both owners.
func (s *Server) registerAttachments(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/files", s.attPageFor(attachments.OwnerProject))
	m.HandleFunc("GET /p/{slug}/files/{att}", s.attDetailPageFor(attachments.OwnerProject))
	m.HandleFunc("GET /c/{slug}/files", s.attPageFor(attachments.OwnerCompendium))
	m.HandleFunc("GET /c/{slug}/files/{att}", s.attDetailPageFor(attachments.OwnerCompendium))
	m.HandleFunc("GET /ui/projects/{id}/files/list", s.attFragmentFor(attachments.OwnerProject))
	m.HandleFunc("GET /ui/compendiums/{id}/files/list", s.attFragmentFor(attachments.OwnerCompendium))
	s.uiRoute("POST /ui/projects/{id}/files/{att}/{action}", s.uiAttachFor(attachments.OwnerProject))
	s.uiRoute("POST /ui/compendiums/{id}/files/{att}/{action}", s.uiAttachFor(attachments.OwnerCompendium))
}
