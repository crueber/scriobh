package imports

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// StageIn is import.stage: the query that goes with a file sent as the request's body.
type StageIn struct {
	Kind             string `query:"kind" enum:"docx,epub,pdf,text,zip,pandoc,file" desc:"What the body is: docx (a Word document), epub, pdf, text (a plain text file, UTF-8), zip (a zip of Markdown, a Scríobh export, an Obsidian vault, or a Scrivener project, WordPress or Ghost export), pandoc (a document Pandoc reads; give format) or file (a lone WordPress .xml or Ghost .json export)."`
	Name             string `query:"name" desc:"The name to show and to start the project's title from, up to 200 characters. Default: nothing."`
	Project          string `query:"project" ref:"project" desc:"Stage for adding to this project (you must be able to edit it) instead of for a new project."`
	Parent           string `query:"parent" desc:"With project: the folder of it to add into; empty for the end of the top level."`
	Origin           string `query:"origin" enum:"folder" desc:"folder: the zip is the Markdown of a folder chosen on the import page, which the review says in its own words."`
	Format           string `query:"format" enum:"odt,rtf,latex,mediawiki,org,textile,rst,fb2,typst,docbook" desc:"With kind pandoc: the document's format."`
	Layout           string `query:"layout" enum:"auto,obsidian,scrivener,wordpress,ghost" desc:"As for import.preview."`
	DocxHeadings     string `query:"docx_headings" enum:"auto,chapters,parts" desc:"As for import.preview."`
	DocxSplit        string `query:"docx_split" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none" desc:"As for import.preview."`
	DocxSplitPattern string `query:"docx_split_pattern" desc:"As for import.preview."`
	DocxSplitMatch   string `query:"docx_split_match" enum:"starts,regex" desc:"As for import.preview."`
	DocxSplitStyle   string `query:"docx_split_style" desc:"As for import.preview."`
	DocxSplitWords   int    `query:"docx_split_words" desc:"As for import.preview."`
	DocxChanges      string `query:"docx_changes" enum:"suggest,accept,reject" desc:"As for import.preview."`
	SplitScenes      string `query:"split_scenes" enum:"none,h2" desc:"As for import.preview."`
	EpubSections     string `query:"epub_sections" enum:"auto,toc,files,headings" desc:"As for import.preview."`
	EpubPages        string `query:"epub_pages" enum:"skip,keep" desc:"As for import.preview."`
	Pictures         string `query:"pictures" enum:"import,skip" desc:"As for import.preview."`
	ScrivResearch    string `query:"scrivener_research" enum:"notes,skip" desc:"As for import.preview."`
	ScrivNotes       string `query:"scrivener_notes" enum:"private,skip" desc:"As for import.preview."`
	ScrivSheets      string `query:"scrivener_sheets" enum:"notes,entities" desc:"As for import.preview."`
	WPStatus         string `query:"wp_status" enum:"published,all" desc:"As for import.preview."`
	WPTypes          string `query:"wp_types" enum:"posts,pages,both" desc:"As for import.preview."`
	GhostStatus      string `query:"ghost_status" enum:"published,all" desc:"As for import.preview."`
	GhostTypes       string `query:"ghost_types" enum:"posts,pages,both" desc:"As for import.preview."`
}

// StageOut says what was kept.
type StageOut struct {
	StageID  string `json:"stage_id" desc:"The draft's id: review it with import.draft.get and import.draft.update, then import.apply or import.append with this stage_id."`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Revision int    `json:"revision"`
	Existing bool   `json:"existing,omitempty" desc:"The same bytes from you for the same target were already waiting: this is that draft, not a second one."`
}

// stageMax is the most a staged file of a kind may be: the limits the page has always had (34 MiB for a Word file or a zip, 32 MiB for a book).
func stageMax(kind string) int64 {
	switch kind {
	case StageEpub, StagePdf:
		return editions.MaxFileBytes
	case StageText:
		return MaxText
	}
	return 34 << 20
}

// MaxStageBody is the largest body import.stage takes.
const MaxStageBody = 34<<20 + 1<<10

// sniff checks the first bytes of a body against what it says it is, in words a person can use.
func sniff(kind string, head []byte) error {
	zipHead := bytes.HasPrefix(head, []byte("PK\x03\x04"))
	switch kind {
	case StageDocx:
		if !zipHead {
			return invalidWhy("", "That is not a Word document (.docx). A .docx is a zip file inside; this one is not. A .doc file from an old Word must be saved again as .docx.", "not_docx")
		}
	case StageZip, StageFolder:
		if !zipHead {
			return invalidWhy("", "That is not a readable zip file.", "not_zip")
		}
	case StageEpub:
		if !zipHead {
			return invalidWhy("", "That is not an EPUB book: an .epub is a zip file inside, and this one is not.", "not_epub")
		}
	case StagePdf:
		if !bytes.HasPrefix(head, []byte("%PDF")) {
			return invalidWhy("", "That is not a PDF.", "not_pdf")
		}
	case StageText:
		if bytes.IndexByte(head, 0) >= 0 || (len(head) >= 2 && (head[0] == 0xFF && head[1] == 0xFE || head[0] == 0xFE && head[1] == 0xFF)) || bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("%PDF")) {
			return invalidWhy("", "That is not a plain text file (.txt, saved as UTF-8). A file saved as UTF-16, a Word document or a PDF is not text; save it as plain text and choose it again.", "not_text")
		}
	case StageFile:
		t := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
		if len(t) == 0 || (t[0] != '<' && t[0] != '{') {
			return invalidWhy("", "That is not a WordPress export (.xml) or a Ghost export (.json).", "not_export")
		}
	}
	return nil
}

var (
	stageMu     sync.Mutex
	stagingUser = map[string]bool{}
)

// takeStageSlot lets one person stream one file at a time; a second at once is told to wait.
func takeStageSlot(user string) (release func(), ok bool) {
	stageMu.Lock()
	defer stageMu.Unlock()
	if stagingUser[user] {
		return nil, false
	}
	stagingUser[user] = true
	return func() { stageMu.Lock(); delete(stagingUser, user); stageMu.Unlock() }, true
}

func (in StageIn) options() DraftOptions {
	o := DraftOptions{DocxHeadings: in.DocxHeadings, DocxChanges: in.DocxChanges, DocxSplit: in.DocxSplit, DocxStyle: cleanLabel(in.DocxSplitStyle), DocxPattern: cleanPattern(in.DocxSplitPattern), DocxMatch: matchOf(in.DocxSplitMatch), DocxWords: in.DocxSplitWords, SplitScenes: in.SplitScenes, EpubSections: in.EpubSections, EpubPages: in.EpubPages,
		Pictures: in.Pictures, ScrivResearch: in.ScrivResearch, ScrivNotes: in.ScrivNotes, ScrivSheets: in.ScrivSheets, WPStatus: in.WPStatus, WPTypes: in.WPTypes,
		GhostStatus: in.GhostStatus, GhostTypes: in.GhostTypes, PandocFormat: in.Format}
	if in.Layout != "" && in.Layout != "auto" {
		o.Layout = in.Layout
	}
	return o
}

// check validates the options the way a preview does, so a wrong one is refused at the door.
func (o DraftOptions) check() error {
	if err := checkChanges(o.DocxChanges); err != nil {
		return err
	}
	return checkSource(o.SplitScenes, o.Layout, o.DocxHeadings, o.conv())
}

func (o DraftOptions) conv() convOpts {
	return convOpts{scrivResearch: o.ScrivResearch, scrivNotes: o.ScrivNotes, wpStatus: o.WPStatus, wpTypes: o.WPTypes, ghostStatus: o.GhostStatus, ghostTypes: o.GhostTypes,
		pictures: o.Pictures, scrivSheets: o.ScrivSheets, pandocFmt: o.PandocFormat, epubSections: o.EpubSections, epubPages: o.EpubPages,
		docxSplit: o.DocxSplit, docxStyle: o.DocxStyle, docxWords: o.DocxWords, docxBreaks: breakList(o.DocxBreaks), docxPattern: o.DocxPattern, docxMatch: o.DocxMatch, docxTry: o.DocxTry, docxTryMatch: o.DocxTryMatch}
}

// canEditProject says whether the caller may add to a project: an owner or editor, and not in the trash. Anything else is a 404.
func canEditProject(c *ops.Ctx, projectID string) bool {
	if c.Env.ProjectRole == nil || projectID == "" {
		return false
	}
	role, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	if !ok || (role != ops.ProjectOwner && role != ops.ProjectEditor) {
		return false
	}
	return c.Env.ProjectTrashed == nil || !c.Env.ProjectTrashed(c, projectID)
}

func stage(c *ops.Ctx, lim StageLimits, in StageIn) (StageOut, error) {
	if c.DryRun() {
		return StageOut{}, ops.DryRunUnsupported("import.stage")
	}
	rb, ok := c.RawBody()
	if !ok {
		return StageOut{}, ops.Invalid("", "Send the file as the request body.")
	}
	kind := in.Kind
	switch kind {
	case StageDocx, StageEpub, StagePdf, StageText, StageZip, StagePandoc, StageFile:
	case "":
		return StageOut{}, ops.Invalid("kind", "Say what the file is with kind: docx, epub, pdf, text, zip, pandoc or file.")
	default:
		return StageOut{}, ops.Invalid("kind", "kind is docx, epub, pdf, text, zip, pandoc or file.")
	}
	if in.Origin != "" && (in.Origin != "folder" || kind != StageZip) {
		return StageOut{}, ops.Invalid("origin", "origin is folder, for a zip.")
	}
	if in.Origin == "folder" {
		kind = StageFolder
	}
	if kind == StagePandoc {
		if !pandoc.ValidImport(in.Format) {
			return StageOut{}, ops.Invalid("format", "format is one of "+strings.Join(pandoc.ImportFormats(), ", ")+".")
		}
	} else if in.Format != "" {
		return StageOut{}, ops.Invalid("format", "format is only for a pandoc document.")
	}
	opts := in.options()
	if err := opts.check(); err != nil {
		return StageOut{}, err
	}
	limit := stageMax(kind)
	if rb.Length > limit {
		return StageOut{}, ops.TooLarge()
	}
	if in.Project == "" && in.Parent != "" {
		return StageOut{}, ops.Invalid("parent", "parent needs project.")
	}
	if in.Project != "" {
		if !canEditProject(c, in.Project) {
			return StageOut{}, ops.NotFound("project")
		}
		if in.Parent != "" {
			var n int
			if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, in.Parent, in.Project).Scan(&n); err != nil {
				return StageOut{}, ops.Internal(err)
			}
			if n == 0 {
				return StageOut{}, ops.Invalid("parent", "That folder does not exist in this project.")
			}
		}
	}
	release, ok := takeStageSlot(c.P.UserID)
	if !ok {
		return StageOut{}, &ops.Error{Code: ops.CodeRateLimited, Message: "You are already sending a file. Wait for it to finish.", RetryAfterSeconds: 5}
	}
	defer release()
	dataDir := c.Env.DataDir
	// The open drafts, the room left in this person's share and the instance's, and the free disk are looked at before a byte is written: the
	// file is cut at the room there is, and a file that would not fit is refused as such. reserve checks the same again in the transaction
	// that inserts the row, so two requests cannot both fit where one does.
	room, over, qerr := quotaRoom(c, c.DB(), lim, c.P.UserID)
	if qerr != nil {
		return StageOut{}, qerr
	}
	want := max(rb.Length, 0)
	if want > room {
		return StageOut{}, over
	}
	if !enoughDisk(c.Env.DataDir, max(want, min(limit, room))) {
		return StageOut{}, conflictWhy("The disk this server keeps imports on is nearly full. Nothing was kept.", "disk_full", nil)
	}
	id := db.NewID(idPrefix)
	br := bufio.NewReaderSize(rb.R, 4096)
	head, _ := br.Peek(8)
	if err := sniff(kind, head); err != nil {
		return StageOut{}, err
	}
	n, sum, err := writeStage(dataDir, id, br, min(limit, room))
	if err != nil {
		if code := ops.AsError(err).Code; code == ops.CodeTooLarge && room < limit {
			return StageOut{}, over
		}
		return StageOut{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(StageDir(dataDir, id))
		}
	}()
	switch kind {
	case StageDocx, StageZip, StageFolder:
		// the cheap structural check a preview would make anyway, so that what is not a readable zip is refused here
		b, rerr := os.ReadFile(sourcePath(dataDir, id))
		if rerr != nil {
			return StageOut{}, ops.Internal(rerr)
		}
		limit := maxZipEntries
		if kind == StageZip {
			limit = maxScrivEntries
		}
		if zerr := checkZipDirectoryN(b, limit); zerr != nil {
			return StageOut{}, invalidWhy("", zerr.Error(), "bad_zip")
		}
	}
	if kind == StageText {
		// the whole file, not only its first bytes (the sniff): a NUL anywhere is not text, and no draft is kept for it
		b, rerr := os.ReadFile(sourcePath(dataDir, id))
		if rerr != nil {
			return StageOut{}, ops.Internal(rerr)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return StageOut{}, invalidWhy("", "That is not a plain text file (.txt, saved as UTF-8). A file saved as UTF-16, a Word document or a PDF is not text; save it as plain text and choose it again.", "not_text")
		}
	}
	name := cleanLabel(in.Name)
	now := db.Now()
	dr := &Draft{ID: id, OwnerID: c.P.UserID, SourceKind: SourceUpload, Kind: kind, Name: name, TargetProject: in.Project, TargetParent: in.Parent,
		Bytes: n, SHA256: sum, Options: opts, Status: DraftOpen, CreatedAt: now, UpdatedAt: now, Revision: 1}
	got, err := reserve(c, c.DB(), lim, dr)
	if err != nil {
		return StageOut{}, err
	}
	keep = got.ID == id
	if got.ID == id {
		c.Audit(ops.AuditEntry{Action: "import.stage", TargetType: "import", TargetID: id, Detail: map[string]any{"kind": kind, "bytes": n}})
	}
	return StageOut{StageID: got.ID, Kind: got.Kind, Name: got.Name, Bytes: got.Bytes, SHA256: got.SHA256, Revision: got.Revision, Existing: got.ID != id}, nil
}

// unsentName is the member of a folder form's zip that lists what the browser did not send (images, hidden files), by name
// and size, so the review can say so as it always has.
const unsentName = ".scriobh-unsent.json"

// folderFiles reads a folder form's zip: its files, with the ones that were not sent listed by name and size, none of them
// under a wrapper folder.
func folderFiles(zipBytes []byte) ([]File, error) {
	fs, err := ReadZip(zipBytes)
	if err != nil {
		return nil, err
	}
	out := fs[:0]
	var unsent []File
	for _, f := range fs {
		if f.Path == unsentName {
			var l []File
			if json.Unmarshal(f.bytesOf(), &l) == nil {
				unsent = l
			}
			continue
		}
		out = append(out, f)
	}
	for _, u := range unsent {
		out = append(out, File{Path: u.Path, Size: u.Size})
	}
	return out, nil
}

// matchOf is how a pattern is read as the draft keeps it: starts is the default and is kept as nothing, so a stage that says it does not look
// like a change of options.
func matchOf(v string) string {
	if v == MatchStarts {
		return ""
	}
	return v
}
