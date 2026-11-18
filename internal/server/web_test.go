package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"github.com/crueber/scriobh/internal/pandoc"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/web"
)

// These tests are the template lint and the performance budgets from
// docs/ARCHITECTURE.md. They render every page and fragment with fixture data
// and hold the output to the CSP, to the privacy invariants, and to the size
// budgets, so a template or stylesheet cannot drift unnoticed.

func newWebServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{"SCRIOBH_DATA_DIR": dir, "SCRIOBH_BASE_URL": "http://127.0.0.1:8080"}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func staticFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// ---- fixtures ----------------------------------------------------------------

func fixtureProjects() []projects.Project {
	mk := func(i int, title, kind, status, author, color string) projects.Project {
		return projects.Project{ID: "prj_" + string(rune('a'+i)), Slug: "p-" + string(rune('a'+i)), Title: title, Kind: kind,
			Status: status, AuthorName: author, Language: "en", CoverColor: color, Role: "owner"}
	}
	return []projects.Project{
		mk(0, "The Salt Road", "novel", "drafting", "Máire Nolan", "#2F5D50"),
		mk(1, "Notes on <b>Quiet</b> Software & Other \"Things\"", "essay", "idea", "", "#7A4B2A"),
		mk(2, strings.Repeat("x", 200), "blank", "idea", "", "#4A4740"),
		mk(3, "Hedgerow", "short", "finished", "Ciara Ní Bhriain", "#4B6B3A"),
		mk(4, "Unknown colour falls back", "report", "revising", "", "#123456"),
	}
}

func fixtureUser(role string) *UserView {
	return &UserView{ID: "usr_1", Name: "Wren Writer", Email: "wren@example.com", Role: role, Initials: "WW",
		IsAdmin: role == "admin", IsWriter: role == "admin" || role == "writer"}
}

type fixture struct {
	name string // template name in s.tmpl
	page Page
}

func basePageFixtures() []fixture {
	signedOut := Page{Title: "Sign in", Path: "/"}
	writer := Page{Title: "Library", Path: "/", User: fixtureUser("writer"), CSRF: "tok_writer"}
	reader := Page{Title: "Library", Path: "/", User: fixtureUser("reader"), CSRF: "tok_reader"}
	with := func(p Page, title, path string, data any) Page { p.Title, p.Path, p.Data = title, path, data; return p }
	form := projectForm{Values: map[string]string{"kind": "blank", "language": "en", "cover_color": "#2F5D50"}, Errors: nil, CSRF: "tok_writer"}
	bad := projectForm{
		Values: map[string]string{"title": "<script>alert(1)</script>", "kind": "novel", "language": "en", "word_goal": "lots", "cover_color": "#6B3E4F"},
		Errors: map[string]string{"title": "Give the project a title.", "word_goal": "The word goal must be a number.", "form": "Something went wrong."},
		CSRF:   "tok_writer", Status: 422,
	}
	pr := fixtureProjects()
	lib := func(items []projects.Project, q string) libraryData {
		d := libraryData{Projects: items, Q: q, State: "active", CSRF: "tok_writer", IsWriter: true}
		if len(items) > 0 && q == "" {
			items[0].LastOpenedAt, items[0].LastDocumentID, items[0].WordCount, items[0].WordGoal, items[0].Pinned = "2026-11-12T10:00:00.000Z", "doc_x", 12345, 80000, true
			d.Continue = &items[0]
		}
		return d
	}
	libView := func(state string, items []projects.Project) libraryData {
		return libraryData{Projects: items, State: state, CSRF: "tok_writer", IsWriter: true}
	}
	return append(append(researchPageFixtures(writer, reader), []fixture{
		{"signin", signedOut},
		{"offline", Page{Title: "Offline"}},
		{"library", with(writer, "Library", "/", lib(nil, ""))},
		{"library", with(writer, "Library", "/", lib(pr, ""))},
		{"library", with(writer, "Library", "/", lib(nil, "zzz"))},
		{"library", with(reader, "Library", "/", lib(nil, ""))},
		{"library", with(reader, "Library", "/", lib(pr[:2], ""))},
		{"write", with(writer, "The Harbour at Low Tide", "/p/the-salt-road/d/doc_c", writeFixture(pr[0], "editor", true))},
		{"write", with(writer, "The Harbour at Low Tide", "/p/the-salt-road/d/doc_c", writeFixture(pr[0], "reader", true))},
		{"write", with(writer, "Long", "/p/the-salt-road/d/doc_c", writeFixture(pr[0], "editor", true, "readonly"))},
		{"write", with(writer, "The Harbour at Low Tide", "/p/the-salt-road/d/doc_c", writeAIFixture("editor", "on", "ask"))},
		{"write", with(writer, "The Harbour at Low Tide", "/p/the-salt-road/d/doc_c", writeAIFixture("editor", "on", "local_only"))},
		{"write", with(writer, "The Harbour at Low Tide", "/p/the-salt-road/d/doc_c", writeAIFixture("editor", "unavailable", ""))},
		{"write", with(writer, "The Salt Road", "/p/the-salt-road", writeFixture(pr[0], "editor", false))},
		{"write", with(reader, "The Salt Road", "/p/the-salt-road", writeFixture(pr[0], "reader", false))},
		{"library", with(writer, "Archived", "/?state=archived", libView("archived", nil))},
		{"library", with(writer, "Archived", "/?state=archived", libView("archived", archivedFixture(pr[:2])))},
		{"library", with(writer, "Trash", "/?state=trashed", libView("trashed", nil))},
		{"library", with(writer, "Trash", "/?state=trashed", libView("trashed", trashedFixture(pr[:2])))},
		{"settings", with(writer, "Settings", "/settings", settingsFixture(""))},
		{"settings", with(writer, "Settings", "/settings", settingsBackupsFixture(false))},
		{"settings", with(writer, "Settings", "/settings", settingsBackupsFixture(true))},
		{"import", with(writer, "Import", "/import", importData{Caps: caps()})},
		{"import", with(writer, "Import into The Salt Road", "/p/the-salt-road/import", importData{Into: importTarget(), Caps: caps()})},
		{"import-repo", with(writer, "Import from a Git repository", "/import/git", repoImportData{Caps: caps(), RepoMiB: 512,
			Remotes: []repoRemoteOpt{{ID: "grm_a", Label: "The Salt Road · Backup (git.example.org)", Branch: "main"}}, Pre: "grm_a", PreProject: "The Salt Road",
			Jobs: []repoJobView{
				{ID: "imp_1", Status: "running", URL: "https://git.example.org/wren/salt-road.git", Branch: "main", When: "13 Nov 2026, 09:12"},
				{ID: "imp_2", Status: "preview", URL: "https://github.com/wren/essays.git", Commit: "9f3c2a1b", When: "13 Nov 2026, 08:50", Summary: true, Documents: 14, Words: 21800},
				{ID: "imp_3", Status: "failed", URL: "https://example.org/x.git", Error: "example.org could not be reached. The commit is kept locally and will be tried again.", When: "12 Nov 2026, 22:01"},
				{ID: "imp_4", Status: "done", URL: "https://github.com/wren/old.git", Slug: "old", When: "10 Nov 2026, 10:00"}}})},
		{"import-repo", with(writer, "Import from a Git repository", "/import/git", repoImportData{Caps: caps(), RepoMiB: 512})},
		{"import-repo", with(writer, "Import from a Git repository", "/import/git", repoImportData{Caps: caps(), RepoMiB: 512, Unavailable: "Git sync is not running on this server."})},
		{"project-actions", with(writer, "The Salt Road", "/ui/projects/prj_a/menu", tileData{P: ownerFixture(pr[0]), State: "active", CSRF: "tok_writer",
			Mirror: &mirrorView{State: "ok", Line: "The Markdown copy is up to date.", Detail: "14 files, last changed 12 Nov 2026, 09:30.", CanRebuild: true}})},
		{"project-actions", with(writer, "The Salt Road", "/ui/projects/prj_a/menu", tileData{P: ownerFixture(pr[0]), State: "active", CSRF: "tok_writer",
			Mirror: &mirrorView{State: "error", Line: "The Markdown copy could not be written.", Detail: "No space left on device. Trying again at 12 Nov 2026, 09:35.", CanRebuild: true}})},
		{"settings", with(writer, "Settings", "/settings", settingsFixture("Give yourself a name."))},
		{"project-actions", with(writer, "The Salt Road", "/ui/projects/prj_a/menu", tileData{P: ownerFixture(pr[0]), State: "active", CSRF: "tok_writer"})},
		{"project-actions", with(writer, "Hedgerow", "/ui/projects/prj_d/menu", tileData{P: pr[3], State: "active", CSRF: "tok_writer"})},
		{"project-purge", with(writer, "Delete", "/ui/projects/prj_a/purge", purgeData{P: trashedFixture(pr[:1])[0], CSRF: "tok_writer", Error: "Type the project's exact title to confirm.", Typed: "The Salt"})},
		{"compendiums", with(writer, "Compendiums", "/compendiums", compendiumsFixture("active", true))},
		{"compendiums", with(writer, "Compendiums", "/compendiums", compendiumsFixture("empty", true))},
		{"compendiums", with(reader, "Compendiums", "/compendiums", compendiumsFixture("empty", false))},
		{"compendiums", with(writer, "Compendiums", "/compendiums?state=archived", compendiumsFixture("archived", true))},
		{"compendiums", with(writer, "Compendiums", "/compendiums?state=trashed", compendiumsFixture("trashed", true))},
		{"compendium-new", with(writer, "New Compendium", "/ui/compendiums/new", compNewFixture(false))},
		{"compendium-new", with(writer, "New Compendium", "/ui/compendiums/new", compNewFixture(true))},
		{"compendium-purge", with(writer, "Delete", "/ui/compendiums/cmp_a/purge", compPurgeFixture(true))},
		{"compendium", with(writer, "The Salt Road Bible", "/c/the-salt-bible", compOverviewFixture("owner"))},
		{"compendium", with(writer, "The Salt Road Bible", "/c/the-salt-bible", compOverviewFixture("editor"))},
		{"compendium", with(reader, "The Salt Road Bible", "/c/the-salt-bible", compOverviewFixture("reader"))},
		{"project-compendium", with(writer, "Compendium", "/p/the-salt-road/compendium", projCompFixture(true, true, false))},
		{"project-compendium", with(writer, "Compendium", "/p/the-salt-road/compendium", projCompFixture(false, true, false))},
		{"project-compendium", with(writer, "Compendium", "/p/the-salt-road/compendium", projCompFixture(true, true, true))},
		{"project-compendium", with(writer, "Compendium", "/p/the-salt-road/compendium", projCompFixture(true, false, false))},
		{"people", with(writer, "People", "/p/the-salt-road/people", peopleFixture(true, true))},
		{"people", with(writer, "People", "/p/the-salt-road/people", peopleFixture(true, false))},
		{"people", with(writer, "People", "/p/the-salt-road/people", peopleFixture(false, true))},
		{"outline", with(writer, "Outline of The Salt Road", "/p/the-salt-road/outline", outlineFixture(pr[0], true, false))},
		{"outline", with(writer, "Outline of The Salt Road", "/p/the-salt-road/outline", outlineFixture(pr[0], true, true))},
		{"outline", with(reader, "Outline of The Salt Road", "/p/the-salt-road/outline", outlineFixture(pr[0], false, false))},
		{"cards", with(writer, "Corkboard of The Salt Road", "/p/the-salt-road/cards", cardsFixture(pr[0], true, "", false))},
		{"cards", with(writer, "Corkboard of The Salt Road", "/p/the-salt-road/cards", cardsFixture(pr[0], true, "draft", false))},
		{"cards", with(writer, "Corkboard of The Salt Road", "/p/the-salt-road/cards", cardsFixture(pr[0], true, "", true))},
		{"cards", with(reader, "Corkboard of The Salt Road", "/p/the-salt-road/cards", cardsFixture(pr[0], false, "", false))},
		{"board", with(writer, "Status board of The Salt Road", "/p/the-salt-road/board", boardFixture(pr[0], true, false))},
		{"board", with(writer, "Status board of The Salt Road", "/p/the-salt-road/board", boardFixture(pr[0], true, true))},
		{"board", with(reader, "Status board of The Salt Road", "/p/the-salt-road/board", boardFixture(pr[0], false, false))},
		{"read", with(writer, "Low Water", "/p/the-salt-road/read/doc_c1", readFixture(pr[0], true, false))},
		{"read", with(reader, "Low Water", "/p/the-salt-road/read/doc_c1", readFixture(pr[0], false, false))},
		{"read", with(writer, "Reading The Salt Road", "/p/the-salt-road/read", readFixture(pr[0], true, true))},
		{"search", with(writer, "Search The Salt Road", "/p/p-a/search", searchFixture(true, "harb", true))},
		{"search", with(writer, "Search The Salt Road", "/p/p-a/search", searchFixture(true, "zzz", false))},
		{"search", with(writer, "Search", "/search", searchFixture(false, "harb", true))},
		{"search", with(writer, "Search", "/search", searchFixture(false, "", false))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitEmptyFixture(true))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitEmptyFixture(false))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitFixture("new", true))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitFixture("synced", false))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitFixture("rejected", true))},
		{"git", with(writer, "Git sync", "/p/p-a/git", gitFixture("secret", true))},
		{"git-force", with(writer, "Replace the history", "/p/p-a/git/force", gitForceFixture())},
		{"publish", with(writer, "Publish to a repository", "/p/p-a/git/publish", publishFixture())},
		{"admin-pandoc", with(writer, "Pandoc", "/admin/pandoc", pandocPage{Tab: "pandoc", CSRF: "tok", Enabled: true, Memory: "1024", Cfg: pandoc.ConfigOut{Enabled: true, Found: true, Version: "3.2", MinVersion: "3.1.6", PlatformOK: true, ImportFormats: []string{"odt"}, ExportFormats: []string{"odt"}}, Test: &pandocTestView{Version: "3.2", OK: true}})},
		{"editions", with(writer, "Editions", "/p/p-a/editions", editionsFixture("list"))},
		{"editions", with(writer, "Editions", "/p/p-a/editions", editionsFixture("empty"))},
		{"editions", with(writer, "Editions", "/p/p-a/editions", editionsFixture("reader"))},
		{"edition", with(writer, "First printing", "/p/p-a/editions/edn_1", editionsFixture("one"))},
		{"editiondiff", with(writer, "Changes since this edition", "/p/p-a/editions/edn_1/diff", diffFixture("list"))},
		{"editiondiff", with(writer, "Changes since this edition", "/p/p-a/editions/edn_1/diff", diffFixture("step"))},
		{"editiondiff", with(writer, "Changes since this edition", "/p/p-a/editions/edn_1/diff", diffFixture("reader"))},
		{"editiondiff", with(writer, "What changed between two editions", "/p/p-a/editions/compare", diffFixture("compare"))},
		{"editiondiff", with(writer, "What changed between two editions", "/p/p-a/editions/compare", diffFixture("picker"))},
		{"editiondiff", with(writer, "What changed between two editions", "/p/p-a/editions/compare", diffFixture("busy"))},
		{"release", with(writer, "Publish an edition as a release", "/p/p-a/git/release", releaseFixture())},
		{"git-push", with(writer, "Push", "/p/p-a/git/pushes/gp_1", gitPushFixture())},
		{"goals", with(writer, "Goals The Salt Road", "/p/p-a/goals", goalsFixture(true, true))},
		{"goals", with(writer, "Goals The Salt Road", "/p/p-a/goals", goalsFixture(true, false))},
		{"goals", with(writer, "Goals The Salt Road", "/p/p-a/goals", goalsFixture(false, false))},
		{"project-ai", with(writer, "AI The Salt Road", "/p/p-a/ai", aiProjectFixture(true, true, "off"))},
		{"project-ai", with(writer, "AI The Salt Road", "/p/p-a/ai", aiProjectFixture(false, true, "on"))},
		{"project-ai", with(reader, "AI The Salt Road", "/p/p-a/ai", aiProjectFixture(false, false, "none"))},
		{"ai-settings", with(writer, "AI", "/settings/ai", aiSettingsData{CSRF: "tok_writer", IsAdmin: true, Writer: true})},
		{"ai-settings", with(writer, "AI", "/settings/ai", aiSettingsData{CSRF: "tok_writer", Writer: true})},
		{"ai-settings", with(reader, "AI", "/settings/ai", aiSettingsData{CSRF: "tok_reader"})},
		{"files", with(writer, "Attachments The Salt Road", "/p/p-a/files", attPageFixture(false, true, false, ""))},
		{"files", with(writer, "Attachments The Salt Road", "/p/p-a/files?kind=pdf", attPageFixture(false, true, true, "pdf"))},
		{"files", with(writer, "Attachments The Salt Road", "/p/p-a/files", attFullFixture())},
		{"files", with(reader, "Attachments The Salt Road", "/p/p-a/files", attPageFixture(false, false, false, ""))},
		{"files", with(reader, "Attachments The Salt Road", "/p/p-a/files", attPageFixture(false, false, true, ""))},
		{"files", with(writer, "Attachments The Salt Road Bible", "/c/the-salt-bible/files", attPageFixture(true, true, false, ""))},
		{"files", with(writer, "Attachments The Salt Road Bible", "/c/the-salt-bible/files", attPageFixture(true, true, true, ""))},
		{"file", with(writer, "harbour map.png", "/p/p-a/files/att_img", attDetailFixture(false, true, false))},
		{"file", with(writer, "harbour map.png", "/p/p-a/files/att_img", attDetailFixture(false, true, true))},
		{"file", with(reader, "harbour map.png", "/p/p-a/files/att_img", attDetailFixture(false, false, false))},
		{"file", with(writer, "tide tables.pdf", "/p/p-a/files/att_pdf", attPDFDetailFixture())},
		{"file", with(writer, "harbour map.png", "/c/the-salt-bible/files/att_img", attDetailFixture(true, true, false))},
		{"project-export", with(writer, "Export The Salt Road", "/ui/projects/prj_a/export", exportData{P: ownerFixture(pr[0]), CSRF: "tok_writer", Mode: "bundle", Notes: true, Error: "Too big to export."})},
		{"new-project", with(writer, "New project", "/ui/projects/new", form)},
		{"new-project", with(writer, "New project", "/ui/projects/new", bad)},
		{"error", with(writer, "Not found", "/x", map[string]any{"Heading": "Not found", "Message": "There is nothing here, or you do not have access to it.", "Code": 404})},
		{"error", with(signedOut, "Sign-in refused", "/auth/login", map[string]any{"Heading": "Sign-in refused", "Message": "Your account is not in a permitted group.", "Code": 403, "Retry": "/auth/login"})},
		{"error", with(signedOut, "Something went wrong", "/", map[string]any{"Heading": "Something went wrong", "Message": "Your work is safe. Try again in a moment.", "Code": 500})},
		{"project-unlock", with(writer, "Unlock", "/p/the-salt-road/unlock", gateData{ID: "prj_a", Title: "The Salt Road", Next: "/p/the-salt-road", Error: "That passphrase is not right."})},
		{"project-unlock", with(writer, "Unlock", "/p/the-salt-road/unlock", gateData{ID: "prj_a", Title: "The Salt Road", Next: "/p/the-salt-road"})},
		{"claim", with(writer, "Claim administration", "/auth/claim-admin", map[string]any{"Name": "Wren Writer", "Email": "wren@example.com", "Issuer": "https://idp.example.com"})},
	}...), entityPageFixtures()...)
}

type fragmentFixture struct {
	name, block string
	data        any
}

func baseFragmentFixtures() []fragmentFixture {
	form := projectForm{Values: map[string]string{"kind": "essay", "language": "ga", "cover_color": "#7A4B2A"}, CSRF: "tok"}
	bad := projectForm{Values: map[string]string{"title": " "}, Errors: map[string]string{"title": "Give the project a title."}, CSRF: "tok", Status: 422}
	return append(append(researchFragmentFixtures(), []fragmentFixture{
		{"new-project", "new-project-form", form},
		{"new-project", "new-project-form", bad},
		{"project-tile", "project-tile", tileData{P: ownerFixture(fixtureProjects()[0]), State: "active", CSRF: "tok"}},
		{"project-tile", "project-tile", tileData{P: trashedFixture(fixtureProjects()[:1])[0], State: "trashed", CSRF: "tok"}},
		{"project-menu", "project-menu", tileData{P: ownerFixture(fixtureProjects()[0]), State: "active", CSRF: "tok"}},
		{"project-menu", "project-purge", purgeData{P: trashedFixture(fixtureProjects()[:1])[0], CSRF: "tok"}},
		{"project-export", "project-export", exportData{P: ownerFixture(fixtureProjects()[0]), CSRF: "tok", Mode: "single"}},
		{"shelf", "shelf", libraryData{Projects: fixtureProjects()[:2], State: "active", CSRF: "tok", IsWriter: true}},
		{"boards", "cards-body", cardsFixture(fixtureProjects()[0], true, "", false)},
		{"boards", "cards-body", cardsFixture(fixtureProjects()[0], true, "idea", false)},
		{"boards", "cards-body", cardsFixture(fixtureProjects()[0], true, "", true)},
		{"boards", "card", map[string]any{"R": cardsFixture(fixtureProjects()[0], true, "", false).Lanes[1].Cards[0], "Slug": "the-salt-road"}},
		{"boards", "board-body", boardFixture(fixtureProjects()[0], true, false)},
		{"boards", "board-body", boardFixture(fixtureProjects()[0], true, true)},
		{"boards", "bcard", map[string]any{"R": boardFixture(fixtureProjects()[0], true, false).Cols[0].Cards[0], "Slug": "the-salt-road"}},
		{"read", "read-article", readFixture(fixtureProjects()[0], false, false).Article},
		{"outline", "outline-body", outlineFixture(fixtureProjects()[0], true, false)},
		{"outline", "outline-body", outlineFixture(fixtureProjects()[0], true, true)},
		{"attach-list", "attach-list", attPageFixture(false, true, false, "")},
		{"attach-list", "attach-list", attPageFixture(false, false, true, "")},
		{"attach-list", "attach-list", attPageFixture(true, true, false, "image")},
		{"ptabs", "ptab-desktop", ptabs("the-salt-road", "doc_x", "write")},
		{"ptabs", "ptab-phone", ptabs("the-salt-road", "", "outline")},
		{"ptabs", "project-more", ptabs("the-salt-road", "", "outline")},
		{"ctabs", "ctab-desktop", ctabs("the-salt-bible", "entities")},
		{"ctabs", "ctab-phone", ctabs("the-salt-bible", "overview")},
		{"compendium-card", "compendium-card", map[string]any{"C": compFixture("owner"), "State": "active", "CSRF": "tok"}},
		{"compendium-card", "compendium-card", map[string]any{"C": compFixture("reader"), "State": "active", "CSRF": "tok"}},
		{"compendium-forms", "compendium-new", compNewFixture(false)},
		{"compendium-forms", "compendium-purge", compPurgeFixture(false)},
		{"live", "live-marker", map[string]any{"Scope": "project", "ID": "prj_a"}},
		{"ctabs", "cmore", ctabs("the-salt-bible", "scratch")},
		{"ctabs", "ccrumb", map[string]any{"C": compendiums.Compendium{Slug: "the-salt-bible", Name: "The Salt Road Bible"}, "Here": "Entities"}},
		{"ctabs", "comp-banner", &compBanner{Kind: "trashed", ID: "cmp_x", Owner: true, CSRF: "tok"}},
		{"ctabs", "comp-banner", &compBanner{Kind: "trashed", ID: "cmp_x"}},
		{"ctabs", "comp-banner", &compBanner{Kind: "archived", ID: "cmp_x", Owner: true, CSRF: "tok"}},
		{"ctabs", "comp-banner", &compBanner{Kind: "archived", ID: "cmp_x"}},
		{"backups", "settings-backups", map[string]any{"B": settingsBackupsFixture(true).Backups, "CSRF": "tok", "Saved": ""}},
		{"git-live", "git-live", gitFixture("behind", true)},
		{"git-parts", "git-setup", gitFixture("new", true)},
		{"git-parts", "git-form", gitEmptyFixture(true)},
		{"git-force", "git-force", gitForceFixture()},
		{"compile-cover", "compile-cover", compileCoverFixture(false)},
		{"compile-cover", "compile-cover", compileCoverFixture(true)},
		{"release-live", "release-live", releaseFixture()},
		{"admin-tabs", "admin-tabs", map[string]any{"T": Page{}, "Tab": "users"}},
		{"icon", "icon", "library"},
		{"labels", "kind-label", "novel"},
		{"tree", "tree", writeFixture(fixtureProjects()[0], "editor", true).Tree},
		{"tree", "tree", writeFixture(fixtureProjects()[0], "reader", true).Tree},
		{"tree", "tree", writeFixture(fixtureProjects()[0], "editor", false).Tree},
	}...), entityFragmentFixtures()...)
}

func renderPage(t *testing.T, s *Server, f fixture) string {
	t.Helper()
	tm, ok := s.tmpl[f.name]
	if !ok {
		t.Fatalf("no page template %q (have %v)", f.name, s.Templates())
	}
	var buf bytes.Buffer
	if err := tm.ExecuteTemplate(&buf, "layout", f.page); err != nil {
		t.Fatalf("render %s: %v", f.name, err)
	}
	return buf.String()
}

func renderFragment(t *testing.T, s *Server, f fragmentFixture) string {
	t.Helper()
	tm, ok := s.tmpl["partials/"+f.name]
	if !ok {
		t.Fatalf("no fragment %q (have %v)", f.name, s.Templates())
	}
	var buf bytes.Buffer
	if err := tm.ExecuteTemplate(&buf, f.block, f.data); err != nil {
		t.Fatalf("render fragment %s/%s: %v", f.name, f.block, err)
	}
	return buf.String()
}

// ---- a small HTML reader (no dependency) -----------------------------------

var (
	reTag     = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9-]*)((?:"[^"]*"|'[^']*'|[^'">])*)>`)
	reAttr    = regexp.MustCompile(`([a-zA-Z_:@][-a-zA-Z0-9_:.@]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reStrip   = regexp.MustCompile(`(?s)<[^>]*>`)
)

type tag struct {
	name  string
	attrs map[string]string
}

func tags(doc string) []tag {
	doc = reComment.ReplaceAllString(doc, "")
	var out []tag
	for _, m := range reTag.FindAllStringSubmatch(doc, -1) {
		t := tag{name: strings.ToLower(m[1]), attrs: map[string]string{}}
		for _, a := range reAttr.FindAllStringSubmatch(m[2], -1) {
			v := a[2]
			if v == "" {
				v = a[3]
			}
			if v == "" {
				v = a[4]
			}
			t.attrs[strings.ToLower(a[1])] = v
		}
		out = append(out, t)
	}
	return out
}

func visibleText(doc string) string {
	doc = reComment.ReplaceAllString(doc, "")
	return reStrip.ReplaceAllString(doc, " ")
}

var urlAttrs = map[string]bool{"href": true, "src": true, "action": true, "formaction": true, "poster": true, "data": true,
	"srcset": true, "xlink:href": true, "hx-get": true, "hx-post": true, "hx-put": true, "hx-patch": true, "hx-delete": true, "hx-push-url": true}

// lint is the CSP and privacy contract for rendered markup.
func lint(t *testing.T, label, doc string) {
	t.Helper()
	lintTo(func(f string, a ...any) { t.Errorf("%s: "+f, append([]any{label}, a...)...) }, doc)
}

func lintTo(fail func(string, ...any), doc string) {
	for _, tg := range tags(doc) {
		for k, v := range tg.attrs {
			switch {
			case k == "style":
				fail("<%s> has a style attribute (CSP forbids it)", tg.name)
			case strings.HasPrefix(k, "on") && len(k) > 2:
				fail("<%s> has an inline event handler %s", tg.name, k)
			case strings.HasPrefix(k, "hx-on"), k == "hx-vals" && strings.Contains(v, "js:"), k == "hx-headers" && strings.Contains(v, "js:"):
				fail("<%s> uses htmx scripting attribute %s (allowEval is off)", tg.name, k)
			}
			if urlAttrs[k] {
				lv := strings.ToLower(strings.TrimSpace(v))
				if strings.HasPrefix(lv, "javascript:") || strings.HasPrefix(lv, "data:text/html") || strings.HasPrefix(lv, "vbscript:") {
					fail("<%s %s> is a script URL: %q", tg.name, k, v)
				}
				if strings.HasPrefix(lv, "http://") || strings.HasPrefix(lv, "https://") || strings.HasPrefix(lv, "//") {
					fail("<%s %s> points off-origin: %q", tg.name, k, v)
				}
			}
		}
		switch tg.name {
		case "style":
			fail("contains a <style> element (CSP forbids it)")
		case "script":
			if !strings.HasPrefix(tg.attrs["src"], "/static/") {
				fail("inline or foreign <script>: %v", tg.attrs)
			}
		case "link":
			if strings.Contains(tg.attrs["rel"], "stylesheet") || tg.attrs["rel"] == "preload" || tg.attrs["rel"] == "icon" {
				if !strings.HasPrefix(tg.attrs["href"], "/static/") {
					fail("<link rel=%s> is not self-hosted: %q", tg.attrs["rel"], tg.attrs["href"])
				}
			}
		case "meta":
			if strings.EqualFold(tg.attrs["http-equiv"], "refresh") {
				fail("meta refresh is not allowed")
			}
		}
	}
	text := visibleText(doc)
	for _, bad := range []string{"!", "Oops", "Whoops"} {
		if strings.Contains(text, bad) {
			fail("copy contains %q, which DESIGN.md forbids", bad)
		}
	}
	for _, bad := range []string{">Submit<", ">OK<", ">Yes<"} {
		if strings.Contains(doc, bad) {
			fail("button copy %q is not a verb (DESIGN.md)", bad)
		}
	}
}

// structure checks what assistive technology depends on.
func structure(t *testing.T, label, doc string) {
	t.Helper()
	fail := func(f string, a ...any) { t.Errorf("%s: "+f, append([]any{label}, a...)...) }
	ids := map[string]int{}
	var refs [][2]string
	var h1, mains int
	for _, tg := range tags(doc) {
		if id := tg.attrs["id"]; id != "" {
			ids[id]++
		}
		switch tg.name {
		case "h1":
			h1++
		case "main":
			mains++
		case "label":
			if f := tg.attrs["for"]; f != "" {
				refs = append(refs, [2]string{"label for", f})
			}
		}
		for _, k := range []string{"aria-controls", "aria-labelledby", "aria-describedby", "popovertarget"} {
			for _, id := range strings.Fields(tg.attrs[k]) {
				refs = append(refs, [2]string{k, id})
			}
		}
	}
	for id, n := range ids {
		if n > 1 {
			fail("duplicate id %q", id)
		}
	}
	for _, r := range refs {
		// The shared dialog is labelled by the heading of whatever form is swapped into it.
		if r[1] == "dialog-title" && r[0] == "aria-labelledby" {
			continue
		}
		if ids[r[1]] == 0 {
			fail("%s refers to a missing id %q", r[0], r[1])
		}
	}
	if h1 != 1 {
		fail("%d <h1> elements, want exactly 1", h1)
	}
	if mains != 1 {
		fail("%d <main> landmarks, want 1", mains)
	}
	for _, need := range []string{`<html lang="en">`, `class="skip" href="#main"`, `id="main"`, "<title>"} {
		if !strings.Contains(doc, need) {
			fail("missing %s", need)
		}
	}
}

// ---- tests -------------------------------------------------------------------

func TestTemplatesRenderAndLint(t *testing.T) {
	s := newWebServer(t)
	for _, f := range pageFixtures() {
		label := f.name + "/" + f.page.Title
		doc := renderPage(t, s, f)
		lint(t, label, doc)
		structure(t, label, doc)
		if f.page.User != nil && f.name != "signin" {
			if !strings.Contains(doc, `name="csrf-token"`) {
				t.Errorf("%s: a signed-in page must carry the CSRF meta tag", label)
			}
		}
		if f.page.User == nil && strings.Contains(doc, `id="palette"`) {
			t.Errorf("%s: the palette is rendered for a signed-out visitor", label)
		}
	}
	for _, f := range fragmentFixtures() {
		lint(t, "fragment "+f.name+"/"+f.block, renderFragment(t, s, f))
	}
	// Every template the server parsed has at least one fixture.
	covered := map[string]bool{}
	for _, f := range pageFixtures() {
		covered[f.name] = true
	}
	for _, f := range fragmentFixtures() {
		covered["partials/"+f.name] = true
	}
	for _, n := range s.Templates() {
		if !covered[n] {
			t.Errorf("template %q has no lint fixture; add one in web_test.go", n)
		}
	}
}

func TestLintCatchesWhatItShould(t *testing.T) {
	// A test of the linter itself, so it cannot silently stop checking.
	cases := map[string]string{
		"style attribute": `<div style="color:red">x</div>`,
		"style element":   `<style>a{}</style>`,
		"inline script":   `<script>alert(1)</script>`,
		"foreign script":  `<script src="https://cdn.example.com/x.js"></script>`,
		"event handler":   `<button onclick="x()">Go</button>`,
		"javascript url":  `<a href="javascript:alert(1)">x</a>`,
		"off-origin href": `<a href="https://example.com/">x</a>`,
		"off-origin hx":   `<a hx-get="//example.com/x">x</a>`,
		"hx-on":           `<a hx-on:click="x()">x</a>`,
		"exclamation":     `<p>Saved!</p>`,
		"oops":            `<p>Oops, no</p>`,
		"foreign font":    `<link rel="preload" href="https://fonts.example.com/a.woff2">`,
	}
	for name, doc := range cases {
		n := 0
		lintTo(func(string, ...any) { n++ }, doc)
		if n == 0 {
			t.Errorf("lint did not flag %s: %s", name, doc)
		}
	}
	clean := `<p>The issuer is https://idp.example.com, shown as text.</p><a href="/p/x">ok</a><script src="/static/js/shell.js" defer></script>`
	lintTo(func(f string, a ...any) { t.Errorf("lint flagged clean markup: "+f, a...) }, clean)
}

var (
	reStaticURL = regexp.MustCompile(`["'(](/static/[^"'()\s#?]+)`)
	reUse       = regexp.MustCompile(`<use href="(/static/[^"#]+)#([^"]+)"`)
	reAssetCall = regexp.MustCompile(`\{\{\s*asset\s+"([^"]+)"`)
	reCSSURL    = regexp.MustCompile(`url\(\s*["']?([^"')]+)["']?\s*\)`)
)

// Every /static URL a rendered page, a stylesheet or a template names must be served.
func TestEveryReferencedAssetIsServed(t *testing.T) {
	s := newWebServer(t)
	static := staticFS(t)
	get := func(u string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.assets.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		return rec
	}
	seen := map[string]string{}
	for _, f := range pageFixtures() {
		doc := renderPage(t, s, f)
		for _, m := range reStaticURL.FindAllStringSubmatch(doc, -1) {
			seen[m[1]] = f.name
		}
		for _, m := range reUse.FindAllStringSubmatch(doc, -1) {
			sprite := get(m[1]).Body.String()
			if !strings.Contains(sprite, `id="`+m[2]+`"`) {
				t.Errorf("%s: icon #%s is not in the sprite", f.name, m[2])
			}
		}
	}
	for _, f := range fragmentFixtures() {
		for _, m := range reStaticURL.FindAllStringSubmatch(renderFragment(t, s, f), -1) {
			seen[m[1]] = "fragment " + f.name
		}
	}
	// url() references inside stylesheets, resolved against the stylesheet's own path.
	cssFiles, _ := fs.Glob(static, "css/*.css")
	for _, cf := range cssFiles {
		b, _ := fs.ReadFile(static, cf)
		for _, m := range reCSSURL.FindAllStringSubmatch(string(b), -1) {
			if strings.HasPrefix(m[1], "data:") {
				continue
			}
			if strings.Contains(m[1], "://") || strings.HasPrefix(m[1], "//") {
				t.Errorf("%s: url(%s) is off-origin", cf, m[1])
				continue
			}
			seen["/static/"+path.Clean(path.Join(path.Dir(cf), m[1]))] = cf
		}
		if strings.Contains(string(b), "@import") {
			t.Errorf("%s: @import adds a request; link the file from the layout instead", cf)
		}
	}
	// asset "..." calls in template source.
	_ = fs.WalkDir(web.FS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(web.FS, p)
		for _, m := range reAssetCall.FindAllStringSubmatch(string(b), -1) {
			if !s.assets.Has(m[1]) {
				t.Errorf("%s: asset %q does not exist under web/static", p, m[1])
			}
		}
		return nil
	})
	var urls []string
	for u := range seen {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	if len(urls) < 6 {
		t.Fatalf("only %d asset URLs found; the scan is broken: %v", len(urls), urls)
	}
	for _, u := range urls {
		rec := get(u)
		if rec.Code != 200 {
			t.Errorf("%s (named by %s) answered %d", u, seen[u], rec.Code)
		} else if rec.Body.Len() == 0 {
			t.Errorf("%s (named by %s) is empty", u, seen[u])
		}
	}
	// The font preload must name the very file the stylesheet loads, or the face downloads twice.
	var preload string
	for _, tg := range tags(renderPage(t, s, pageFixtures()[0])) {
		if tg.name == "link" && tg.attrs["rel"] == "preload" {
			preload = tg.attrs["href"]
		}
	}
	if preload == "" || seen[preload] != "css/app.css" {
		t.Errorf("preload %q does not match a font the stylesheet requests", preload)
	}
}

var (
	reHex      = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	reFuncCol  = regexp.MustCompile(`\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(`)
	reCSSBlock = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	reComments = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// DESIGN.md: tokens first, no colour literal in a component, no gradients.
func TestStylesheetIsTokensFirst(t *testing.T) {
	static := staticFS(t)
	files, _ := fs.Glob(static, "css/*.css")
	if len(files) == 0 {
		t.Fatal("no stylesheet")
	}
	for _, f := range files {
		b, _ := fs.ReadFile(static, f)
		css := reComments.ReplaceAllString(string(b), "")
		if strings.Contains(strings.ToLower(css), "gradient(") {
			t.Errorf("%s: gradients are forbidden (invariant 9)", f)
		}
		for _, m := range reCSSBlock.FindAllStringSubmatch(css, -1) {
			sel, body := strings.TrimSpace(m[1]), m[2]
			if idx := strings.LastIndex(sel, "}"); idx >= 0 {
				sel = strings.TrimSpace(sel[idx+1:])
			}
			tokenBlock := strings.Contains(sel, ":root")
			swatch := regexp.MustCompile(`^(\.cover-\d, \.avatar-\d|\.cover-\d|\.avatar-\d|\.a-(moss|slate|sepia|meadow|contrast) i:(first|last)-child)$`).MatchString(sel) // a static site's accents are its colours, not the app's
			if tokenBlock || swatch {
				continue
			}
			if reHex.MatchString(body) || reFuncCol.MatchString(body) {
				t.Errorf("%s: colour literal in component rule %q: use a token", f, sel)
			}
		}
		for _, m := range regexp.MustCompile(`(?s)@font-face\s*\{([^}]*)\}`).FindAllStringSubmatch(css, -1) {
			if !strings.Contains(m[1], "font-display: swap") {
				t.Errorf("%s: @font-face without font-display: swap: %s", f, strings.TrimSpace(m[1]))
			}
		}
		if !strings.Contains(css, ":root") || strings.HasSuffix(f, "editor.css") || strings.HasSuffix(f, "themes.css") {
			continue // the theme and motion requirements belong to the token stylesheet
		}
		for _, need := range []string{"prefers-reduced-motion", "prefers-color-scheme: dark", `[data-theme="dark"]`, `[data-theme="light"]`, "--dur-1", "--ease"} {
			if !strings.Contains(css, need) {
				t.Errorf("%s: missing %s", f, need)
			}
		}
	}
}

// writeGlue is the writing view's own script, loaded on document routes only,
// with its own budget (TestWritingViewBudgets) instead of the shell's.
var writeGlue = []string{"js/doc.js", "js/tree.js"}

// lazyJS lists every script that is fetched on demand, or on one page only, with its own gzip
// budget instead of the shell's. A new lazy file is registered here, or the shell total counts it.
var lazyJS = map[string]int{
	"js/compare.js": 2 * 1024, "js/rowmenu.js": 3 * 1024, "js/link.js": 1536, "js/modes.js": 3 * 1024,
	"js/compendium.js":     1280,
	"js/format.js":         6 * 512,   // the writing view's formatting bar, the cue when it is wider than the screen, and the selected-words figure: wired a moment after the page loads
	"js/alts.js":           16 * 1024, // the writing view: alternatives and ghosts in the text (marks, cycling, keys, menu, the held editor and its checks), and the stash hook
	"js/altpanel.js":       5 * 1024,  // the Alternatives tab of the inspector
	"js/stash.js":          5 * 1024,  // the Stash tab of the inspector
	"js/git.js":            2 * 1024,
	"js/publish.js":        1280, // the release page only: follows an edition being built and uploaded, replacing its own fragment
	"js/inspector.js":      5 * 1024,
	"js/live.js":           2 * 1024,
	"js/livewrite.js":      2 * 1024,
	"js/mentions.js":       5 * 1024,
	"js/outsort.js":        2 * 1024,
	"js/scene.js":          4 * 1024,
	"js/today.js":          768,
	"js/versions.js":       5 * 1024,
	"js/wiki.js":           4 * 1024,
	"js/themes.js":         1024,      // the Settings theme picker, loaded on that page only
	"js/import.js":         8192,      // the import pages only: reads a folder or zip, renders the preview tree, edits, applies, takes back a change the server refuses, offers a retry when it is busy
	"js/import-books.js":   9216,      // the import pages only: the EPUB and PDF sources, the book, fidelity and tracked-changes panels of the preview, nest and join, the answer after an apply that kept an edition; about a hundred catalogue words
	"js/import-formats.js": 3584,      // the import pages only: the format chooser, Scrivener, WordPress and Ghost sources, the research notes and the converter's report in the preview
	"js/import-repo.js":    4608,      // the repository import page only: starts and follows the fetch, then hands the job to import.js
	"js/attach.js":         6144,      // the writing view (upload and insert) and the attachment pages: one file, loaded where files are handled
	"js/outline.js":        8 * 1024,  // the outline page only: inline edit, reorder by drag and by key, add, merge after each change
	"js/boards.js":         6656,      // the corkboard and the status board only: move a card, edit a synopsis, change a status
	"js/read.js":           3584,      // reading mode only: page keys, the remembered position, continuous scroll
	"js/entity.js":         2 * 1024,  // the entity bible pages only: the Markdown preview and the live refresh of the list
	"js/research.js":       5 * 512,   // the research list and board: focus after a change, board keys, live refresh
	"js/pad.js":            5 * 1024,  // the note and scratchpad pages: autosave with a revision, local draft, conflict choice, preview, clipping click-through
	"js/timeline.js":       5 * 1024,  // the timeline pages only: live date hints, the entity picker, keyboard moves of events
	"js/review.js":         14 * 1024, // the reading view for reviewers and up (comment margin, selection, keys) and the Comments tab of the writing view's inspector
	"js/compile.js":        8 * 1024,  // the Compile page: the options form built from the ops, the preview viewer, custom presets, the download with its manifest summary
	"js/when.js":           640,       // times the server wrote in UTC, shown in the person's own zone and language
	"js/arm.js":            1792,      // a confirmation a double tap cannot get through: bulk actions, remove a person, revoke a link, delete a comment
	"js/share-links.js":    1024,      // the share links page: the link shown once, with this server's address in front, and a copy button
	"js/connect.js":        1280,      // the static site, webhooks and connect-an-agent pages: this server's own origin in the snippets, copy buttons, focus on a secret shown once
	"js/gate.js":           4608,      // every page of a project that has a passphrase, once it is open: the cover over the page when the project locks, the unlock from it, Lock now
	"js/held.js":           3584,      // every page of a project that has a passphrase: a local draft for every field that is not saved yet, put back after an unlock
	"js/jump.js":           1024,      // the writing view opened from a search by meaning: scrolls to the passage, selected, and changes nothing
	"js/compile-print.js":  5 * 1024,  // the Compile page: the print proof drawn from the placed lines, the printer's checklist, the paperback cover form (the server draws and words it)
	"js/share.js":          8 * 1024,  // the page a share link opens: unlock, contents, chapters as they are reached, comments for a comment link
	"js/suggest.js":        7 * 1024,  // suggestion mode in the reading view: the marks, the Suggestions tab, accept and reject, the plan for accept all
	"js/aiadmin.js":        11776,     // /settings/ai and /p/{slug}/ai only: providers, policy, exclusions, budgets and usage, all through the ai.* ops
	"js/aiboot.js":         1280,      // the writing view when AI is on: wires the chord and loads ai.js on first use; the one AI file that rides with the page
	"js/ai.js":             12288,     // the writing partner's core (preview, run, proposals), fetched when an AI control is first used
	"js/aipanel.js":        2048,      // the AI tab of the inspector: its two views, each fetched when shown
	"js/aichat.js":         3584,      // the chat view
	"js/aireviews.js":      5632,      // the reviews view, the summary proposal and the extraction list
	"js/lab.js":            8192,      // the Lab tab: the local checks (no AI needed), marks in the text, accept and dismiss, accept all after a preview
	"js/sprint.js":         2944,      // the writing view, only while a sprint runs or when one is started from the status bar: the sprint line (one text, rewritten when the minute changes), the end's notice (of the sprint that ended, by its id and state) and the focus put back in the text (2,560 until the Phase 7.5 fix round, 2,816 after its first pass)
	"js/studio.js":         1280,      // the dashboard only: the calendar opens at its newest week, and a day's figure is read by tap, click or the arrow keys
	"js/cover.js":          5 * 1024,  // the cover page only: a picture chosen or dropped is sent at once, the position grid answers the up and down keys, and a page-mode front is redrawn as the controls change (an SVG placed by the server's own arithmetic)
	"js/card.js":           1536,      // the progress card page only: the link shown once with this server's address, copy, and the PNG drawn from the card's SVG on a canvas
}

func isWriteGlue(f string) bool {
	if _, ok := lazyJS[f]; ok {
		return true
	}
	for _, g := range writeGlue {
		if f == g {
			return true
		}
	}
	return false
}

func gzipSize(b []byte) int {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Len()
}

var reStylesheet = regexp.MustCompile(`<link rel="stylesheet" href="(/static/[^"]+)"`)

// docs/ARCHITECTURE.md "Performance budgets": initial CSS is at most 25 KB gzip for the page being loaded, that is
// the base sheet and the sheets that page links, not every sheet the app has. Each page of the app is rendered here
// and the sheets it names are summed.
func TestCSSBudgetPerPage(t *testing.T) {
	s := newWebServer(t)
	static := staticFS(t)
	logical := map[string]string{}
	for l, h := range s.assets.hashed {
		logical["/static/"+h] = l
	}
	size := map[string]int{}
	worst := map[string]int{}
	for _, f := range pageFixtures() {
		page := f.name
		if f.name == "write" {
			if wd, ok := f.page.Data.(writeData); ok && wd.Doc == nil {
				page = "write (empty project)"
			}
		}
		total := 0
		for _, m := range reStylesheet.FindAllStringSubmatch(renderPage(t, s, f), -1) {
			name := logical[m[1]]
			if name == "" {
				name = strings.TrimPrefix(m[1], "/static/")
			}
			if _, ok := size[name]; !ok {
				b, err := fs.ReadFile(static, name)
				if err != nil {
					t.Fatalf("%s links %s, which is not in the static files", f.name, m[1])
				}
				size[name] = gzipSize(b)
			}
			total += size[name]
		}
		if total > worst[page] {
			worst[page] = total
		}
	}
	for page, total := range worst {
		t.Logf("%-24s %6d bytes gzip of CSS on load", page, total)
		if total > 25*1024 {
			t.Errorf("%s loads %d bytes gzip of CSS, the per-page budget is %d", page, total, 25*1024)
		}
	}
}

// docs/ARCHITECTURE.md "Performance budgets".
func TestAssetBudgets(t *testing.T) {
	static := staticFS(t)

	total := 0
	css, _ := fs.Glob(static, "css/*.css")
	for _, f := range css {
		b, _ := fs.ReadFile(static, f)
		total += gzipSize(b)
	}
	t.Logf("CSS: %d bytes gzip across %d file(s)", total, len(css))
	// The 25 KB budget is per page load (TestCSSBudgetPerPage). The sum over every sheet only catches sprawl.
	if total > 96*1024 {
		t.Errorf("all stylesheets together are %d bytes gzip, the sprawl cap is %d", total, 96*1024)
	}

	// The shell is everything in static/js except the editor bundle, which has its own 180 KB budget.
	js, _ := fs.Glob(static, "js/*.js")
	total = 0
	for _, f := range js {
		if path.Base(f) == "editor.js" || isWriteGlue(f) {
			continue
		}
		b, _ := fs.ReadFile(static, f)
		n := gzipSize(b)
		t.Logf("  %s: %d bytes gzip", f, n)
		total += n
	}
	t.Logf("shell JS: %d bytes gzip", total)
	if total > 40*1024 {
		t.Errorf("shell JS (htmx included) is %d bytes gzip, budget is %d", total, 40*1024)
	}
	// The palette and switcher load on every signed-in page, with their own budget on top of the shell's total.
	if b, err := fs.ReadFile(static, "js/palette.js"); err != nil || len(b) < 500 {
		t.Errorf("palette.js is missing")
	} else if n := gzipSize(b); n > 12*1024 {
		t.Errorf("palette.js is %d bytes gzip, budget is %d", n, 12*1024)
	}
	if b, err := fs.ReadFile(static, "js/htmx.min.js"); err != nil || len(b) < 10000 {
		t.Errorf("htmx.min.js is missing or still a placeholder")
	}
	if b, _ := fs.ReadFile(static, "js/shell.js"); len(b) < 500 {
		t.Errorf("shell.js is missing or still a placeholder")
	}

	fonts, _ := fs.Glob(static, "fonts/*.woff2")
	if len(fonts) == 0 || len(fonts) > 4 {
		t.Errorf("%d font files, budget is 1 to 4: %v", len(fonts), fonts)
	}
	for _, f := range fonts {
		b, _ := fs.ReadFile(static, f)
		t.Logf("  %s: %d bytes", f, len(b))
	}
	if all, _ := fs.Glob(static, "fonts/*"); len(all) > 0 {
		for _, f := range all {
			if strings.HasSuffix(f, ".woff2") || strings.HasPrefix(path.Base(f), "OFL-") {
				continue
			}
			t.Errorf("unexpected file in fonts/: %s", f)
		}
	}
	sprite, _ := fs.ReadFile(static, "icons/sprite.svg")
	if n := strings.Count(string(sprite), "<symbol "); n == 0 || n > 48 {
		t.Errorf("sprite has %d symbols; expected 1 to 48", n)
	}
	if strings.Contains(strings.ToLower(string(sprite)), "gradient") {
		t.Errorf("sprite contains a gradient")
	}
}

func ownerFixture(p projects.Project) projects.Project {
	p.Role = "owner"
	p.WordCount, p.WordGoal = 4200, 60000
	return p
}

func archivedFixture(in []projects.Project) []projects.Project {
	out := append([]projects.Project(nil), in...)
	for i := range out {
		out[i].Role, out[i].Archived = "owner", true
	}
	return out
}

func trashedFixture(in []projects.Project) []projects.Project {
	out := append([]projects.Project(nil), in...)
	for i := range out {
		out[i].Role, out[i].Trashed, out[i].DocumentCount = "owner", true, 12
	}
	return out
}

func settingsFixture(errMsg string) settingsData {
	d := settingsData{Profile: account.Profile{ID: "usr_1", Email: "wren@example.com", DisplayName: "Wren Writer", Timezone: "Europe/Dublin", Locale: "en"},
		Prefs: defaultPrefs(), CSRF: "tok_writer", Error: errMsg, Others: 2,
		Sessions: []sessionRow{{ID: "ses_1", Agent: "Chrome on macOS", When: "Last used today", Current: true}, {ID: "ses_2", Agent: "Safari on iOS", When: "Last used yesterday"}, {ID: "ses_3", Agent: "Firefox", When: "Last used last week"}}}
	for _, l := range account.Locales {
		d.Locales = append(d.Locales, opt{l, localeName(l)})
	}
	d.Look = []selectField{{Name: "theme", Label: "Theme", Live: "theme", Current: "system", Options: opts("system", "Follow the system", "dark", "Ink")}}
	d.Editor = []selectField{{Name: "editor_size", Label: "Size", Current: "19", Options: rangeOpts(16, 24, 1, " px")}}
	d.Checks = []checkField{{Name: "show_word_count", Label: "Show counts", On: true}}
	if errMsg != "" {
		d.Field = "display_name"
	}
	return d
}

func importTarget() *projects.Project {
	p := ownerFixture(fixtureProjects()[0])
	return &p
}

func settingsBackupsFixture(problem bool) settingsData {
	d := settingsFixture("")
	d.Backups = &backupsView{Rows: []backupRow{
		{ID: "auto-2026-11-13T03-00-00", Kind: "Scheduled", When: "13 Nov 2026, 03:00", Size: "4.2 MB"},
		{ID: "2026-11-12T18-20-11", Kind: "Manual", When: "12 Nov 2026, 18:20", Size: "4.1 MB"}}}
	if problem {
		d.Backups = &backupsView{Problem: "Backups are not available on this server."}
	}
	return d
}

func searchFixture(single bool, q string, hits bool) searchData {
	pr := ownerFixture(fixtureProjects()[0])
	d := searchData{Q: q, Searched: q != "", CSRF: "tok_writer"}
	if single {
		d.Project = &pr
	}
	if hits {
		h := []hitView{
			{ID: "doc_a", Title: "The <b>Harbour</b> at Low Tide", Kind: "Chapter", MatchedIn: "body", URL: "/p/p-a/d/doc_a", Words: 2140,
				Parts: snippetParts("the <mark>harb</mark>our was a bowl of &lt;script&gt;alert(1)&lt;/script&gt; mud")},
			{ID: "doc_b", Title: "Notes", Kind: "Note", MatchedIn: "title", URL: "/p/p-a/d/doc_b", Words: 1, Parts: snippetParts("Notes")},
		}
		d.Groups = []searchGroup{{Project: pr, Hits: h, Total: 7, More: "/p/p-a/search?q=" + q}}
		d.Total, d.From, d.To = 7, 1, 2
		if single {
			d.Next, d.Prev = "/p/p-a/search?q=harb&offset=20", "/p/p-a/search?q=harb"
		}
		d.Degraded = true
	}
	if q == "" && !single {
		d.Message = "Type a word to search for."
	}
	return d
}
