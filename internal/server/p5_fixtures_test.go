package server

import (
	"html/template"

	"github.com/crueber/scriobh/internal/domain/admin"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/share"
)

// Fixtures for the Phase 5 screens: what the template lint, the structure checks and the stylesheet budget render.

func p5Base() Page {
	return Page{Title: "x", Path: "/", User: fixtureUser("writer"), CSRF: "tok_writer", Lang: "en", UIDir: "ltr"}
}

func p5With(title, path string, data any) Page {
	p := p5Base()
	p.Title, p.Path, p.Data = title, path, data
	return p
}

func reviewFixture(kind string) reviewData {
	proj := fixtureProjects()[0]
	d := reviewData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", ReadURL: "/p/" + proj.Slug + "/read", DocTitles: map[string]string{"doc_c1": "Low Water"}}
	if kind == "empty" {
		return d
	}
	d.Total, d.Orphaned, d.SugTotal = 3, 1, 2
	d.Chapters = []review.DocCount{{DocumentID: "doc_c1", Title: "Low Water", Open: 2, Orphaned: 1}, {DocumentID: "doc_c2", Title: "", Open: 1}}
	d.Groups = []reviewGroup{
		{DocID: "doc_c1", Title: "Low Water", Crumb: "Part One", Open: 2, Orphaned: 1, Threads: []reviewThread{
			{ID: "cmt_1", Author: "Ada <b>Admin</b>", Kind: "comment", Body: "Cut <script>alert(1)</script> this.", Quote: "the tide came in", When: "13 Nov 2026, 09:12 UTC", Replies: 1, ReadURL: "/p/the-salt-road/read/doc_c1#t=cmt_1"},
			{ID: "cmt_2", Author: "Rory Reader", Kind: "question", Body: "Who is Mara?", Quote: "she counted", Orphan: true, When: "13 Nov 2026, 09:20 UTC", ReadURL: "/p/the-salt-road/read/doc_c1#t=cmt_2"}}},
		{DocID: "", Title: "On the book as a whole", Open: 1, Threads: []reviewThread{{ID: "cmt_3", Author: "Rory Reader", Kind: "comment", Body: "A general note.", When: "13 Nov 2026, 09:30 UTC"}}},
	}
	d.SugGroups = []reviewSugGroup{{DocID: "doc_c1", Title: "Low Water", Sugs: []reviewSug{
		{ID: "sug_1", Author: "Rory Reader", When: "13 Nov 2026, 09:40 UTC", Note: "Plainer.", Changes: []reviewChange{{Old: "the tide", New: "the sea"}}},
		{ID: "sug_2", Author: "Rory Reader", When: "13 Nov 2026, 09:41 UTC", Stale: true, Changes: []reviewChange{{New: " at dusk", Insert: true}}}}}}
	switch kind {
	case "resolve":
		d.Plan = &reviewPlan{Kind: "resolve", Count: 3, Total: 3}
	case "accept":
		d.Plan = &reviewPlan{Kind: "accept", Count: 1, Total: 2, DocID: "doc_c1", Title: "Low Water", IDs: []string{"sug_1", "sug_2"}, Skipped: []reviewSkip{{Reason: "It overlaps one that comes before it.", Message: "Change 1."}}}
	case "message":
		d.Flash, d.Error = "Resolved.", "The set of open comments changed while you were looking, so nothing was resolved. Look again."
	case "filtered":
		d.Filter, d.AnyFilter, d.More = reviewFilter{Doc: "doc_c1", Author: "me", Kind: "question", State: "orphaned"}, true, true
	}
	return d
}

func readReviewFixture(role string) readData {
	d := readFixture(fixtureProjects()[0], role == "editor", false)
	d.Role, d.Review, d.Decide, d.Dir = role, role != "reader", role == "editor", "ltr"
	d.Toc[1].Docs = "doc_c1 doc_s1"
	d.Article.Body[0].Doc, d.Article.Body[0].Rev = "doc_c1", 4
	d.Article.Body[1].Doc, d.Article.Body[1].Rev = "doc_s1", 2
	d.Article.Body[0].HTML = template.HTML("<p>Mara counted the <em>boats</em> that did not come in.</p>")
	return d
}

func shareAdminFixture(kind string) shareAdminData {
	proj := fixtureProjects()[0]
	d := shareAdminData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", Today: "2026-11-13", Form: map[string]string{"scope": "read", "contents": "whole", "ghosts": "omit"}, Chosen: map[string]bool{},
		Docs: []shareDocOpt{{ID: "doc_p1", Title: "Part One", Depth: 0}, {ID: "doc_c1", Title: "Low <b>Water</b>", Depth: 1, Checked: kind == "error"}, {ID: "doc_c2", Title: "High Water", Depth: 1}}}
	if kind == "empty" {
		return d
	}
	usage := &share.UsageOut{Item: share.Out{Views: 12}, Threads: 4, OpenThreads: 2, Failed: 3, LockedBuckets: 1}
	d.Links = []shareLinkView{
		{Out: share.Out{ID: "shl_1", Label: "Beta reader <i>Sam</i>", Scope: "comment", Status: "active", Whole: true, HasPassphrase: true, AllowDownload: true, Ghosts: "omit", ExpiresAt: "2026-12-01T23:59:59Z"}, Usage: usage, What: "The whole book", ExpiresText: "1 Dec 2026, 23:59 UTC", ExpiresDate: "2026-12-01", Created: "13 Nov 2026, 09:00 UTC", LastUsed: "13 Nov 2026, 09:30 UTC", Active: true},
		{Out: share.Out{ID: "shl_2", Scope: "read", Status: "revoked", DocumentIDs: []string{"doc_c1", "doc_c2"}, Ghosts: "keep"}, What: "Chosen parts (2)", Created: "12 Nov 2026, 09:00 UTC"},
		{Out: share.Out{ID: "shl_3", Label: "Old", Scope: "read", Status: "expired", Whole: true, Ghosts: "omit"}, Usage: &share.UsageOut{}, What: "The whole book", ExpiresText: "1 Nov 2026, 00:00 UTC", Created: "1 Oct 2026, 09:00 UTC"},
	}
	switch kind {
	case "once":
		d.Once = &shareOnce{Path: "/s/shl_01EXAMPLE.tokentokentokentokentokentokentokentokentok", Label: "Beta reader"}
	case "error":
		d.Error = "A passphrase must be at least 8 characters."
		d.Form = map[string]string{"scope": "comment", "contents": "chosen", "label": "Sam", "ghosts": "keep", "download": "on"}
	}
	return d
}

func adminFixture(tab string) adminData {
	d := adminData{Tab: tab, CSRF: "tok_admin", Total: 3, Active: 2, Disabled: 1, Admins: 1}
	d.Rows = []adminRow{
		{UserOut: admin.UserOut{ID: "usr_1", Name: "Ada <b>Admin</b>", Email: "ada@example.org", Role: "admin", Self: true, Projects: 2, Sessions: 1}, Storage: "1.2 MB", Seen: "13 Nov 2026, 09:00 UTC"},
		{UserOut: admin.UserOut{ID: "usr_2", Name: "Wren Writer", Email: "wren@example.org", Role: "writer", Projects: 5, MemberOf: 1}, Storage: "0 B"},
		{UserOut: admin.UserOut{ID: "usr_3", Name: "Rory Reader", Email: "rory@example.org", Role: "reader", Disabled: true}, Storage: "0 B", Seen: "1 Nov 2026, 10:00 UTC"},
	}
	switch tab {
	case "confirm":
		d.Tab = "users"
		d.Confirm = &d.Rows[1]
		d.Error = "That is not the name. Type it exactly as it is shown, or cancel."
	case "idp":
		d.Tab, d.FromIdP, d.Flash = "users", true, "Wren Writer is switched off."
	case "quotas":
		d.Note = "These limits come from the server's configuration."
		d.Lines = []adminQuotaLine{{Name: "Attachment storage, whole instance", Used: "5 MB", Limit: "no limit"}, {Name: "Attachment storage, the fullest project or Compendium", Used: "900 MB", Limit: "1 GB", Percent: 88}, {Name: "Largest single file allowed", Limit: "25 MB"}, {Name: "Projects", Used: "7", Limit: "no limit"}}
		d.QuotaUsers = []adminQuotaUser{{UserQuota: admin.UserQuota{Name: "Wren Writer", Keys: 2, KeyLimit: 20, Projects: 5}, Storage: "3 MB", Limit: "2 GB", Percent: 1}, {UserQuota: admin.UserQuota{Name: "Rory", Disabled: true}, Storage: "2 GB", Limit: "2 GB", Percent: 100}}
	case "diagnostics":
		d.Diag = &adminDiag{Version: "v0.5.0", Go: "go1.27", OS: "linux", Arch: "arm64", Applied: 56, Known: 56, Tables: 80, DiskTotal: "34 MB", LogLines: 3,
			Queues: []admin.QueueState{{Name: "imports", States: map[string]int{"done": 2, "failed": 1}}}}
	}
	return d
}

func p5PageFixtures() []fixture {
	return []fixture{
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("empty"))},
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("full"))},
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("resolve"))},
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("accept"))},
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("message"))},
		{"review", p5With("Review of The Salt Road", "/p/the-salt-road/review", reviewFixture("filtered"))},
		{"read", p5With("Low Water", "/p/the-salt-road/read/doc_c1", readReviewFixture("editor"))},
		{"read", p5With("Low Water", "/p/the-salt-road/read/doc_c1", readReviewFixture("reviewer"))},
		{"read", p5With("Low Water", "/p/the-salt-road/read/doc_c1", readReviewFixture("reader"))},
		{"share-links", p5With("Share links of The Salt Road", "/p/the-salt-road/share", shareAdminFixture("empty"))},
		{"share-links", p5With("Share links of The Salt Road", "/p/the-salt-road/share", shareAdminFixture("list"))},
		{"share-links", p5With("Share links of The Salt Road", "/p/the-salt-road/share", shareAdminFixture("once"))},
		{"share-links", p5With("Share links of The Salt Road", "/p/the-salt-road/share", shareAdminFixture("error"))},
		{"compile", p5With("Compile The Salt Road", "/p/the-salt-road/compile", compileFixture(false))},
		{"compile", p5With("Compile The Salt Road", "/p/the-salt-road/compile", compileFixture(true))},
		{"admin", adminPage(adminFixture("users"))},
		{"admin", adminPage(adminFixture("confirm"))},
		{"admin", adminPage(adminFixture("idp"))},
		{"admin", adminPage(adminFixture("quotas"))},
		{"admin", adminPage(adminFixture("diagnostics"))},
		{"share", Page{Title: "Shared reading", Lang: "en", UIDir: "ltr", Bare: true}},
		{"share-gone", Page{Title: "This link is not available", Lang: "en", UIDir: "ltr", Bare: true}},
	}
}

func p5FragmentFixtures() []fragmentFixture { return nil }

func adminPage(d adminData) Page {
	p := p5With("Administration", "/admin/users", d)
	p.User = fixtureUser("admin")
	p.CSRF = "tok_admin"
	return p
}

func compileFixture(withErr bool) compileData {
	proj := fixtureProjects()[0]
	d := compileData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: "tok_writer", Dir: "ltr", Lang: "en",
		Presets: `{"presets":[{"id":"shunn-us","name":"US Standard Manuscript","builtin":true,"format":"docx","description":"Shunn","options":{"font":"Times New Roman"}}]}`,
		Options: `[{"key":"font","type":"string","enum":["Times New Roman"],"group":"page"}]`,
		Contact: compile.ContactOut{Name: "Máire <b>Nolan</b>", Email: "m@example.org"}, Profiles: coverProfileViews()}
	if withErr {
		d.Flash, d.Error = "The contact block was saved.", "That email address is too long."
	}
	return d
}
