package editiondiff

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// MaxSuggestHunks is how many differences one edition.suggest makes suggestions of: each is a suggestion of its own,
// and each tells the project's owner and editors, so a whole book's worth is not made in one call.
const MaxSuggestHunks = 20

// DiffIn compares the project as it stands with one of its editions.
type DiffIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	EditionID string `path:"edition_id" json:"-" desc:"Edition id (edn_...): the text the project is compared against."`
	Chapter   string `query:"chapter" desc:"A step's key (from a previous answer): returns that step with its differences (hunks), one by one, each with the words that changed. The hunks carry the edition's text: this needs the editor role and, for a key, the write scope (as edition.download does). Without it the steps carry no hunks and readers can call it."`
}

func (in DiffIn) ProjectRef() string { return in.ProjectID }

// CompareIn compares two editions.
type CompareIn struct {
	ProjectID   string `path:"id" json:"-" desc:"Project id of the first edition."`
	EditionID   string `path:"edition_id" json:"-" desc:"The first (earlier) edition."`
	WithProject string `query:"with_project" desc:"Project id of the second edition; default: the same project. You need to be able to open it."`
	WithEdition string `query:"with_edition" desc:"The second (later) edition."`
	Source      string `query:"source" enum:"auto,file,record" desc:"What the chapters are read from: file (each edition's source file, read again as an import would, with the same sections and pages on both), record (the text each import recorded), auto (default: files when both are kept, else records; the answer says which)."`
	Sections    string `query:"sections" enum:"auto,toc,files,headings" desc:"With files: how the books are cut into chapters (as epub_sections of an import). Default auto."`
	Pages       string `query:"pages" enum:"skip,keep" desc:"With files: skip (default) leaves out the cover and title page."`
	Chapter     string `query:"chapter" desc:"A step's key: returns that step with its differences."`
}

func (in CompareIn) ProjectRef() string { return in.ProjectID }

// SuggestIn turns differences of a chapter into suggestions.
type SuggestIn struct {
	ProjectID    string   `path:"id" json:"-" desc:"Project id."`
	EditionID    string   `path:"edition_id" json:"-" desc:"The edition the project is compared against."`
	Chapter      string   `json:"chapter" desc:"The step's key (edition.diff). It must be a chapter paired one to one with a document, and edited."`
	BaseRevision int      `json:"base_revision" desc:"The document's revision you read; it must be current (409 with current_revision otherwise)."`
	Hunks        []string `json:"hunks,omitempty" desc:"Which differences (their ids from the step's hunks): each becomes a suggestion that puts the edition's text back (or, for a passage the edition does not have, removes it). Empty: all of them that can be offered, up to 20."`
	Note         string   `json:"note,omitempty" desc:"Added to the suggestions' note, up to 2,000 characters."`
}

func (in SuggestIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *SuggestIn) Validate() error {
	if in.Chapter == "" || len(in.Chapter) > 200 {
		return ops.Invalid("chapter", "Name the step (its key from edition.diff).")
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you read.")
	}
	if len(in.Hunks) > MaxSuggestHunks {
		return ops.Invalid("hunks", fmt.Sprintf("At most %d differences at a time: each is a suggestion of its own.", MaxSuggestHunks))
	}
	if utf8.RuneCountInString(in.Note) > 1500 {
		return ops.Invalid("note", "A note is limited to 1,500 characters here.")
	}
	return nil
}

// SuggestOut says what was made and what could not be.
type SuggestOut struct {
	Created []Created `json:"created"`
	Skipped []Skipped `json:"skipped"`
	// More counts the differences that could be offered and were not made because 20 is the most a call makes (with no
	// hunks named): call again.
	More int `json:"more,omitempty" desc:"Differences that could be offered and were not, because a call makes at most 20: call again."`
	// Stopped says why the call ended before the last difference: what it made stays made (each suggestion is its own
	// write), and the rest is in skipped.
	Stopped string `json:"stopped,omitempty" desc:"Why the call ended early (the project's open-suggestion cap, a save that came in); what was made stays made, and the rest is in skipped."`
}

// Created is a suggestion made from a difference.
type Created struct {
	Hunk         string `json:"hunk"`
	SuggestionID string `json:"suggestion_id"`
}

// Skipped is a difference that could not be offered, and why.
type Skipped struct {
	Hunk   string `json:"hunk"`
	Reason string `json:"reason"`
}

// TakeIn puts a chapter back as it was in the edition.
type TakeIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	EditionID    string `path:"edition_id" json:"-" desc:"The edition the project is compared against."`
	Chapter      string `json:"chapter" desc:"The step's key (edition.diff)."`
	BaseRevision int    `json:"base_revision" desc:"The document's revision you read; it must be current."`
	// ConfirmGuess says the person has seen that the chapter was paired with the edition's by a guess (the step's
	// match.guess, offer.confirm), and wants it taken all the same.
	ConfirmGuess bool `json:"confirm_guess,omitempty" desc:"Needed when the step's offer says confirm: the chapter was paired with the edition's by a guess (title or similarity, or a merged step with one guessed member), not from what the import recorded."`
}

func (in TakeIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *TakeIn) Validate() error {
	if in.Chapter == "" || len(in.Chapter) > 200 {
		return ops.Invalid("chapter", "Name the step (its key from edition.diff).")
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you read.")
	}
	return nil
}

// TakeOut is the chapter after it was put back.
type TakeOut struct {
	DocumentID string `json:"document_id"`
	RevisionID string `json:"revision_id,omitempty" desc:"The version written. The text before it was kept first, so restoring the version before this one undoes it."`
	Revision   int    `json:"revision" desc:"The document's revision now."`
	Unchanged  bool   `json:"unchanged" desc:"The document already had exactly the edition's text."`
}

// Register adds the edition diff ops.
func Register(r *ops.Registry) {
	l := docs.LeasesOf(r.Env())
	ops.Register(r, ops.Op{Name: "edition.diff", Method: "GET", Path: "/api/v1/projects/{id}/editions/{edition_id}/diff", Auth: ops.RequireProject(ops.ProjectReader),
		Desc: "Compare the project as it stands with one of its editions, chapter by chapter, so a reviewer can step through what changed in a revised book. " +
			"Each chapter of the edition is paired with its document (by what the import recorded, then by source path, then by title, then by the text they share), so renamed, moved, split, merged, added and removed chapters are found. " +
			"Each step has a status (unchanged, edited, moved, split, merged, added, removed), word counts, words added and removed (never a net figure), and how it was paired: a method and a score, and guess=true for a pairing made by title or similarity, which is a heuristic and not a fact. " +
			"Pass chapter (a step's key) to get that step's differences one at a time, paragraph by paragraph and word by word inside a reworded paragraph (the edition's text: editors, and a key with the write scope). A chapter that was left out of the project at import is shown only to editors. Two texts that share too little say too_different and are not diffed. Nothing is changed."},
		func(c *ops.Ctx, in DiffIn) (Result, error) { return diffOp(c, in) })
	ops.Register(r, ops.Op{Name: "edition.compare", Method: "GET", Path: "/api/v1/projects/{id}/editions/{edition_id}/compare", Auth: ops.RequireProject(ops.ProjectEditor), Scope: ops.ScopeWrite,
		Desc: "Compare two editions of a book (\"what changed in the second edition\"): two editions of this project, or this project's edition with one of another project you can open. " +
			"It reads the two source files as an import would, without importing either into a project (or the text each import recorded, when a file is not kept), and returns the same steps as edition.diff: the first edition is the older text, the second the newer. It shows the text of both editions, so it is for editors of both projects and, for a key, the write scope (as edition.download); a person runs one at a time. Nothing is imported or changed."},
		func(c *ops.Ctx, in CompareIn) (Result, error) { return compareOp(c, in) })
	ops.Register(r, ops.Op{Name: "edition.suggest", Method: "POST", Path: "/api/v1/projects/{id}/editions/{edition_id}/suggest", Status: 201, Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "Turn differences between a chapter and its edition into suggestions on the document, one suggestion for each difference, to be accepted or rejected one at a time in the review queue (\"restore this passage from the source edition\"). " +
			"The same caps and refusals as suggestion.create: a difference whose text carries a link, a picture, a heading or a marker the document would not accept is skipped with its reason, and so is every difference of a chapter that was split or merged. base_revision must be current. The text is not changed until an editor accepts a suggestion; each one tells the project's owner and editors."},
		func(c *ops.Ctx, in SuggestIn) (SuggestOut, error) { return suggestOp(c, in) })
	ops.Register(r, ops.Op{Name: "edition.take", Method: "POST", Path: "/api/v1/projects/{id}/editions/{edition_id}/take", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "Take the edition's text for a whole chapter (\"keep mine\" is not calling this): the document's text becomes what the edition had, as a new version of the document. " +
			"The text it replaces is kept first, with reason snapshot, and the new version has reason restore, so the history undoes it; no words are credited to the writer's goals. Only a chapter paired one to one (or merged into one document) is taken: a chapter that was split into several documents, added or removed is not. base_revision must be current. Use dry_run to see it first."},
		func(c *ops.Ctx, in TakeIn) (TakeOut, error) { return takeOp(c, in, l) })
}

// testLimits lets a test shrink the limits the ops use (export_test.go).
var testLimits func(*Limits)

func limitsFor(project bool) Limits {
	l := Defaults()
	if project {
		l.Join = imports.MergeBodies
	}
	if testLimits != nil {
		testLimits(&l)
	}
	return l
}

// roleOn is the caller's role on a project, with no gate (the registry has gated the project it authorized).
func roleOn(c *ops.Ctx, projectID string) (string, error) {
	roleOf := c.Env.ProjectMember
	if roleOf == nil {
		roleOf = c.Env.ProjectRole
	}
	if roleOf == nil {
		return "", ops.NotFound("project")
	}
	role, ok := roleOf(c, c.P.UserID, projectID)
	if !ok {
		return "", ops.NotFound("project")
	}
	return role, nil
}

// member is roleOn and then the passphrase gate, for the second project of a comparison, which the registry did not
// authorize: nothing for a stranger (404, before the gate, so a locked project's existence is not told), the gate's
// refusal for a project that is locked for the caller (403 project_locked, or keys refused). The gate is asked once
// for each project, since a key's uses are counted for each call.
func member(c *ops.Ctx, projectID string) (string, error) {
	role, err := roleOn(c, projectID)
	if err != nil {
		return "", err
	}
	if c.Env.ProjectGate != nil {
		if err := c.Env.ProjectGate(c, c.P, projectID); err != nil {
			return "", err
		}
	}
	return role, nil
}

// textAllowed refuses unless the caller may be shown the text of an edition: the project's editors (or owner), and,
// for an API key, the write scope: the rule of edition.download. An edition's text is the book as it was received,
// which includes what the writer left out of the project at import and what the project no longer holds; the
// project's own documents and their history do not.
func textAllowed(c *ops.Ctx, role string) error {
	if role != ops.ProjectEditor && role != ops.ProjectOwner {
		return ops.Forbidden("the text of an edition is for the project's editors: readers and reviewers can see the chapters that are in the project and their counts")
	}
	if c.P.Scope != "" && c.P.Scope != ops.ScopeWrite && c.P.Scope != ops.ScopeAdmin {
		return ops.Forbidden("the text of an edition needs a key with the write scope, as downloading the edition does")
	}
	return nil
}

func sideOf(e editions.Edition, projectName, source string) Side {
	return Side{Kind: "edition", ProjectID: e.ProjectID, Project: projectName, EditionID: e.ID, Label: e.Label, SHA256: e.SHA256, Chapters: e.Chapters, Source: source}
}

func projectName(ctx context.Context, q editions.Querier, id string) string {
	var n string
	_ = q.QueryRowContext(ctx, `SELECT title FROM projects WHERE id = ?`, id).Scan(&n)
	return n
}

func tooLarge(err error) error {
	if errors.Is(err, ErrTooLarge) {
		return &ops.Error{Code: ops.CodeInvalid, Message: "That book is too large to compare: " + strings.TrimPrefix(err.Error(), "editiondiff: ")}
	}
	return err
}

// projectCompare loads and compares the project with its edition, keeping what the write ops need.
type projectCompare struct {
	Res     *Result
	Edition editions.Edition
	Head    sideData
	Base    sideData
	// baseByOrd is every chapter of the edition, whoever is asking: only an editor's call has them in the comparison.
	baseByOrd map[int]Unit
}

func loadProjectCompare(c *ops.Ctx, projectID, editionID, detail string, full bool) (*projectCompare, error) {
	q := c.DB().Read()
	e, err := editions.Get(c, q, projectID, editionID)
	if errors.Is(err, editions.ErrNotFound) {
		return nil, ops.NotFound("edition")
	}
	if err != nil {
		return nil, ops.Internal(err)
	}
	lim := limitsFor(true)
	base, err := loadEditionRecord(c, q, e, lim)
	if err != nil {
		return nil, tooLarge(wrapInternal(err))
	}
	mapped := map[string]bool{}
	for _, u := range base.Units {
		if u.Mapped != "" {
			mapped[u.Mapped] = true
		}
	}

	head, err := loadProject(c, q, projectID, e.SourceID, mapped, lim)
	if err != nil {
		return nil, tooLarge(wrapInternal(err))
	}
	if err := toProjectPictures(c, q, projectID, base.Units); err != nil {
		return nil, ops.Internal(err)
	}
	baseByOrd := map[int]Unit{}
	for _, u := range base.Units {
		baseByOrd[u.Ord] = u
	}
	if !full {
		// A chapter the writer left out at import (no document) is not in the project: below the editors its text, its
		// title and its existence stay out of what is returned, and it takes no part in the pairing.
		base.Units = withoutLeftOut(base.Units)
	}
	res, err := Compare(c, Input{Base: base.Units, Head: head.Units, ProjectMode: true, Detail: detail, Limits: lim})
	if err != nil {
		return nil, tooLarge(wrapInternal(err))
	}
	name := projectName(c, q, projectID)
	res.Base = sideOf(e, name, "record")
	res.Base.Words = res.Summary.BaseWords
	res.Head = Side{Kind: "project", ProjectID: projectID, Project: name, Chapters: len(head.Units), Words: res.Summary.HeadWords, Source: "project"}
	res.Base.Chapters = len(base.Units)
	return &projectCompare{Res: res, Edition: e, Head: head, Base: base, baseByOrd: baseByOrd}, nil
}

// withoutLeftOut drops the chapters that became no document, and gives the rest the parents they have among them.
func withoutLeftOut(us []Unit) []Unit {
	idx := make([]int, len(us))
	out := make([]Unit, 0, len(us))
	for i, u := range us {
		if u.Mapped == "" {
			idx[i] = -1
			continue
		}
		idx[i] = len(out)
		out = append(out, u)
	}
	for i := range out {
		if p := out[i].Parent; p >= 0 && p < len(idx) {
			out[i].Parent = idx[p]
		} else {
			out[i].Parent = -1
		}
	}
	return out
}

func wrapInternal(err error) error {
	var oe *ops.Error
	if errors.As(err, &oe) || errors.Is(err, ErrTooLarge) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ops.Internal(err)
}

func diffOp(c *ops.Ctx, in DiffIn) (Result, error) {
	role, err := roleOn(c, in.ProjectID)
	if err != nil {
		return Result{}, err
	}
	full := textAllowed(c, role) == nil
	if in.Chapter != "" && !full { // the differences carry the edition's text
		return Result{}, textAllowed(c, role)
	}
	pc, err := loadProjectCompare(c, in.ProjectID, in.EditionID, in.Chapter, full)
	if err != nil {
		return Result{}, err
	}
	if in.Chapter != "" && !hasStep(pc.Res, in.Chapter) {
		return Result{}, ops.NotFound("chapter")
	}
	pc.decorate(c, in.Chapter)
	return *pc.Res, nil
}

func hasStep(r *Result, key string) bool {
	for i := range r.Steps {
		if r.Steps[i].Key == key {
			return true
		}
	}
	return false
}

// replacement is the text a difference puts into the document when it is offered, and where.
type replacement struct {
	start, end int // bytes of the document's body
	text       string
}

// replacementFor is what offering a hunk means for the document's text: the paragraphs the edition has go where the
// document's paragraphs are (a passage only the edition has goes in after the paragraph before it). why is not empty
// when it cannot be offered. The review rules keep a document's structure, so a suggestion can reword inside a
// paragraph, or add paragraphs, but never take a whole paragraph out: a passage only the project has is not offered.
func replacementFor(h Hunk, body string) (rp replacement, why string) {
	base := strings.Trim(h.Base, "\n")
	switch h.Kind {
	case HunkAdded:
		return rp, "A suggestion cannot take a whole paragraph out (the review rules keep the document's structure): take the chapter, or edit it."
	case HunkRemoved:
		pos := h.HeadStart
		if pos > len(body) {
			pos = len(body)
		}
		for pos > 0 && body[pos-1] == '\n' {
			pos--
		}
		rp = replacement{start: pos, end: pos, text: "\n\n" + base}
		if pos == 0 {
			rp.text = base + "\n\n"
		}
		return rp, ""
	}
	head := h.Head
	start := h.HeadStart + len(head) - len(strings.TrimLeft(head, "\n"))
	end := h.HeadEnd - (len(head) - len(strings.TrimRight(head, "\n")))
	if end > len(body) {
		end = len(body)
	}
	if start > end || body[start:end] != strings.Trim(head, "\n") {
		return rp, "The difference could not be placed in the document's text."
	}
	return replacement{start: start, end: end, text: base}, ""
}

// decorate adds what a screen needs beside the steps of a comparison with the project: what each can be offered, and
// for the step asked for, why a difference cannot.
func (pc *projectCompare) decorate(c *ops.Ctx, detail string) {
	for i := range pc.Res.Steps {
		s := &pc.Res.Steps[i]
		s.Offer = pc.offerFor(s)
		if len(s.Hunks) == 0 {
			continue
		}
		var info *docInfo
		if len(s.Head) == 1 {
			info = pc.Head.Docs[s.Head[0].DocumentID]
		}
		for j := range s.Hunks {
			h := &s.Hunks[j]
			switch {
			case !s.Offer.Suggest || info == nil:
				h.Offer = s.Offer.Reason
			default:
				rp, why := replacementFor(*h, info.Body)
				if why != "" {
					h.Offer = why
					break
				}
				if err := docs.CheckInsertion(info.Body[rp.start:rp.end], rp.text); err != nil {
					h.Offer = refusal(err)
				} else if strings.Contains(rp.text, "scriobh-picture:") {
					h.Offer = "The passage has a picture the project does not hold."
				}
			}
		}
	}
}

func refusal(err error) string {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe.Message
	}
	return err.Error()
}

func (pc *projectCompare) offerFor(s *Step) *Offer {
	no := func(why string) *Offer { return &Offer{Reason: why} }
	switch s.Status {
	case StatusAdded:
		return no("The edition does not have this chapter.")
	case StatusRemoved:
		return no("This chapter is not in the project any more; import the edition's chapter to bring it back.")
	case StatusUnchanged, StatusMoved:
		return no("The text is the same as the edition's.")
	case StatusSplit:
		return no("The edition's chapter is now several documents; compare them one by one, or edit them.")
	}
	if len(s.Head) != 1 {
		return no("The chapter is not one document.")
	}
	info := pc.Head.Docs[s.Head[0].DocumentID]
	if info == nil {
		return no("The document is not in the project.")
	}
	if info.Composite {
		return no("The import made this chapter into scenes: the text is spread over several documents.")
	}
	o := &Offer{Take: true, Confirm: s.Match != nil && s.Match.Guess}
	leftOut := false
	for _, b := range s.Base {
		if u, ok := pc.baseByOrd[b.Ord]; ok && u.Mapped == "" {
			leftOut = true
		}
	}
	if s.Status == StatusEdited && !leftOut && len(s.Base) == 1 && s.Compared != ComparedNone && s.Compared != ComparedNot && s.Compared != ComparedSkipped {
		o.Suggest = true
	}
	if leftOut {
		o.Reason = "This chapter of the edition was left out of the project at import: its text is not offered as suggestions, which reviewers can read. It can be taken whole, by an editor."
	} else if s.Compared == ComparedNot || s.Compared == ComparedSkipped {
		o.Reason = "The chapter was not compared word by word, so its differences cannot be offered one at a time; it can be taken whole."
	} else if !o.Suggest {
		o.Reason = "Several chapters were merged into this document: it can be taken whole, not suggested."
	}
	return o
}

// ---- compare ------------------------------------------------------------------------------------------------------

func compareOp(c *ops.Ctx, in CompareIn) (Result, error) {
	if in.WithEdition == "" {
		return Result{}, ops.Invalid("with_edition", "Name the second edition.")
	}
	second := in.WithProject
	if second == "" {
		second = in.ProjectID
	}
	if in.Source != "" && in.Source != "auto" && in.Source != "file" && in.Source != "record" {
		return Result{}, ops.Invalid("source", "Use auto, file or record.")
	}
	q := c.DB().Read()
	// The registry authorized and gated the first project; the second is the caller's to open too, and its gate is
	// asked here, once. A comparison reads two books and shows their text: it is for editors of both projects, and
	// for a key with the write scope, as downloading an edition is.
	role1, err := roleOn(c, in.ProjectID)
	if err != nil {
		return Result{}, err
	}
	role2 := role1
	if second != in.ProjectID {
		if c.P.ProjectID != "" || c.P.CompendiumID != "" {
			return Result{}, ops.NotFound("edition")
		}
		if role2, err = member(c, second); err != nil {
			if oe := ops.AsError(err); oe != nil && oe.Code == ops.CodeNotFound {
				return Result{}, ops.NotFound("edition")
			}
			return Result{}, err
		}
	}
	for _, r := range []string{role1, role2} {
		if err := textAllowed(c, r); err != nil {
			return Result{}, err
		}
	}
	release, err := acquireCompare(c.P.UserID)
	if err != nil {
		return Result{}, err
	}
	defer release()
	e1, err := editions.Get(c, q, in.ProjectID, in.EditionID)
	if err != nil {
		if errors.Is(err, editions.ErrNotFound) {
			return Result{}, ops.NotFound("edition")
		}
		return Result{}, ops.Internal(err)
	}
	e2, err := editions.Get(c, q, second, in.WithEdition)
	if err != nil {
		if errors.Is(err, editions.ErrNotFound) {
			return Result{}, ops.NotFound("edition")
		}
		return Result{}, ops.Internal(err)
	}
	lim := limitsFor(false)
	var s1, s2 sideData
	var notices []string
	try := in.Source != "record" && e1.Kind == editions.KindEPUB && e2.Kind == editions.KindEPUB
	if try {
		s1, err = loadEditionFile(c, c.Env, e1, in.Sections, in.Pages, lim)
		if err == nil {
			s2, err = loadEditionFile(c, c.Env, e2, in.Sections, in.Pages, lim)
		}
		if err != nil {
			var oe *ops.Error
			switch {
			case errors.Is(err, editions.ErrNoFile):
				if in.Source == "file" {
					return Result{}, ops.Conflict("An edition's source file is not kept on this server, so it cannot be read again; compare the recorded text (source=record).")
				}
				try = false
				notices = append(notices, "An edition's source file is not kept on this server: the text each import recorded was compared.")
			case errors.As(err, &oe):
				return Result{}, err
			default:
				return Result{}, tooLarge(wrapInternal(err))
			}
		}
	} else if in.Source == "file" {
		return Result{}, ops.Invalid("source", "Only an EPUB edition can be read from its file.")
	}
	if !try {
		if s1, err = loadEditionRecord(c, q, e1, lim); err != nil {
			return Result{}, tooLarge(wrapInternal(err))
		}
		if s2, err = loadEditionRecord(c, q, e2, lim); err != nil {
			return Result{}, tooLarge(wrapInternal(err))
		}
	}
	for i := range s1.Units {
		s1.Units[i].Mapped = ""
	}
	for i := range s2.Units {
		s2.Units[i].Mapped = ""
	}
	res, err := Compare(c, Input{Base: s1.Units, Head: s2.Units, Detail: in.Chapter, Limits: lim})
	if err != nil {
		return Result{}, tooLarge(wrapInternal(err))
	}
	if in.Chapter != "" && !hasStep(res, in.Chapter) {
		return Result{}, ops.NotFound("chapter")
	}
	res.Base = sideOf(e1, projectName(c, q, e1.ProjectID), s1.Source)
	res.Base.Words = res.Summary.BaseWords
	res.Base.Chapters = len(s1.Units)
	res.Head = sideOf(e2, projectName(c, q, e2.ProjectID), s2.Source)
	res.Head.Words = res.Summary.HeadWords
	res.Head.Chapters = len(s2.Units)
	res.Notices = append(notices, res.Notices...)
	return *res, nil
}

// ---- suggest and take ---------------------------------------------------------------------------------------------

// stepOf finds the step named by key.
func stepOf(r *Result, key string) (*Step, bool) {
	for i := range r.Steps {
		if r.Steps[i].Key == key {
			return &r.Steps[i], true
		}
	}
	return nil, false
}

// ---- limits on the people who offer -------------------------------------------------------------------------------

const (
	// suggestCalls is how many times a person may call edition.suggest for one project in suggestWindow. Each call
	// makes up to 20 suggestions and tells the project's owner and editors once; a loop (an agent, a retry) must not
	// fill the open-suggestion cap or push real notifications out of a person's list.
	suggestCalls  = 10
	suggestWindow = 10 * time.Minute
)

var (
	rateMu    sync.Mutex
	rateCalls = map[string][]time.Time{} // person|project -> the times of the calls in the window (in this process)
	// now is the clock of the rate limit (a test moves it).
	now = time.Now
)

// allowSuggest counts a call and says whether it may go on; when not, how long to wait.
func allowSuggest(user, project string) (bool, time.Duration) {
	rateMu.Lock()
	defer rateMu.Unlock()
	t := now()
	k := user + "|" + project
	kept := rateCalls[k][:0]
	for _, x := range rateCalls[k] {
		if t.Sub(x) < suggestWindow {
			kept = append(kept, x)
		}
	}
	if len(kept) >= suggestCalls {
		rateCalls[k] = kept
		return false, suggestWindow - t.Sub(kept[0])
	}
	rateCalls[k] = append(kept, t)
	if len(rateCalls) > 10000 { // a long-running server: forget the people whose window has passed
		for k2, v := range rateCalls {
			if len(v) == 0 || t.Sub(v[len(v)-1]) >= suggestWindow {
				delete(rateCalls, k2)
			}
		}
	}
	return true, 0
}

var (
	compareMu sync.Mutex
	comparing = map[string]bool{}
)

// acquireCompare lets a person run one comparison at a time: it reads two books, which is heavy, and the import's
// own limiter (ReadEpub takes a place in it for each book) is shared by everyone's imports.
func acquireCompare(user string) (func(), error) {
	compareMu.Lock()
	defer compareMu.Unlock()
	if comparing[user] {
		return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "Another comparison of yours is still running. Wait for it to finish.", RetryAfterSeconds: 5}
	}
	comparing[user] = true
	return func() { compareMu.Lock(); delete(comparing, user); compareMu.Unlock() }, nil
}

// pendingChange is the part of a stored suggestion the duplicate guard compares.
type pendingChange struct {
	Kind     string `json:"kind"`
	U1       int    `json:"u1"`
	U2       int    `json:"u2"`
	Original string `json:"original"`
	Text     string `json:"text"`
}

// alreadyOffered says an open suggestion on the document makes this one change, at the same place, to the same
// text: offering it again would only add a second decision for the same difference.
func alreadyOffered(c *ops.Ctx, projectID, docID string, u1, u2 int, original, text string) (bool, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT ops_json FROM suggestions WHERE project_id = ? AND document_id = ? AND status = 'open'`, projectID, docID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var chs []pendingChange
		if json.Unmarshal([]byte(raw), &chs) != nil || len(chs) != 1 {
			continue
		}
		if ch := chs[0]; ch.U1 == u1 && ch.U2 == u2 && ch.Original == original && ch.Text == text {
			return true, nil
		}
	}
	return false, rows.Err()
}

func suggestOp(c *ops.Ctx, in SuggestIn) (SuggestOut, error) {
	if !c.DryRun() {
		if ok, wait := allowSuggest(c.P.UserID, in.ProjectID); !ok {
			secs := int(wait.Seconds()) + 1
			return SuggestOut{}, &ops.Error{Code: ops.CodeRateLimited, RetryAfterSeconds: secs,
				Message: fmt.Sprintf("You have offered differences from editions %d times in the last %d minutes. Wait a little, and offer more of them in one call (up to %d).", suggestCalls, int(suggestWindow.Minutes()), MaxSuggestHunks)}
		}
	}
	pc, err := loadProjectCompare(c, in.ProjectID, in.EditionID, in.Chapter, true)
	if err != nil {
		return SuggestOut{}, err
	}
	pc.decorate(c, in.Chapter)
	st, ok := stepOf(pc.Res, in.Chapter)
	if !ok {
		return SuggestOut{}, ops.NotFound("chapter")
	}
	if st.Offer == nil || !st.Offer.Suggest {
		why := "This chapter's differences cannot be offered as suggestions."
		if st.Offer != nil && st.Offer.Reason != "" {
			why = st.Offer.Reason
		}
		return SuggestOut{}, ops.Conflict(why)
	}
	info := pc.Head.Docs[st.Head[0].DocumentID]
	if info.Revision != in.BaseRevision {
		return SuggestOut{}, ops.ConflictWith("This document changed since you read it.", map[string]any{"reason": "revision", "current_revision": info.Revision})
	}
	want := map[string]bool{}
	for _, id := range in.Hunks {
		want[id] = true
	}
	known := map[string]bool{}
	for _, h := range st.Hunks {
		known[h.ID] = true
	}
	for id := range want {
		if !known[id] {
			return SuggestOut{}, ops.Invalid("hunks", "No difference "+id+" in this chapter (it changed since you read it?).")
		}
	}
	out := SuggestOut{Created: []Created{}, Skipped: []Skipped{}}
	ix := alts.NewIndex(info.Body)
	made := 0
	for _, h := range st.Hunks {
		if len(want) > 0 && !want[h.ID] {
			continue
		}
		if h.Offer != "" {
			out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: h.Offer})
			continue
		}
		if out.Stopped != "" {
			out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: "Not made: " + out.Stopped})
			continue
		}
		if len(want) == 0 && made >= MaxSuggestHunks {
			out.More++
			continue
		}
		rp, why := replacementFor(h, info.Body)
		if why != "" {
			out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: why})
			continue
		}
		u1, u2 := ix.Unit(rp.start), ix.Unit(rp.end)
		if dup, err := alreadyOffered(c, in.ProjectID, info.ID, u1, u2, info.Body[rp.start:rp.end], rp.text); err != nil {
			return SuggestOut{}, ops.Internal(err)
		} else if dup {
			out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: "Already suggested: an open suggestion makes this change."})
			continue
		}
		note := "From the comparison with the edition " + quote(pc.Edition.Label) + "."
		if h.Kind == HunkAdded {
			note = "A passage the edition " + quote(pc.Edition.Label) + " does not have."
		}
		if n := strings.TrimSpace(in.Note); n != "" {
			note += " " + n
		}
		item, err := review.ProposeQuiet(c, review.SuggestionCreateIn{ProjectID: in.ProjectID, DocumentID: info.ID, BaseRevision: in.BaseRevision, Note: note,
			Changes: []review.ChangeIn{{Start: &u1, End: &u2, Text: rp.text}}})
		if err != nil {
			var oe *ops.Error
			switch {
			case errors.As(err, &oe) && oe.Code == ops.CodeInvalid:
				out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: oe.Message})
			case len(out.Created) == 0:
				return SuggestOut{}, err // nothing was made, so nothing is lost by failing
			default:
				// each suggestion is committed on its own: what was made is reported, not dropped, so a retry cannot duplicate it
				out.Stopped = refusal(err)
				out.Skipped = append(out.Skipped, Skipped{Hunk: h.ID, Reason: "Not made: " + out.Stopped})
			}
			continue
		}
		made++
		out.Created = append(out.Created, Created{Hunk: h.ID, SuggestionID: item.Item.ID})
	}
	// the people who decide are told once for the call, not once for each suggestion
	if len(out.Created) > 0 {
		if err := review.NotifyProposed(c, in.ProjectID, info.ID, out.Created[0].SuggestionID); err != nil && c.Env.Log != nil {
			c.Env.Log.Warn("editiondiff: could not notify of suggestions", "err", err)
		}
	}
	return out, nil
}

// endsWithAltsComment says the text ends with a live alternatives comment: the line the document's own alternatives
// are kept in, which altstore would read as theirs. The words "scriobh:alts" inside a sentence, a code span or an
// escaped comment (how the import keeps a book's own) are text.
func endsWithAltsComment(text string) bool {
	return alts.Split(alts.NormalizeEOL(text)).HasLine
}

func quote(s string) string {
	if s == "" {
		return "it was imported from"
	}
	return "“" + s + "”"
}

func takeOp(c *ops.Ctx, in TakeIn, l *docs.Leases) (TakeOut, error) {
	pc, err := loadProjectCompare(c, in.ProjectID, in.EditionID, "", true)
	if err != nil {
		return TakeOut{}, err
	}
	pc.decorate(c, "")
	st, ok := stepOf(pc.Res, in.Chapter)
	if !ok {
		return TakeOut{}, ops.NotFound("chapter")
	}
	if st.Offer == nil || !st.Offer.Take {
		why := "This chapter cannot be taken from the edition."
		if st.Offer != nil && st.Offer.Reason != "" {
			why = st.Offer.Reason
		}
		return TakeOut{}, ops.Conflict(why)
	}
	if st.Offer.Confirm && !in.ConfirmGuess {
		why := "This chapter was paired with the edition's by a guess (" + st.Match.Method + ", " + fmt.Sprintf("%.2f", st.Match.Score) + ") and not from what the import recorded. Check the pairing, then send confirm_guess to take it."
		return TakeOut{}, ops.ConflictWith(why, map[string]any{"reason": "guess_unconfirmed", "method": st.Match.Method, "score": st.Match.Score})
	}
	info := pc.Head.Docs[st.Head[0].DocumentID]
	if info.Revision != in.BaseRevision {
		return TakeOut{}, ops.ConflictWith("This document changed since you read it.", map[string]any{"reason": "revision", "current_revision": info.Revision})
	}
	// the edition's text, as the comparison read it: the chapters of a merged step put together as the import did
	var text string
	for i, b := range st.Base {
		u := pc.baseByOrd[b.Ord]
		if i == 0 {
			text = u.Text
		} else {
			text = imports.MergeBodies(text, u.Text)
		}
	}
	if strings.Contains(text, "scriobh-picture:") {
		return TakeOut{}, ops.Conflict("The edition's chapter has a picture this project does not hold, so it cannot be put back here.")
	}
	if endsWithAltsComment(text) { // a live alternatives comment, which the importer never writes (it escapes a book's own)
		return TakeOut{}, ops.Conflict("The edition's text ends with an alternatives comment, which the document cannot take as text.")
	}
	var out TakeOut
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := docs.ReplaceBody(c, tx, l, docs.ReplaceBodyIn{DocID: info.ID, ProjectID: in.ProjectID, BaseRevision: in.BaseRevision, Text: text,
			Note: "Took the text of the edition " + quote(pc.Edition.Label)})
		if err != nil {
			return err
		}
		out = TakeOut{DocumentID: info.ID, RevisionID: r.RevisionID, Revision: r.Revision, Unchanged: r.Unchanged}
		if !r.Unchanged {
			c.Audit(ops.AuditEntry{Action: "edition.take", TargetType: "document", TargetID: info.ID,
				Detail: map[string]any{"edition_id": in.EditionID, "revision_id": r.RevisionID}})
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return TakeOut{}, err
	}
	return out, nil
}
