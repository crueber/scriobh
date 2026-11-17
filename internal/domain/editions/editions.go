// Package editions is the provenance of an imported book: the source file kept
// intact, its hash, the label and identifier it came with, and the chapters it
// was read as. See migration 1800 and docs/p7-notes/epub.md.
//
// The Go surface is small on purpose, because a later feature (a diff of a
// revised book against its source edition, a comparison of two editions) builds
// on it:
//
//	List, Get             the editions of a project, and one with its chapters
//	Chapters, Baseline    the chapters of an edition in reading order, and the
//	                      Markdown the converter made for one (before any edit)
//	ForDocument           the edition and chapter a document came from
//	Insert                record an edition (and its chapters) in a transaction
//	Stage                 keep the source file: staged before the transaction,
//	                      placed inside it, cleaned up after (see Staged)
//	Open                  the intact source file of an edition
//
// An edition is never a second writer: nothing here changes a document.
package editions

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Kinds an edition can have. Only epub is built; the list is the one
// project_sources allows.
const (
	KindEPUB     = "epub"
	KindDocx     = "docx"
	KindPDF      = "pdf"
	KindMarkdown = "markdown"
)

// Limits.
const (
	// MaxFileBytes is the largest source file kept. It is also what an import
	// request can carry (the body limit of the import ops, base64 included).
	MaxFileBytes = 32 << 20
	// MaxPerProject is how many editions one project may hold.
	MaxPerProject = 50
	// MaxChapters is how many chapters one edition records (the import's own
	// document limit).
	MaxChapters = 5000
	// MaxBaseline is the Markdown kept for one chapter. A chapter the importer keeps is at most 2 MiB (a document's
	// cap), and the baseline names each picture by a 64-character hash instead of a number: a text made only of
	// picture marks (22 bytes each) grows by under four times, so 8 MiB is enough for every chapter the importer
	// accepts, and the importer refuses a chapter over 2 MiB at the preview.
	MaxBaseline = 8 << 20
	// MaxLabel is the longest edition label.
	MaxLabel = 200
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Edition is one imported source of a project.
type Edition struct {
	ID         string         `json:"id" desc:"edn_..."`
	ProjectID  string         `json:"project_id"`
	SourceID   string         `json:"source_id,omitempty" desc:"The import record (project_sources) this edition was made by."`
	Kind       string         `json:"kind" enum:"epub,docx,pdf,markdown"`
	Label      string         `json:"label" desc:"What the edition is called: its title and date unless you named it."`
	Identifier string         `json:"identifier,omitempty" desc:"The book's own identifier (an ISBN or urn:uuid), as the source states it."`
	SHA256     string         `json:"sha256" desc:"Of the source file's bytes."`
	Size       int64          `json:"size" desc:"Bytes of the source file."`
	HasFile    bool           `json:"has_file" desc:"The source file is kept and can be downloaded by an editor or the owner."`
	Metadata   map[string]any `json:"metadata" desc:"What the source says about itself (title, authors, language, publisher, date) and counts of what the import read and did not keep."`
	Chapters   int            `json:"chapters" desc:"Chapters recorded."`
	ImportedAt string         `json:"imported_at"`
	ImportedBy string         `json:"imported_by,omitempty" desc:"The person who imported it: shown to the project's owners and to administrators only."`
}

// Chapter is one chapter of an edition as the converter read it.
type Chapter struct {
	Ord        int    `json:"ord" desc:"1-based reading order within the edition."`
	ParentOrd  int    `json:"parent_ord,omitempty" desc:"The ord of the part or chapter it sits in; 0 at the top."`
	DocumentID string `json:"document_id,omitempty" desc:"The document it became. Empty when the writer left it out or the document is gone; several chapters can name one document (merged), and a chapter split into scenes names the chapter's document."`
	SourcePath string `json:"source_path" desc:"The file in the source, with #fragment when a file holds more than one chapter."`
	Kind       string `json:"kind" enum:"part,chapter,scene,note,front_matter,back_matter"`
	Title      string `json:"title"`
	Words      int    `json:"words"`
	SHA256     string `json:"sha256" desc:"Of the baseline Markdown (Baseline)."`
}

// New is an edition to record.
type New struct {
	ID         string // empty: a new id
	ProjectID  string
	SourceID   string // empty for none
	Kind       string
	Label      string
	Identifier string
	SHA256     string // of the source file
	Size       int64
	Metadata   map[string]any
	By         string // the person; empty for none
	Now        string // empty: now
	Chapters   []NewChapter
}

// NewChapter is a chapter to record. Its ord is its position in New.Chapters
// (1-based); ParentOrd names an earlier one, or 0.
type NewChapter struct {
	ParentOrd  int
	DocumentID string
	SourcePath string
	Kind       string
	Title      string
	Words      int
	Baseline   string // the Markdown as the converter made it
}

// Querier is the part of *sql.DB, *sql.Tx and *sql.Conn the readers use.
type Querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// HashOf is the hash recorded for a chapter's baseline text.
func HashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// SumOf is the SHA-256 of a source file, as recorded.
func SumOf(b []byte) string { return HashOf(string(b)) }

func deflate(s string) ([]byte, error) {
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func inflate(b []byte) (string, error) {
	out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(b)), MaxBaseline+1))
	if err != nil {
		return "", err
	}
	if len(out) > MaxBaseline {
		return "", errors.New("editions: a baseline is larger than it may be")
	}
	return string(out), nil
}

// Validate checks what Insert would refuse for the shape of the input alone.
func (n *New) Validate() error {
	switch n.Kind {
	case KindEPUB, KindDocx, KindPDF, KindMarkdown:
	default:
		return ops.Invalid("kind", "An edition is an epub, docx, pdf or markdown source.")
	}
	if !shaRe.MatchString(n.SHA256) {
		return ops.Invalid("sha256", "An edition records the SHA-256 of its source file.")
	}
	if len(n.Chapters) > MaxChapters {
		return ops.Invalid("chapters", fmt.Sprintf("An edition records at most %d chapters.", MaxChapters))
	}
	if !db.ValidID(n.ProjectID) {
		return ops.Invalid("project_id", "Not a project id.")
	}
	for i, c := range n.Chapters {
		if c.ParentOrd < 0 || c.ParentOrd > i {
			return ops.Invalid("chapters", "A chapter's parent comes before it.")
		}
		if len(c.Baseline) > MaxBaseline {
			return ops.Invalid("chapters", "A chapter's text is larger than a document may be.")
		}
	}
	return nil
}

// Insert records an edition and its chapters in the caller's transaction. The
// source file, if it is kept, is placed by Staged.Place in the same one (it is
// what makes has_file true). It refuses a project that already holds
// MaxPerProject editions.
func Insert(tx *sql.Tx, n New, hasFile bool) (Edition, error) {
	if err := n.Validate(); err != nil {
		return Edition{}, err
	}
	label := cleanLine(n.Label, MaxLabel)
	if n.ID == "" {
		n.ID = db.NewID("edn")
	}
	if n.Now == "" {
		n.Now = db.Now()
	}
	var have int
	if err := tx.QueryRow(`SELECT count(*) FROM editions WHERE project_id = ?`, n.ProjectID).Scan(&have); err != nil {
		return Edition{}, err
	}
	if have >= MaxPerProject {
		return Edition{}, ops.Conflict(fmt.Sprintf("This project already holds %d editions, which is the most it keeps.", MaxPerProject))
	}
	meta, err := json.Marshal(orEmpty(n.Metadata))
	if err != nil {
		return Edition{}, err
	}
	ident := cleanLine(n.Identifier, 300)
	hf := 0
	if hasFile {
		hf = 1
	}
	if _, err := tx.Exec(`INSERT INTO editions(id, project_id, source_id, kind, label, identifier, sha256, size, has_file, metadata_json, imported_by, imported_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ProjectID, nullable(n.SourceID), n.Kind, label, ident, n.SHA256, n.Size, hf, string(meta), nullable(n.By), n.Now); err != nil {
		return Edition{}, err
	}
	for i, c := range n.Chapters {
		gz, err := deflate(c.Baseline)
		if err != nil {
			return Edition{}, err
		}
		if _, err := tx.Exec(`INSERT INTO edition_chapters(edition_id, ord, parent_ord, document_id, source_path, kind, title, words, sha256, baseline_gz) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			n.ID, i+1, c.ParentOrd, nullable(c.DocumentID), cleanLine(c.SourcePath, 600), c.Kind, cleanLine(c.Title, 300), c.Words, HashOf(c.Baseline), gz); err != nil {
			return Edition{}, err
		}
	}
	return Edition{ID: n.ID, ProjectID: n.ProjectID, SourceID: n.SourceID, Kind: n.Kind, Label: label, Identifier: ident, SHA256: n.SHA256, Size: n.Size,
		HasFile: hasFile, Metadata: orEmpty(n.Metadata), Chapters: len(n.Chapters), ImportedAt: n.Now, ImportedBy: n.By}, nil
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// cleanLine is a value as one short line.
func cleanLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

const editionCols = `e.id, e.project_id, COALESCE(e.source_id,''), e.kind, e.label, e.identifier, e.sha256, e.size, e.has_file, e.metadata_json,
	(SELECT count(*) FROM edition_chapters c WHERE c.edition_id = e.id), e.imported_at, COALESCE(e.imported_by,'')`

type scanner interface{ Scan(...any) error }

func scanEdition(s scanner) (Edition, error) {
	var e Edition
	var meta string
	var hf int
	if err := s.Scan(&e.ID, &e.ProjectID, &e.SourceID, &e.Kind, &e.Label, &e.Identifier, &e.SHA256, &e.Size, &hf, &meta, &e.Chapters, &e.ImportedAt, &e.ImportedBy); err != nil {
		return Edition{}, err
	}
	e.HasFile = hf == 1
	e.Metadata = map[string]any{}
	_ = json.Unmarshal([]byte(meta), &e.Metadata)
	return e, nil
}

// ErrNotFound says there is no such edition in the project.
var ErrNotFound = errors.New("editions: not found")

// List is the project's editions, oldest first.
func List(ctx context.Context, q Querier, projectID string) ([]Edition, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+editionCols+` FROM editions e WHERE e.project_id = ? ORDER BY e.imported_at, e.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Edition{}
	for rows.Next() {
		e, err := scanEdition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get is one edition of the project; ErrNotFound when the project has no such
// edition (an id from another project is not found).
func Get(ctx context.Context, q Querier, projectID, editionID string) (Edition, error) {
	e, err := scanEdition(q.QueryRowContext(ctx, `SELECT `+editionCols+` FROM editions e WHERE e.project_id = ? AND e.id = ?`, projectID, editionID))
	if errors.Is(err, sql.ErrNoRows) {
		return Edition{}, ErrNotFound
	}
	return e, err
}

// Chapters is the edition's chapters in reading order (without their text).
func Chapters(ctx context.Context, q Querier, editionID string) ([]Chapter, error) {
	rows, err := q.QueryContext(ctx, `SELECT ord, parent_ord, COALESCE(document_id,''), source_path, kind, title, words, sha256 FROM edition_chapters WHERE edition_id = ? ORDER BY ord`, editionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chapter{}
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.Ord, &c.ParentOrd, &c.DocumentID, &c.SourcePath, &c.Kind, &c.Title, &c.Words, &c.SHA256); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Baseline is the Markdown the converter made for a chapter, as it was before
// the writer's edits. Picture links in it name the picture by the SHA-256 of its
// cleaned bytes (scriobh-picture:<sha256>), not by an attachment, so a baseline
// reads the same in every project.
func Baseline(ctx context.Context, q Querier, editionID string, ord int) (string, error) {
	var gz []byte
	err := q.QueryRowContext(ctx, `SELECT baseline_gz FROM edition_chapters WHERE edition_id = ? AND ord = ?`, editionID, ord).Scan(&gz)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return inflate(gz)
}

// ForDocument says which edition and chapter a document was imported as (the
// most recent edition if several name it). ok is false for a document no edition
// names. A scene split from a chapter is not named: use its parent chapter's
// document, or document_sources.
func ForDocument(ctx context.Context, q Querier, documentID string) (Edition, Chapter, bool, error) {
	var eid string
	var c Chapter
	err := q.QueryRowContext(ctx, `SELECT c.edition_id, c.ord, c.parent_ord, c.source_path, c.kind, c.title, c.words, c.sha256
		FROM edition_chapters c JOIN editions e ON e.id = c.edition_id WHERE c.document_id = ? ORDER BY e.imported_at DESC, c.ord LIMIT 1`, documentID).
		Scan(&eid, &c.Ord, &c.ParentOrd, &c.SourcePath, &c.Kind, &c.Title, &c.Words, &c.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return Edition{}, Chapter{}, false, nil
	}
	if err != nil {
		return Edition{}, Chapter{}, false, err
	}
	c.DocumentID = documentID
	e, err := scanEdition(q.QueryRowContext(ctx, `SELECT `+editionCols+` FROM editions e WHERE e.id = ?`, eid))
	if err != nil {
		return Edition{}, Chapter{}, false, err
	}
	return e, c, true, nil
}
