// Package export writes a project out as Markdown: one file, or a zip that
// mirrors the manuscript tree.
//
// Exports are deterministic. The same project state gives the same bytes: the
// walk order is fixed, a document's text is written exactly as stored (never
// re-serialised, so the alternatives comment and every source block survive),
// the zip has sorted entries, no compression and a fixed time taken from the
// project's updated_at, and nothing about the exporting user or the clock
// reaches the file. Every export is recorded in the exports table with the
// ids and hashes of what it held.
//
// Memory: an export is built in memory. The project is capped at 2,000,000
// words, 5,000 documents and 64 MB of Markdown (words alone would not bound
// code blocks or HTML, which count as almost no words), so the text read plus
// the output buffer stays under a few hundred megabytes.
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Limits: past these an export is refused rather than built.
const (
	MaxWords     = 2_000_000
	MaxDocuments = 5_000
	MaxBytes     = 64 << 20 // of Markdown text
)

// SchemaVersion is the version of the bundle manifest (scriobh.json).
const SchemaVersion = 1

// MarkdownIn asks for a Markdown export.
type MarkdownIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Mode      string `json:"mode,omitempty" enum:"single,bundle" desc:"single: one .md file; bundle: a .zip mirroring the manuscript tree with a manifest. Default single."`
	Notes     bool   `json:"notes,omitempty" desc:"Include the notes documents (the ones you keep for yourself) in a notes section, or a notes/ folder. Default false."`
	// Attachments is on by default for a bundle: the files the exported documents link to go in attachments/.
	Attachments *bool `json:"attachments,omitempty" desc:"Bundle only: put the files the documents link to under attachments/ (default true). A single file never holds them."`
	// Ghosts chooses what happens to ghosted text.
	Ghosts string `json:"ghosts,omitempty" enum:"keep,omit" desc:"keep (default): ghosted text is exported with the rest, tags and all, and each document's alternatives comment is kept byte for byte. omit: ghosted text and its tags are left out and the comment is brought in line."`
	// Alternatives chooses what happens to each document's alternatives comment.
	Alternatives string `json:"alternatives,omitempty" enum:"keep,omit" desc:"keep (default): the trailing comment that holds the other wordings of each document is exported byte for byte, so the file can be imported again with them. omit: the comment is left off, and the file is the chosen text alone, as plain Markdown. The chosen wording is in the text either way."`
	// IncludeExcluded keeps the documents marked not to compile.
	IncludeExcluded *bool `json:"include_excluded,omitempty" desc:"Documents marked 'do not compile' (and everything inside them). Default: a bundle keeps them (it is the full copy that can be restored, and omits nothing) and a single file leaves them out, as a compile does. Set it to override either way. The manifest lists what was left out."`
}

// ProjectRef names the project to export.
func (in MarkdownIn) ProjectRef() string { return in.ProjectID }

// File is the export. Over REST it is the file itself; elsewhere it is this
// JSON, with the file base64-encoded in body.
type File struct {
	ExportID    string `json:"export_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Documents   int    `json:"documents" desc:"How many documents the export holds."`
	Skipped     int    `json:"skipped,omitempty" desc:"Documents left out because they are marked not to compile (or sit inside one that is)."`
	Body        []byte `json:"body" desc:"The file. The REST route returns it as the response body instead of JSON."`
}

// Download makes File an ops.Download.
func (f File) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Register adds the export op.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "export.markdown", Method: "POST", Path: "/api/v1/projects/{id}/export", Status: 200,
		Desc: "Export the manuscript as Markdown: one file, or a zip that mirrors the tree. Recorded with a manifest of what it held.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, markdown)
	registerHTML(r)
}

// ---- reading the project -----------------------------------------------------------

type projectRow struct {
	ID, Title, Subtitle, Author, Kind, Language, UpdatedAt string
	// Read for the mirror's manifest; the export itself does not use them.
	Slug, Genre, Status string
	Trashed, Archived   bool
	// CoverColor is read for the HTML export's title page.
	CoverColor string
	// cover is the project's own cover picture for the HTML export's title page (nil: the generated cover).
	cover *htmlCover
	// Editions are the books the project was imported from, as metadata: for the manifests of the bundle and of
	// the mirror. The source file itself is never part of either.
	Editions []EditionMeta
}

// EditionMeta is what a manifest says about an imported edition (migration 1800): enough to know which book
// the project came from and whether a file is the one received (its SHA-256), and nothing of the file.
type EditionMeta struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Identifier string `json:"identifier,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	ImportedAt string `json:"imported_at"`
	Chapters   int    `json:"chapters"`
}

// loadEditions reads a project's editions in the order they were imported.
func loadEditions(ctx context.Context, q interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}, projectID string) ([]EditionMeta, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.kind, e.label, e.identifier, e.sha256, e.size, e.imported_at,
		(SELECT count(*) FROM edition_chapters c WHERE c.edition_id = e.id) FROM editions e WHERE e.project_id = ? ORDER BY e.imported_at, e.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EditionMeta
	for rows.Next() {
		var m EditionMeta
		if err := rows.Scan(&m.ID, &m.Kind, &m.Label, &m.Identifier, &m.SHA256, &m.Size, &m.ImportedAt, &m.Chapters); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// doc is one live document of the project.
type doc struct {
	ID, ParentID, Kind, Title, Content, Status string
	Position                                   float64
	Revision, WordCount                        int
	kids                                       []*doc
	hash                                       string
	// Outline metadata, read for the mirror's manifest; the export itself
	// does not use it.
	SynopsisShort, SynopsisLong, Pov, Location, StoryDate, StoryDateEnd string
	TargetWords                                                         int
	Include                                                             bool
	// The entities the point of view and location are linked to, with the
	// book's names for them, when they are in the project's scope.
	PovEntity, LocationEntity *EntityRef
}

// EntityRef names an entity in a manifest.
type EntityRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func load(ctx context.Context, d *db.DB, projectID string) (projectRow, []*doc, error) {
	return loadWith(ctx, d, projectID, true)
}

// loadWith is load with the export caps optional: the mirror writes every
// project, however large, and must not refuse one the export would.
func loadWith(ctx context.Context, d *db.DB, projectID string, capped bool) (projectRow, []*doc, error) {
	// One read transaction, so the project row and every document are one snapshot.
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return projectRow{}, nil, err
	}
	defer tx.Rollback()
	var p projectRow
	p.ID = projectID
	if err := tx.QueryRowContext(ctx, `SELECT title, subtitle, author_name, kind, language, updated_at, slug, genre, status, trashed_at IS NOT NULL, archived_at IS NOT NULL, cover_color
		FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).
		Scan(&p.Title, &p.Subtitle, &p.Author, &p.Kind, &p.Language, &p.UpdatedAt, &p.Slug, &p.Genre, &p.Status, &p.Trashed, &p.Archived, &p.CoverColor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return projectRow{}, nil, ops.NotFound("project")
		}
		return projectRow{}, nil, err
	}
	var eerr error
	if p.Editions, eerr = loadEditions(ctx, tx, projectID); eerr != nil {
		return projectRow{}, nil, eerr
	}
	const live = ` FROM documents WHERE project_id = ? AND deleted_at IS NULL AND compendium_id IS NULL AND kind <> 'research'`
	var count, words, size int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(word_count), 0), COALESCE(sum(length(CAST(content AS BLOB))), 0)`+live, projectID).Scan(&count, &words, &size); err != nil {
		return projectRow{}, nil, err
	}
	if capped && count > MaxDocuments {
		return projectRow{}, nil, ops.Invalid("", fmt.Sprintf("This project has %d documents; exports are limited to %d. Export part of it by moving the rest to the trash first, or ask the administrator.", count, MaxDocuments))
	}
	if capped && size > MaxBytes {
		return projectRow{}, nil, ops.Invalid("", fmt.Sprintf("This project holds %d MB of text; exports are limited to %d MB.", size>>20, MaxBytes>>20))
	}
	if capped && words > MaxWords {
		return projectRow{}, nil, ops.Invalid("", fmt.Sprintf("This project has %d words; exports are limited to %d.", words, MaxWords))
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, content, status, position, revision, word_count,
		synopsis_short, synopsis_long, pov, location, story_date, story_date_end, target_words, include_in_compile,
		pov_entity_id, location_entity_id`+live, projectID)
	if err != nil {
		return projectRow{}, nil, err
	}
	defer rows.Close()
	var all []*doc
	linked := false
	povIDs, locIDs := map[string]string{}, map[string]string{}
	for rows.Next() {
		x := &doc{}
		var pe, le sql.NullString
		if err := rows.Scan(&x.ID, &x.ParentID, &x.Kind, &x.Title, &x.Content, &x.Status, &x.Position, &x.Revision, &x.WordCount,
			&x.SynopsisShort, &x.SynopsisLong, &x.Pov, &x.Location, &x.StoryDate, &x.StoryDateEnd, &x.TargetWords, &x.Include, &pe, &le); err != nil {
			return projectRow{}, nil, err
		}
		if pe.Valid {
			povIDs[x.ID], linked = pe.String, true
		}
		if le.Valid {
			locIDs[x.ID], linked = le.String, true
		}
		sum := sha256.Sum256([]byte(x.Content))
		x.hash = hex.EncodeToString(sum[:])
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return projectRow{}, nil, err
	}
	if linked {
		// The names are the book's: the override applied. An entity that has
		// left the project's scope has no name here, and is left out.
		v, err := vocab.Load(ctx, tx, projectID)
		if err != nil {
			return projectRow{}, nil, err
		}
		names := map[string]string{}
		for _, e := range v {
			names[e.ID] = e.Name
		}
		for _, x := range all {
			if id, ok := povIDs[x.ID]; ok && names[id] != "" {
				x.PovEntity = &EntityRef{ID: id, Name: names[id]}
			}
			if id, ok := locIDs[x.ID]; ok && names[id] != "" {
				x.LocationEntity = &EntityRef{ID: id, Name: names[id]}
			}
		}
	}
	return p, all, nil
}

// tree links the documents and returns the manuscript in reading order and the
// notes in reading order. The order is the one doc.list reports: depth first,
// siblings by position then id, anything whose parent is gone after the rest.
// A note, and everything inside one, is a note.
func tree(all []*doc) (manuscript, notes []*doc) {
	byID := map[string]*doc{}
	for _, x := range all {
		byID[x.ID] = x
	}
	var roots []*doc
	for _, x := range all {
		if p, ok := byID[x.ParentID]; ok && x.ParentID != "" && p != x {
			p.kids = append(p.kids, x)
		} else if x.ParentID == "" {
			roots = append(roots, x)
		}
	}
	less := func(s []*doc) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Position != s[j].Position {
				return s[i].Position < s[j].Position
			}
			return s[i].ID < s[j].ID
		})
	}
	less(roots)
	seen := map[string]bool{}
	var walk func(x *doc, inNotes bool, depth int) *doc
	// walk returns x with only its manuscript children linked; notes are lifted out.
	walk = func(x *doc, inNotes bool, depth int) *doc {
		seen[x.ID] = true
		less(x.kids)
		kids := x.kids
		x.kids = nil
		isNote := inNotes || x.Kind == "note"
		if isNote {
			notes = append(notes, x)
		}
		for _, k := range kids {
			if seen[k.ID] {
				continue
			}
			child := walk(k, isNote, depth+1)
			if !isNote && child.Kind != "note" {
				x.kids = append(x.kids, child)
			}
		}
		return x
	}
	var top []*doc
	for _, r := range roots {
		if !seen[r.ID] {
			top = append(top, walk(r, false, 0))
		}
	}
	// Orphans: a parent that is trashed or missing. They come last, as in docs.list.
	var orphans []*doc
	for _, x := range all {
		if !seen[x.ID] {
			if _, ok := byID[x.ParentID]; !ok {
				orphans = append(orphans, x)
			}
		}
	}
	less(orphans)
	for _, o := range orphans {
		if !seen[o.ID] {
			top = append(top, walk(o, false, 0))
		}
	}
	// The manuscript is the top-level nodes that are not notes, depth first.
	var flat func(x *doc)
	flat = func(x *doc) {
		manuscript = append(manuscript, x)
		for _, k := range x.kids {
			flat(k)
		}
	}
	for _, x := range top {
		if x.Kind != "note" {
			flat(x)
		}
	}
	return manuscript, notes
}

// ---- the op ---------------------------------------------------------------------------

type manifestProject struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Author   string `json:"author"`
	Language string `json:"language"`
}

type manifestDoc struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Revision  int    `json:"revision"`
	WordCount int    `json:"word_count"`
	SHA256    string `json:"sha256"`
}

type manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Format        string          `json:"format"`
	Project       manifestProject `json:"project"`
	ExportedAt    string          `json:"exported_at"`
	IncludesNotes bool            `json:"includes_notes"`
	Documents     []manifestDoc   `json:"documents"`
	// Attachments maps each attachment id the Markdown links (/files/att_...) to
	// its file in the bundle. Absent when there are none.
	Attachments []manifestAttachment `json:"attachments,omitempty"`
	// Editions are the books the project was imported from, as metadata. Absent when there are none.
	Editions []EditionMeta `json:"editions,omitempty"`
}

func markdown(c *ops.Ctx, in MarkdownIn) (File, error) {
	mode := in.Mode
	if mode == "" {
		mode = "single"
	}
	if mode != "single" && mode != "bundle" {
		return File{}, ops.Invalid("mode", "The mode must be single or bundle.")
	}
	if in.Ghosts != "" && in.Ghosts != "keep" && in.Ghosts != "omit" {
		return File{}, ops.Invalid("ghosts", "ghosts is keep or omit.")
	}
	if in.Alternatives != "" && in.Alternatives != "keep" && in.Alternatives != "omit" {
		return File{}, ops.Invalid("alternatives", "alternatives is keep or omit.")
	}
	p, all, err := load(c, c.DB(), in.ProjectID)
	if err != nil {
		return File{}, err
	}
	keepAll := mode == "bundle" // a bundle is a full copy: it omits nothing unless asked to
	if in.IncludeExcluded != nil {
		keepAll = *in.IncludeExcluded
	}
	all, skipped := dropNotCompiled(all, keepAll)
	applyGhosts(all, in.Ghosts, in.Alternatives, false)
	manuscript, notes := tree(all)
	if !in.Notes {
		notes = nil
	}

	var (
		body        []byte
		contentType string
		name        string
		included    []*doc
		err2        error
	)
	slug := projects.Slugify(p.Title)
	switch mode {
	case "single":
		body = single(p, manuscript, notes)
		contentType, name = "text/markdown; charset=utf-8", slug+".md"
		included = append(append(included, manuscript...), notes...)
	default:
		var entries []entry
		entries, included = layout(manuscript, notes)
		var attEntries []entry
		var attMeta []manifestAttachment
		// A preview runs without the data directory, so it describes the bundle
		// without the files.
		if (in.Attachments == nil || *in.Attachments) && !c.DryRun() {
			if attEntries, attMeta, err2 = bundleAttachments(c, c.Env, in.ProjectID, included); err2 != nil {
				return File{}, ops.AsError(err2)
			}
		}
		body, err2 = bundle("scriobh-markdown-bundle", p, in.Notes, entries, attEntries, attMeta)
		if err2 != nil {
			return File{}, ops.Internal(err2)
		}
		contentType, name = "application/zip", slug+".zip"
	}

	file := newFile(name, contentType, body, len(included))
	file.Skipped = len(skipped)
	if err := record(c, in.ProjectID, mode, "md", file, included,
		withSkipped(map[string]any{"includes_notes": in.Notes, "ghosts": ghostMode(in.Ghosts), "alternatives": ghostMode(in.Alternatives)}, skipped, true),
		withSkipped(map[string]any{"notes": in.Notes, "ghosts": ghostMode(in.Ghosts), "alternatives": ghostMode(in.Alternatives)}, skipped, false)); err != nil {
		return File{}, err
	}
	return file, nil
}

func ghostMode(m string) string {
	if m == "omit" {
		return "omit"
	}
	return "keep"
}

// applyGhosts prepares each document's text for an export. For Markdown, keep
// leaves the text exactly as stored; omit removes the ghosted text and mends the
// alternatives comment. For HTML the ghost tags are never shown: keep drops the
// tags and leaves the text, omit drops both. The hash recorded in the manifest
// is of the text as exported.
func applyGhosts(all []*doc, mode, alternatives string, html bool) {
	if mode != "omit" && !html && alternatives != "omit" {
		return
	}
	for _, x := range all {
		var out string
		switch {
		case html:
			out = text.StripGhosts(x.Content, mode == "omit")
		case alternatives == "omit":
			// The chosen text alone: no comment, so nothing to mend after the ghosts.
			out = alts.BodyWithoutComment(x.Content)
			if mode == "omit" {
				out = text.StripGhosts(out, true)
			}
		default:
			out = alts.OmitGhosts(x.Content)
		}
		if out != x.Content {
			x.Content = out
			sum := sha256.Sum256([]byte(out))
			x.hash = hex.EncodeToString(sum[:])
		}
	}
}

// newFile describes a built export.
func newFile(name, contentType string, body []byte, documents int) File {
	sum := sha256.Sum256(body)
	return File{ExportID: db.NewID("exp"), Filename: name, ContentType: contentType, Bytes: len(body),
		SHA256: hex.EncodeToString(sum[:]), Documents: documents, Body: body}
}

// record writes the export's row, with the ids and hashes of the documents it
// held, and its audit entry, in one transaction. manifest and audit add keys to
// the manifest JSON and the audit detail.
func record(c *ops.Ctx, projectID, preset, format string, file File, included []*doc, manifest, audit map[string]any) error {
	type held struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	}
	heldDocs := []held{}
	for _, x := range included {
		heldDocs = append(heldDocs, held{x.ID, x.hash})
	}
	m := map[string]any{"documents": heldDocs}
	for k, v := range manifest {
		m[k] = v
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return ops.Internal(err)
	}
	detail := map[string]any{"export_id": file.ExportID, "format": format, "preset": preset, "documents": file.Documents, "bytes": file.Bytes}
	for k, v := range audit {
		detail[k] = v
	}
	return c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO exports(id, project_id, user_id, preset, format, filename, bytes, sha256, manifest_json, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, file.ExportID, projectID, c.P.UserID, preset, format, file.Filename, file.Bytes, file.SHA256, string(mj), db.Now()); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "export.run", TargetType: "project", TargetID: projectID, Detail: detail})
		// A finished export is an event: the stream tells editors, webhooks tell the owner's
		// receivers. Ids and counts only (no title, no file name, no preset name); the mirror
		// ignores it, since an export changes nothing the projection shows.
		c.Emit(ops.Event{Type: "export.completed", ProjectID: projectID,
			Payload: map[string]any{"export_id": file.ExportID, "format": format, "documents": file.Documents, "bytes": file.Bytes}})
		return c.FlushTo(tx)
	})
}

// ---- single file -----------------------------------------------------------------------

var headingLevel = map[string]int{"part": 1, "front_matter": 1, "back_matter": 1, "chapter": 2, "scene": 3, "note": 2}

// single is one Markdown file: the title block, then every document in
// reading order, each under a heading made from its title (when it has one).
// A document's text is written exactly as stored.
func single(p projectRow, manuscript, notes []*doc) []byte {
	var b bytes.Buffer
	b.WriteString("# " + escapeInline(p.Title) + "\n")
	if s := strings.TrimSpace(p.Subtitle); s != "" {
		b.WriteString("\n*" + escapeInline(s) + "*\n")
	}
	if a := strings.TrimSpace(p.Author); a != "" {
		b.WriteString("\nby " + escapeInline(a) + "\n")
	}
	section := func(x *doc) {
		title := strings.TrimSpace(x.Title)
		if title == "" && strings.TrimSpace(x.Content) == "" {
			return
		}
		b.WriteString("\n")
		if title != "" {
			level := headingLevel[x.Kind]
			if level == 0 {
				level = 2
			}
			b.WriteString(strings.Repeat("#", level) + " " + escapeInline(title) + "\n")
			if x.Content != "" {
				b.WriteString("\n")
			}
		}
		if x.Content != "" {
			b.WriteString(x.Content)
			if !strings.HasSuffix(x.Content, "\n") {
				b.WriteString("\n")
			}
		}
	}
	for _, x := range manuscript {
		section(x)
	}
	if len(notes) > 0 {
		b.WriteString("\n# Notes\n")
		for _, x := range notes {
			section(x)
		}
	}
	return b.Bytes()
}

// escapeInline backslash-escapes the characters that would turn a plain title
// into Markdown, so a heading shows the title as written.
func escapeInline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_[]<>~|%&", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if strings.HasSuffix(out, "#") && !strings.HasSuffix(out, `\#`) {
		out = out[:len(out)-1] + `\#`
	}
	return out
}

// ---- the bundle ------------------------------------------------------------------------

// entry is one file of the bundle.
type entry struct {
	path string
	data []byte
	doc  *doc
}

// layout names the files: the manuscript mirrors the tree (a folder for every
// document that has children, holding the document's own text as 00-<slug>.md
// ahead of its children), and notes are flat under notes/. Names are a
// two-digit number from reading order and a slug of the title; a folder whose
// first document is front matter is numbered from 00.
func layout(manuscript, notes []*doc) ([]entry, []*doc) {
	var entries []entry
	var included []*doc
	// The manuscript is already flat in reading order; rebuild the nesting from
	// the kids links of its roots.
	isKid := map[string]bool{}
	for _, x := range manuscript {
		for _, k := range x.kids {
			isKid[k.ID] = true
		}
	}
	var roots []*doc
	for _, x := range manuscript {
		if !isKid[x.ID] {
			roots = append(roots, x)
		}
	}
	var place func(dir string, siblings []*doc)
	place = func(dir string, siblings []*doc) {
		start := 1
		if dir == "manuscript" && len(siblings) > 0 && siblings[0].Kind == "front_matter" {
			start = 0
		}
		width := digits(start + len(siblings) - 1)
		used := map[string]bool{}
		for i, x := range siblings {
			base := fmt.Sprintf("%0*d-%s", width, start+i, slug(x.Title))
			if len(x.kids) == 0 {
				name := unique(used, base, ".md")
				entries = append(entries, entry{path: dir + "/" + name, data: []byte(x.Content), doc: x})
				included = append(included, x)
				continue
			}
			folder := unique(used, base, "")
			entries = append(entries, entry{path: dir + "/" + folder + "/" + strings.Repeat("0", width) + "-" + slug(x.Title) + ".md", data: []byte(x.Content), doc: x})
			included = append(included, x)
			place(dir+"/"+folder, x.kids)
		}
	}
	place("manuscript", roots)
	if len(notes) > 0 {
		used := map[string]bool{}
		width := digits(len(notes))
		for i, x := range notes {
			name := unique(used, fmt.Sprintf("%0*d-%s", width, i+1, slug(x.Title)), ".md")
			entries = append(entries, entry{path: "notes/" + name, data: []byte(x.Content), doc: x})
			included = append(included, x)
		}
	}
	return entries, included
}

func digits(n int) int {
	d := len(strconv.Itoa(n))
	if d < 2 {
		d = 2
	}
	return d
}

// unique returns base+ext, or base-2+ext, base-3+ext... when the name is taken
// (names are compared without regard to case, as on a case-folding disk).
func unique(used map[string]bool, base, ext string) string {
	name := base
	for n := 2; used[strings.ToLower(name+ext)]; n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	used[strings.ToLower(name+ext)] = true
	return name + ext
}

var foldMap = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ī': "i", 'ĭ': "i", 'į': "i", 'ı': "i",
	'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o", 'ō': "o", 'ŏ': "o", 'ő': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y", 'ñ': "n", 'ń': "n", 'ň': "n", 'ç': "c", 'ć': "c", 'č': "c", 'ĉ': "c",
	'š': "s", 'ś': "s", 'ş': "s", 'ž': "z", 'ź': "z", 'ż': "z", 'ď': "d", 'đ': "d", 'ł': "l",
	'ř': "r", 'ť': "t", 'ğ': "g", 'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d",
}

// slug makes a file-name-safe word of a title: diacritics folded, lower case,
// everything else a single hyphen, at most 48 characters; "untitled" when
// nothing is left.
func slug(title string) string {
	var b strings.Builder
	dash := true
	emit := func(s string) {
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				dash = false
			} else if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	for _, r := range title {
		r = unicode.ToLower(r)
		if f, ok := foldMap[r]; ok {
			emit(f)
		} else if unicode.IsMark(r) {
			continue // a combining accent on a letter already written
		} else {
			emit(string(r))
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 48 {
		cut := s[:48]
		if s[48] != '-' { // do not leave half a word
			if i := strings.LastIndexByte(cut, '-'); i > 0 {
				cut = cut[:i]
			}
		}
		s = strings.Trim(cut, "-")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// bundle writes the zip: scriobh.json and every file, sorted by name, stored
// without compression, with a fixed time, no extra fields and no owner. Two
// exports of the same project are the same bytes.
func bundle(format string, p projectRow, includesNotes bool, entries []entry, attEntries []entry, attMeta []manifestAttachment, extra ...entry) ([]byte, error) {
	m := manifest{
		SchemaVersion: SchemaVersion,
		Format:        format,
		Project:       manifestProject{ID: p.ID, Title: p.Title, Kind: p.Kind, Author: p.Author, Language: p.Language},
		ExportedAt:    p.UpdatedAt,
		IncludesNotes: includesNotes,
		Documents:     []manifestDoc{},
		Attachments:   attMeta,
		Editions:      p.Editions,
	}
	for _, e := range entries {
		m.Documents = append(m.Documents, manifestDoc{ID: e.doc.ID, Path: e.path, Kind: e.doc.Kind, Title: e.doc.Title,
			Status: e.doc.Status, Revision: e.doc.Revision, WordCount: e.doc.WordCount, SHA256: e.doc.hash})
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	files := make([]entry, 0, len(entries)+len(attEntries)+len(extra)+1)
	files = append(files, entries...)
	files = append(files, extra...) // files that are not documents (the HTML bundle's index)
	files = append(files, attEntries...)
	files = append(files, entry{path: "scriobh.json", data: append(mj, '\n')})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	date, tm := dosTime(p.UpdatedAt)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.path, Method: zip.Store, ModifiedDate: date, ModifiedTime: tm}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dosTime is the MS-DOS date and time of a stored timestamp. They are set
// directly rather than through FileHeader.Modified, which would add an extra
// field. Anything unreadable or before 1980 becomes 1980-01-01.
func dosTime(stored string) (date, tm uint16) {
	t, err := db.Parse(stored)
	if err != nil || t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	t = t.UTC()
	return uint16(t.Year()-1980)<<9 | uint16(t.Month())<<5 | uint16(t.Day()),
		uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
}
