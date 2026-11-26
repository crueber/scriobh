package server

import (
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
)

// Fixtures of the import review: a Word file with tracked changes, an EPUB with its book panel and an open row, a PDF, a long tree, a
// Git fetch under way, a failure and a finished import. Each is a view the server would build, drawn through the real templates.

func importReviewView(kind, layout string) *impView {
	v := &impView{Lang: "en", CSRF: "tok_writer", ID: "imd_01TESTTESTTESTTESTTESTTES0", Rev: 3, Token: "imp_abc", Kind: kind, Status: "open",
		PageURL: "/import/d/imd_01TESTTESTTESTTESTTESTTES0", Act: "/ui/import/d/imd_01TESTTESTTESTTESTTESTTES0", Layout: layout, Label: "A Word document",
		Docs: 3, Words: 4210, Title: "The Salt Road", ProjKind: "novel", Author: "Máire Nolan", ProjectKinds: projects.KindOptions(), CanUndo: true, Live: "Moved The Mill above The Road: now 1 of 3."}
	v.Rows = []impRow{
		{Key: "01-the-road.md", RID: "r1", Title: "The Road", Kind: "chapter", KindName: "Chapter", Words: 1200, First: true, Siblings: 3, Level: 1},
		{Key: "02-the-mill.md", RID: "r2", Title: "The Mill <b>", Kind: "chapter", KindName: "Chapter", Words: 1500, Siblings: 3, Index: 1, Level: 1, Headings: 2, Open: true, Warnings: []string{"A comment in a heading was counted, not imported."},
			Panel: &impPanel{BookLike: true, CanNest: true, CanJoin: true, Ends: true, Targets: []impTarget{{"01-the-road.md", "The Road"}, {"03-snow.md", "Snow (in The Road)"}}}},
		{Key: "03-snow.md", RID: "r3", Title: "Snow", Kind: "back_matter", KindName: "Back matter", Words: 1510, Last: true, Siblings: 3, Index: 2, Depth: 4, Level: 5},
	}
	v.Shown, v.RowsAll = 3, 3
	v.Left = []impLeft{{Key: "00-front.md", RID: "r0", Text: "Front Matter (joined into The Road)"}}
	v.Skipped = impSkipped{Total: 2, Reasons: []impKV{{"not a Markdown file", "2"}}, List: []imports.Skipped{{Path: "cover.png", Reason: "not a Markdown file"}}}
	v.Hidden = 1
	v.Warnings = []string{"This is a note.", "And another."}
	v.Folded = true
	v.MapN = true
	v.Report = []impKV{{"Comments", "3"}, {"Images", "1"}}
	v.Research = []impResearch{{Key: "research/a", RID: "r5", Title: "Notes", Meta: "Folder"}, {Key: "research/b", RID: "r6", Title: "Tides", Meta: "40 words · place sheet", Depth: 1}}
	v.ResN = 2
	v.Word = &impWord{Say: "The chapters hold the original text.", Rule: "Where two reviewers changed the same sentence...", Items: []string{"Suggestions to be written: 4.", "Word comments to be written as comments: 3."}, Why: []string{"In headings: 1."}, Authors: []string{"Wren (from a Word file), changes: 4"}}
	v.Opts = impOpts{Show: true, Docx: true, Headings: "chapters", Changes: "suggest", SplitScenes: true}
	return v
}

func importPageFixtures() []fixture {
	writer := Page{Title: "Import", Path: "/import/d/imd_1", User: fixtureUser("writer"), CSRF: "tok_writer"}
	with := func(title string, data any) fixture {
		p := writer
		p.Title, p.Data = title, data
		return fixture{"import-review", p}
	}
	word := importReviewView("docx", "docx")
	epub := importReviewView("epub", "epub")
	epub.Opts = impOpts{Show: true, Epub: true, Sections: "auto", Pages: "skip"}
	epub.Word = nil
	epub.Book = &impBook{Facts: []impKV{{"Title", "Salt"}, {"Author", "W. Wren"}}, Cut: "How it was cut: chosen for you, by the book's table of contents.", Cover: "The cover picture is kept as an attachment of the project.", UseCover: true,
		Label: "Salt, 2026", Files: []impFile{{"OEBPS/c1.xhtml", "Becomes The Road"}, {"OEBPS/toc.xhtml", "Not imported: a contents page"}}, FilesN: 2}
	pdf := importReviewView("pdf", "pdf")
	pdf.Word = nil
	pdf.Opts = impOpts{Show: true}
	pdf.Pdf = &impPdf{Facts: []impKV{{"Pages read", "12 (12 with text)"}}, Lines: []impTick{{false, "Links: not kept."}, {true, "Tables: none found."}}, Pages: []impKV{{"Page 3: 61%", "Two columns."}}, PageN: 1}
	long := importReviewView("zip", "markdown-folder")
	long.Word, long.Opts, long.MapN = nil, impOpts{Show: true, HasObsidian: true}, false
	long.More, long.MoreHref, long.Shown = 800, "/import/d/imd_1?rows=400", 200
	into := importReviewView("docx", "docx")
	into.Into = &projects.Project{ID: "prj_a", Slug: "the-salt-road", Title: "The Salt Road"}
	into.PageURL = "/p/the-salt-road/import/d/imd_01TESTTESTTESTTESTTESTTES0"
	stale := importReviewView("docx", "docx")
	stale.Notice, stale.NoticeKind, stale.Focus = "This import was changed in another tab. This is the current version. Your change was not made.", "warn", "imp-notice"
	wait := &impView{Lang: "en", CSRF: "tok_writer", ID: "imd_1", Rev: 1, Kind: "git", Status: "open", Pending: true, PageURL: "/import/d/imd_1", Act: "/ui/import/d/imd_1"}
	failed := &impView{Lang: "en", CSRF: "tok_writer", ID: "imd_1", Rev: 1, Kind: "git", Status: "open", Failed: "example.org could not be reached.", PageURL: "/import/d/imd_1", Act: "/ui/import/d/imd_1"}
	done := &impView{Lang: "en", CSRF: "tok_writer", ID: "imd_1", Rev: 4, Kind: "epub", Status: "done", PageURL: "/import/d/imd_1", Act: "/ui/import/d/imd_1",
		Done: &imports.DraftResult{ProjectID: "prj_a", Slug: "salt", Documents: 41, Words: 80000, EditionID: "edn_1", Suggestions: 3, Comments: 2, Warnings: []string{"A warning."}}}
	done.DoneLinks = []impLink{{"/p/salt/d/doc_1", "Open the book", true}, {"/p/salt/editions", "Editions", false}, {"/import", "Import another", false}}
	chooser := importData{Caps: caps(), Pandoc: true, Drafts: []impDraftItem{
		{ID: "imd_1", Name: "The Salt Road", Kind: "docx", When: "13 Nov 2026, 09:12", Status: "open", Href: "/import/d/imd_1"},
		{ID: "imd_2", Name: "Old", Kind: "epub", When: "12 Nov 2026, 09:12", Status: "done", Done: true, Docs: 41, ProjectHref: "/p/old", Href: "/import/d/imd_2"}}}
	bad := importData{Caps: caps(), Error: "That is not a Word document (.docx).", Panel: "docx"}
	return []fixture{
		with("Review the import", word), with("Review the import", epub), with("Review the import", pdf), with("Review the import", long), with("Review the import", into),
		with("Review the import", stale), with("Review the import", wait), with("Review the import", failed), with("Review the import", done),
		{"import", func() Page { p := writer; p.Title, p.Path, p.Data = "Import", "/import", chooser; return p }()},
		{"import", func() Page { p := writer; p.Title, p.Path, p.Data = "Import", "/import", bad; return p }()},
	}
}

func importFragmentFixtures() []fragmentFixture {
	v := importReviewView("docx", "docx")
	return []fragmentFixture{
		{"import-review", "imp-review", v},
		{"import-review", "imp-panel", impPanelCtx{V: v, R: &v.Rows[1]}},
		{"import-review", "imp-rows", v},
		{"import-review", "imp-stage-error", map[string]any{"Msg": "That is not a Word document (.docx)."}},
	}
}
