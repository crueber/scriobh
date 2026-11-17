package editions

import (
	"errors"

	"github.com/crueber/scriobh/internal/ops"
)

// ProjectIn names a project.
type ProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

func (in ProjectIn) ProjectRef() string { return in.ProjectID }

// EditionIn names one edition of a project.
type EditionIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	EditionID string `path:"edition_id" json:"-" desc:"Edition id (edn_...)."`
}

func (in EditionIn) ProjectRef() string { return in.ProjectID }

// ListOut is a project's editions.
type ListOut struct {
	Items []Edition `json:"items"`
}

// GetOut is one edition with its chapters.
type GetOut struct {
	Item     Edition   `json:"item"`
	Chapters []Chapter `json:"chapters" desc:"In reading order. Each says where it sits in the source (source_path), what it became (document_id) and how big it is; the text it was imported as is not returned."`
}

// FileOut is the source file. Over REST it is the file itself, with a download
// header; elsewhere it is this JSON, with the bytes in body (base64).
type FileOut struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	Body        []byte `json:"body"`
}

// Download makes FileOut an ops.Download.
func (f FileOut) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Register adds the edition ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "edition.list", Method: "GET", Path: "/api/v1/projects/{id}/editions", Auth: ops.RequireProject(ops.ProjectReader),
		Desc: "The editions of a project: the books it was imported from, with each one's label, identifier, source file hash and size, and when it was imported. The source file itself is edition.download."},
		list)
	ops.Register(r, ops.Op{Name: "edition.get", Method: "GET", Path: "/api/v1/projects/{id}/editions/{edition_id}", Auth: ops.RequireProject(ops.ProjectReader),
		Desc: "One edition with its chapters: where each came from in the book (source_path), the document it became, and its size. The book's own text is not returned."},
		get)
	ops.Register(r, ops.Op{Name: "edition.download", Method: "GET", Path: "/api/v1/projects/{id}/editions/{edition_id}/download", Auth: ops.RequireProject(ops.ProjectEditor), Scope: ops.ScopeWrite,
		Desc: "Download the source file of an edition, byte for byte as it was imported (checked against its recorded SHA-256). Owners and editors only, and an API key needs the write scope, as for an export: it is the whole book as it was received."},
		download)
}

// mayKnowWho says whether the caller may see who imported an edition: the project's owners, and the instance's
// administrators, as for the members list ("who is on the project is the owner's to know"). A reader, a reviewer or an
// editor reads the edition's record without the person's id.
func mayKnowWho(c *ops.Ctx, projectID string) bool {
	if c.P.Role == ops.RoleAdmin && !c.P.Pinned() {
		return true
	}
	if c.Env.ProjectRole == nil {
		return false
	}
	role, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	return ok && role == ops.ProjectOwner
}

// mayReadBook says whether the caller may be shown what the project left out of the book at import: the project's
// editors and owner, and for an API key the write scope (as for the download). Below that a chapter of the edition
// that became no document is not shown: not its title, not its path, not that it exists.
func mayReadBook(c *ops.Ctx, projectID string) bool {
	if c.P.Scope != "" && c.P.Scope != ops.ScopeWrite && c.P.Scope != ops.ScopeAdmin {
		return false
	}
	if c.Env.ProjectRole == nil {
		return false
	}
	role, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	return ok && (role == ops.ProjectEditor || role == ops.ProjectOwner)
}

func list(c *ops.Ctx, in ProjectIn) (ListOut, error) {
	items, err := List(c, c.DB().Read(), in.ProjectID)
	if err != nil {
		return ListOut{}, ops.Internal(err)
	}
	if !mayReadBook(c, in.ProjectID) {
		for i := range items {
			if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM edition_chapters WHERE edition_id = ? AND document_id IS NOT NULL`, items[i].ID).Scan(&items[i].Chapters); err != nil {
				return ListOut{}, ops.Internal(err)
			}
		}
	}
	if !mayKnowWho(c, in.ProjectID) {
		for i := range items {
			items[i].ImportedBy = ""
		}
	}
	return ListOut{Items: items}, nil
}

func get(c *ops.Ctx, in EditionIn) (GetOut, error) {
	e, err := Get(c, c.DB().Read(), in.ProjectID, in.EditionID)
	if errors.Is(err, ErrNotFound) {
		return GetOut{}, ops.NotFound("edition")
	}
	if err != nil {
		return GetOut{}, ops.Internal(err)
	}
	ch, err := Chapters(c, c.DB().Read(), e.ID)
	if err != nil {
		return GetOut{}, ops.Internal(err)
	}
	if !mayKnowWho(c, in.ProjectID) {
		e.ImportedBy = ""
	}
	if !mayReadBook(c, in.ProjectID) {
		kept := ch[:0]
		for _, x := range ch {
			if x.DocumentID != "" {
				kept = append(kept, x)
			}
		}
		ch, e.Chapters = kept, len(kept)
	}
	return GetOut{Item: e, Chapters: ch}, nil
}

func download(c *ops.Ctx, in EditionIn) (FileOut, error) {
	e, err := Get(c, c.DB().Read(), in.ProjectID, in.EditionID)
	if errors.Is(err, ErrNotFound) {
		return FileOut{}, ops.NotFound("edition")
	}
	if err != nil {
		return FileOut{}, ops.Internal(err)
	}
	b, err := Open(c.Env, e)
	if err != nil {
		return FileOut{}, ops.NotFound("source file") // not kept, missing, replaced or damaged: all the same
	}
	name := ops.SafeFilename(e.Label)
	ct := map[string]string{KindEPUB: "application/epub+zip", KindDocx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindPDF: "application/pdf"}[e.Kind]
	if ct == "" {
		ct = "application/octet-stream"
	}
	ext := map[string]string{KindEPUB: ".epub", KindDocx: ".docx", KindPDF: ".pdf"}[e.Kind]
	if ext == "" {
		ext = ".zip"
	}
	return FileOut{Filename: name + ext, ContentType: ct, SHA256: e.SHA256, Body: b}, nil
}
