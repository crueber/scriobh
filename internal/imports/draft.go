package imports

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// Import drafts: a file chosen for import is held on the server while the writer reviews what it would create.
//
// The staged source lives in data/imports/stage/<id>/. That tree is outside files/, mirror/ and conflicts/, the only
// trees a backup archives, so a staged file is never in a backup, and nothing reads it but the review: not the mirror, an
// export, the compile, the search index, the diagnostics or a Git push. The draft row (migration 1320) holds the options
// the source is read with, the writer's edits and a revision; it holds no text of the book.
//
// The file is deleted when the import is applied, when the draft is discarded, and after the idle limit
// (SCRIOBH_IMPORT_STAGE_HOURS). A finished draft keeps only its counts and the new project's id, for a week.

// StageLimits are the limits of staged imports. Zero fields take the defaults.
type StageLimits struct {
	Hours   int // a draft idle this long is removed (default 72)
	MaxOpen int // open drafts one person may hold (default 5)
	UserMB  int // staged bytes one person may hold (default 256)
	TotalMB int // staged bytes in all (default 2048)
}

// Defaults of StageLimits.
const (
	DefaultStageHours   = 72
	DefaultStageMaxOpen = 5
	DefaultStageUserMB  = 256
	DefaultStageTotalMB = 2048
	// DoneKeep is how long a finished draft's row stays, so a reload of its page can say what happened.
	DoneKeep = 7 * 24 * time.Hour
)

func (l StageLimits) withDefaults() StageLimits {
	if l.Hours <= 0 {
		l.Hours = DefaultStageHours
	}
	if l.MaxOpen <= 0 {
		l.MaxOpen = DefaultStageMaxOpen
	}
	if l.UserMB <= 0 {
		l.UserMB = DefaultStageUserMB
	}
	if l.TotalMB <= 0 {
		l.TotalMB = DefaultStageTotalMB
	}
	return l
}

// Draft statuses.
const (
	DraftOpen     = "open"
	DraftApplying = "applying"
	DraftDone     = "done"
)

// Draft sources.
const (
	SourceUpload = "upload"
	SourceGitJob = "git_job"
)

// cutKind says the kind is one whose chapters the shared detector cuts and names for the paragraph they start at: a Word file, a text
// file, and a PDF or Pandoc document that has no headings of its own (or whose cut the writer chose). The cut options, the chapters
// started by hand, and the edits that stay across a change of rule belong to these.
func cutKind(kind string) bool {
	switch kind {
	case StageDocx, StageText, StagePdf, StagePandoc:
		return true
	}
	return false
}

// Kinds of staged source.
const (
	StageDocx   = "docx"
	StageEpub   = "epub"
	StagePdf    = "pdf"
	StageText   = "text" // a plain text file
	StageZip    = "zip"
	StagePandoc = "pandoc"
	StageFile   = "file"   // a lone WordPress .xml or Ghost .json
	StageFolder = "folder" // a folder's Markdown, written as a zip by the folder form
	StageGit    = "git"
)

// DraftOptions are the options a draft's source is read with: the option fields of the four preview and apply inputs.
type DraftOptions struct {
	Layout        string `json:"layout,omitempty"`
	DocxHeadings  string `json:"docx_headings,omitempty"`
	DocxChanges   string `json:"docx_changes,omitempty"`
	DocxSplit     string `json:"docx_split,omitempty"`
	DocxStyle     string `json:"docx_split_style,omitempty"`
	DocxPattern   string `json:"docx_split_pattern,omitempty" desc:"The text or expression a chapter's first line must match, with docx_split pattern."`
	DocxMatch     string `json:"docx_split_match,omitempty" enum:"starts,regex"`
	DocxTry       string `json:"docx_split_try,omitempty" desc:"A pattern the writer is trying: it feeds the count and the first matches the review shows, and cuts nothing."`
	DocxTryMatch  string `json:"docx_split_try_match,omitempty" enum:"starts,regex"`
	DocxWords     int    `json:"docx_split_words,omitempty"`
	DocxBreaks    string `json:"docx_breaks,omitempty" desc:"The writer's own chapter starts, as paragraph ordinals separated by commas."`
	SplitScenes   string `json:"split_scenes,omitempty"`
	EpubSections  string `json:"epub_sections,omitempty"`
	EpubPages     string `json:"epub_pages,omitempty"`
	Pictures      string `json:"pictures,omitempty"`
	ScrivResearch string `json:"scrivener_research,omitempty"`
	ScrivNotes    string `json:"scrivener_notes,omitempty"`
	ScrivSheets   string `json:"scrivener_sheets,omitempty"`
	WPStatus      string `json:"wp_status,omitempty"`
	WPTypes       string `json:"wp_types,omitempty"`
	GhostStatus   string `json:"ghost_status,omitempty"`
	GhostTypes    string `json:"ghost_types,omitempty"`
	PandocFormat  string `json:"pandoc_format,omitempty"`
	UseCover      bool   `json:"use_cover,omitempty"`
	AckBig        bool   `json:"ack_big,omitempty" desc:"The writer said to import documents of 25,000 words or more as they are, though they open read-only."`
	EditionLabel  string `json:"edition_label,omitempty"`
}

// optionKeys are the names an update may set, with how each is read; the value is checked by the op that reads it.
var optionKeys = []string{"layout", "docx_headings", "docx_changes", "docx_split", "docx_split_style", "docx_split_pattern", "docx_split_match", "docx_split_try", "docx_split_try_match", "docx_split_words", "docx_breaks", "split_scenes", "epub_sections", "epub_pages", "pictures",
	"scrivener_research", "scrivener_notes", "scrivener_sheets", "wp_status", "wp_types", "ghost_status", "ghost_types", "pandoc_format", "use_cover", "edition_label", "ack_big"}

// set puts one option by its wire name. It reports false for a name that is not an option.
func (o *DraftOptions) set(k, v string) bool {
	switch k {
	case "layout":
		o.Layout = v
	case "docx_headings":
		o.DocxHeadings = v
		if v == "auto" {
			o.DocxHeadings = ""
		}
	case "docx_changes":
		o.DocxChanges = v
	case "docx_split":
		o.DocxSplit = v
		if v == SplitAuto {
			o.DocxSplit = ""
		}
	case "docx_split_style":
		o.DocxStyle = cleanLabel(v)
	case "docx_split_pattern":
		o.DocxPattern = cleanPattern(v)
	case "docx_split_match":
		o.DocxMatch = v
		if v == MatchStarts {
			o.DocxMatch = ""
		}
	case "docx_split_try":
		o.DocxTry = cleanPattern(v)
	case "docx_split_try_match":
		o.DocxTryMatch = v
		if v == MatchStarts {
			o.DocxTryMatch = ""
		}
	case "docx_split_words":
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		o.DocxWords = n
	case "docx_breaks":
		o.DocxBreaks = canonBreaks(v)
	case "split_scenes":
		o.SplitScenes = v
	case "epub_sections":
		o.EpubSections = v
	case "epub_pages":
		o.EpubPages = v
	case "pictures":
		o.Pictures = v
	case "scrivener_research":
		o.ScrivResearch = v
	case "scrivener_notes":
		o.ScrivNotes = v
	case "scrivener_sheets":
		o.ScrivSheets = v
	case "wp_status":
		o.WPStatus = v
	case "wp_types":
		o.WPTypes = v
	case "ghost_status":
		o.GhostStatus = v
	case "ghost_types":
		o.GhostTypes = v
	case "pandoc_format":
		o.PandocFormat = v
	case "use_cover":
		o.UseCover = v == "true" || v == "on" || v == "1"
	case "ack_big":
		o.AckBig = v == "true" || v == "on" || v == "1"
	case "edition_label":
		o.EditionLabel = cleanLabel(v)
	default:
		return false
	}
	return true
}

// canonBreaks reads a list of paragraph ordinals ("12, 40") and writes it sorted, without repeats, as the draft keeps it. Anything that is not a
// number is dropped; the list is bounded.
func canonBreaks(v string) string {
	seen := map[int]bool{}
	var ns []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= maxParagraphs && !seen[n] {
			seen[n] = true
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	if len(ns) > MaxExplicitBrks+1 {
		ns = ns[:MaxExplicitBrks+1]
	}
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// breakList is the ordinals of a canonical list.
func breakList(v string) []int {
	var ns []int
	for _, f := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(f); err == nil {
			ns = append(ns, n)
		}
	}
	return ns
}

// shaped is the options with those that do not change the cut taken away: a pattern that is only being tested (docx_split is not pattern)
// shapes nothing, so a test neither re-cuts the book nor clears the writer's tick.
func (o DraftOptions) shaped() DraftOptions {
	o.UseCover, o.EditionLabel, o.AckBig = false, "", false
	o.DocxTry, o.DocxTryMatch = "", "" // a pattern being tried is never part of the cut
	if o.DocxSplit != SplitPattern {
		o.DocxPattern, o.DocxMatch = "", ""
	}
	return o
}

// cleanPattern is a pattern a person typed: no control character (a line break or a tab) or direction override, and nothing else changed (the
// spaces in an expression mean something, so none is taken from either end, and a pattern that is too long is refused, not cut). Only a size that no pattern can be is cut, so a
// request cannot make the draft hold a large string.
func cleanPattern(s string) string {
	if len(s) > 4096 {
		s = s[:4096]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x200e || r == 0x200f || r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanLabel is a name a person typed: no direction override or control character, one space between words.
func cleanLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x200e || r == 0x200f || r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len([]rune(out)) > 200 {
		out = string([]rune(out)[:200])
	}
	return out
}

// Gone is what the writer left out, or joined into another chapter, remembered with the title it had: the preview no longer
// lists it, and "Put back" needs a name.
type Gone struct {
	Title  string `json:"title"`
	Into   string `json:"into,omitempty"`   // the title of the document it was joined into
	To     string `json:"to,omitempty"`     // the key of that document
	Inside int    `json:"inside,omitempty"` // how many documents were inside it when it was left out
	Words  int    `json:"words,omitempty"`  // the words of the document it was made on: a leaving out or a join is for that text only
	// ToWords is the words the document it was joined into had on its own, before anything was joined into it: a join is for the end of that
	// text, so if the target is cut, the join no longer says where it was.
	ToWords int `json:"to_words,omitempty"`
}

// Stashed is an edit set aside because its chapter is not in the cut now, kept for when it is.
type Stashed struct {
	Edit  Edit   `json:"edit"`
	Gone  *Gone  `json:"gone,omitempty"`
	Title string `json:"title,omitempty"`
}

// MaxStash bounds the edits a draft sets aside.
const MaxStash = 5000

// ProjectEdit is what the writer typed for the new project.
type ProjectEdit struct {
	Title  string `json:"title,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Author string `json:"author,omitempty"`
}

// DraftEdits are the writer's changes to what the preview proposes.
type DraftEdits struct {
	Edits   map[string]Edit     `json:"edits,omitempty"` // by node key
	Order   map[string][]string `json:"order,omitempty"` // a parent's key (empty: the top) to its children in the order wanted
	Gone    map[string]Gone     `json:"gone,omitempty"`
	Stash   map[string]Stashed  `json:"stash,omitempty"` // edits set aside by a change of how a Word file is cut, by key
	Project ProjectEdit         `json:"project,omitempty"`
	KV      int                 `json:"kv,omitempty"` // 2: the keys are the ones the cut makes now (see draftkeys.go)
}

// list gives the edits as the preview and apply read them, in a fixed order.
func (e DraftEdits) list() ([]Edit, []Reorder) {
	var edits []Edit
	for _, k := range sortedKeys(e.Edits) {
		ed := e.Edits[k]
		ed.Key = k
		edits = append(edits, ed)
	}
	var re []Reorder
	for _, k := range sortedKeys(e.Order) {
		re = append(re, Reorder{Parent: k, Keys: e.Order[k]})
	}
	return edits, re
}

// DraftResult is what a finished draft keeps: counts and ids, never text.
type DraftResult struct {
	ProjectID     string   `json:"project_id,omitempty"`
	Slug          string   `json:"slug,omitempty"`
	Title         string   `json:"title,omitempty"`
	SourceID      string   `json:"source_id,omitempty" desc:"The import's record (project_sources): what import.undo takes. Empty on a result from before it was kept."`
	Documents     int      `json:"documents"`
	Words         int      `json:"words"`
	FirstDocument string   `json:"first_document_id,omitempty"`
	EditionID     string   `json:"edition_id,omitempty"`
	Suggestions   int      `json:"suggestions,omitempty"`
	Comments      int      `json:"comments,omitempty"`
	Pictures      int      `json:"pictures,omitempty"`
	ResearchNotes int      `json:"research_notes,omitempty"`
	Entities      int      `json:"entities,omitempty"`
	CoverUsed     bool     `json:"cover_used,omitempty"`
	Skipped       int      `json:"skipped_total,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// Draft is a draft row.
type Draft struct {
	ID            string
	OwnerID       string
	SourceKind    string
	SourceRef     string
	Kind          string
	Name          string
	TargetProject string // empty: a new project
	TargetParent  string
	Bytes         int64
	SHA256        string
	Options       DraftOptions
	Edits         DraftEdits
	Prev          string
	Revision      int
	Token         string
	Status        string
	Result        DraftResult
	CreatedAt     string
	UpdatedAt     string
}

const draftCols = `id, owner_id, source_kind, source_ref, kind, name, COALESCE(target_project_id,''), target_parent_id, bytes, sha256,
	options_json, edits_json, prev_json, revision, token, status, result_json, created_at, updated_at`

func scanDraft(sc interface{ Scan(...any) error }) (*Draft, error) {
	var d Draft
	var opts, edits, res string
	if err := sc.Scan(&d.ID, &d.OwnerID, &d.SourceKind, &d.SourceRef, &d.Kind, &d.Name, &d.TargetProject, &d.TargetParent, &d.Bytes, &d.SHA256,
		&opts, &edits, &d.Prev, &d.Revision, &d.Token, &d.Status, &res, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(opts), &d.Options)
	_ = json.Unmarshal([]byte(edits), &d.Edits)
	_ = json.Unmarshal([]byte(res), &d.Result)
	return &d, nil
}

// StageDir is where a draft's files are.
func StageDir(dataDir, id string) string { return filepath.Join(dataDir, "imports", "stage", id) }

// stageRoot is the folder of all staged drafts.
func stageRoot(dataDir string) string { return filepath.Join(dataDir, "imports", "stage") }

// sourcePath is a draft's staged source file.
func sourcePath(dataDir, id string) string { return filepath.Join(StageDir(dataDir, id), "source") }

const idPrefix = "imd"

// invalidWhy is a refusal with a stable reason in its data, so a page can say it in its own language and the op's English is only for
// callers that read it.
func invalidWhy(field, msg, reason string) *ops.Error {
	e := ops.Invalid(field, msg)
	e.Data = map[string]any{"reason": reason}
	return e
}

// conflictWhy is the same for a conflict.
func conflictWhy(msg, reason string, data map[string]any) *ops.Error {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = reason
	return ops.ConflictWith(msg, data)
}

// validDraftID reports whether s has the shape of a draft id, so a path that holds anything else is refused before it names a file.
func validDraftID(s string) bool { return strings.HasPrefix(s, idPrefix+"_") && db.ValidID(s) }

// loadDraft reads a draft of the owner. Another person's draft and a missing one are the same 404.
func loadDraft(ctx context.Context, d *db.DB, owner, id string) (*Draft, error) {
	if !validDraftID(id) {
		return nil, ops.Invalid("id", "That is not an import draft's id.")
	}
	dr, err := scanDraft(d.Read().QueryRowContext(ctx, `SELECT `+draftCols+` FROM import_drafts WHERE id = ? AND owner_id = ?`, id, owner))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ops.NotFound("import")
	}
	if err != nil {
		return nil, ops.Internal(err)
	}
	// A draft that targets a project is only as reachable as the project: one in the trash or gone is not found.
	return dr, nil
}

// ListDrafts lists a person's drafts, newest change first: the ones open or being applied, and those finished in the last week.
func ListDrafts(ctx context.Context, d *db.DB, owner string) ([]*Draft, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT `+draftCols+` FROM import_drafts WHERE owner_id = ? ORDER BY updated_at DESC, id DESC LIMIT 100`, owner)
	if err != nil {
		return nil, ops.Internal(err)
	}
	defer rows.Close()
	var out []*Draft
	for rows.Next() {
		dr, err := scanDraft(rows)
		if err != nil {
			return nil, ops.Internal(err)
		}
		out = append(out, dr)
	}
	return out, rows.Err()
}

// reserve inserts the draft row inside one transaction that also checks the quota, so two requests cannot both pass a check that
// only one of them fits. bytes is what is on disk for it (the source).
func reserve(ctx context.Context, d *db.DB, lim StageLimits, dr *Draft) (*Draft, error) {
	lim = lim.withDefaults()
	var existing *Draft
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		existing = nil
		// The same bytes by the same person for the same target are the draft already open.
		if dr.SourceKind == SourceUpload && dr.SHA256 != "" {
			// the same bytes with the same options: another choice of options (the cut, tracked changes) is another draft
			optsNow, _ := json.Marshal(dr.Options)
			row := tx.QueryRowContext(ctx, `SELECT `+draftCols+` FROM import_drafts WHERE owner_id = ? AND sha256 = ? AND COALESCE(target_project_id,'') = ? AND target_parent_id = ? AND kind = ? AND status = 'open' AND source_kind = 'upload' AND options_json = ?`,
				dr.OwnerID, dr.SHA256, dr.TargetProject, dr.TargetParent, dr.Kind, string(optsNow))
			if found, err := scanDraft(row); err == nil {
				existing = found
				return nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var open int
		var mine, all sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM import_drafts WHERE owner_id = ? AND status IN ('open','applying')`, dr.OwnerID).Scan(&open); err != nil {
			return err
		}
		if open >= lim.MaxOpen {
			return openDraftsErr(open, lim)
		}
		if err := tx.QueryRowContext(ctx, `SELECT SUM(CASE WHEN owner_id = ? THEN bytes ELSE 0 END), SUM(bytes) FROM import_drafts WHERE status IN ('open','applying')`, dr.OwnerID).Scan(&mine, &all); err != nil {
			return err
		}
		if int64(lim.UserMB)<<20 < mine.Int64+dr.Bytes {
			return userQuotaErr(mine.Int64, lim)
		}
		if int64(lim.TotalMB)<<20 < all.Int64+dr.Bytes {
			return totalQuotaErr(lim)
		}
		opts, _ := json.Marshal(dr.Options)
		edits, _ := json.Marshal(dr.Edits)
		_, err := tx.ExecContext(ctx, `INSERT INTO import_drafts(id, owner_id, source_kind, source_ref, kind, name, target_project_id, target_parent_id, bytes, sha256, options_json, edits_json, prev_json, revision, token, status, result_json, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			dr.ID, dr.OwnerID, dr.SourceKind, dr.SourceRef, dr.Kind, dr.Name, nullable(dr.TargetProject), dr.TargetParent, dr.Bytes, dr.SHA256, string(opts), string(edits), "", 1, "", DraftOpen, "{}", dr.CreatedAt, dr.UpdatedAt)
		return err
	})
	if err != nil {
		var oe *ops.Error
		if errors.As(err, &oe) {
			return nil, oe
		}
		return nil, ops.Internal(err)
	}
	if existing != nil {
		return existing, nil
	}
	return dr, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// saveState stores a new state of a draft if its revision is still the one the caller read. The state before it is kept for one Undo.
func saveState(ctx context.Context, d *db.DB, dr *Draft, prev, token string) error {
	opts, _ := json.Marshal(dr.Options)
	edits, _ := json.Marshal(dr.Edits)
	now := db.Now()
	var res sql.Result
	err := d.WithTx(ctx, func(tx *sql.Tx) (err error) {
		res, err = tx.ExecContext(ctx, `UPDATE import_drafts SET options_json = ?, edits_json = ?, prev_json = ?, token = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND owner_id = ? AND revision = ? AND status = 'open'`,
			string(opts), string(edits), prev, token, now, dr.ID, dr.OwnerID, dr.Revision)
		return err
	})
	if err != nil {
		return ops.Internal(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return staleRevision()
	}
	dr.Revision++
	dr.Token, dr.Prev, dr.UpdatedAt = token, prev, now
	return nil
}

// staleRevision is the answer to a change made on a draft that has moved on (another tab, or a double press).
func staleRevision() *ops.Error {
	return ops.ConflictWith("This import was changed in another tab. Reload it to see the current version; your change was not made.", map[string]any{"reason": "revision"})
}

// claimApply moves a draft from open to applying if the caller is looking at its current revision. Two tabs or a double press
// make one claim.
func claimApply(ctx context.Context, d *db.DB, dr *Draft, rev int) error {
	var n int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE import_drafts SET status = 'applying', updated_at = ? WHERE id = ? AND owner_id = ? AND status = 'open' AND revision = ?`, db.Now(), dr.ID, dr.OwnerID, rev)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return ops.Internal(err)
	}
	if n > 0 {
		return nil
	}
	cur, err := loadDraft(ctx, d, dr.OwnerID, dr.ID)
	if err != nil {
		return err
	}
	switch {
	case cur.Status == DraftApplying:
		return ops.ConflictWith("This import is already being applied.", map[string]any{"reason": "applying"})
	case cur.Status == DraftDone:
		return ops.ConflictWith("This import was already applied.", map[string]any{"reason": "done"})
	}
	return staleRevision()
}

// releaseApply puts a draft that failed to apply back to open: it is still usable. A draft that cannot be released stays "applying" and is
// put right by the sweep.
func releaseApply(ctx context.Context, d *db.DB, dr *Draft) error {
	return d.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE import_drafts SET status = 'open', updated_at = ? WHERE id = ? AND status = 'applying'`, db.Now(), dr.ID)
		return err
	})
}

// finishApply records the result, keeps only counts and ids, and removes the staged files. The row is marked first: a file that is left
// when the row could not be written is removed by the sweep, and a row that is not marked is recovered from the source the apply recorded.
func finishApply(ctx context.Context, d *db.DB, dataDir string, dr *Draft, res DraftResult) error {
	b, _ := json.Marshal(res)
	err := d.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE import_drafts SET status = 'done', result_json = ?, bytes = 0, prev_json = '', edits_json = '{}', updated_at = ? WHERE id = ?`, string(b), db.Now(), dr.ID)
		return err
	})
	if err != nil {
		return err
	}
	if dr.SourceKind == SourceUpload {
		_ = os.RemoveAll(StageDir(dataDir, dr.ID))
		dropCaches(dr.SHA256)
	}
	return nil
}

// applyingGrace is how long a draft may be "applying" before the sweep looks at it: an apply of the largest book takes seconds.
const applyingGrace = 15 * time.Minute

// recoverApplying settles drafts a stopped process left "applying". An upload whose apply committed is in a project source that names the
// draft (the id is written in the transaction that commits the import): it is marked done with that project's id. One that did not commit
// goes back to open, its file still there. A Git draft follows its job. It returns how many it settled.
func recoverApplying(ctx context.Context, d *db.DB, dataDir string, now time.Time) int {
	rows, err := d.Read().QueryContext(ctx, `SELECT `+draftCols+` FROM import_drafts WHERE status = 'applying' AND updated_at < ?`, db.Format(now.Add(-applyingGrace)))
	if err != nil {
		return 0
	}
	var stuck []*Draft
	for rows.Next() {
		if dr, err := scanDraft(rows); err == nil {
			stuck = append(stuck, dr)
		}
	}
	rows.Close()
	n := 0
	for _, dr := range stuck {
		var pid string
		if dr.SourceKind == SourceGitJob {
			_ = d.Read().QueryRowContext(ctx, `SELECT COALESCE(project_id,'') FROM import_jobs WHERE id = ? AND status = 'done'`, dr.SourceRef).Scan(&pid)
		} else {
			_ = d.Read().QueryRowContext(ctx, `SELECT project_id FROM project_sources WHERE metadata_json LIKE ? LIMIT 1`, `%"draft_id":"`+dr.ID+`"%`).Scan(&pid)
		}
		if pid == "" {
			if releaseApply(ctx, d, dr) == nil {
				n++
			}
			continue
		}
		res := DraftResult{ProjectID: pid}
		_ = d.Read().QueryRowContext(ctx, `SELECT id FROM project_sources WHERE project_id = ? AND metadata_json LIKE ? LIMIT 1`, pid, `%"draft_id":"`+dr.ID+`"%`).Scan(&res.SourceID)
		_ = d.Read().QueryRowContext(ctx, `SELECT slug, title FROM projects WHERE id = ?`, pid).Scan(&res.Slug, &res.Title)
		_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM document_sources ds JOIN project_sources ps ON ps.id = ds.source_id WHERE ps.project_id = ? AND ps.metadata_json LIKE ?`, pid, `%"draft_id":"`+dr.ID+`"%`).Scan(&res.Documents)
		if finishApply(ctx, d, dataDir, dr, res) == nil {
			n++
		}
	}
	return n
}

// removeDraft deletes the row and the staged files. It is not a deletion of anything imported.
func removeDraft(ctx context.Context, d *db.DB, dataDir string, dr *Draft) error {
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM import_drafts WHERE id = ? AND owner_id = ?`, dr.ID, dr.OwnerID)
		return err
	})
	if err != nil {
		return ops.Internal(err)
	}
	if dr.SourceKind == SourceUpload {
		_ = os.RemoveAll(StageDir(dataDir, dr.ID))
		dropCaches(dr.SHA256)
	}
	return nil
}

// hashingWriter counts and hashes what passes through.
type hashingWriter struct {
	w io.Writer
	h interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	n int64
}

func (h *hashingWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

// writeStage streams r to the draft's source file, refusing more than max bytes, and returns its size and SHA-256. A failed or
// abandoned stream leaves nothing.
func writeStage(dataDir, id string, r io.Reader, max int64) (n int64, sum string, err error) {
	dir := StageDir(dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, "", ops.Internal(err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	f, err := os.OpenFile(sourcePath(dataDir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", ops.Internal(err)
	}
	hw := &hashingWriter{w: f, h: sha256.New()}
	_, cerr := io.Copy(hw, io.LimitReader(r, max+1))
	if serr := f.Sync(); cerr == nil {
		cerr = serr
	}
	if clerr := f.Close(); cerr == nil {
		cerr = clerr
	}
	if cerr != nil {
		var mbe interface{ Timeout() bool }
		if errors.As(cerr, &mbe) {
			return 0, "", ops.TooLarge()
		}
		return 0, "", invalidWhy("", "The file could not be read to the end. Nothing was kept.", "unreadable")
	}
	if hw.n > max {
		return 0, "", ops.TooLarge()
	}
	if hw.n == 0 {
		return 0, "", invalidWhy("", "That file is empty.", "empty")
	}
	return hw.n, hex.EncodeToString(hw.h.Sum(nil)), nil
}

// StagedBytes is what the staged files of drafts hold: for the storage report.
func StagedBytes(ctx context.Context, d *db.DB) (n int64, drafts int) {
	var b sql.NullInt64
	_ = d.Read().QueryRowContext(ctx, `SELECT SUM(bytes), count(*) FROM import_drafts WHERE status IN ('open','applying') AND source_kind = 'upload'`).Scan(&b, &drafts)
	return b.Int64, drafts
}

// SweepDrafts removes what staged imports leave behind: drafts idle past the limit, finished ones older than a week, rows whose
// folder is gone (a restored backup), rows of a Git job that no longer exists, and folders that no row names (a crash between
// writing the file and inserting the row; only ones older than an hour, so a stage in progress is never taken).
func SweepDrafts(ctx context.Context, d *db.DB, dataDir string, lim StageLimits, now time.Time) (removed int, err error) {
	lim = lim.withDefaults()
	removed += SweepTmp(dataDir, now)
	recoverApplying(ctx, d, dataDir, now)
	idle := db.Format(now.Add(-time.Duration(lim.Hours) * time.Hour))
	old := db.Format(now.Add(-DoneKeep))
	rows, err := d.Read().QueryContext(ctx, `SELECT `+draftCols+` FROM import_drafts`)
	if err != nil {
		return 0, err
	}
	var all []*Draft
	for rows.Next() {
		dr, err := scanDraft(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, dr)
	}
	rows.Close()
	have := map[string]bool{}
	for _, dr := range all {
		gone := false
		switch {
		case dr.Status == DraftDone:
			gone = dr.UpdatedAt < old
		case dr.Status == DraftApplying:
			gone = dr.UpdatedAt < idle // an apply that died with the process
		case dr.UpdatedAt < idle:
			gone = true
		case dr.SourceKind == SourceUpload:
			if _, serr := os.Stat(sourcePath(dataDir, dr.ID)); errors.Is(serr, fs.ErrNotExist) {
				gone = true
			}
		case dr.SourceKind == SourceGitJob:
			var n int
			_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM import_jobs WHERE id = ?`, dr.SourceRef).Scan(&n)
			gone = n == 0
		}
		if !gone {
			have[dr.ID] = true
			continue
		}
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM import_drafts WHERE id = ?`, dr.ID)
			return err
		}); err != nil {
			return removed, err
		}
		if dr.SourceKind == SourceUpload {
			_ = os.RemoveAll(StageDir(dataDir, dr.ID))
			dropCaches(dr.SHA256)
		}
		removed++
	}
	ents, rerr := os.ReadDir(stageRoot(dataDir))
	if rerr != nil {
		return removed, nil
	}
	for _, e := range ents {
		if have[e.Name()] {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || now.Sub(info.ModTime()) < time.Hour {
			continue
		}
		// a row appeared since the list was read: leave it
		var n int
		_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM import_drafts WHERE id = ?`, e.Name()).Scan(&n)
		if n > 0 {
			continue
		}
		_ = os.RemoveAll(filepath.Join(stageRoot(dataDir), e.Name()))
		removed++
	}
	return removed, nil
}

// GitDraftTx records the draft of a Git import job, in the transaction that records the job. A Git draft holds no file: the clone is
// the Git service's, and the draft keeps the writer's edits.
func GitDraftTx(tx *sql.Tx, owner, jobID, name, split string) (string, error) {
	id := db.NewID(idPrefix)
	now := db.Now()
	opts, _ := json.Marshal(DraftOptions{SplitScenes: split})
	_, err := tx.Exec(`INSERT INTO import_drafts(id, owner_id, source_kind, source_ref, kind, name, options_json, edits_json, revision, status, result_json, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,1,'open','{}',?,?)`, id, owner, SourceGitJob, jobID, StageGit, name, string(opts), "{}", now, now)
	return id, err
}

// EnsureGitDraft returns the draft of a Git import job, making it when there is none (a job started before drafts existed).
func EnsureGitDraft(ctx context.Context, d *db.DB, owner, jobID, name, split string) (string, error) {
	var id string
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM import_drafts WHERE owner_id = ? AND source_kind = 'git_job' AND source_ref = ?`, owner, jobID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id, err = GitDraftTx(tx, owner, jobID, name, split)
		}
		return err
	})
	return id, err
}

// GitDraftIDs maps a person's Git import jobs to their drafts.
func GitDraftIDs(ctx context.Context, d *db.DB, owner string) map[string]string {
	out := map[string]string{}
	rows, err := d.Read().QueryContext(ctx, `SELECT source_ref, id FROM import_drafts WHERE owner_id = ? AND source_kind = 'git_job'`, owner)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var job, id string
		if rows.Scan(&job, &id) == nil {
			out[job] = id
		}
	}
	return out
}

func openDraftsErr(open int, lim StageLimits) *ops.Error {
	return conflictWhy(fmt.Sprintf("You have %d imports in progress, which is the most at once. Discard one of your open imports first.", open), "open_drafts", map[string]any{"limit": lim.MaxOpen, "open": open})
}

func userQuotaErr(held int64, lim StageLimits) *ops.Error {
	return conflictWhy(fmt.Sprintf("Your imports in progress hold %d MiB, and this one would pass your share of %d MiB. Discard one of your open imports first.", held>>20, lim.UserMB), "user_quota", map[string]any{"limit_mb": lim.UserMB, "held_mb": held >> 20})
}

func totalQuotaErr(lim StageLimits) *ops.Error {
	return conflictWhy(fmt.Sprintf("The space this server keeps for imports in progress is full (%d MiB). Nothing was kept. Try again when someone has finished or discarded an import.", lim.TotalMB), "total_quota", map[string]any{"limit_mb": lim.TotalMB})
}

// quotaRoom is checked before a byte of a file is written: how many bytes this person may still stage (the smaller of what is left of their
// share and of the instance's), or the refusal when the open drafts or the room are already used. The row is inserted later in a transaction
// that checks the same again, so two requests cannot both fit where one does.
func quotaRoom(ctx context.Context, d *db.DB, lim StageLimits, owner string) (room int64, over *ops.Error, err error) {
	lim = lim.withDefaults()
	var open int
	var mine, all sql.NullInt64
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM import_drafts WHERE owner_id = ? AND status IN ('open','applying')`, owner).Scan(&open); err != nil {
		return 0, nil, ops.Internal(err)
	}
	if open >= lim.MaxOpen {
		return 0, nil, openDraftsErr(open, lim)
	}
	if err := d.Read().QueryRowContext(ctx, `SELECT SUM(CASE WHEN owner_id = ? THEN bytes ELSE 0 END), SUM(bytes) FROM import_drafts WHERE status IN ('open','applying')`, owner).Scan(&mine, &all); err != nil {
		return 0, nil, ops.Internal(err)
	}
	user := int64(lim.UserMB)<<20 - mine.Int64
	if user <= 0 {
		return 0, nil, userQuotaErr(mine.Int64, lim)
	}
	total := int64(lim.TotalMB)<<20 - all.Int64
	if total <= 0 {
		return 0, nil, totalQuotaErr(lim)
	}
	if user <= total {
		return user, userQuotaErr(mine.Int64, lim), nil
	}
	return total, totalQuotaErr(lim), nil
}

// diskReserve is what is left free on the volume after a staged file, so a stage does not take the last of it.
const diskReserve = 128 << 20

// enoughDisk says whether the data directory's volume can take need more bytes and keep diskReserve. A volume that cannot be asked is
// taken to have room.
func enoughDisk(dir string, need int64) bool {
	free, ok := pandoc.FreeBytes(dir)
	return !ok || free >= uint64(need)+diskReserve
}

// SweepTmp removes the temporary files a stage form leaves when a request dies half-way (upload-* and folder-* under data/tmp), once they
// are over an hour old, which no request in progress is.
func SweepTmp(dataDir string, now time.Time) (removed int) {
	dir := filepath.Join(dataDir, "tmp")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "upload-") && !strings.HasPrefix(n, "folder-") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < time.Hour {
			continue
		}
		if os.Remove(filepath.Join(dir, n)) == nil {
			removed++
		}
	}
	return removed
}
