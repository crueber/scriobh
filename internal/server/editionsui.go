package server

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The Editions pages: the books a project was imported from, each with the file as it was received, its checksum and the
// chapters it was read as. Everything is read through the edition.* ops; the file is served through a route of its own
// that calls edition.download, so a file that is not on this server is a sentence on a page and not a JSON error. The
// list is readable by anyone who can read the project (the op says so); the download and the person who imported are
// not. The file stays on this server: it is not in the Markdown copy, not in a Git push, and is shared with no one.

func (s *Server) registerEditionsUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/editions", s.editionsPage)
	m.HandleFunc("GET /p/{slug}/editions/{edn}", s.editionPage)
	m.HandleFunc("GET /p/{slug}/editions/{edn}/download", s.editionDownload)
}

// editionRow is one edition as the list shows it.
type editionRow struct {
	editions.Edition
	KindWord string
	Source   string // the file's name when the import recorded it
	Short    string // the first 12 characters of the checksum
	SizeText string
	When     string
	ByName   string // the person who imported it, for an owner who can read the members; empty otherwise
}

type editionChapter struct {
	editions.Chapter
	Depth      int
	Left       bool
	JoinedInto string // the title of the chapter whose document this one was joined into, when it was
	Short      string // the file's own name, for the row (the whole path is its title)
}

type editionsData struct {
	Project   projects.Project
	Slug      string
	CSRF      string
	CanGet    bool // an owner or an editor may download the original
	IsOwner   bool
	Items     []editionRow
	Max       int
	MaxFileMB int
	Notice    string // a one-shot line from the page that sent the person here
	Error     string
	// the detail page
	One      *editionRow
	Chapters []editionChapter
	Hidden   int // chapters beyond the ones shown
	Parts    int
}

// editionChaptersShown is how many chapters the detail page lists; the edition keeps up to 5,000.
const editionChaptersShown = 1000

func editionKindKey(kind string) string {
	switch kind {
	case editions.KindEPUB, editions.KindDocx, editions.KindPDF:
		return "editions.kind_" + kind
	}
	return "editions.kind_other"
}

func (s *Server) editionRows(r *http.Request, proj projects.Project, items []editions.Edition) []editionRow {
	T := s.page(r, "", nil).T
	loc := s.userZone(r)
	names := map[string]string{}
	listed := false // the project's members were read: only then can a missing name be said to be someone who left
	for _, e := range items {
		if e.ImportedBy != "" { // the op returns it to owners and administrators only
			if mo, err := s.Reg.Call(r.Context(), s.principal(r), "project.member.list", projects.MemberListIn{ID: proj.ID}); err == nil {
				if l, ok := mo.(projects.MemberListOut); ok {
					listed = true
					for _, mem := range l.Items {
						names[mem.UserID] = mem.DisplayName
					}
				}
			}
			break
		}
	}
	env := s.Reg.Env()
	rows := make([]editionRow, 0, len(items))
	for _, e := range items {
		// The record says a file was kept; the disk says whether it is still there. A link that saves a file carries the
		// download attribute, and a browser saves whatever answers it, so a file that is gone is said on the page instead.
		e.HasFile = editions.Present(env, e)
		row := editionRow{Edition: e, KindWord: T(editionKindKey(e.Kind)), SizeText: humanBytes(e.Size), When: whenIn(e.ImportedAt, loc),
			Short: e.SHA256}
		if len(row.Short) > 12 {
			row.Short = row.Short[:12]
		}
		if n, _ := e.Metadata["source_name"].(string); n != "" {
			row.Source = n
		}
		if e.ImportedBy != "" {
			row.ByName = names[e.ImportedBy]
			if row.ByName == "" && listed {
				row.ByName = T("editions.by_gone")
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Server) editionsBase(w http.ResponseWriter, r *http.Request) (editionsData, bool) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return editionsData{}, false
	}
	d := editionsData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, Max: editions.MaxPerProject, MaxFileMB: editions.MaxFileBytes >> 20,
		CanGet: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor, IsOwner: proj.Role == ops.ProjectOwner}
	if fl := s.flashTakeFor(r, "editions", func(m map[string]string) bool { return m["pid"] == proj.ID }); fl != nil {
		d.Notice, d.Error = fl["ok"], fl["bad"]
	}
	return d, true
}

func (s *Server) editionsPage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editionsBase(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "edition.list", editions.ProjectIn{ProjectID: d.Project.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Items = s.editionRows(r, d.Project, out.(editions.ListOut).Items)
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "editions", s.page(r, s.tr(r, "editions.title"), d))
}

func (s *Server) editionPage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.editionsBase(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "edition.get", editions.EditionIn{ProjectID: d.Project.ID, EditionID: r.PathValue("edn")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	g := out.(editions.GetOut)
	row := s.editionRows(r, d.Project, []editions.Edition{g.Item})[0]
	d.One = &row
	depth := map[int]int{}
	first := map[string]string{} // the document of a chapter -> the title of the first chapter that went into it
	for i, c := range g.Chapters {
		if i >= editionChaptersShown {
			d.Hidden = len(g.Chapters) - i
			break
		}
		dp := 0
		if c.ParentOrd > 0 {
			dp = depth[c.ParentOrd] + 1
		}
		if dp > 4 {
			dp = 4
		}
		depth[c.Ord] = dp
		ec := editionChapter{Chapter: c, Depth: dp, Left: c.DocumentID == "", Short: path.Base(strings.SplitN(c.SourcePath, "#", 2)[0])}
		if i := strings.Index(c.SourcePath, "#"); i >= 0 {
			ec.Short += c.SourcePath[i:]
		}
		if c.DocumentID != "" {
			if t, ok := first[c.DocumentID]; ok {
				ec.JoinedInto = t
			} else {
				first[c.DocumentID] = c.Title
			}
		}
		d.Chapters = append(d.Chapters, ec)
		if c.Kind == "part" {
			d.Parts++
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "edition", s.page(r, row.Label, d))
}

// editionDownload serves the original file. A person who may not download is refused by the op (403); a file that is not
// kept, or no longer matches its checksum, is a 404 that says so in one sentence.
func (s *Server) editionDownload(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("edn")
	out, err := s.Reg.Call(r.Context(), s.principal(r), "edition.download", editions.EditionIn{ProjectID: proj.ID, EditionID: id})
	if err != nil {
		var oe *ops.Error
		if errors.As(err, &oe) && oe.Code == ops.CodeNotFound {
			// Not a redirect to the page: the link has the download attribute, so a browser would save that page as the file.
			// The page itself says the file is gone (editionRows looks at the disk); this is for a file that went after the
			// page was drawn, or fails its checksum, and a failed download with the sentence is the honest answer.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintln(w, s.tr(r, "editions.no_file"))
			return
		}
		s.fail(w, r, err)
		return
	}
	writeDownload(w, out.(editions.FileOut))
}
