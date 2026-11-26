package server

import (
	"archive/zip"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// The import review is a server-rendered shell over a server-held draft (imports.Draft). A file is chosen on the chooser page and
// sent once (POST /ui/import/stage); the review page /import/d/{id} is drawn from the draft, and every change is a small form
// that posts to /ui/import/d/{id}/update and gets the review body back as a fragment. The browser holds no import state: a
// reload, the Back button and a second tab all show the draft as it is. Nothing here writes but through the import ops.

func (s *Server) registerImportShell(m *http.ServeMux) {
	m.HandleFunc("GET /import/d/{id}", s.importReviewPage)
	m.HandleFunc("GET /p/{slug}/import/d/{id}", s.importReviewPage)
	s.uiRouteStream("POST /ui/import/stage", s.importStageAuthorize, s.importStageBusy, s.uiImportStage)
	s.uiRoute("POST /ui/import/d/{id}/update", s.uiImportUpdate)
	s.uiRoute("POST /ui/import/d/{id}/apply", s.uiImportApply)
	s.uiRoute("POST /ui/import/d/{id}/discard", s.uiImportDiscard)
	m.HandleFunc("GET /ui/import/d/{id}/row", s.uiImportRow)
	m.HandleFunc("GET /ui/import/d/{id}/look", s.uiImportLook)
	m.HandleFunc("GET /ui/import/d/{id}/more", s.uiImportMore)
	m.HandleFunc("GET /ui/import/d/{id}/progress", s.uiImportProgress)
	s.uiRoute("POST /ui/import/git", s.uiImportGitStart)
	s.uiRoute("POST /ui/import/git/{job}/open", s.uiImportGitOpen)
	s.uiRoute("POST /ui/projects/{id}/imports/{source}/undo", s.uiImportUndo)
}

// isImportStage says whether a path is the stage form, which carries a file (see bodyLimit).
func isImportStage(p string) bool { return p == "/ui/import/stage" }

// importBodyLimit is the largest stage form: a Word file or a zip (34 MiB) with the form's own fields.
const importBodyLimit = 36 << 20

// ---- the chooser's list of imports in progress ----------------------------------------------------------------------

type impDraftItem struct {
	ID, Name, Kind, KindName, When, Status, Href, Target string
	Unreachable                                          bool
	Docs, Words                                          int
	Slug                                                 string
	Done                                                 bool
	ProjectHref                                          string
}

func (s *Server) importDrafts(r *http.Request, into *projects.Project) []impDraftItem {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.list", struct{}{})
	if err != nil {
		return nil
	}
	loc := s.userZone(r)
	var items []impDraftItem
	for _, d := range out.(imports.DraftListOut).Items {
		if into != nil && d.ProjectID != into.ID {
			continue
		}
		if into == nil && d.ProjectID != "" && !d.Unreachable {
			continue // a draft for a project is listed on that project's import page; one whose project is out of reach is listed here, to discard
		}
		it := impDraftItem{ID: d.ID, Name: d.Name, Kind: d.Kind, When: whenIn(d.UpdatedAt, loc), Status: d.Status, Done: d.Status == imports.DraftDone}
		if it.Name == "" {
			it.Name = s.tr(r, "import.kind_"+d.Kind) // a draft with no name is called for what it is, in words
		}
		it.KindName = s.tr(r, "import.kind_"+d.Kind)
		it.Unreachable = d.Unreachable
		if !d.Unreachable {
			it.Href = s.importReviewURL(d.ID, into)
		}
		if d.Result != nil {
			it.Docs, it.Words = d.Result.Documents, d.Result.Words
			if d.Result.Slug != "" {
				it.ProjectHref = "/p/" + d.Result.Slug
			}
		}
		items = append(items, it)
	}
	return items
}

func (s *Server) importReviewURL(id string, into *projects.Project) string {
	if into != nil {
		return "/p/" + into.Slug + "/import/d/" + id
	}
	return "/import/d/" + id
}

// ---- staging ----------------------------------------------------------------------------------------------------------

func (s *Server) importStageAuthorize(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	p := s.principal(r)
	if p.Role != ops.RoleAdmin && p.Role != ops.RoleWriter {
		s.fail(w, r, ops.Forbidden("Your account can read projects but not create them."))
		return r, false
	}
	if slug := r.URL.Query().Get("into"); slug != "" {
		pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, slug)
		if !ok {
			s.fail(w, r, ops.NotFound("project"))
			return r, false
		}
		out, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
		if err != nil {
			s.fail(w, r, err)
			return r, false
		}
		pr := out.(projects.Item).Item.Addressed()
		if pr.Trashed {
			s.fail(w, r, ops.NotFound("project"))
			return r, false
		}
		if pr.Role != ops.ProjectOwner && pr.Role != ops.ProjectEditor {
			s.fail(w, r, ops.Forbidden("Only an owner or an editor can add files to a project."))
			return r, false
		}
		r = r.WithContext(context.WithValue(r.Context(), impIntoKey{}, pr))
	}
	return r, true
}

type impIntoKey struct{}

func (s *Server) importStageBusy(w http.ResponseWriter, r *http.Request, own bool) {
	msg := s.tr(r, "import.err_busy")
	if own {
		msg = s.tr(r, "import.err_busy_you")
	}
	w.Header().Set("Retry-After", "5")
	s.importStageFail(w, r, http.StatusTooManyRequests, msg, "")
}

// importStageFail answers a stage form that did not make a draft: for htmx the message alone, for a plain post the chooser with it.
func (s *Server) importStageFail(w http.ResponseWriter, r *http.Request, status int, msg, panel string) {
	if isHTMX(r) {
		status = http.StatusUnprocessableEntity // htmx swaps what a 422 holds and nothing of a 413 or a 429; the Retry-After stays
		s.fragment(w, r, status, "import-review", "imp-stage-error", map[string]any{"Msg": msg})
		return
	}
	into, _ := r.Context().Value(impIntoKey{}).(projects.Project)
	var ip *projects.Project
	if into.ID != "" {
		ip = &into
	}
	d := importData{Into: ip, Caps: caps(), Pandoc: s.pandocUsable(r.Context()), Error: msg, Panel: panel, Drafts: s.importDrafts(r, ip)}
	title := s.tr(r, "import.page_title")
	if ip != nil {
		title = s.tr(r, "import.page_title_into", "t", ip.Title)
	}
	s.render(w, r, status, "import", s.page(r, title, d))
}

// uiImportStage reads the stage form a part at a time and hands the file to import.stage as a stream. The fields come first in the
// form (the kind and the options), then the file, so nothing of a large body is held. A folder arrives as many parts, written as a zip.
func (s *Server) uiImportStage(w http.ResponseWriter, r *http.Request, mr *multipart.Reader) {
	if mr == nil {
		s.importStageFail(w, r, http.StatusUnprocessableEntity, s.tr(r, "import.err_nofile"), "")
		return
	}
	into, _ := r.Context().Value(impIntoKey{}).(projects.Project)
	vals := url.Values{}
	var staged *imports.StageOut
	var stageErr error
	var folder *folderWriter
	defer func() {
		if folder != nil {
			folder.cleanup()
		}
	}()
	var single *spooledFile
	defer func() { // a spooled file is removed on every way out: a refused form, a dropped connection, a staged file
		if single != nil {
			single.cleanup()
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				s.importStageFail(w, r, http.StatusRequestEntityTooLarge, s.tr(r, "import.too_big_34"), vals.Get("panel"))
				return
			}
			s.importStageFail(w, r, http.StatusUnprocessableEntity, s.tr(r, "import.err_read"), vals.Get("panel"))
			return
		}
		name := part.FormName()
		if part.FileName() == "" && !(name == "f" || name == "file") {
			lim := int64(1 << 20)
			if name == "unsent" {
				lim = 4 << 20 // the files a folder did not send, one line each
			}
			b, _ := io.ReadAll(io.LimitReader(part, lim))
			vals.Add(name, string(b))
			continue
		}
		cd := part.Header.Get("Content-Disposition")
		_, params, _ := mime.ParseMediaType(cd)
		full := params["filename"] // the browser's own path for a file from a folder: the relative path
		if full == "" {
			full = part.FileName()
		}
		if full == "" || stagedAlready(staged, stageErr) {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
			continue
		}
		if name == "f" { // a folder: its files come as many parts
			if folder == nil {
				if folder, err = newFolderWriter(s.Cfg.DataDir, vals.Get("fmt")); err != nil {
					stageErr = err
					continue
				}
			}
			if err := folder.add(full, part); err != nil {
				stageErr = err
			}
			continue
		}
		// One file. A browser sends the form's fields in the order of the page, and the options may come after the file: it is
		// written to disk as it arrives (never held), and staged when the whole form has been read.
		if single == nil {
			sp, err := spool(s.Cfg.DataDir, full, part)
			if err != nil {
				stageErr = err
				continue
			}
			single = sp
		}
	}
	if single != nil {
		if stageErr == nil && folder == nil {
			in := s.stageInputOf(vals, vals.Get("kind"), into)
			in.Name = importName(single.name)
			if (in.Kind == "file" || in.Kind == "") && single.pk {
				in.Kind = "zip"
			}
			f, err := os.Open(single.path)
			if err == nil {
				var out imports.StageOut
				if out, err = s.stageCall(r, in, f, single.size); err == nil {
					staged = &out
				}
				_ = f.Close()
			}
			if err != nil {
				stageErr = err
			}
		}
	}
	if folder == nil && staged == nil && stageErr == nil && vals.Get("unsent") != "" {
		// a folder with nothing an import reads in it: the browser sent no file, only the list of what it did not send
		if folder, stageErr = newFolderWriter(s.Cfg.DataDir, vals.Get("fmt")); stageErr == nil {
			folder.listed = 1
		}
	}
	if folder != nil && folder.root == "" {
		folder.root = vals.Get("root")
	}
	if staged == nil && stageErr == nil && folder != nil {
		for _, line := range strings.Split(vals.Get("unsent"), "\n") {
			if p, n, ok := strings.Cut(line, "\t"); ok && p != "" && len(folder.unsent) < imports.MaxListed {
				size, _ := strconv.ParseInt(n, 10, 64)
				folder.unsent = append(folder.unsent, imports.File{Path: p, Size: size})
			}
		}
		in := s.stageInputOf(vals, "zip", into)
		in.Origin = "folder"
		if vals.Get("fmt") == "scrivener" {
			in.Origin = ""
			in.Layout = "scrivener"
		}
		in.Name = importName(folder.root)
		if out, err := folder.stage(s, r, in); err != nil {
			stageErr = err
		} else {
			staged = &out
		}
	}
	panel := vals.Get("panel")
	if stageErr != nil {
		status, msg := s.importStageError(r, stageErr, vals.Get("kind"))
		s.importStageFail(w, r, status, msg, panel)
		return
	}
	if staged != nil {
		// A file the importer refuses (a book with DRM, a scanned or encrypted PDF, Pandoc switched off) is found now and said here, with the
		// file still chosen and nothing kept, not on a review page that cannot be drawn. The conversion is cached for the review that follows.
		if _, perr := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: staged.StageID, Preview: true}); perr != nil {
			_, _ = s.Reg.Call(r.Context(), s.principal(r), "import.draft.discard", imports.DraftIDIn{ID: staged.StageID})
			status, msg := s.importStageError(r, perr, vals.Get("kind"))
			s.importStageFail(w, r, status, msg, panel)
			return
		}
	}
	if staged == nil {
		s.importStageFail(w, r, http.StatusUnprocessableEntity, s.tr(r, "import.err_nofile"), panel)
		return
	}
	var ip *projects.Project
	if into.ID != "" {
		ip = &into
	}
	dest := s.importReviewURL(staged.StageID, ip)
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// spooledFile is a file part written to disk while the rest of the form is read.
type spooledFile struct {
	name, path string
	size       int64
	pk         bool // it starts like a zip
}

func (f *spooledFile) cleanup() { _ = os.Remove(f.path) }

// spool writes a part to a temporary file, refusing more than the largest file an import takes.
func spool(dataDir, name string, r io.Reader) (*spooledFile, error) {
	dir := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ops.Internal(err)
	}
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return nil, ops.Internal(err)
	}
	out := &spooledFile{name: name, path: f.Name()}
	n, err := io.Copy(f, io.LimitReader(r, imports.MaxStageBody))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > 34<<20 {
		err = ops.TooLarge()
	}
	if err != nil {
		out.cleanup()
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, ops.TooLarge()
		}
		return nil, err
	}
	out.size = n
	if g, err := os.Open(out.path); err == nil {
		var h [4]byte
		m, _ := io.ReadFull(g, h[:])
		out.pk = string(h[:m]) == "PK\x03\x04"
		_ = g.Close()
	}
	return out, nil
}

func stagedAlready(o *imports.StageOut, err error) bool { return o != nil || err != nil }

// importName is the name a file or folder gives an import: its base name without the extension that says what it is.
func importName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	b := path.Base(p)
	for _, ext := range []string{".docx", ".zip", ".epub", ".pdf", ".txt", ".text", ".xml", ".json", ".scriv"} {
		if strings.HasSuffix(strings.ToLower(b), ext) {
			b = b[:len(b)-len(ext)]
		}
	}
	return b
}

func (s *Server) stageInputOf(vals url.Values, kind string, into projects.Project) imports.StageIn {
	// a checkbox sends a hidden "off" value first and its own after it: the last one is the choice
	opt := func(k string) string {
		if vs := vals["opt."+k]; len(vs) > 0 {
			return vs[len(vs)-1]
		}
		return ""
	}
	in := imports.StageIn{Kind: kind, Project: into.ID, Format: opt("pandoc_format"), Layout: opt("layout"), DocxHeadings: opt("docx_headings"), DocxChanges: opt("docx_changes"),
		SplitScenes: opt("split_scenes"), EpubSections: opt("epub_sections"), EpubPages: opt("epub_pages"), Pictures: opt("pictures"), ScrivResearch: opt("scrivener_research"),
		ScrivNotes: opt("scrivener_notes"), ScrivSheets: opt("scrivener_sheets"), WPStatus: opt("wp_status"), WPTypes: opt("wp_types"), GhostStatus: opt("ghost_status"),
		GhostTypes: opt("ghost_types")}
	if in.Kind != "pandoc" {
		in.Format = ""
	}
	if f := vals.Get("fmt"); f == "wordpress" || f == "ghost" || f == "scrivener" {
		in.Layout = f
		if in.Kind == "" {
			in.Kind = "file"
		}
	}
	return in
}

// stageCall runs import.stage over a stream.
func (s *Server) stageCall(r *http.Request, in imports.StageIn, body io.Reader, length int64) (imports.StageOut, error) {
	ctx := ops.WithRawBody(r.Context(), ops.RawBody{R: body, ContentType: "application/octet-stream", Length: length})
	out, err := s.Reg.Call(ctx, s.principal(r), "import.stage", in)
	if err != nil {
		return imports.StageOut{}, err
	}
	return out.(imports.StageOut), nil
}

// importStageError says what went wrong staging a file, in the page's words, with the status the answer carries.
func (s *Server) importStageError(r *http.Request, err error, kind string) (int, string) {
	e := ops.AsError(err)
	status := e.Code.HTTPStatus()
	if e.Code == ops.CodeTooLarge {
		status = http.StatusRequestEntityTooLarge
	}
	if e.Code == ops.CodeRateLimited || (e.Code == ops.CodeConflict && e.Data["reason"] != nil) {
		status = http.StatusUnprocessableEntity
	}
	return status, s.importErrText(r, e, kind)
}

// sentence starts a refusal the op wrote in lower case ("this PDF has no text layer") with a capital.
func sentence(m string) string {
	if m == "" {
		return m
	}
	r, n := utf8.DecodeRuneInString(m)
	return string(unicode.ToUpper(r)) + m[n:]
}

// peekReader lets the first bytes of a stream be looked at and still read.
type peekReader struct {
	r    io.Reader
	head []byte
	used bool
}

func newPeekReader(r io.Reader) *peekReader { return &peekReader{r: r} }

func (p *peekReader) peek(n int) string {
	b := make([]byte, n)
	m, _ := io.ReadFull(p.r, b)
	p.head = b[:m]
	return string(p.head)
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	return p.r.Read(b)
}

// folderWriter turns the files of a folder into a zip: the Markdown (and the manifests), with the rest listed by name and size, as the
// browser always did, so the review says what was left out and why. It holds nothing in memory but one file at a time.
type folderWriter struct {
	f       *os.File
	zw      *zip.Writer
	dir     string
	keepAll bool // a Scrivener project: its binder, text and pictures, nothing hidden
	root    string
	listed  int
	total   int64
	unsent  []imports.File
	entries map[string]bool
	err     error
}

func newFolderWriter(dataDir, fmtName string) (*folderWriter, error) {
	dir := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ops.Internal(err)
	}
	f, err := os.CreateTemp(dir, "folder-*.zip")
	if err != nil {
		return nil, ops.Internal(err)
	}
	return &folderWriter{f: f, zw: zip.NewWriter(f), dir: dir, keepAll: fmtName == "scrivener", entries: map[string]bool{}}, nil
}

func (w *folderWriter) cleanup() {
	if w.f != nil {
		name := w.f.Name()
		_ = w.f.Close()
		_ = os.Remove(name)
	}
}

var reFolderKeep = func(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".markdown") || p == "scriobh.json" || p == "project.json"
}

var reScrivKeep = func(p string) bool {
	l := strings.ToLower(p)
	for _, e := range []string{".scrivx", ".rtf", ".txt", ".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// add takes one file of the folder. full is the browser's path: the folder's name, then the path inside it.
func (w *folderWriter) add(full string, r io.Reader) error {
	full = strings.ReplaceAll(full, "\\", "/")
	segs := strings.Split(full, "/")
	if w.root == "" {
		w.root = segs[0]
	}
	rel := strings.Join(segs[1:], "/")
	if len(segs) < 2 {
		rel = full // a file chosen on its own
	}
	if rel == "" {
		return nil
	}
	w.listed++
	if w.listed > imports.MaxListed {
		return ops.Invalid("", "This folder holds more than "+commas(imports.MaxListed)+" files, which is the limit. Choose a smaller folder.")
	}
	hidden := false
	for _, sg := range strings.Split(rel, "/") {
		if strings.HasPrefix(sg, ".") || sg == "node_modules" {
			hidden = true
		}
	}
	keep := reFolderKeep(rel)
	if w.keepAll {
		keep = reScrivKeep(rel) && !hidden
	}
	if hidden && !w.keepAll {
		keep = false
	}
	if hidden && w.keepAll {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	if !keep {
		n, _ := io.Copy(io.Discard, r)
		w.unsent = append(w.unsent, imports.File{Path: rel, Size: n})
		return nil
	}
	if !w.keepAll && w.entries[rel] {
		return nil
	}
	w.entries[rel] = true
	hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate}
	hdr.Modified = time.Time{}
	zf, err := w.zw.CreateHeader(hdr)
	if err != nil {
		return ops.Internal(err)
	}
	limit := int64(imports.MaxFileBytes)
	if w.keepAll {
		limit = 34 << 20
	}
	n, err := io.Copy(zf, io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if !w.keepAll && n > limit {
		// too big for a document: it is listed, with its size, and not sent on (the zip entry is left as a short stub)
		w.unsent = append(w.unsent, imports.File{Path: rel, Size: n})
		return nil
	}
	w.total += n
	if !w.keepAll && w.total > imports.MaxTotalBytes {
		return ops.Invalid("", "This folder holds more than 32 MiB of Markdown, which is the most one import can take. Import it in parts.")
	}
	return nil
}

// stage closes the zip, appends what was not sent as a list, and stages it.
func (w *folderWriter) stage(s *Server, r *http.Request, in imports.StageIn) (imports.StageOut, error) {
	if !w.keepAll && len(w.unsent) > 0 {
		b, _ := json.Marshal(w.unsent)
		zf, err := w.zw.Create(".scriobh-unsent.json")
		if err != nil {
			return imports.StageOut{}, ops.Internal(err)
		}
		_, _ = zf.Write(b)
	}
	if err := w.zw.Close(); err != nil {
		return imports.StageOut{}, ops.Internal(err)
	}
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return imports.StageOut{}, ops.Internal(err)
	}
	st, _ := w.f.Stat()
	if w.listed == 0 || st == nil {
		return imports.StageOut{}, ops.Invalid("", "That folder holds no files.")
	}
	return s.stageCall(r, in, w.f, st.Size())
}

// ---- the review page --------------------------------------------------------------------------------------------------

// reviewOpts are what a request asks of the view.
type reviewOpts struct {
	open, focus, notice, kind string
	rows                      int
	live                      string
	sug, sugAt                int    // how many possible chapter starts are listed, and which takes focus after Show more
	sugOrd                    int    // the place a chapter was just started at
	look, lookKey             string // a list asked for: the paragraphs of a chapter to split it at, or what is inside it
	lookAfter, lookCount      int
	reveal                    string // a chapter that must be among the rows drawn, to be focused
	patText, patMatch         string // what the writer typed in the pattern field, kept when the pattern is refused
	patTyped                  bool
}

// importReviewView reads a draft and its preview and builds the view. The project a draft adds to is the draft's own, read from the
// server; it is never taken from the address or from a form.
func (s *Server) importReviewView(r *http.Request, id string, o reviewOpts) (*impView, int, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: id, Preview: true, LookKey: o.lookKey, LookMode: o.look, LookAfter: o.lookAfter, LookCount: cmp.Or(o.lookCount, lookPage)})
	if err != nil {
		var oe *ops.Error
		if errors.As(err, &oe) && oe.Code == ops.CodeConflict && oe.Data["reason"] == "gone" {
			dr, derr := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: id})
			if derr != nil {
				return nil, 0, derr
			}
			it := dr.(imports.DraftOut).Item
			v := s.importViewBase(r, it, s.projectOfDraft(r, it.ProjectID), o)
			v.Failed = s.tr(r, "import.notice_gone")
			return v, http.StatusOK, nil
		}
		return nil, 0, err
	}
	d := out.(imports.DraftOut)
	return s.importViewOf(r, d, s.projectOfDraft(r, d.Item.ProjectID), o), http.StatusOK, nil
}

// projectOfDraft is the project a draft adds to, or nil for a new project.
func (s *Server) projectOfDraft(r *http.Request, projectID string) *projects.Project {
	if projectID == "" {
		return nil
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.get", projects.GetIn{ID: projectID})
	if err != nil {
		return nil
	}
	pr := out.(projects.Item).Item.Addressed()
	return &pr
}

// draftInto reads the draft (without its preview) and answers the project it adds to. ok is false when the draft cannot be read, which the
// caller answers with the op's own error.
func (s *Server) draftInto(r *http.Request, id string) (*projects.Project, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: id})
	if err != nil {
		return nil, err
	}
	return s.projectOfDraft(r, out.(imports.DraftOut).Item.ProjectID), nil
}

func (s *Server) importViewBase(r *http.Request, it imports.DraftView, into *projects.Project, o reviewOpts) *impView {
	a := authFrom(r.Context())
	v := &impView{SugAfter: o.sug, SugAt: o.sugAt, SugOrd: o.sugOrd, Lang: s.page(r, "", nil).Lang, CSRF: a.CSRF, ID: it.ID, Rev: it.Revision, Token: it.Token, Kind: it.Kind, Into: into, Status: it.Status,
		PageURL: s.importReviewURL(it.ID, into), Act: "/ui/import/d/" + it.ID, CanUndo: it.CanUndo, OpenKey: o.open, Focus: o.focus, Notice: o.notice, NoticeKind: o.kind}
	if v.NoticeKind == "" && v.Notice != "" {
		v.NoticeKind = "warn"
	}
	return v
}

// importViewOf builds the view of a draft and what its preview found.
func (s *Server) importViewOf(r *http.Request, d imports.DraftOut, into *projects.Project, o reviewOpts) *impView {
	v := s.importViewBase(r, d.Item, into, o)
	v.Pending, v.Failed = d.Pending, d.Failed
	if d.Failed != "" {
		v.Hint, v.Live = s.importGitHint(r, "", d.Failed), s.tr(r, "import.live_failed", "why", d.Failed)
	}
	if d.Item.Status == imports.DraftDone {
		v.Done = d.Item.Result
		v.doneLinks(s, r)
		s.undoResult(r, v)
		return v
	}
	if o.live != "" {
		v.Live = o.live
	}
	if d.Preview != nil {
		v.Token = d.Preview.Token
		v.Reveal = o.reveal
		v.PatTyped, v.PatText, v.PatMatch = o.patTyped, o.patText, o.patMatch
		v.attachLook(d.Look, o.look, o.lookAfter)
		v.fill(d.Preview, d.Item, o.rows)
		v.Already = s.alreadyNotice(r, d.Preview, into)
		for _, o := range d.Item.Reuse {
			v.Reuse = append(v.Reuse, impReuse{ID: o.ID, Label: s.trN(r, "import.reuse_btn", o.Changes)})
		}
		if v.Layout == "pandoc" {
			for _, f := range pandoc.ImportFormats() {
				v.Opts.PandocFormats = append(v.Opts.PandocFormats, impTarget{f, s.tr(r, "pandoc.fmt_"+f)})
			}
		}
	}
	return v
}

// doneLinks are the links on a finished import's page.
func (v *impView) doneLinks(s *Server, r *http.Request) {
	d := v.Done
	if d == nil || d.Slug == "" {
		return
	}
	home := "/p/" + d.Slug
	first := home
	if d.FirstDocument != "" {
		first = home + "/d/" + d.FirstDocument
	}
	v.DoneLinks = []impLink{{first, v.T("import.done_open"), true}}
	if d.EditionID != "" {
		v.DoneLinks = append(v.DoneLinks, impLink{home + "/editions", v.T("import.done_editions"), false})
	}
	if d.Suggestions > 0 || d.Comments > 0 {
		v.DoneLinks = append(v.DoneLinks, impLink{home + "/review", v.T("import.done_review"), false})
	}
	v.DoneLinks = append(v.DoneLinks, impLink{"/import", v.T("import.done_again"), false})
}

// importProject finds the project a /p/{slug}/import page is under, and checks the caller may add to it.
func (s *Server) importProject(w http.ResponseWriter, r *http.Request) (*projects.Project, bool) {
	slug := r.PathValue("slug")
	if slug == "" {
		return nil, true
	}
	p := s.principal(r)
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, slug)
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return nil, false
	}
	out, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	pr := out.(projects.Item).Item.Addressed()
	if pr.Trashed {
		s.fail(w, r, ops.NotFound("project"))
		return nil, false
	}
	if pr.Role != ops.ProjectOwner && pr.Role != ops.ProjectEditor {
		s.fail(w, r, ops.Forbidden("Only an owner or an editor can add files to a project."))
		return nil, false
	}
	return &pr, true
}

func (s *Server) importWriter(w http.ResponseWriter, r *http.Request) bool {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return false
	}
	if p.Role != ops.RoleAdmin && p.Role != ops.RoleWriter {
		s.fail(w, r, ops.Forbidden("Your account can read projects but not create them."))
		return false
	}
	return true
}

// importReviewPage is GET /import/d/{id} and GET /p/{slug}/import/d/{id}: the whole review, drawn from the draft.
func (s *Server) importReviewPage(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	at, ok := s.importProject(w, r) // the project of the address, if it has one
	if !ok {
		return
	}
	id := r.PathValue("id")
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	sug, _ := strconv.Atoi(r.URL.Query().Get("sug"))
	sugAt, _ := strconv.Atoi(r.URL.Query().Get("at"))
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	look := lookMode(r.URL.Query().Get("look"))
	lookKey := ""
	if look != "" {
		lookKey = r.URL.Query().Get("edit")
	}
	v, _, err := s.importReviewView(r, id, reviewOpts{open: r.URL.Query().Get("edit"), rows: rows, sug: sug, sugAt: sugAt, look: look, lookKey: lookKey, lookCount: lookPage + max(after, 0), reveal: r.URL.Query().Get("reveal")}) // with no script the page is drawn again with more of the list, from its start
	if err != nil {
		s.importReviewFailed(w, r, id, err)
		return
	}
	// A draft that adds to a project is read under that project's page, and a new project's under /import; any other address is not found.
	switch {
	case v.Into != nil && (at == nil || at.ID != v.Into.ID):
		http.Redirect(w, r, v.PageURL, http.StatusSeeOther)
		return
	case v.Into == nil && at != nil:
		s.fail(w, r, ops.NotFound("import"))
		return
	}
	s.importPageRender(w, r, http.StatusOK, v)
}

// importPageRender draws the review's page.
func (s *Server) importPageRender(w http.ResponseWriter, r *http.Request, status int, v *impView) {
	title := s.tr(r, "import.page_title")
	if v.Into != nil {
		title = s.tr(r, "import.page_title_into", "t", v.Into.Title)
	}
	s.render(w, r, status, "import-review", s.page(r, title, v))
}

// importReviewFailed answers a review that could not be drawn: a missing, foreign or forbidden draft the way every page does, and anything
// else (a file the importer refuses, a reader that is busy) as the review's own page with the reason in words and the way out, not a bare
// error page.
func (s *Server) importReviewFailed(w http.ResponseWriter, r *http.Request, id string, err error) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeNotFound, ops.CodeUnauthenticated, ops.CodeForbidden, ops.CodeInternal:
		s.fail(w, r, err)
		return
	}
	out, derr := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: id})
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	it := out.(imports.DraftOut).Item
	v := s.importViewBase(r, it, s.projectOfDraft(r, it.ProjectID), reviewOpts{})
	v.Failed = s.importErrText(r, e, it.Kind)
	status := http.StatusUnprocessableEntity
	if e.Code == ops.CodeRateLimited {
		status, v.Retry = http.StatusTooManyRequests, true
		w.Header().Set("Retry-After", "5")
	}
	s.importPageRender(w, r, status, v)
}

// reviewFragment draws the review body.
func (s *Server) reviewFragment(w http.ResponseWriter, r *http.Request, status int, v *impView) {
	s.fragment(w, r, status, "import-review", "imp-review", v)
}

// uiImportRow is the edit panel of one row, drawn when it is asked for.
func (s *Server) uiImportRow(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	v, _, err := s.importReviewView(r, r.PathValue("id"), reviewOpts{open: r.URL.Query().Get("key")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for i := range v.Rows {
		if v.Rows[i].Open {
			s.fragment(w, r, http.StatusOK, "import-review", "imp-panel", impPanelCtx{V: v, R: &v.Rows[i]})
			return
		}
	}
	s.fail(w, r, ops.NotFound("document"))
}

type impPanelCtx struct {
	V *impView
	R *impRow
}

// uiImportMore draws the next rows of a long tree.
func (s *Server) uiImportMore(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	v, _, err := s.importReviewView(r, r.PathValue("id"), reviewOpts{rows: after + rowPage})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if after > len(v.Rows) {
		after = len(v.Rows)
	}
	v.Rows = v.Rows[after:]
	v.Shown = after + len(v.Rows)
	s.fragment(w, r, http.StatusOK, "import-review", "imp-rows", v)
}

// uiImportProgress is what a Git clone, or a slow conversion, polls while it runs: the same fragment once it is ready.
func (s *Server) uiImportProgress(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	sug, _ := strconv.Atoi(r.URL.Query().Get("sug"))
	at, _ := strconv.Atoi(r.URL.Query().Get("at"))
	o := reviewOpts{sug: sug, sugAt: at}
	if sug > 0 {
		o.focus = "imp-sugmore"
	}
	v, _, err := s.importReviewView(r, r.PathValue("id"), o)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.reviewFragment(w, r, http.StatusOK, v)
}

// ---- changes ----------------------------------------------------------------------------------------------------------

// uiImportUpdate is one change to the draft: a form of one control. It answers the review body; a plain post gets a redirect back.
func (s *Server) uiImportUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	id := r.PathValue("id")
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	rev, _ := strconv.Atoi(f("rev"))
	in := imports.DraftUpdateIn{ID: id, BaseRevision: rev}
	act, key := f("act"), f("key")
	focus := ""
	rid0 := rid(key)
	ctl := func(a string) string { return "imp-c-" + rid0 + "-" + a }
	open := f("edit")
	sugOrd := 0
	reveal := ""
	inside := f("look") == imports.LookInside // the change was made from what is inside a long chapter: that stays open, and is made again
	switch act {
	case "title":
		t := r.PostFormValue("title")
		in.Edit = &imports.DraftEdit{Key: key, Title: &t}
		focus = ctl("title")
	case "kind":
		k := f("kind")
		in.Edit = &imports.DraftEdit{Key: key, Kind: &k}
		focus = ctl("kind")
		if r.PostForm.Has("button") {
			focus = ctl("make")
		}
	case "split":
		sv := r.PostForm["split"] // a hidden "false" first, the checkbox's "true" after it when it is ticked
		v := len(sv) > 0 && sv[len(sv)-1] == "true"
		in.Edit = &imports.DraftEdit{Key: key, Split: &v}
		focus = ctl("split")
	case "up", "down", "top", "bottom":
		in.Move = &imports.DraftMove{Key: key, To: act}
		focus = ctl(act)
	case "skip":
		t := true
		in.Edit = &imports.DraftEdit{Key: key, Skip: &t}
		focus = ctl("back")
		open = ""
	case "back":
		fl := false
		in.Edit = &imports.DraftEdit{Key: key, Skip: &fl}
		focus = "imp-s-" + rid0
		open = key
	case "parent":
		to := f("to")
		in.Edit = &imports.DraftEdit{Key: key, Parent: &to}
		focus = "imp-s-" + rid0
	case "merge":
		to := f("to")
		in.Edit = &imports.DraftEdit{Key: key, MergeInto: &to}
		focus = ctl("back")
		open = ""
	case "options":
		in.Options = map[string]string{}
		for k, vs := range r.PostForm {
			if name, ok := strings.CutPrefix(k, "opt."); ok && len(vs) > 0 {
				in.Options[name] = vs[len(vs)-1] // a checkbox's hidden "off" comes first
			}
		}
		focus = "imp-opts"
	case "cut":
		// the radios of "how it was cut", or one of the bulk buttons beside them
		in.Options = map[string]string{}
		for k, vs := range r.PostForm {
			if name, ok := strings.CutPrefix(k, "opt."); ok && len(vs) > 0 {
				in.Options[name] = vs[len(vs)-1]
			}
		}
		if v, ok := strings.CutPrefix(in.Options["docx_split"], "style:"); ok {
			in.Options["docx_split"], in.Options["docx_split_style"] = "style", v
		}
		focus = "imp-cut"
	case "break":
		n, _ := strconv.Atoi(f("ord"))
		in.Break = &n
		focus = "imp-sug"
		sugOrd = n
	case "joinprev":
		yes := true
		in.Edit = &imports.DraftEdit{Key: key, JoinPrevious: &yes}
		focus = ctl("back")
		open = ""
	case "splitat":
		// a paragraph chosen from a chapter's list: the chapter is split there, and focus goes to the chapter that begins
		n, _ := strconv.Atoi(f("ord"))
		in.Break, in.BreakFrom = &n, key
		sugOrd = n
		reveal = "ord"
		open = ""
	case "within":
		in.Within = &imports.DraftWithin{Key: key, Rule: f("rule")}
		reveal = key
		focus = "imp-s-" + rid0
		open = ""
	case "matter":
		in.Matter = &imports.DraftMatter{Key: key, Side: f("side")}
		focus = "imp-s-" + rid0
		open = key
	case "pattern":
		// the pattern the writer typed: Test counts what it matches and cuts nothing; the other button cuts the book at it
		text, how := r.PostFormValue("opt.docx_split_pattern"), f("opt.docx_split_match")
		in.Options = map[string]string{"docx_split_try": text, "docx_split_try_match": how}
		focus = "imp-pat-test"
		if r.PostForm.Has("go") {
			in.Options = map[string]string{"docx_split": imports.SplitPattern, "docx_split_pattern": text, "docx_split_match": how, "docx_split_try": "", "docx_split_try_match": ""}
			focus = "imp-cut"
		}
	case "titlecase":
		in.TitleCaseAll = true
		focus = "imp-caps"
	case "reuse":
		// the fixes of an earlier review of this file: a review of the writer's own, checked by the op
		in.Reuse = f("from")
	case "lineall":
		in.ChapterLineAll = true
		focus = "imp-lines"
	case "usecase":
		t := r.PostFormValue("title")
		in.Edit = &imports.DraftEdit{Key: key, Title: &t}
		focus = "imp-s-" + rid0
		open = ""
	case "ack":
		sv := r.PostForm["opt.ack_big"] // a hidden "false" first, the checkbox's "true" after it when it is ticked
		in.Options = map[string]string{"ack_big": map[bool]string{true: "true", false: "false"}[len(sv) > 0 && sv[len(sv)-1] == "true"]}
		focus = "imp-ack"
	case "project":
		in.Project = &imports.ProjectEdit{Title: f("title"), Kind: f("kind"), Author: f("author")}
	case "undo":
		in.Undo = true
		focus = "imp-undo"
	default:
		s.fail(w, r, ops.Invalid("act", "That is not something the review does."))
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.update", in)
	if err != nil {
		prefix := ""
		if ops.AsError(err).Data["reason"] == nil {
			prefix = s.tr(r, "import.undone") // a change the draft would not make: "That change was not made."
		}
		var keep []func(*reviewOpts)
		if act == "pattern" { // the field keeps what was typed, so it can be mended
			text, how := r.PostFormValue("opt.docx_split_pattern"), f("opt.docx_split_match")
			keep = append(keep, func(o *reviewOpts) { o.patText, o.patMatch, o.patTyped = text, how, true })
		}
		s.importRefused(w, r, id, err, open, prefix, keep...)
		return
	}
	u := out.(imports.DraftUpdateOut)
	if act == "project" {
		if isHTMX(r) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, s.importReviewURL(id, s.projectOfDraft(r, u.Item.ProjectID)), http.StatusSeeOther) // with no script, the page comes back with the fields as saved
		return
	}
	d := imports.DraftOut{Item: u.Item, Preview: u.Preview}
	into := s.projectOfDraft(r, u.Item.ProjectID) // the draft's own project, never the form's
	// A move that leaves the control with nowhere further to go hands the focus to the Edit summary.
	if act == "up" || act == "top" || act == "down" || act == "bottom" {
		focus = s.moveFocus(u, key, act, rid0)
	}
	if act == "within" && u.Preview != nil && !slices.ContainsFunc(u.Preview.Nodes, func(n imports.Node) bool { return n.Key == key }) {
		// a book with no marks was one document named for the book; its first piece is a new key: focus goes to that
		for _, n := range u.Preview.Nodes {
			if n.Ordinal > 0 {
				reveal, focus = n.Key, "imp-s-"+rid(n.Key)
				break
			}
		}
	}
	if reveal == "ord" { // the chapter that begins at the paragraph: found by the ordinal it starts at
		reveal = ""
		focus = "imp-s-" + rid0
		for _, n := range u.Preview.Nodes {
			if n.Ordinal == sugOrd {
				reveal, focus = n.Key, "imp-s-"+rid(n.Key)
			}
		}
	}
	live := s.importLive(r, u.Note)
	notice := ""
	if u.Note != nil && u.Note.Code == "recut" && len(u.Note.Dropped) > 0 {
		// nothing is dropped silently: what no longer applies is listed, and Undo brings it back
		list := strings.Join(u.Note.Dropped, ", ")
		if u.Note.NoMore > 0 {
			list += s.tr(r, "import.and_more", "n", u.Note.NoMore)
		}
		notice = s.trN(r, "import.recut_dropped", len(u.Note.Dropped)+u.Note.NoMore, "list", list)
	}
	if inside && (act == "splitat" || act == "within") && u.Preview != nil {
		// The panel follows the places still to come: after a chapter is started, those are in the chapter that begins there; after all of a
		// kind are started, it is the chapter looked into (or what it became when its name went with its first break).
		lk := key
		if act == "splitat" {
			for _, n := range u.Preview.Nodes {
				if n.Ordinal == sugOrd {
					lk = n.Key
				}
			}
		}
		if !slices.ContainsFunc(u.Preview.Nodes, func(n imports.Node) bool { return n.Key == lk }) {
			for _, n := range u.Preview.Nodes {
				if n.Ordinal > 0 {
					lk = n.Key
					break
				}
			}
		}
		ro := reviewOpts{live: live, notice: notice, kind: "info", rows: s.rowsAsked(r, key, u), sugOrd: sugOrd, reveal: lk, look: imports.LookInside, lookKey: lk}
		if act == "splitat" {
			ro.focus = "imp-ls"
		}
		if isHTMX(r) {
			v, _, err := s.importReviewView(r, id, ro)
			if err != nil {
				s.importReviewFailed(w, r, id, err)
				return
			}
			s.reviewFragment(w, r, http.StatusOK, v)
			return
		}
		http.Redirect(w, r, s.importReviewURL(id, into)+"?edit="+url.QueryEscape(lk)+"&look=inside&reveal="+url.QueryEscape(lk)+"#imp-look-"+rid(lk), http.StatusSeeOther)
		return
	}
	if isHTMX(r) {
		v := s.importViewOf(r, d, into, reviewOpts{open: open, focus: focus, live: live, notice: notice, kind: "info", rows: s.rowsAsked(r, key, u), sugOrd: sugOrd, reveal: reveal})
		s.reviewFragment(w, r, http.StatusOK, v)
		return
	}
	// with no script the chapter that was made, or looked into, is among the rows and the page opens at it
	target, q := rid0, "?edit="+url.QueryEscape(open)
	if reveal != "" {
		target, q = rid(reveal), q+"&reveal="+url.QueryEscape(reveal)
	}
	http.Redirect(w, r, s.importReviewURL(id, into)+q+"#imp-r-"+target, http.StatusSeeOther)
}

func (s *Server) rowsAsked(r *http.Request, key string, u imports.DraftUpdateOut) int {
	n, _ := strconv.Atoi(r.PostFormValue("rows"))
	return n
}

// moveFocus: the control that moved the row stays where the row went, unless it can go no further that way.
func (s *Server) moveFocus(u imports.DraftUpdateOut, key, act, r string) string {
	if u.Preview == nil {
		return "imp-c-" + r + "-" + act
	}
	var self *imports.Node
	for i := range u.Preview.Nodes {
		if u.Preview.Nodes[i].Key == key {
			self = &u.Preview.Nodes[i]
		}
	}
	if self == nil {
		return "imp-s-" + r
	}
	var sib []string
	for _, n := range u.Preview.Nodes {
		if n.ParentKey == self.ParentKey {
			sib = append(sib, n.Key)
		}
	}
	first, last := len(sib) > 0 && sib[0] == key, len(sib) > 0 && sib[len(sib)-1] == key
	if ((act == "up" || act == "top") && first) || ((act == "down" || act == "bottom") && last) {
		return "imp-s-" + r
	}
	return "imp-c-" + r + "-" + act
}

// importLive is the words for a screen reader after a change: where a chapter went, not only that it moved.
func (s *Server) importLive(r *http.Request, n *imports.DraftNote) string {
	if n == nil {
		return ""
	}
	switch n.Code {
	case "moved":
		k := "import.moved_below"
		if n.Above {
			k = "import.moved_above"
		}
		return s.tr(r, k, "t", n.Title, "o", n.Other, "i", n.Pos, "n", n.Count)
	case "moved_under":
		return s.tr(r, "import.moved_under", "t", n.Title, "p", n.Other)
	case "moved_top":
		return s.tr(r, "import.moved_top", "t", n.Title)
	case "joined":
		return s.tr(r, "import.joined", "t", n.Title, "p", n.Other)
	case "left_out":
		return s.tr(r, "import.live_left_out", "t", n.Title)
	case "put_back":
		return s.tr(r, "import.live_put_back", "t", n.Title)
	case "renamed":
		return s.tr(r, "import.live_renamed", "t", n.Title)
	case "retyped":
		return s.tr(r, "import.live_retyped", "t", n.Title)
	case "split":
		return s.tr(r, "import.live_split", "t", n.Title)
	case "options":
		return s.tr(r, "import.live_options")
	case "recut":
		l := s.tr(r, "import.live_recut0")
		if n.Kept > 0 {
			l = s.trN(r, "import.live_recut", n.Kept)
		}
		if len(n.Dropped) > 0 {
			l += " " + s.trN(r, "import.recut_dropped", len(n.Dropped)+n.NoMore, "list", strings.Join(n.Dropped, ", "))
		}
		return l
	case "reuse":
		l := s.trN(r, "import.live_reuse", n.Count)
		if len(n.Dropped) > 0 {
			l += " " + s.trN(r, "import.recut_dropped", len(n.Dropped)+n.NoMore, "list", strings.Join(n.Dropped, ", "))
		}
		return l
	case "break":
		if n.Other == "" { // the first chapter of the book: nothing is before it, and it names the one document
			return s.tr(r, "import.live_break_first", "t", n.Title)
		}
		return s.tr(r, "import.live_break", "t", n.Title, "o", n.Other)
	case "title_case":
		return s.trN(r, "import.live_title_case", n.Count)
	case "line_title":
		return s.trN(r, "import.live_line_title", n.Count)
	case "split_para":
		return s.tr(r, "import.live_split_para", "o", n.Other, "e", n.Ends, "t", n.Title, "s", n.Starts)
	case "within":
		return s.trN(r, "import.live_within", n.Count, "rule", s.tr(r, "import.in_where_"+n.Title))
	case "matter":
		return s.trN(r, "import.live_matter_"+n.Side, n.Count, "t", n.Title)
	case "pattern":
		return s.trN(r, "import.live_pattern", n.Count)
	case "undone":
		return s.tr(r, "import.live_undone")
	}
	return ""
}

// importErrText says an op's refusal in the page's language, by the reason the op gave; the op's English is the fallback, started with a
// capital, for what has no reason (a converter's own refusal of a file).
func (s *Server) importErrText(r *http.Request, e *ops.Error, kind string) string {
	reason, _ := e.Data["reason"].(string)
	switch reason {
	case "revision":
		return s.tr(r, "import.notice_revision")
	case "applying":
		return s.tr(r, "import.notice_applying")
	case "done":
		return s.tr(r, "import.notice_done")
	case "gone":
		return s.tr(r, "import.notice_gone")
	case "joined_into":
		return s.tr(r, "import.stays", "t", e.Data["title"], "j", e.Data["other"])
	case "join_target":
		return s.tr(r, "import.no_join")
	case "unreadable":
		return s.tr(r, "import.err_read")
	case "open_drafts":
		return s.tr(r, "import.err_open_drafts", "n", e.Data["open"])
	case "user_quota":
		return s.tr(r, "import.err_user_quota", "held", e.Data["held_mb"], "limit", e.Data["limit_mb"])
	case "total_quota":
		return s.tr(r, "import.err_total_quota", "limit", e.Data["limit_mb"])
	case "ack_big":
		return s.tr(r, "import.err_ack_big", "limit", commas(docs.ReadOnlyWords))
	case "no_caps":
		return s.tr(r, "import.err_no_caps")
	case "no_line":
		return s.tr(r, "import.err_no_line")
	case "too_many_docs":
		return s.tr(r, "import.err_too_many_docs", "n", commas(intOf(e.Data["n"])), "max", commas(imports.MaxMarkdown))
	case "text_utf16", "text_empty":
		return s.tr(r, "import.err_"+reason)
	case "text_big", "text_paragraphs", "text_overflow":
		return s.tr(r, "import.err_"+reason, "n", commas(intOf(e.Data["n"])))
	case "pattern_too_much":
		return s.tr(r, "import.err_pattern_too_much")
	case "break_cap", "break_kind", "break_noop", "no_previous", "within_none", "matter_none", "look_kind", "pattern_empty", "pattern_long", "pattern_invalid", "pattern_big":
		return s.tr(r, "import.err_"+reason)
	case "not_docx", "not_zip", "not_epub", "not_pdf", "not_text", "not_export", "bad_zip", "empty", "disk_full", "title_empty", "nothing_to_undo", "cannot_move":
		return s.tr(r, "import.err_"+reason)
	}
	switch {
	case e.Code == ops.CodeTooLarge && (kind == "epub" || kind == "pdf"):
		return s.tr(r, "import.too_big")
	case e.Code == ops.CodeTooLarge:
		return s.tr(r, "import.too_big_34")
	case e.Code == ops.CodeRateLimited && strings.Contains(e.Message, "sending a file"):
		return s.tr(r, "import.err_busy_you")
	case e.Code == ops.CodeRateLimited && kind == "pdf":
		return s.tr(r, "import.pdf_busy")
	case e.Code == ops.CodeRateLimited:
		return s.tr(r, "import.busy")
	}
	return sentence(e.Message)
}

// importRefused answers a change or an apply the draft did not accept, with the draft as it is and the reason: the review body for htmx
// (status 422, so it is swapped in), the whole page for a plain post. Nothing of the draft goes in the address.
func (s *Server) importRefused(w http.ResponseWriter, r *http.Request, id string, err error, open, prefix string, mod ...func(*reviewOpts)) {
	e := ops.AsError(err)
	if e.Code == ops.CodeNotFound || e.Code == ops.CodeUnauthenticated || e.Code == ops.CodeForbidden || e.Code == ops.CodeInternal {
		s.fail(w, r, err)
		return
	}
	msg := prefix + s.importErrText(r, e, "")
	ro := reviewOpts{open: open, notice: msg, focus: "imp-notice"}
	for _, m := range mod {
		m(&ro)
	}
	v, _, gerr := s.importReviewView(r, id, ro)
	if gerr != nil {
		s.importReviewFailed(w, r, id, gerr)
		return
	}
	v.Live = msg
	if isHTMX(r) {
		s.reviewFragment(w, r, http.StatusUnprocessableEntity, v)
		return
	}
	s.importPageRender(w, r, http.StatusUnprocessableEntity, v)
}

// uiImportApply creates the import: the draft is claimed once, so two tabs or a double press make one project.
func (s *Server) uiImportApply(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	id := r.PathValue("id")
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	rev, _ := strconv.Atoi(f("rev"))
	into, ierr := s.draftInto(r, id) // the draft's own project
	if ierr != nil {
		s.fail(w, r, ierr)
		return
	}
	// A Word document with a chapter of docs.ReadOnlyWords or more would open read-only: the writer says so once, or cuts it further.
	// The check fails closed: a draft that cannot be read is not applied.
	gate, gerr := s.Reg.Call(r.Context(), s.principal(r), "import.draft.get", imports.DraftGetIn{ID: id, Preview: true})
	if gerr != nil {
		s.importRefused(w, r, id, gerr, "", "")
		return
	}
	if d := gate.(imports.DraftOut); d.Preview != nil && d.Preview.Breaks != nil && !d.Item.Options.AckBig {
		for _, n := range d.Preview.Nodes {
			if n.Words >= docs.ReadOnlyWords {
				s.importRefused(w, r, id, ops.ConflictWith("A document that long opens read-only.", map[string]any{"reason": "ack_big"}), "", "")
				return
			}
		}
	}
	var out any
	var err error
	if into != nil {
		out, err = s.Reg.Call(r.Context(), s.principal(r), "import.append", imports.AppendIn{ProjectID: into.ID, StageID: id, Rev: rev, Token: f("token"), EditionLabel: f("edition_label")})
	} else {
		out, err = s.Reg.Call(r.Context(), s.principal(r), "import.apply", imports.ApplyIn{StageID: id, Rev: rev, Token: f("token"), Title: f("title"), Kind: f("kind"), Author: f("author"),
			EditionLabel: f("edition_label"), UseCover: f("use_cover") == "true"})
	}
	if err != nil {
		s.importRefused(w, r, id, err, "", "")
		return
	}
	ao := out.(imports.ApplyOut)
	slug := ao.Slug
	if into != nil {
		slug = into.Slug
	}
	dest := "/p/" + slug
	if ao.FirstDocumentID != "" {
		dest += "/d/" + ao.FirstDocumentID
	}
	// An answer with more to say (an edition kept, suggestions and comments written, warnings) is a page of its own, with the result first.
	if ao.EditionID != "" || ao.Suggestions > 0 || ao.Comments > 0 {
		dest = s.importReviewURL(id, into)
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// uiImportDiscard throws the draft away and goes back to the chooser, which no longer lists it.
func (s *Server) uiImportDiscard(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	id := r.PathValue("id")
	dest := "/import"
	// where to go back to is the draft's own project, read before the draft is gone; one that cannot be read (its project is out of reach) goes to /import
	if into, err := s.draftInto(r, id); err == nil && into != nil {
		dest = "/p/" + into.Slug + "/import"
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.discard", imports.DraftIDIn{ID: id}); err != nil {
		s.fail(w, r, err)
		return
	}
	if r.PostFormValue("to") == "git" {
		dest = "/import/git"
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// ---- Git ------------------------------------------------------------------------------------------------------------

// uiImportGitStart starts the clone of a repository, and goes to its draft, whose page follows the fetch.
func (s *Server) uiImportGitStart(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	in := gitmirror.GitPreviewIn{Branch: f("branch"), Path: f("path"), SplitScenes: "none"}
	if f("split_scenes") == "h2" {
		in.SplitScenes = "h2"
	}
	if f("rg-src") == "remote" {
		in.RemoteID = f("remote_id")
	} else {
		in.URL = f("url")
		if t := r.PostFormValue("token"); t != "" {
			in.Token, in.Username = t, f("username")
		}
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.git_preview", in)
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeUnauthenticated || e.Code == ops.CodeNotFound && in.RemoteID == "" {
			s.fail(w, r, err)
			return
		}
		code, _ := e.Data["git_code"].(string)
		if code == "" {
			switch e.Code {
			case ops.CodeForbidden:
				code = "forbidden"
			case ops.CodeNotFound:
				code = "not_found"
			case ops.CodeInvalid:
				code = "invalid"
			}
		}
		s.importGitPage(w, r, http.StatusUnprocessableEntity, e.Message, code)
		return
	}
	dest := "/import/d/" + out.(gitmirror.GitPreviewOut).DraftID
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// uiImportGitOpen opens the draft of a job that was started before drafts existed.
func (s *Server) uiImportGitOpen(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	job := r.PathValue("job")
	// import.git_preview with a job makes the job's draft if it has none; it answers a conflict for a job that failed, but the draft is made first.
	_, err := s.Reg.Call(r.Context(), s.principal(r), "import.git_preview", gitmirror.GitPreviewIn{JobID: job})
	if err != nil && ops.AsError(err).Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	id := ""
	if jo, jerr := s.Reg.Call(r.Context(), s.principal(r), "import.git_jobs", struct{}{}); jerr == nil {
		for _, j := range jo.(gitmirror.GitJobsOut).Items {
			if j.ID == job {
				id = j.DraftID
			}
		}
	}
	if id == "" {
		s.fail(w, r, ops.NotFound("import"))
		return
	}
	if r.PostFormValue("discard") != "" {
		if _, err := s.Reg.Call(r.Context(), s.principal(r), "import.draft.discard", imports.DraftIDIn{ID: id}); err != nil {
			s.fail(w, r, err)
			return
		}
		http.Redirect(w, r, "/import/git", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/import/d/"+id, http.StatusSeeOther)
}

// intOf is a number an op's error data holds, whatever it was stored as.
func intOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}
