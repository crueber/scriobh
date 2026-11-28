package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// RemoteDraft is how a draft of a Git import reaches the Git service, which owns the clone (the Git package imports this one,
// so it hands these over when its ops are registered). A Git draft keeps only the writer's edits here.
type RemoteDraft struct {
	// Preview previews the job's files with the edits. status is the job's: running, preview, done or failed; failure is its error.
	Preview func(c *ops.Ctx, jobID, split string, edits []Edit, reorders []Reorder) (pv *PreviewOut, status, failure string, err error)
	// Apply creates the project from the job.
	Apply func(c *ops.Ctx, jobID string, in ApplyIn) (ApplyOut, error)
	// Discard throws away the job and its clone (a job still being cloned stays until it ends).
	Discard func(c *ops.Ctx, jobID string) error
}

var remoteDraft RemoteDraft

// SetRemoteDraft is called by the Git service when its ops are registered.
func SetRemoteDraft(r RemoteDraft) { remoteDraft = r }

// ---- the ops' types ----------------------------------------------------------------------------------------------

// DraftView is a draft as the ops answer it.
type DraftView struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind" enum:"docx,epub,pdf,zip,pandoc,file,folder,git"`
	Name        string       `json:"name"`
	Status      string       `json:"status" enum:"open,applying,done"`
	Revision    int          `json:"revision" desc:"Changes with every saved change: send it back as base_revision."`
	ProjectID   string       `json:"project_id,omitempty" desc:"The project this import adds to; empty for a new project."`
	ParentID    string       `json:"parent_id,omitempty"`
	SourceKind  string       `json:"source_kind" enum:"upload,git_job"`
	SourceRef   string       `json:"source_ref,omitempty" desc:"For a Git import, the import job's id."`
	Bytes       int64        `json:"bytes"`
	SHA256      string       `json:"sha256,omitempty"`
	Options     DraftOptions `json:"options"`
	Edits       DraftEdits   `json:"edits"`
	CanUndo     bool         `json:"can_undo"`
	Reuse       []ReuseOffer `json:"reuse,omitempty" desc:"Earlier reviews of this same file that have changes, which Reuse can take. Only while this review has no changes of its own."`
	Unreachable bool         `json:"unreachable,omitempty" desc:"The draft adds to a project this person can no longer edit or open (their access ended, or it is locked or in the trash): it can only be discarded."`
	Token       string       `json:"token,omitempty"`
	Result      *DraftResult `json:"result,omitempty" desc:"After the apply: counts and ids."`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

func (d *Draft) view() DraftView {
	v := DraftView{ID: d.ID, Kind: d.Kind, Name: d.Name, Status: d.Status, Revision: d.Revision, ProjectID: d.TargetProject, ParentID: d.TargetParent,
		SourceKind: d.SourceKind, SourceRef: d.SourceRef, Bytes: d.Bytes, SHA256: d.SHA256, Options: d.Options, Edits: d.Edits, CanUndo: d.Prev != "", Token: d.Token,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	if d.Status == DraftDone {
		r := d.Result
		v.Result = &r
	}
	return v
}

// DraftIDIn names a draft.
type DraftIDIn struct {
	ID string `path:"id" desc:"The draft's id (stage_id)."`
}

// DraftGetIn reads a draft, and with preview the preview of its current state.
type DraftGetIn struct {
	ID        string `path:"id" desc:"The draft's id (stage_id)."`
	Preview   bool   `query:"preview" desc:"Also run the preview of the draft's current state (the same answer as import.preview with stage_id)."`
	LookKey   string `query:"look_key" desc:"With a Word document: look into the chapter with this key (from the preview). Nothing is changed."`
	LookMode  string `query:"look_mode" enum:"split,inside" desc:"split (default) lists the chapter's paragraphs, to split it at one; inside lists the places a chapter could start inside it (a page break, a section break, a heading below the chapter level, a scene break, an unformatted label, a lone number), the weak rules included."`
	LookAfter int    `query:"look_after" desc:"How many of the list to skip: it comes a page at a time."`
	LookCount int    `query:"look_count" desc:"How many to list, up to 1,000 (default 100)."`
}

// DraftOut is a draft with, when asked, its preview.
type DraftOut struct {
	Item    DraftView   `json:"item"`
	Preview *PreviewOut `json:"preview,omitempty"`
	Pending bool        `json:"pending,omitempty" desc:"A Git import whose clone is still being made: there is nothing to preview yet."`
	Failed  string      `json:"failed,omitempty" desc:"A Git import whose clone failed: why."`
	Look    *LookOut    `json:"look,omitempty" desc:"With look_key: what is in that chapter."`
}

// DraftListOut lists a person's drafts.
type DraftListOut struct {
	Items []DraftView `json:"items"`
}

// DraftEdit is one change to one proposed document, by its key from the preview.
type DraftEdit struct {
	Key          string  `json:"key" desc:"The proposed document's key from the preview."`
	Title        *string `json:"title,omitempty" desc:"Rename it."`
	Kind         *string `json:"kind,omitempty" enum:"part,chapter,scene,note,front_matter,back_matter" desc:"Change what kind it is."`
	Split        *bool   `json:"split,omitempty" desc:"Split this file at its second-level headings, or not."`
	Skip         *bool   `json:"skip,omitempty" desc:"true leaves it, and what is inside it, out; false puts it back (and takes back a join)."`
	MergeInto    *string `json:"merge_into,omitempty" desc:"Join it into the document with this key."`
	JoinPrevious *bool   `json:"join_previous,omitempty" desc:"true joins it into the document before it (the one before it among those with the same parent), as merge_into would. Refused when there is none to join it to, or when it is a part, has documents inside it, or the one before is a part."`
	Parent       *string `json:"parent,omitempty" desc:"Move it to the end of the document with this key, or to the top level with an empty string."`
}

// DraftMove moves a document one place or to an end among its brothers and sisters.
type DraftMove struct {
	Key string `json:"key"`
	To  string `json:"to" enum:"up,down,top,bottom"`
}

// DraftUpdateIn is one change set: it is applied to a copy of the draft, the preview is run, and the draft is saved only if the
// preview accepts it.
type DraftUpdateIn struct {
	ID             string            `path:"id" desc:"The draft's id (stage_id)."`
	BaseRevision   int               `json:"base_revision" desc:"The revision the change was made on: a draft that has moved on refuses it with reason revision."`
	Options        map[string]string `json:"options,omitempty" desc:"Options the source is read with, by name (docx_headings, docx_changes, docx_split, docx_split_style, docx_split_words, docx_breaks, split_scenes, layout, epub_sections, epub_pages, pictures, scrivener_research, scrivener_notes, scrivener_sheets, wp_status, wp_types, ghost_status, ghost_types, pandoc_format) and, for the apply, use_cover (true or false), edition_label and ack_big (a Word document with a chapter of 25,000 words or more imports only when this is true). Changing one that changes how the file is read clears the edits, except for a Word document, whose chapters are named for the paragraph they start at: the edits of those that remain stay and the rest are listed; undo takes that back."`
	Edit           *DraftEdit        `json:"edit,omitempty"`
	Move           *DraftMove        `json:"move,omitempty"`
	Reorder        *Reorder          `json:"reorder,omitempty" desc:"A parent's children in the order wanted."`
	Project        *ProjectEdit      `json:"project,omitempty" desc:"The new project's title, kind and author. These are saved without a new revision, since nothing in the mapping depends on them."`
	BreakFrom      string            `json:"break_from,omitempty" desc:"With break: the key of the chapter the paragraph is in, so the answer can say where it was split (the break itself is only the paragraph's ordinal)."`
	Within         *DraftWithin      `json:"within,omitempty" desc:"With a Word document: start a chapter at every place of one kind inside a chapter (a page break, a section break, a heading below the chapter level, a scene break, an unformatted label, a lone number, or pieces of docx_split_words), as one change."`
	Matter         *DraftMatter      `json:"matter,omitempty" desc:"Make this document, and everything above it (side front) or below it (side back), front matter or back matter, as one change. Parts are left as they are."`
	Break          *int              `json:"break,omitempty" desc:"With a Word document, a text file, or a PDF or Pandoc document cut by the detector: start a chapter at the paragraph with this ordinal (a suggestion's ordinal from the preview's breaks). The chapter that was there is split in two at it."`
	TitleCaseAll   bool              `json:"title_case_all,omitempty" desc:"Rename every chapter whose title is in capitals to the title case the preview suggests (suggested_title), except those the writer already renamed."`
	ChapterLineAll bool              `json:"chapter_line_all,omitempty" desc:"Rename every chapter whose title holds text before a chapter label to that label (line_title), except those the writer has named. The words before the label stay in the chapter."`
	Undo           bool              `json:"undo,omitempty" desc:"Go back to the state before the last change (and again to redo it)."`
	Reuse          string            `json:"reuse,omitempty" desc:"The id of an earlier review of this same file (one of this review's reuse offers): its chapter changes are used here, through the same rules as a change of cut. Those whose chapter is not in this cut are set aside and listed. Only while this review has no changes of its own."`
}

// ReuseOffer is an earlier review of the same file, by the same person, that has changes of its own.
type ReuseOffer struct {
	ID        string `json:"id" desc:"The earlier draft's id: what reuse takes."`
	Name      string `json:"name"`
	Changes   int    `json:"changes" desc:"How many chapters the earlier review changed (renamed, left out, joined, moved or made a kind)."`
	UpdatedAt string `json:"updated_at"`
}

// changesOf counts the chapters a review has changed or left out.
func changesOf(e DraftEdits) int {
	keys := map[string]bool{}
	for k := range e.Edits {
		keys[k] = true
	}
	for k := range e.Gone {
		keys[k] = true
	}
	return len(keys)
}

// offersFor lists the writer's own open drafts of the same file that have changes, for a review that has none yet. Another person's
// drafts are never listed. An applied import keeps counts only (the plan), so its changes are not offered.
func offersFor(c *ops.Ctx, dr *Draft) []ReuseOffer {
	if dr.Status != DraftOpen || dr.SourceKind != SourceUpload || dr.SHA256 == "" || changesOf(dr.Edits) > 0 {
		return nil
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, name, edits_json, updated_at FROM import_drafts WHERE owner_id = ? AND sha256 = ? AND kind = ? AND id <> ? AND status = ? AND edits_json NOT IN ('', '{}') ORDER BY updated_at DESC LIMIT 5`,
		c.P.UserID, dr.SHA256, dr.Kind, dr.ID, DraftOpen)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ReuseOffer
	for rows.Next() {
		var id, name, edits, updated string
		if rows.Scan(&id, &name, &edits, &updated) != nil {
			continue
		}
		var e DraftEdits
		if json.Unmarshal([]byte(edits), &e) != nil {
			continue
		}
		if n := changesOf(e); n > 0 {
			out = append(out, ReuseOffer{ID: id, Name: name, Changes: n, UpdatedAt: updated})
		}
	}
	return out
}

// DraftWithin asks for every place of one kind inside a chapter.
type DraftWithin struct {
	Key  string `json:"key" desc:"The chapter's key from the preview."`
	Rule string `json:"rule" enum:"headings,page_breaks,section_breaks,labels,numerals,scenes,size"`
}

// DraftMatter makes a run of documents front matter or back matter.
type DraftMatter struct {
	Key  string `json:"key" desc:"The document's key from the preview."`
	Side string `json:"side" enum:"front,back" desc:"front: it and every document above it are front matter. back: it and every document below it are back matter."`
}

// DraftNote says, for the page, what a change did, in a code and the names to put in its words.
type DraftNote struct {
	Code     string   `json:"code" enum:"moved,moved_under,moved_top,joined,left_out,put_back,renamed,retyped,split,options,recut,undone,project,break,title_case,line_title,split_para,within,matter,pattern"`
	Title    string   `json:"title,omitempty"`
	Other    string   `json:"other,omitempty"`
	Pos      int      `json:"pos,omitempty"`
	Count    int      `json:"count,omitempty"`
	Above    bool     `json:"above,omitempty"`
	Ends     string   `json:"ends,omitempty" desc:"After a split at a paragraph: the end of the chapter that was split."`
	Starts   string   `json:"starts,omitempty" desc:"After a split at a paragraph: the start of the new chapter."`
	Side     string   `json:"side,omitempty" desc:"After matter: front or back."`
	Kept     int      `json:"kept,omitempty" desc:"After a change of how a Word document is cut: how many of the writer's edits still apply."`
	Dropped  []string `json:"dropped,omitempty" desc:"The titles of the edits that no longer apply (their chapter is not in this cut, or holds other text now), the first 20. They are set aside in the draft, and come back with their chapter."`
	Restored []string `json:"restored,omitempty" desc:"The titles of edits that were set aside and are back, because their chapter is in this cut again with the same text."`
	NoMore   int      `json:"dropped_more,omitempty"`
}

// DraftUpdateOut is the draft after the change, its preview, and what was done.
type DraftUpdateOut struct {
	Item    DraftView   `json:"item"`
	Preview *PreviewOut `json:"preview,omitempty"`
	Note    *DraftNote  `json:"note,omitempty"`
}

// DraftDiscardOut says the draft is gone.
type DraftDiscardOut struct {
	Discarded bool `json:"discarded"`
}

// Register adds the draft ops. The server calls it through the registry with the stage limits it was configured with.
func registerDrafts(r *ops.Registry, lim StageLimits) {
	ops.Register(r, ops.Op{Name: "import.stage", Method: "POST", Path: "/api/v1/imports/stage", Status: 201, RawBody: true, MaxBody: MaxStageBody, NoDryRun: true,
		Desc: "Hold a file for import on the server while you review what it would create: the request's body is the file (a Word document, an EPUB, a PDF, a zip, a document for Pandoc, or one WordPress or Ghost export), its kind and the options in the query. It answers a stage_id. Nothing is imported. Review it with import.draft.get and import.draft.update (renames, kinds, joins, moves, what to leave out), then import.apply (or import.append, with project) with the stage_id, the preview's token and the draft's revision. The file is kept only until the import is applied or discarded, and for 72 hours at most (SCRIOBH_IMPORT_STAGE_HOURS); it is never in a backup, an export or a Git push, and the original is not kept after the apply. Limits: 34 MiB for a Word document or a zip, 32 MiB for a book; five drafts a person, 256 MiB a person.",
		Auth: ops.Writer()}, func(c *ops.Ctx, in StageIn) (StageOut, error) { return stage(c, lim, in) })
	ops.Register(r, ops.Op{Name: "import.draft.list", Method: "GET", Path: "/api/v1/imports/drafts",
		Desc: "Your imports in progress, and those you finished in the last week, newest change first. Only you can see them.", Auth: ops.Writer()}, listDrafts)
	ops.Register(r, ops.Op{Name: "import.draft.get", Method: "GET", Path: "/api/v1/imports/drafts/{id}",
		Desc: "One import draft: its options, your edits and its revision; with preview=true also what the import would create now (the same answer as import.preview). Only its owner can read it; anyone else, and a draft that does not exist, are not found.", Auth: ops.Writer()}, getDraft)
	ops.Register(r, ops.Op{Name: "import.draft.update", Method: "POST", Path: "/api/v1/imports/drafts/{id}", NoDryRun: true,
		Desc: "Change an import draft: the options its file is read with, a rename, a kind, a join into another document, a move, a reorder, what to leave out, the new project's title; or undo the last change. Send base_revision: a draft that has moved on (another tab) is refused with reason revision. The change is made on a copy and previewed first, and saved only if the preview accepts it (a join into itself or a move into what is inside it is refused with the reason and nothing is stored). It answers the new revision and the preview.", Auth: ops.Writer()},
		func(c *ops.Ctx, in DraftUpdateIn) (DraftUpdateOut, error) { return updateDraft(c, lim, in) })
	ops.Register(r, ops.Op{Name: "import.draft.discard", Method: "DELETE", Path: "/api/v1/imports/drafts/{id}", NoDryRun: true, Permanent: true,
		Desc: "Throw away an import draft and its staged file. Nothing that was imported is touched.", Auth: ops.Writer()}, discardDraft)
}

// ---- access ------------------------------------------------------------------------------------------------------

// access loads a draft the caller owns. A draft that adds to a project is only as reachable as the project: its owner must still
// be able to edit it, or the draft is not found.
func access(c *ops.Ctx, id string) (*Draft, error) {
	dr, err := loadDraft(c, c.DB(), c.P.UserID, id)
	if err != nil {
		return nil, err
	}
	if dr.TargetProject != "" && dr.Status != DraftDone && !canEditProject(c, dr.TargetProject) {
		return nil, ops.NotFound("import")
	}
	normaliseKeys(c, dr)
	return dr, nil
}

func listDrafts(c *ops.Ctx, _ struct{}) (DraftListOut, error) {
	ds, err := ListDrafts(c, c.DB(), c.P.UserID)
	if err != nil {
		return DraftListOut{}, err
	}
	out := DraftListOut{Items: []DraftView{}}
	for _, d := range ds {
		v := d.view()
		// One that adds to a project this person can no longer reach is listed, so it can be discarded: it counts toward their open drafts.
		v.Unreachable = d.TargetProject != "" && d.Status != DraftDone && !canEditProject(c, d.TargetProject)
		out.Items = append(out.Items, v)
	}
	return out, nil
}

func getDraft(c *ops.Ctx, in DraftGetIn) (DraftOut, error) {
	dr, err := access(c, in.ID)
	if err != nil {
		return DraftOut{}, err
	}
	out := DraftOut{Item: dr.view()}
	out.Item.Reuse = offersFor(c, dr)
	if in.LookKey != "" && dr.Status == DraftOpen {
		mode := in.LookMode
		if mode == "" {
			mode = LookSplit
		}
		if mode != LookSplit && mode != LookInside {
			return out, ops.Invalid("look_mode", "look_mode is split or inside.")
		}
		ask := &lookAsk{key: in.LookKey, mode: mode, after: in.LookAfter, count: in.LookCount, size: dr.Options.DocxWords}
		if err := lookDraft(c, c, dr, dr.Options, ask); err != nil {
			return out, err
		}
		out.Look = ask.out
	}
	if !in.Preview || dr.Status == DraftDone {
		return out, nil
	}
	pv, pending, failed, err := previewDraft(c, dr, dr.Options, dr.Edits)
	if err != nil {
		return out, err
	}
	out.Preview, out.Pending, out.Failed = pv, pending, failed
	return out, nil
}

// discardDraft is the owner's alone and does not look at the project the draft adds to: it removes only the owner's staged copy and the row,
// nothing in any project, and a draft whose project the owner can no longer reach (access ended, locked, in the trash) must still be
// clearable, since it counts toward the open drafts they may hold.
func discardDraft(c *ops.Ctx, in DraftIDIn) (DraftDiscardOut, error) {
	if c.DryRun() {
		return DraftDiscardOut{}, ops.DryRunUnsupported("import.draft.discard")
	}
	dr, err := loadDraft(c, c.DB(), c.P.UserID, in.ID)
	if err != nil {
		return DraftDiscardOut{}, err
	}
	if dr.Status == DraftApplying {
		return DraftDiscardOut{}, ops.ConflictWith("This import is being applied. It cannot be thrown away until that ends.", map[string]any{"reason": "applying"})
	}
	if dr.SourceKind == SourceGitJob && dr.Status != DraftDone && remoteDraft.Discard != nil {
		if err := remoteDraft.Discard(c, dr.SourceRef); err != nil {
			return DraftDiscardOut{}, err
		}
	}
	if err := removeDraft(c, c.DB(), c.Env.DataDir, dr); err != nil {
		return DraftDiscardOut{}, err
	}
	c.Audit(ops.AuditEntry{Action: "import.draft.discard", TargetType: "import", TargetID: dr.ID})
	return DraftDiscardOut{Discarded: true}, nil
}

// ---- the staged source ---------------------------------------------------------------------------------------------

// stagedSource is a draft's file, read back for one request.
type stagedSource struct {
	files                        []File
	zip, docx, epub, pdf, pandoc []byte
	text                         []byte
}

// loadSource reads a draft's staged file. A file that is gone (a restored backup, a sweep) is a conflict that says so.
func loadSource(c *ops.Ctx, dr *Draft) (*stagedSource, error) {
	b, err := os.ReadFile(sourcePath(c.Env.DataDir, dr.ID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ops.ConflictWith("The file for this import is no longer on the server. Choose it again.", map[string]any{"reason": "gone"})
		}
		return nil, ops.Internal(err)
	}
	s := &stagedSource{}
	switch dr.Kind {
	case StageDocx:
		s.docx = b
	case StageEpub:
		s.epub = b
	case StagePdf:
		s.pdf = b
	case StageText:
		s.text = b
	case StagePandoc:
		s.pandoc = b
	case StageZip:
		s.zip = b
	case StageFolder:
		if s.files, err = folderFiles(b); err != nil {
			return nil, ops.Invalid("zip", err.Error())
		}
	case StageFile:
		name := "export.json"
		if t := strings.TrimLeft(strings.TrimPrefix(string(b[:min(len(b), 16)]), "\xef\xbb\xbf"), " \t\r\n"); strings.HasPrefix(t, "<") {
			name = "export.xml"
		}
		s.files = []File{{Path: name, Data: b, Size: int64(len(b))}}
	default:
		return nil, ops.Invalid("", "This draft has no staged file.")
	}
	return s, nil
}

// previewIn is the preview input a draft's state stands for.
func (s *stagedSource) previewIn(dr *Draft, o DraftOptions, e DraftEdits) PreviewIn {
	edits, re := e.list()
	return PreviewIn{Name: dr.Name, Files: s.files, Zip: s.zip, Docx: s.docx, Epub: s.epub, Pdf: s.pdf, Text: s.text, Pandoc: s.pandoc, PandocFormat: o.PandocFormat,
		Layout: o.Layout, DocxHeadings: o.DocxHeadings, DocxChanges: o.DocxChanges, SplitScenes: o.SplitScenes, EpubSections: o.EpubSections, EpubPages: o.EpubPages,
		Pictures: o.Pictures, ScrivResearch: o.ScrivResearch, ScrivNotes: o.ScrivNotes, ScrivSheets: o.ScrivSheets, WPStatus: o.WPStatus, WPTypes: o.WPTypes,
		GhostStatus: o.GhostStatus, GhostTypes: o.GhostTypes, DocxSplit: o.DocxSplit, DocxSplitStyle: o.DocxStyle, DocxSplitWords: o.DocxWords, DocxBreaks: breakList(o.DocxBreaks), DocxSplitPattern: o.DocxPattern, DocxSplitMatch: o.DocxMatch, DocxSplitTry: o.DocxTry, DocxSplitTryMatch: o.DocxTryMatch, Edits: edits, Reorder: re, sum: dr.SHA256}
}

// applyIn is the apply input a draft's state stands for, with the project's own fields the writer typed.
func (s *stagedSource) applyIn(dr *Draft, token string) ApplyIn {
	o := dr.Options
	edits, re := dr.Edits.list()
	return ApplyIn{Token: token, Name: dr.Name, Files: s.files, Zip: s.zip, Docx: s.docx, Epub: s.epub, Pdf: s.pdf, Text: s.text, Pandoc: s.pandoc, PandocFormat: o.PandocFormat,
		Layout: o.Layout, DocxHeadings: o.DocxHeadings, DocxChanges: o.DocxChanges, SplitScenes: o.SplitScenes, EpubSections: o.EpubSections, EpubPages: o.EpubPages,
		Pictures: o.Pictures, ScrivResearch: o.ScrivResearch, ScrivNotes: o.ScrivNotes, ScrivSheets: o.ScrivSheets, WPStatus: o.WPStatus, WPTypes: o.WPTypes,
		GhostStatus: o.GhostStatus, GhostTypes: o.GhostTypes, DocxSplit: o.DocxSplit, DocxSplitStyle: o.DocxStyle, DocxSplitWords: o.DocxWords, DocxBreaks: breakList(o.DocxBreaks), DocxSplitPattern: o.DocxPattern, DocxSplitMatch: o.DocxMatch, Title: dr.Edits.Project.Title, Kind: dr.Edits.Project.Kind, Author: dr.Edits.Project.Author,
		EditionLabel: o.EditionLabel, UseCover: o.UseCover, Edits: edits, Reorder: re, sum: dr.SHA256, draft: dr.ID}
}

// appendIn is the same for adding to a project.
func (s *stagedSource) appendIn(dr *Draft, token string) AppendIn {
	a := s.applyIn(dr, token)
	return AppendIn{ProjectID: dr.TargetProject, ParentID: dr.TargetParent, Token: token, Name: a.Name, Files: a.Files, Zip: a.Zip, Docx: a.Docx, Epub: a.Epub, Pdf: a.Pdf, Text: a.Text,
		Pandoc: a.Pandoc, PandocFormat: a.PandocFormat, Layout: a.Layout, DocxHeadings: a.DocxHeadings, DocxChanges: a.DocxChanges, SplitScenes: a.SplitScenes,
		EpubSections: a.EpubSections, EpubPages: a.EpubPages, Pictures: a.Pictures, ScrivResearch: a.ScrivResearch, ScrivNotes: a.ScrivNotes, ScrivSheets: a.ScrivSheets,
		WPStatus: a.WPStatus, WPTypes: a.WPTypes, GhostStatus: a.GhostStatus, GhostTypes: a.GhostTypes, DocxSplit: a.DocxSplit, DocxSplitStyle: a.DocxSplitStyle, DocxSplitWords: a.DocxSplitWords, DocxBreaks: a.DocxBreaks, DocxSplitPattern: a.DocxSplitPattern, DocxSplitMatch: a.DocxSplitMatch, EditionLabel: a.EditionLabel, Edits: a.Edits, Reorder: a.Reorder, sum: a.sum, draft: a.draft}
}

// previewDraft runs the preview of a draft in a given state. For a Git draft it asks the Git service; pending says the clone is
// still being made and failed says why it did not finish.
func previewDraft(c *ops.Ctx, dr *Draft, o DraftOptions, e DraftEdits) (pv *PreviewOut, pending bool, failed string, err error) {
	if c.DryRun() {
		return nil, false, "", ops.DryRunUnsupported("import.draft")
	}
	if dr.SourceKind == SourceGitJob {
		if remoteDraft.Preview == nil {
			return nil, false, "", ops.Conflict("Git sync is not running on this server.")
		}
		edits, re := e.list()
		p, status, failure, err := remoteDraft.Preview(c, dr.SourceRef, o.SplitScenes, edits, re)
		switch {
		case err != nil:
			return nil, false, "", err
		case status == "running":
			return nil, true, "", nil
		case status == "failed":
			return nil, false, failure, nil
		}
		return p, false, "", nil
	}
	ss, err := loadSource(c, dr)
	if err != nil {
		return nil, false, "", err
	}
	in := ss.previewIn(dr, o, e)
	if dr.TargetProject != "" {
		out, err := previewInto(c, AppendPreviewIn{ProjectID: dr.TargetProject, Name: in.Name, Files: in.Files, Zip: in.Zip, Docx: in.Docx, Epub: in.Epub, Pdf: in.Pdf, Text: in.Text, Pandoc: in.Pandoc,
			PandocFormat: in.PandocFormat, Layout: in.Layout, DocxHeadings: in.DocxHeadings, DocxChanges: in.DocxChanges, SplitScenes: in.SplitScenes, EpubSections: in.EpubSections,
			EpubPages: in.EpubPages, Pictures: in.Pictures, ScrivResearch: in.ScrivResearch, ScrivNotes: in.ScrivNotes, ScrivSheets: in.ScrivSheets, WPStatus: in.WPStatus,
			WPTypes: in.WPTypes, GhostStatus: in.GhostStatus, GhostTypes: in.GhostTypes, DocxSplit: in.DocxSplit, DocxSplitStyle: in.DocxSplitStyle, DocxSplitWords: in.DocxSplitWords, DocxBreaks: in.DocxBreaks, DocxSplitPattern: in.DocxSplitPattern, DocxSplitMatch: in.DocxSplitMatch, DocxSplitTry: in.DocxSplitTry, DocxSplitTryMatch: in.DocxSplitTryMatch, Edits: in.Edits, Reorder: in.Reorder, sum: in.sum})
		if err != nil {
			return nil, false, "", err
		}
		return &out, false, "", nil
	}
	out, err := previewNew(c, in)
	if err != nil {
		return nil, false, "", err
	}
	return &out, false, "", nil
}

// ---- changing a draft -------------------------------------------------------------------------------------------------

type draftState struct {
	Options DraftOptions `json:"options"`
	Edits   DraftEdits   `json:"edits"`
}

func (d *Draft) state() draftState { return draftState{Options: d.Options, Edits: d.Edits} }

func (d *Draft) stateJSON() string {
	b, _ := json.Marshal(d.state())
	return string(b)
}

// clone copies the parts of a state a change edits, so a refused change leaves the draft as it was.
func (s draftState) clone() draftState {
	b, _ := json.Marshal(s)
	var out draftState
	_ = json.Unmarshal(b, &out)
	return out
}

var chapterKinds = []string{"part", "chapter", "scene", "note", "front_matter", "back_matter"}

func updateDraft(c *ops.Ctx, lim StageLimits, in DraftUpdateIn) (DraftUpdateOut, error) {
	if c.DryRun() {
		return DraftUpdateOut{}, ops.DryRunUnsupported("import.draft.update")
	}
	dr, err := access(c, in.ID)
	if err != nil {
		return DraftUpdateOut{}, err
	}
	switch dr.Status {
	case DraftApplying:
		return DraftUpdateOut{}, ops.ConflictWith("This import is being applied.", map[string]any{"reason": "applying"})
	case DraftDone:
		return DraftUpdateOut{}, ops.ConflictWith("This import was already applied.", map[string]any{"reason": "done"})
	}
	if in.BaseRevision != dr.Revision {
		return DraftUpdateOut{}, staleRevision()
	}
	onlyProject := in.Project != nil && !in.Undo && in.Reuse == "" && in.Options == nil && in.Edit == nil && in.Move == nil && in.Reorder == nil && in.Break == nil && in.Within == nil && in.Matter == nil && !in.TitleCaseAll && !in.ChapterLineAll
	if in.Project != nil {
		p, err := cleanProject(*in.Project)
		if err != nil {
			return DraftUpdateOut{}, err
		}
		if onlyProject {
			if err := saveProject(c, c.DB(), dr, p); err != nil {
				return DraftUpdateOut{}, err
			}
			out := DraftUpdateOut{Item: dr.view(), Note: &DraftNote{Code: "project"}}
			return out, nil
		}
		in.Project = &p
	}
	prev := dr.stateJSON()
	next := dr.state().clone()
	note := &DraftNote{}
	keepEdits := false
	var cur *PreviewOut
	current := func() (*PreviewOut, error) {
		if cur != nil {
			return cur, nil
		}
		pv, pending, _, err := previewDraft(c, dr, dr.Options, dr.Edits)
		if err != nil {
			return nil, err
		}
		if pv == nil {
			if pending {
				return nil, ops.Conflict("This import is still being fetched.")
			}
			return nil, ops.Conflict("There is nothing to change yet.")
		}
		cur = pv
		return cur, nil
	}
	switch {
	case in.Undo:
		if in.Options != nil || in.Edit != nil || in.Move != nil || in.Reorder != nil || in.Project != nil || in.Break != nil || in.Within != nil || in.Matter != nil || in.TitleCaseAll || in.ChapterLineAll || in.Reuse != "" {
			return DraftUpdateOut{}, ops.Invalid("undo", "Undo is a change of its own.")
		}
		if dr.Prev == "" {
			return DraftUpdateOut{}, conflictWhy("There is nothing to undo.", "nothing_to_undo", nil)
		}
		var st draftState
		if err := json.Unmarshal([]byte(dr.Prev), &st); err != nil {
			return DraftUpdateOut{}, ops.Internal(err)
		}
		next = st
		note.Code = "undone"
	default:
		if in.Break != nil {
			if !cutKind(dr.Kind) {
				return DraftUpdateOut{}, invalidWhy("break", "A chapter can be started by hand in a Word document, a text file, or a PDF or Pandoc document.", "break_kind")
			}
			if *in.Break < 1 || *in.Break > maxParagraphs {
				return DraftUpdateOut{}, ops.Invalid("break", "A break is a paragraph's ordinal: 1 or more.")
			}
			if slices.Contains(breakList(next.Options.DocxBreaks), *in.Break) { // one is started there already
				return DraftUpdateOut{}, conflictWhy("A chapter cannot start there, or one already does.", "break_noop", nil)
			}
			if in.Options == nil {
				in.Options = map[string]string{}
			}
			in.Options["docx_breaks"] = canonBreaks(next.Options.DocxBreaks + "," + strconv.Itoa(*in.Break))
			if len(breakList(in.Options["docx_breaks"])) > MaxExplicitBrks {
				return DraftUpdateOut{}, invalidWhy("break", "There are as many chapters of your own as there can be.", "break_cap")
			}
			note.Code = "break"
			if in.BreakFrom != "" {
				note.Code = "split_para"
			}
		}
		if in.Within != nil {
			if in.Break != nil {
				return DraftUpdateOut{}, ops.Invalid("within", "Start one chapter, or every one of a kind, not both at once.")
			}
			if dr.Kind != StageDocx {
				return DraftUpdateOut{}, invalidWhy("within", "A chapter can be started by hand in a Word document.", "break_kind")
			}
			if !slices.Contains(withinRules, in.Within.Rule) {
				return DraftUpdateOut{}, ops.Invalid("within.rule", "That is not a kind of place a chapter could start.")
			}
			ask := &lookAsk{key: in.Within.Key, mode: lookRule, rule: in.Within.Rule, size: next.Options.DocxWords}
			if err := lookDraft(c, c, dr, dr.Options, ask); err != nil {
				return DraftUpdateOut{}, err
			}
			if len(ask.ords) == 0 {
				return DraftUpdateOut{}, conflictWhy("There is no place of that kind in this chapter.", "within_none", nil)
			}
			if in.Options == nil {
				in.Options = map[string]string{}
			}
			var jb strings.Builder
			jb.WriteString(next.Options.DocxBreaks)
			for _, o := range ask.ords {
				jb.WriteByte(',')
				jb.WriteString(strconv.Itoa(o))
			}
			in.Options["docx_breaks"] = canonBreaks(jb.String())
			if len(breakList(in.Options["docx_breaks"])) > MaxExplicitBrks {
				return DraftUpdateOut{}, invalidWhy("within", "There are as many chapters of your own as there can be.", "break_cap")
			}
			note.Code = "within"
			note.Title = in.Within.Rule
		}
		if in.Options != nil {
			reset, err := applyOptions(&next, in.Options)
			if err != nil {
				return DraftUpdateOut{}, err
			}
			if reset {
				if cutKind(dr.Kind) {
					keepEdits = true // a Word document's chapters are keyed by the paragraph they start at: the edits of those that remain stay
				} else {
					next.Edits = DraftEdits{Project: next.Edits.Project}
				}
			}
			if note.Code == "" {
				note.Code = "options"
				if _, tried := in.Options["docx_split_try"]; tried && !reset {
					note.Code = "pattern" // a pattern only tried: nothing is cut, and nothing of the writer's is touched
				}
			}
		}
		if in.Project != nil {
			next.Edits.Project = *in.Project
		}
		if in.Edit != nil {
			pv, err := current()
			if err != nil {
				return DraftUpdateOut{}, err
			}
			if err := applyEdit(&next.Edits, pv, *in.Edit, note); err != nil {
				return DraftUpdateOut{}, err
			}
			if g, ok := next.Edits.Gone[in.Edit.Key]; ok && note.Code == "joined" && g.To != "" {
				// a join is for the end of the text it was made on: the target's own words are what says it is the same text later
				g.ToWords = rawWords(c, dr, next, g.To)
				next.Edits.Gone[in.Edit.Key] = g
			}
		}
		if in.Matter != nil {
			pv, err := current()
			if err != nil {
				return DraftUpdateOut{}, err
			}
			if err := applyMatter(&next.Edits, pv, *in.Matter, note); err != nil {
				return DraftUpdateOut{}, err
			}
		}
		if in.Move != nil {
			pv, err := current()
			if err != nil {
				return DraftUpdateOut{}, err
			}
			if err := applyMove(&next.Edits, pv, *in.Move, note); err != nil {
				return DraftUpdateOut{}, err
			}
		}
		if in.Reorder != nil {
			if next.Edits.Order == nil {
				next.Edits.Order = map[string][]string{}
			}
			next.Edits.Order[in.Reorder.Parent] = slices.Clone(in.Reorder.Keys)
		}
	}
	if in.TitleCaseAll {
		pv, err := current()
		if err != nil {
			return DraftUpdateOut{}, err
		}
		n := 0
		for _, nd := range pv.Nodes {
			if nd.SuggestedTitle == "" {
				continue
			}
			if next.Edits.Edits == nil {
				next.Edits.Edits = map[string]Edit{}
			}
			ed := next.Edits.Edits[nd.Key]
			if ed.Title != nil {
				continue // the writer has named it already
			}
			t := nd.SuggestedTitle
			ed.Title = &t
			next.Edits.Edits[nd.Key] = ed
			n++
		}
		if n == 0 {
			return DraftUpdateOut{}, conflictWhy("There is no title in capitals left to change.", "no_caps", nil)
		}
		note.Code, note.Count = "title_case", n
	}
	if in.ChapterLineAll {
		pv, err := current()
		if err != nil {
			return DraftUpdateOut{}, err
		}
		n := 0
		for _, nd := range pv.Nodes {
			if nd.LineTitle == "" {
				continue
			}
			if next.Edits.Edits == nil {
				next.Edits.Edits = map[string]Edit{}
			}
			ed := next.Edits.Edits[nd.Key]
			if ed.Title != nil {
				continue // the writer has named it already
			}
			t := nd.LineTitle
			ed.Title = &t
			ed.KeepLine = true // the words before the chapter line stay in the text
			next.Edits.Edits[nd.Key] = ed
			n++
		}
		if n == 0 {
			return DraftUpdateOut{}, conflictWhy("There is no chapter line left to use as a title.", "no_line", nil)
		}
		note.Code, note.Count = "line_title", n
	}
	if in.Reuse != "" {
		// the chapter changes of an earlier review of this file, taken as a change of cut is: a change whose chapter is not in this cut is set aside
		src, err := access(c, in.Reuse)
		if err != nil {
			return DraftUpdateOut{}, err
		}
		if src.Status != DraftOpen || src.ID == dr.ID || src.SHA256 != dr.SHA256 || src.Kind != dr.Kind {
			return DraftUpdateOut{}, conflictWhy("That review is not of this file.", "reuse_other", nil)
		}
		if changesOf(next.Edits) > 0 {
			return DraftUpdateOut{}, conflictWhy("This review already has changes of its own.", "reuse_not_fresh", nil)
		}
		n := changesOf(src.Edits)
		if n == 0 {
			return DraftUpdateOut{}, conflictWhy("That review has no changes to use.", "reuse_none", nil)
		}
		next.Edits = DraftEdits{Project: next.Edits.Project, Edits: maps.Clone(src.Edits.Edits), Gone: maps.Clone(src.Edits.Gone), Order: maps.Clone(src.Edits.Order)}
		keepEdits = true
		note.Code, note.Count = "reuse", n
	}
	tmp := *dr
	tmp.Options, tmp.Edits = next.Options, next.Edits
	if keepEdits {
		kept, dropped, restored, err := keepSurvivors(c, &tmp, &next, current, dr.Options)
		if err != nil {
			return DraftUpdateOut{}, err
		}
		note.Kept = kept
		note.Dropped = dropped
		note.Restored = restored
		if note.Code == "options" {
			note.Code = "recut"
		}
		if len(note.Dropped) > 20 {
			note.NoMore = len(note.Dropped) - 20
			note.Dropped = note.Dropped[:20]
		}
	}
	pv, _, _, err := previewDraft(c, &tmp, next.Options, next.Edits)
	if err != nil {
		return DraftUpdateOut{}, err
	}
	if (note.Code == "break" || note.Code == "split_para") && pv != nil && in.Break != nil {
		// a break that adds no chapter (one already starts there, or it is not a place a chapter can start) is refused, not stored
		if pv.Breaks == nil || !slices.Contains(pv.Breaks.Added, *in.Break) {
			return DraftUpdateOut{}, conflictWhy("A chapter cannot start there, or one already does.", "break_noop", nil)
		}
		if note.Code == "split_para" {
			if !nameSplit(pv, in.BreakFrom, *in.Break, note) {
				return DraftUpdateOut{}, conflictWhy("That chapter is not in the import any more.", "break_noop", nil)
			}
		} else {
			note.Title, note.Other = whereBreak(pv, *in.Break)
		}
	}
	if note.Code == "within" && pv != nil && pv.Breaks != nil {
		was, err := current()
		if err != nil {
			return DraftUpdateOut{}, err
		}
		note.Count = len(pv.Breaks.Added)
		if was.Breaks != nil {
			note.Count -= len(was.Breaks.Added)
		}
		if note.Count < 1 {
			return DraftUpdateOut{}, conflictWhy("A chapter already starts at each of those places.", "within_none", nil)
		}
	}
	if note.Code == "pattern" && pv != nil && pv.Breaks != nil && pv.Breaks.Try != nil {
		note.Count = pv.Breaks.Try.Count
	}
	token := ""
	if pv != nil {
		token = pv.Token
	}
	saved := *dr
	saved.Options, saved.Edits = next.Options, next.Edits
	if err := saveState(c, c.DB(), &saved, prev, token); err != nil {
		return DraftUpdateOut{}, err
	}
	if in.Undo { // the state that was undone is the one before the next undo
		saved.Prev = prev
	}
	if note.Code == "" {
		note = nil
	}
	item := saved.view()
	item.Reuse = offersFor(c, &saved)
	return DraftUpdateOut{Item: item, Preview: pv, Note: note}, nil
}

// keepSurvivors settles, after a change of how a Word document is cut, the writer's edits against the chapters that are now there. A
// chapter's key is named for the paragraph it starts at, so an edit stays with its chapter when another boundary comes or goes. An edit
// whose chapter is not in this cut, or whose chapter now holds other text (a leaving out or a join is tied to the words it was made on),
// is set aside in the draft's stash and listed by title; when a later cut brings the same chapter back, with the same text, the edit
// comes back with it. If the edits that remain would not make a plan, none are kept (all are set aside).
// tmp is the draft in its new state and is changed in place; next is the state being built.
func keepSurvivors(c *ops.Ctx, tmp *Draft, next *draftState, current func() (*PreviewOut, error), before DraftOptions) (kept int, dropped, restored []string, err error) {
	old, err := current() // the preview before the change: the titles the writer saw
	if err != nil {
		return 0, nil, nil, err
	}
	titles := map[string]string{}
	for _, n := range old.Nodes {
		titles[n.Key] = n.Title
	}
	for k, g := range next.Edits.Gone {
		titles[k] = g.Title
	}
	// the cut as it is with none of the writer's edits but the splitting of a chapter into scenes, which makes the keys of its scenes
	fresh := *tmp
	pv, err := cutWithSplits(c, &fresh, next.Options, next.Edits)
	if err != nil || pv == nil {
		return 0, nil, nil, err
	}
	// a scene the writer renamed or left out whose chapter was cut: it is the scene with the same heading and the same words in the
	// chapter that has it now, when there is exactly one, and the key of the scene follows it
	if moved := movedScenes(c, tmp, before, next.Edits, pv); len(moved) > 0 {
		st := draftState{Options: next.Options, Edits: next.Edits}
		st.mapKeys(func(k string) string {
			if nk, ok := moved[k]; ok {
				return nk
			}
			return k
		})
		next.Edits = st.Edits
	}
	words := map[string]int{}
	for _, n := range pv.Nodes {
		words[n.Key] = n.Words
	}
	for _, n := range pv.LeftOut {
		words[n.Key] = n.Words
	}
	exists := func(k string) bool {
		if k == "" {
			return true
		}
		_, ok := words[k]
		return ok
	}
	// still the same text: what a leaving out or a join was made on
	// and, for a join, the text it was joined onto the end of: a split inside that chapter leaves the join with the wrong half
	sameText := func(k string, g Gone) bool {
		return (g.Words == 0 || words[k] == g.Words) && (g.To == "" || g.ToWords == 0 || words[g.To] == g.ToWords)
	}
	was := next.Edits
	nw := DraftEdits{Project: was.Project}
	stash := func(k string, ed Edit, g *Gone, title string) {
		if nw.Stash == nil {
			nw.Stash = map[string]Stashed{}
		}
		if len(nw.Stash) < MaxStash {
			nw.Stash[k] = Stashed{Edit: ed, Gone: g, Title: title}
		}
	}
	for _, k := range sortedKeys(was.Edits) {
		ed := was.Edits[k]
		g, isGone := was.Gone[k]
		title := firstNonEmpty(titles[k], derefStr(ed.Title), k)
		if !exists(k) || (ed.MergeInto != "" && !exists(ed.MergeInto)) || (ed.Parent != nil && !exists(*ed.Parent)) || (isGone && !sameText(k, g)) {
			var gp *Gone
			if isGone {
				gg := g
				gp = &gg
			}
			stash(k, ed, gp, title)
			dropped = append(dropped, title)
			continue
		}
		if nw.Edits == nil {
			nw.Edits = map[string]Edit{}
		}
		nw.Edits[k] = ed
		if isGone {
			if nw.Gone == nil {
				nw.Gone = map[string]Gone{}
			}
			nw.Gone[k] = g
		}
		kept++
	}
	// what was set aside before comes back with its chapter
	for _, k := range sortedKeys(was.Stash) {
		st := was.Stash[k]
		if _, now := nw.Edits[k]; now { // the writer has edited it since: that stands
			continue
		}
		okExt := st.Gone == nil || sameText(k, *st.Gone)
		okRefs := (st.Edit.MergeInto == "" || exists(st.Edit.MergeInto)) && (st.Edit.Parent == nil || exists(*st.Edit.Parent))
		if exists(k) && okExt && okRefs {
			if nw.Edits == nil {
				nw.Edits = map[string]Edit{}
			}
			nw.Edits[k] = st.Edit
			if st.Gone != nil {
				if nw.Gone == nil {
					nw.Gone = map[string]Gone{}
				}
				nw.Gone[k] = *st.Gone
			}
			kept++
			restored = append(restored, firstNonEmpty(st.Title, k))
			continue
		}
		if nw.Stash == nil {
			nw.Stash = map[string]Stashed{}
		}
		if _, dup := nw.Stash[k]; !dup {
			nw.Stash[k] = st
		}
	}
	for _, p := range sortedKeys(was.Order) {
		if !exists(p) {
			continue
		}
		var keys []string
		for _, k := range was.Order[p] {
			if exists(k) {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			if nw.Order == nil {
				nw.Order = map[string][]string{}
			}
			nw.Order[p] = keys
		}
	}
	// the edits that remain must make a plan; if they would not, none are kept
	test := *tmp
	test.Edits = nw
	if _, _, _, perr := previewDraft(c, &test, next.Options, nw); perr != nil {
		dropped, restored, kept = nil, nil, 0
		nw = DraftEdits{Project: was.Project, Stash: was.Stash}
		for _, k := range sortedKeys(was.Edits) {
			ed := was.Edits[k]
			g, isGone := was.Gone[k]
			var gp *Gone
			if isGone {
				gg := g
				gp = &gg
			}
			title := firstNonEmpty(titles[k], derefStr(ed.Title), k)
			stash(k, ed, gp, title)
			dropped = append(dropped, title)
		}
	}
	next.Edits, tmp.Edits = nw, nw
	return kept, dropped, restored, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// whereBreak names, for the page's words, the chapter that starts at a paragraph and the one before it.
func whereBreak(pv *PreviewOut, ord int) (title, before string) {
	at := -1
	for i, n := range pv.Nodes {
		if n.Ordinal >= ord && at < 0 {
			at = i
		}
	}
	if at < 0 {
		return "", ""
	}
	title = pv.Nodes[at].Title
	if at > 0 {
		before = pv.Nodes[at-1].Title
	}
	return title, before
}

func cleanProject(p ProjectEdit) (ProjectEdit, error) {
	p.Title = cleanLabel(p.Title)
	p.Author = cleanLabel(p.Author)
	if p.Kind != "" && !slices.Contains(projects.Kinds, p.Kind) {
		return p, ops.Invalid("project.kind", "That is not a kind of project.")
	}
	return p, nil
}

// saveProject stores the project's fields without a new revision.
func saveProject(ctx context.Context, d *db.DB, dr *Draft, p ProjectEdit) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT edits_json FROM import_drafts WHERE id = ? AND owner_id = ? AND status = 'open'`, dr.ID, dr.OwnerID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("import")
			}
			return err
		}
		var e DraftEdits
		_ = json.Unmarshal([]byte(raw), &e)
		e.Project = p
		b, _ := json.Marshal(e)
		_, err := tx.ExecContext(ctx, `UPDATE import_drafts SET edits_json = ?, updated_at = ? WHERE id = ?`, string(b), db.Now(), dr.ID)
		dr.Edits = e
		return err
	})
}

// applyOptions sets options by name on a state. reset says one of them changed how the file is read, so the edits keyed to its
// documents no longer apply.
func applyOptions(st *draftState, m map[string]string) (reset bool, err error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	was := st.Options.shaped()
	for _, k := range keys {
		if !st.Options.set(k, m[k]) {
			return false, ops.Invalid("options."+k, "That is not an option of an import.")
		}
		if st.Options.Layout == "auto" {
			st.Options.Layout = ""
		}
	}
	if err := st.Options.check(); err != nil {
		return false, err
	}
	reset = was != st.Options.shaped()
	if _, said := m["ack_big"]; reset && !said {
		st.Options.AckBig = false // the tick was for the cut the writer saw: another cut asks again
	}
	return reset, nil
}

// nodeWords is the words of the proposed document with this key, 0 if there is none.
func nodeWords(pv *PreviewOut, key string) int {
	for _, n := range pv.Nodes {
		if n.Key == key {
			return n.Words
		}
	}
	return 0
}

func findNode(pv *PreviewOut, key string) (title string, found bool) {
	for _, n := range pv.Nodes {
		if n.Key == key {
			return n.Title, true
		}
	}
	for _, n := range pv.Research {
		if n.Key == key {
			return n.Title, true
		}
	}
	return "", false
}

// below lists the keys of the document and everything inside it.
func below(pv *PreviewOut, key string) map[string]bool {
	in := map[string]bool{key: true}
	for _, n := range pv.Nodes { // nodes come in reading order: a parent before what is inside it
		if in[n.ParentKey] {
			in[n.Key] = true
		}
	}
	return in
}

func tidyEdit(e *DraftEdits, k string) {
	if ed, ok := e.Edits[k]; ok {
		ed.Key = ""
		if ed == (Edit{}) {
			delete(e.Edits, k)
		}
	}
}

func dropFromOrders(e *DraftEdits, k string) {
	for p, keys := range e.Order {
		e.Order[p] = slices.DeleteFunc(slices.Clone(keys), func(x string) bool { return x == k })
	}
}

// applyEdit is the writer's change to one document, with the bookkeeping the review has always done: what was left out is
// remembered by title, a join stands alone, and a move leaves the order lists.
func applyEdit(e *DraftEdits, pv *PreviewOut, in DraftEdit, note *DraftNote) error {
	k := in.Key
	title, ok := findNode(pv, k)
	if !ok && !(in.Skip != nil && !*in.Skip) {
		return ops.Invalid("edit.key", "The import has no document with that key.")
	}
	if in.JoinPrevious != nil && *in.JoinPrevious {
		if in.MergeInto != nil {
			return ops.Invalid("edit.join_previous", "Join it with the one before, or into another, not both.")
		}
		to, why := JoinTarget(pv, k)
		if to == nil {
			return conflictWhy("There is no document before it that it can be joined into.", "no_previous", map[string]any{"why": why})
		}
		in.MergeInto = &to.Key
	}
	if e.Edits == nil {
		e.Edits = map[string]Edit{}
	}
	ed := e.Edits[k]
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return invalidWhy("edit.title", "A title cannot be empty.", "title_empty")
		}
		if len([]rune(t)) > 300 {
			return ops.Invalid("edit.title", "A title is at most 300 characters.")
		}
		ed.Title = &t
		ed.KeepLine = false // a rename the writer makes is the writer's: nothing is added to the text
		note.Code, note.Title = "renamed", t
	}
	if in.Kind != nil {
		if !slices.Contains(chapterKinds, *in.Kind) {
			return ops.Invalid("edit.kind", "That is not a kind of document.")
		}
		kind := *in.Kind
		ed.Kind = &kind
		note.Code, note.Title = "retyped", title
	}
	if in.Split != nil {
		v := *in.Split
		ed.Split = &v
		for x := range e.Edits {
			if strings.HasPrefix(x, k+"#") {
				delete(e.Edits, x)
			}
		}
		for x := range e.Order {
			if strings.HasPrefix(x, k+"#") {
				delete(e.Order, x)
			}
		}
		delete(e.Order, k)
		note.Code, note.Title = "split", title
	}
	if in.Skip != nil {
		if *in.Skip {
			// a chapter that others are joined into stays until they are put back (the mapping is refused in either order)
			ins := below(pv, k)
			for _, g := range sortedKeys(e.Gone) {
				if to := e.Gone[g].To; to != "" && ins[to] {
					return ops.ConflictWith(fmt.Sprintf("%s cannot be left out while %s is joined into it. Put %s back first.", title, e.Gone[g].Title, e.Gone[g].Title),
						map[string]any{"reason": "joined_into", "title": title, "other": e.Gone[g].Title})
				}
			}
			if e.Gone == nil {
				e.Gone = map[string]Gone{}
			}
			e.Gone[k] = Gone{Title: title, Inside: len(ins) - 1, Words: nodeWords(pv, k)}
			ed.Skip, ed.Keep = true, false
			dropFromOrders(e, k)
			note.Code, note.Title = "left_out", title
		} else {
			ed.Skip = false
			ed.MergeInto = ""
			if g, had := e.Gone[k]; had {
				note.Title = g.Title
			}
			for _, l := range pv.LeftOut {
				if l.Key == k { // left out by the import itself: putting it back is an edit of its own
					ed.Keep = true
					note.Title = l.Title
				}
			}
			delete(e.Gone, k)
			note.Code = "put_back"
		}
	}
	if in.Parent != nil {
		to := *in.Parent
		toTitle := ""
		if to != "" {
			t, found := findNode(pv, to)
			if !found {
				return ops.Invalid("edit.parent", "The import has no document with that key to move it under.")
			}
			toTitle = t
		}
		dropFromOrders(e, k)
		ed.Parent = &to
		note.Code, note.Title, note.Other = "moved_under", title, toTitle
		if to == "" {
			note.Code = "moved_top"
		}
	}
	if in.MergeInto != nil {
		to := *in.MergeInto
		toTitle, found := findNode(pv, to)
		if to == "" || !found || to == k {
			return invalidWhy("edit.merge_into", "Choose a chapter to join this one into.", "join_target")
		}
		dropFromOrders(e, k)
		if e.Gone == nil {
			e.Gone = map[string]Gone{}
		}
		e.Gone[k] = Gone{Title: title, Into: toTitle, To: to, Words: nodeWords(pv, k)}
		ed = Edit{MergeInto: to} // a join stands alone
		note.Code, note.Title, note.Other = "joined", title, toTitle
	}
	e.Edits[k] = ed
	tidyEdit(e, k)
	return nil
}

// applyMove puts a document one place up or down, or at an end, among those with the same parent.
func applyMove(e *DraftEdits, pv *PreviewOut, m DraftMove, note *DraftNote) error {
	var self *Node
	for i := range pv.Nodes {
		if pv.Nodes[i].Key == m.Key {
			self = &pv.Nodes[i]
		}
	}
	if self == nil {
		return ops.Invalid("move.key", "The import has no document with that key.")
	}
	par := self.ParentKey
	keys := e.Order[par]
	if keys == nil {
		for _, n := range pv.Nodes {
			if n.ParentKey == par {
				keys = append(keys, n.Key)
			}
		}
	}
	at := slices.Index(keys, m.Key)
	to := at
	switch m.To {
	case "up":
		to = at - 1
	case "down":
		to = at + 1
	case "top":
		to = 0
	case "bottom":
		to = len(keys) - 1
	default:
		return ops.Invalid("move.to", "to is up, down, top or bottom.")
	}
	if at < 0 || to < 0 || to >= len(keys) || to == at {
		return conflictWhy("It cannot move further that way.", "cannot_move", nil)
	}
	keys = slices.Clone(keys)
	keys = slices.Delete(keys, at, at+1)
	keys = slices.Insert(keys, to, m.Key)
	if e.Order == nil {
		e.Order = map[string][]string{}
	}
	e.Order[par] = keys
	tidyEdit(e, m.Key)
	nb := keys[to+1:]
	if to >= len(keys)-1 {
		nb = keys[to-1 : to]
	}
	other := ""
	if len(nb) > 0 {
		other, _ = findNode(pv, nb[0])
	}
	*note = DraftNote{Code: "moved", Title: self.Title, Other: other, Pos: to + 1, Count: len(keys), Above: to < len(keys)-1}
	return nil
}

// ---- applying a draft ------------------------------------------------------------------------------------------------

// stagedApplyFields are the fields an apply may carry beside stage_id: what the person types on the review, and the preview's token.
var stagedApplyAllowed = map[string]bool{"stage_id": true, "rev": true, "token": true, "title": true, "kind": true, "author": true, "language": true,
	"edition_label": true, "use_cover": true, "id": true, "parent_id": true}

// onlyStage refuses a request that sends a source or options besides its stage_id: it is the stage or the files, not both.
func onlyStage(in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return ops.Internal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	var extra []string
	for k := range m {
		if !stagedApplyAllowed[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		return ops.Invalid(extra[0], "Send stage_id, or the files, not both: with a stage_id the file, the options and the edits are the draft's.")
	}
	return nil
}

// stagedApply runs an apply of a draft: it claims the draft (one apply, whatever the tabs), runs fn, and then deletes the staged
// file and keeps only the counts. A failed apply gives the draft back, open and usable.
func stagedApply(c *ops.Ctx, stageID string, rev int, token, project string, fn func(dr *Draft, ss *stagedSource, token string) (ApplyOut, error)) (ApplyOut, error) {
	if c.DryRun() {
		return ApplyOut{}, ops.DryRunUnsupported("import.apply")
	}
	dr, err := access(c, stageID)
	if err != nil {
		return ApplyOut{}, err
	}
	if project != dr.TargetProject {
		return ApplyOut{}, ops.NotFound("import")
	}
	if rev <= 0 {
		return ApplyOut{}, ops.Invalid("rev", "Send rev, the draft's revision you reviewed.")
	}
	if token == "" {
		return ApplyOut{}, ops.Invalid("token", "Preview the import first and send its token.")
	}
	if err := claimApply(c, c.DB(), dr, rev); err != nil {
		return ApplyOut{}, err
	}
	var out ApplyOut
	var ss *stagedSource
	if dr.SourceKind != SourceGitJob {
		if ss, err = loadSource(c, dr); err != nil {
			releaseApply(c, c.DB(), dr)
			return ApplyOut{}, err
		}
	}
	out, err = fn(dr, ss, token)
	if err != nil {
		releaseApply(c, c.DB(), dr)
		return ApplyOut{}, err
	}
	if out.Slug == "" && project != "" { // an addition names its project's page
		_ = c.DB().Read().QueryRowContext(c, `SELECT slug FROM projects WHERE id = ?`, project).Scan(&out.Slug)
	}
	if ferr := finishApply(c, c.DB(), c.Env.DataDir, dr, DraftResult{ProjectID: out.ProjectID, Slug: out.Slug, Title: out.Title, SourceID: out.SourceID, Documents: out.Documents, Words: out.Words,
		FirstDocument: out.FirstDocumentID, EditionID: out.EditionID, Suggestions: out.Suggestions, Comments: out.Comments, Pictures: out.Pictures,
		ResearchNotes: out.ResearchNotes, Entities: out.Entities, CoverUsed: out.CoverUsed, Skipped: out.SkippedTotal, Warnings: out.Warnings}); ferr != nil && c.Env.Log != nil {
		// The project exists. The draft stays "applying" with the apply's id in the project's source, and the next sweep finds it and marks it done.
		c.Env.Log.Warn("import: the draft of a committed import could not be marked done; the sweep will", "draft", dr.ID, "err", ferr)
	}
	return out, nil
}

func applyStagedNew(c *ops.Ctx, in ApplyIn) (ApplyOut, error) {
	if err := onlyStage(in); err != nil {
		return ApplyOut{}, err
	}
	return stagedApply(c, in.StageID, in.Rev, in.Token, "", func(dr *Draft, ss *stagedSource, token string) (ApplyOut, error) {
		if dr.SourceKind == SourceGitJob {
			if remoteDraft.Apply == nil {
				return ApplyOut{}, ops.Conflict("Git sync is not running on this server.")
			}
			edits, re := dr.Edits.list()
			ai := ApplyIn{Token: token, SplitScenes: dr.Options.SplitScenes, Title: pick(in.Title, dr.Edits.Project.Title), Kind: pick(in.Kind, dr.Edits.Project.Kind),
				Author: pick(in.Author, dr.Edits.Project.Author), Language: in.Language, Edits: edits, Reorder: re}
			return remoteDraft.Apply(c, dr.SourceRef, ai)
		}
		ai := ss.applyIn(dr, token)
		ai.Title, ai.Kind, ai.Author = pick(in.Title, ai.Title), pick(in.Kind, ai.Kind), pick(in.Author, ai.Author)
		ai.Language = in.Language
		if in.EditionLabel != "" {
			ai.EditionLabel = cleanLabel(in.EditionLabel)
		}
		ai.UseCover = ai.UseCover || in.UseCover
		return applyNewWith(c, ai, nil)
	})
}

func applyStagedInto(c *ops.Ctx, in AppendIn) (ApplyOut, error) {
	if err := onlyStage(in); err != nil {
		return ApplyOut{}, err
	}
	return stagedApply(c, in.StageID, in.Rev, in.Token, in.ProjectID, func(dr *Draft, ss *stagedSource, token string) (ApplyOut, error) {
		if dr.SourceKind == SourceGitJob {
			return ApplyOut{}, ops.Conflict("A Git import makes a new project; it cannot be added to one.")
		}
		ai := ss.appendIn(dr, token)
		if in.EditionLabel != "" {
			ai.EditionLabel = cleanLabel(in.EditionLabel)
		}
		return appendWith(c, ai)
	})
}

// stagedPreview is the preview of a draft, for import.preview and import.append_preview with a stage_id.
func stagedPreview(c *ops.Ctx, stageID, project string) (PreviewOut, error) {
	dr, err := access(c, stageID)
	if err != nil {
		return PreviewOut{}, err
	}
	if project != dr.TargetProject {
		return PreviewOut{}, ops.NotFound("import")
	}
	if dr.Status != DraftOpen {
		return PreviewOut{}, ops.ConflictWith("This import is not open: it was applied or is being applied.", map[string]any{"reason": dr.Status})
	}
	pv, pending, failed, err := previewDraft(c, dr, dr.Options, dr.Edits)
	if err != nil {
		return PreviewOut{}, err
	}
	switch {
	case pending:
		return PreviewOut{}, ops.Conflict("This import is still being fetched.")
	case failed != "":
		return PreviewOut{}, ops.Conflict("This import failed: " + failed)
	case pv == nil:
		return PreviewOut{}, ops.Conflict("There is nothing to preview.")
	}
	return *pv, nil
}

// idle is how long a draft may sit, as a duration, for the page's words.
func (l StageLimits) idle() time.Duration { return time.Duration(l.withDefaults().Hours) * time.Hour }

// StageHours is the idle limit in hours, with the default applied.
func (l StageLimits) StageHours() int { return l.withDefaults().Hours }

// ---- the writer's own breaks ---------------------------------------------------------------------------------------------

// withinRules are the kinds of place a chapter can be asked for inside a chapter.
var withinRules = append(slices.Clone(insideRules), "size")

// nameSplit says, for the page's words, where a split was made: the chapter that was split (by its key), the new chapter that begins at
// the paragraph, and the end of the one and the start of the other. It is false when either is not in the preview.
func nameSplit(pv *PreviewOut, from string, ord int, note *DraftNote) bool {
	var was, now, before *Node
	for i := range pv.Nodes {
		n := &pv.Nodes[i]
		if n.Key == from {
			was = n
		}
		if n.Ordinal == ord {
			now = n
		}
		// the document the paragraph was in: the one that starts nearest above it
		if n.Ordinal > 0 && n.Ordinal < ord && (before == nil || n.Ordinal > before.Ordinal || (n.Ordinal == before.Ordinal && before.Kind == "part" && n.Kind != "part")) {
			before = n // (a part and the text of its own, before its first chapter, start at the same paragraph: the text is the document)
		}
	}
	if now == nil || before == nil {
		return false
	}
	// A book with no marks is one document named for the book; its first break makes it "Chapter 1", and its key goes with the name: the
	// chapter that was split is then the one the paragraph was in. Else the chapter named must be that one, or the paragraph is not in it.
	if was != nil && was.Key != before.Key {
		return false
	}
	was = before
	note.Title, note.Other, note.Ends, note.Starts = now.Title, was.Title, was.LastLine, now.FirstLine
	return true
}

// rawWords is the words a document has on its own, in the cut the draft has now with none of the writer's edits (the text a join or a
// leaving out was made on), 0 when it is not there.
func rawWords(c *ops.Ctx, dr *Draft, next draftState, key string) int {
	fresh := *dr
	fresh.Edits = DraftEdits{Project: next.Edits.Project}
	pv, _, _, err := previewDraft(c, &fresh, next.Options, fresh.Edits)
	if err != nil || pv == nil {
		return 0
	}
	if n := nodeWords(pv, key); n > 0 {
		return n
	}
	for _, l := range pv.LeftOut {
		if l.Key == key {
			return l.Words
		}
	}
	return 0
}

// JoinTarget is the document a "join with previous" joins into: the one before it among those with the same parent. why is empty when
// there is one, and else the reason there is not (a part, or a document with others inside it, is not joined; nor into a part, or into
// a document with others inside it, whose text would then come before them).
func JoinTarget(pv *PreviewOut, key string) (to *Node, why string) {
	self := -1
	for i := range pv.Nodes {
		if pv.Nodes[i].Key == key {
			self = i
		}
	}
	if self < 0 {
		return nil, "none"
	}
	n := pv.Nodes[self]
	kids := map[string]bool{}
	for _, x := range pv.Nodes {
		kids[x.ParentKey] = true
	}
	switch {
	case n.Kind == "part" || kids[n.Key]:
		return nil, "part"
	}
	for i := self - 1; i >= 0; i-- {
		if pv.Nodes[i].ParentKey == n.ParentKey {
			if pv.Nodes[i].Kind == "part" || kids[pv.Nodes[i].Key] {
				return nil, "before_part"
			}
			return &pv.Nodes[i], ""
		}
	}
	return nil, "first"
}

// applyMatter makes a document, and every document above it (or below it), front matter (or back matter). Parts stay what they are.
func applyMatter(e *DraftEdits, pv *PreviewOut, m DraftMatter, note *DraftNote) error {
	kind := "front_matter"
	switch m.Side {
	case "front":
	case "back":
		kind = "back_matter"
	default:
		return ops.Invalid("matter.side", "side is front or back.")
	}
	at := -1
	for i := range pv.Nodes {
		if pv.Nodes[i].Key == m.Key {
			at = i
		}
	}
	if at < 0 {
		return ops.Invalid("matter.key", "The import has no document with that key.")
	}
	from, to := 0, at+1
	if m.Side == "back" {
		from, to = at, len(pv.Nodes)
	}
	if e.Edits == nil {
		e.Edits = map[string]Edit{}
	}
	n := 0
	for _, nd := range pv.Nodes[from:to] {
		if nd.Kind == "part" || nd.Kind == kind {
			continue
		}
		ed := e.Edits[nd.Key]
		k := kind
		ed.Kind = &k
		e.Edits[nd.Key] = ed
		n++
	}
	if n == 0 {
		return conflictWhy("They are that already.", "matter_none", nil)
	}
	title, _ := findNode(pv, m.Key)
	note.Code, note.Title, note.Count, note.Side = "matter", title, n, m.Side
	return nil
}

// cutWithSplits is the preview of a draft's cut with none of the writer's edits except which chapters are split into scenes (an edit that
// is for a chapter that is in the cut): what has the keys of the scenes, and so what a scene's key is checked against.
func cutWithSplits(c *ops.Ctx, d *Draft, o DraftOptions, e DraftEdits) (*PreviewOut, error) {
	bare := DraftEdits{Project: e.Project}
	pv, _, _, err := previewDraft(c, d, o, bare)
	if err != nil || pv == nil {
		return pv, err
	}
	have := map[string]bool{}
	for _, n := range pv.Nodes {
		have[n.Key] = true
	}
	splits := DraftEdits{Project: e.Project}
	for _, k := range sortedKeys(e.Edits) {
		if ed := e.Edits[k]; ed.Split != nil && have[k] {
			if splits.Edits == nil {
				splits.Edits = map[string]Edit{}
			}
			splits.Edits[k] = Edit{Split: ed.Split}
		}
	}
	if len(splits.Edits) == 0 {
		return pv, nil
	}
	if out, _, _, err := previewDraft(c, d, o, splits); err == nil && out != nil {
		return out, nil
	}
	return pv, nil
}

// movedScenes finds, for each edit of a scene the cut no longer has under its key, the scene it is now: the one with the same heading and
// the same words, when both before and after there is exactly one such scene, so a rename never lands on a scene it was not made on.
func movedScenes(c *ops.Ctx, tmp *Draft, before DraftOptions, e DraftEdits, now *PreviewOut) map[string]string {
	gone := map[string]bool{}
	have := map[string]bool{}
	for _, n := range now.Nodes {
		have[n.Key] = true
	}
	for k := range e.Edits {
		if strings.Contains(k, "#") && !have[k] {
			gone[k] = true
		}
	}
	if len(gone) == 0 {
		return nil
	}
	was := *tmp
	pv, err := cutWithSplits(c, &was, before, e)
	if err != nil || pv == nil {
		return nil
	}
	type tw struct {
		t string
		w int
	}
	count := func(p *PreviewOut) (map[tw][]string, map[string]tw) {
		by, of := map[tw][]string{}, map[string]tw{}
		for _, n := range p.Nodes {
			if strings.Contains(n.Key, "#") {
				x := tw{n.Title, n.Words}
				by[x] = append(by[x], n.Key)
				of[n.Key] = x
			}
		}
		return by, of
	}
	byOld, ofOld := count(pv)
	byNew, _ := count(now)
	out := map[string]string{}
	for k := range gone {
		x, ok := ofOld[k]
		if !ok || len(byOld[x]) != 1 || len(byNew[x]) != 1 {
			continue
		}
		out[k] = byNew[x][0]
	}
	return out
}
