package imports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type env struct {
	t   *testing.T
	d   *db.DB
	reg *ops.Registry
	w   ops.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, d: d, reg: registry.Build(d, nil, t.TempDir())}
	e.w = e.user("writer", ops.RoleWriter)
	return e
}

func (e *env) user(name, role string) ops.Principal {
	id := testutil.User(e.t, e.d, role, name+"@test.local")
	return ops.Principal{UserID: id, Role: role}
}

func (e *env) call(p ops.Principal, op string, in any) (any, error) {
	return e.reg.Call(context.Background(), p, op, in)
}

func (e *env) must(p ops.Principal, op string, in any) any {
	e.t.Helper()
	out, err := e.call(p, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

// files builds a listing from path -> text.
func files(m map[string]string) []imports.File {
	var paths []string
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]imports.File, 0, len(m))
	for _, p := range paths {
		out = append(out, imports.File{Path: p, Data: []byte(m[p])})
	}
	return out
}

func (e *env) preview(f []imports.File, split string) imports.PreviewOut {
	e.t.Helper()
	return e.must(e.w, "import.preview", imports.PreviewIn{Name: "the-salt-road", Files: f, SplitScenes: split}).(imports.PreviewOut)
}

func (e *env) apply(f []imports.File, split string, mut func(*imports.ApplyIn)) imports.ApplyOut {
	e.t.Helper()
	pv := e.preview(f, split)
	in := imports.ApplyIn{Token: pv.Token, Name: "the-salt-road", Files: f, SplitScenes: split}
	if mut != nil {
		mut(&in)
	}
	return e.must(e.w, "import.apply", in).(imports.ApplyOut)
}

// outline renders nodes as "kind:title" lines indented by depth.
func outline(nodes []imports.Node) []string {
	depth := map[string]int{"": 0}
	var out []string
	for _, n := range nodes {
		d := depth[n.ParentKey]
		depth[n.Key] = d + 1
		out = append(out, strings.Repeat("  ", d)+n.Kind+":"+n.Title)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGenericFolderMapping(t *testing.T) {
	e := newEnv(t)
	pv := e.preview(files(map[string]string{
		"README.md":                       "About this folder.\n",
		"front-matter.md":                 "Copyright 2026.\n",
		"01-the-beginning/01-arrival.md":  "# Arrival at Dusk\n\nThe bus left them at the crossroads.\n",
		"01-the-beginning/02-the_road.md": "They walked.\n",
		"02-middle/10-chapter-ten.md":     "Ten.\n",
		"02-middle/2-chapter-two.md":      "---\ntitle: \"The Second Chapter\"\nauthor: x\n---\nTwo.\n",
		"acknowledgements.md":             "Thanks.\n",
		"03.md":                           "Plain.\n",
		"notes.txt":                       "not markdown",
		"cover.png":                       "",
		".hidden/secret.md":               "hidden",
		"../evil.md":                      "traversal",
		"/abs.md":                         "absolute",
		"a/../b.md":                       "dots",
		"C:/drive.md":                     "drive",
	}), "")
	if pv.Layout != imports.LayoutMarkdown {
		t.Fatalf("layout %q", pv.Layout)
	}
	eq(t, outline(pv.Nodes), []string{
		"front_matter:Front Matter",
		"part:The Beginning",
		"  chapter:Arrival at Dusk",
		"  chapter:The Road",
		"part:Middle",
		"  chapter:The Second Chapter",
		"  chapter:Chapter Ten",
		"chapter:Chapter 3",
		"note:README",
		"back_matter:Acknowledgements",
	})
	reasons := pv.SkippedByReason
	if reasons["not a Markdown file"] != 2 || reasons["hidden file or folder"] != 1 || reasons["unsafe path"] != 4 {
		t.Errorf("skip reasons: %v (%+v)", reasons, pv.Skipped)
	}
	// The unsafe paths are reported, not echoed as files to import.
	for _, n := range pv.Nodes {
		if strings.Contains(n.SourcePath, "evil") || strings.Contains(n.SourcePath, "abs") {
			t.Errorf("hostile path became a node: %+v", n)
		}
	}
	// The H1 became the title and left the text; the front matter stayed in the text.
	e2 := e.apply(files(map[string]string{
		"02-middle/2-chapter-two.md": "---\ntitle: \"The Second Chapter\"\nauthor: x\n---\nTwo.\n",
		"01-arrival.md":              "# Arrival at Dusk\n\nThe bus left them at the crossroads.\n",
	}), "", nil)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: e2.ProjectID}).(docs.ListOut)
	got := map[string]string{}
	for _, id := range list.Order {
		d := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
		got[d.Title] = d.Content
	}
	if got["Arrival at Dusk"] != "The bus left them at the crossroads.\n" {
		t.Errorf("H1 should leave the body: %q", got["Arrival at Dusk"])
	}
	if got["The Second Chapter"] != "---\ntitle: \"The Second Chapter\"\nauthor: x\n---\nTwo.\n" {
		t.Errorf("front matter must stay in the text: %q", got["The Second Chapter"])
	}
}

func TestNormalisationAndBadFiles(t *testing.T) {
	e := newEnv(t)
	big := bytes.Repeat([]byte("word "), 500_000) // 2.5 MB
	f := []imports.File{
		{Path: "01-crlf.md", Data: []byte("\xEF\xBB\xBFLine one\r\nLine two\rLine three\n")},
		{Path: "02-binary.md", Data: []byte{0xff, 0xfe, 'a', 0x00, 'b'}},
		{Path: "03-latin1.md", Data: []byte("caf\xe9 au lait")},
		{Path: "04-huge.md", Data: big},
		{Path: "05-unsent.md", Size: 10_000_000},
		{Path: "06-empty.md"},
		{Path: "07-link.md", Size: -1},
		{Path: "dup.md", Data: []byte("a")},
		{Path: "dup.md", Data: []byte("b")},
	}
	pv := e.preview(f, "")
	eq(t, outline(pv.Nodes), []string{"chapter:Crlf", "chapter:Empty", "chapter:Dup"})
	want := map[string]string{
		"02-binary.md": "not valid UTF-8 text", "03-latin1.md": "not valid UTF-8 text", "04-huge.md": "larger than the 2 MiB file limit",
		"05-unsent.md": "larger than the 2 MiB file limit", "07-link.md": "symbolic link", "dup.md": "duplicate path",
	}
	for _, s := range pv.Skipped {
		if want[s.Path] != s.Reason {
			t.Errorf("%s: %q, want %q", s.Path, s.Reason, want[s.Path])
		}
		delete(want, s.Path)
	}
	if len(want) != 0 {
		t.Errorf("not reported as skipped: %v", want)
	}
	joined := strings.Join(pv.Warnings, " ")
	if !strings.Contains(joined, "1 empty file") {
		t.Errorf("an empty file should warn: %v", pv.Warnings)
	}
	out := e.apply(f, "", nil)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	first := e.must(e.w, "doc.get", docs.DocIn{ID: list.Order[0]}).(docs.Item).Item
	if first.Content != "Line one\nLine two\nLine three\n" {
		t.Errorf("BOM and CRLF should be normalised: %q", first.Content)
	}
}

func TestEmptyAndHostileFolders(t *testing.T) {
	e := newEnv(t)
	pv := e.preview(nil, "")
	if pv.Documents != 0 || len(pv.Warnings) == 0 {
		t.Errorf("an empty folder previews as nothing, with a warning: %+v", pv)
	}
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token})
	if ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("applying nothing: %v", err)
	}
	// Only non-Markdown.
	pv = e.preview([]imports.File{{Path: "a.docx", Size: 100}, {Path: "b.png", Size: 5}}, "")
	if pv.Documents != 0 || pv.SkippedTotal != 2 {
		t.Errorf("%+v", pv)
	}
	// Caps.
	var many []imports.File
	for i := 0; i < imports.MaxMarkdown+1; i++ {
		many = append(many, imports.File{Path: fmt.Sprintf("c%04d.md", i), Data: []byte("x")})
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Files: many}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("too many Markdown files: %v", err)
	}
	var tooDeep imports.File
	tooDeep.Path = strings.Repeat("d/", imports.MaxDepth+1) + "x.md"
	tooDeep.Data = []byte("x")
	long := imports.File{Path: strings.Repeat("a", imports.MaxPathBytes) + ".md", Data: []byte("x")}
	pv = e.preview([]imports.File{tooDeep, long}, "")
	if pv.SkippedByReason["nested too deeply"] != 1 || pv.SkippedByReason["path too long"] != 1 {
		t.Errorf("%v", pv.SkippedByReason)
	}
	// Total bytes.
	var heavy []imports.File
	chunk := bytes.Repeat([]byte("a "), 1<<20-1) // just under 2 MiB
	for i := 0; i < 17; i++ {
		heavy = append(heavy, imports.File{Path: fmt.Sprintf("h%02d.md", i), Data: chunk})
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Files: heavy}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("over the byte cap: %v", err)
	}
	// Both files and a zip is ambiguous.
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Files: files(map[string]string{"a.md": "x"}), Zip: []byte("PK")}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("files and zip: %v", err)
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Zip: []byte("not a zip")}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("garbage zip: %v", err)
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{SplitScenes: "h3"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("bad split option: %v", err)
	}
}

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var names []string
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(entries[n]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestZipUploadsAreBoundedAndSanitised(t *testing.T) {
	e := newEnv(t)
	z := zipOf(t, map[string]string{
		"book/01-one.md":        "One.\n",
		"book/02-two.md":        "Two.\n",
		"book/../../escape.md":  "zip slip",
		"book/img/cover.png":    "png",
		"__MACOSX/book/._01.md": "junk",
	})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Zip: z, Name: "book"}).(imports.PreviewOut)
	// The one wrapper folder is stripped, so the layout is the same as the folder's own.
	eq(t, outline(pv.Nodes), []string{"chapter:One", "chapter:Two"})
	if pv.SkippedByReason["unsafe path"] != 1 || pv.SkippedByReason["hidden file or folder"] != 1 {
		t.Errorf("%v", pv.SkippedByReason)
	}
	// An entry that claims to be small but inflates past the file limit is refused.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("big.md")
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < 4; i++ {
		w.Write(chunk)
	}
	zw.Close()
	pv = e.must(e.w, "import.preview", imports.PreviewIn{Zip: buf.Bytes()}).(imports.PreviewOut)
	if pv.Documents != 0 || pv.SkippedByReason["larger than the 2 MiB file limit"] != 1 {
		t.Errorf("an oversize zip entry: %+v", pv.SkippedByReason)
	}
}

func TestSplitByHeading(t *testing.T) {
	e := newEnv(t)
	chapter := "# The Crossing\n\nOpening paragraph.\n\n## The Dock\n\nShe waited.\n\n```\n## not a heading\n```\n\n## The Boat\n\nIt rocked.\n"
	f := files(map[string]string{"01-crossing.md": chapter, "02-plain.md": "No headings here.\n"})

	pv := e.preview(f, "")
	if pv.Nodes[0].Headings != 2 || pv.Nodes[0].Split {
		t.Errorf("by default a file is not split, but the preview says how many headings it has: %+v", pv.Nodes[0])
	}
	eq(t, outline(pv.Nodes), []string{"chapter:The Crossing", "chapter:Plain"})

	pv = e.preview(f, "h2")
	eq(t, outline(pv.Nodes), []string{"chapter:The Crossing", "  scene:The Dock", "  scene:The Boat", "chapter:Plain"})

	out := e.apply(f, "h2", nil)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	var bodies []string
	for _, id := range list.Order {
		bodies = append(bodies, e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item.Content)
	}
	want := []string{"Opening paragraph.\n", "She waited.\n\n```\n## not a heading\n```\n", "It rocked.\n", "No headings here.\n"}
	eq(t, bodies, want)

	// One file can be split or kept whole whatever the default says.
	keep := false
	out = e.apply(f, "h2", func(in *imports.ApplyIn) { in.Edits = []imports.Edit{{Key: "01-crossing.md", Split: &keep}} })
	if out.Documents != 2 {
		t.Errorf("a per-file split=false keeps the file whole: %d documents", out.Documents)
	}
	yes := true
	out = e.apply(f, "", func(in *imports.ApplyIn) { in.Edits = []imports.Edit{{Key: "01-crossing.md", Split: &yes}} })
	if out.Documents != 4 {
		t.Errorf("a per-file split=true: %d documents", out.Documents)
	}
}

func TestApplyCreatesTheProjectInOneTransaction(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"01-part/01-a.md": "Alpha beta gamma.\n",
		"01-part/02-b.md": "Delta epsilon.\n",
		"02-c.md":         "Zeta.\n",
	})
	out := e.apply(f, "", nil)
	if out.Documents != 4 || out.Words != 6 || out.Slug != "the-salt-road" || out.SourceID == "" {
		t.Fatalf("%+v", out)
	}
	p := e.must(e.w, "project.get", projects.GetIn{ID: out.ProjectID}).(projects.Item).Item
	if p.Title != "The Salt Road" || p.Role != "owner" || p.WordCount != 6 || p.DocumentCount != 4 {
		t.Errorf("%+v", p)
	}
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	var titles []string
	byID := map[string]docs.Meta{}
	for _, m := range list.Items {
		byID[m.ID] = m
	}
	for _, id := range list.Order {
		titles = append(titles, byID[id].Kind+":"+byID[id].Title)
	}
	eq(t, titles, []string{"part:Part", "chapter:A", "chapter:B", "chapter:C"})
	// Search text exists for every document (FTS is in step), revision is 1.
	var n, nonRev1 int
	e.d.Read().QueryRow(`SELECT count(*) FROM doc_text WHERE project_id = ?`, out.ProjectID).Scan(&n)
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND revision != 1`, out.ProjectID).Scan(&nonRev1)
	if n != 4 || nonRev1 != 0 {
		t.Errorf("search rows %d, documents with revision != 1: %d", n, nonRev1)
	}
	// Positions are gaps, in order, per parent.
	var bad int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND position != round(position) OR position <= 0`, out.ProjectID).Scan(&bad)
	if bad != 0 {
		t.Errorf("%d documents with odd positions", bad)
	}
	// Provenance.
	var kind, hash, path string
	if err := e.d.Read().QueryRow(`SELECT kind, source_hash, source_path FROM project_sources WHERE id = ?`, out.SourceID).Scan(&kind, &hash, &path); err != nil {
		t.Fatal(err)
	}
	pv := e.preview(f, "")
	if kind != "markdown" || hash != pv.Token || path != "the-salt-road" {
		t.Errorf("source: %s %s %s", kind, hash, path)
	}
	var docSources int
	var hashes int
	e.d.Read().QueryRow(`SELECT count(*), count(DISTINCT source_hash) FROM document_sources WHERE source_id = ?`, out.SourceID).Scan(&docSources, &hashes)
	if docSources != 3 || hashes != 3 { // the folder has no file of its own
		t.Errorf("document_sources %d rows, %d distinct hashes", docSources, hashes)
	}
	// The editor can open and save an imported document like any other.
	id := list.Order[1]
	doc := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	text := doc.Content + "More.\n"
	e.must(e.w, "doc.update", docs.UpdateIn{ID: id, BaseRevision: doc.Revision, Content: &text})
}

func TestApplyNeedsTheTokenOfTheSameFiles(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{"a.md": "One.\n"})
	pv := e.preview(f, "")
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Files: f}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("no token: %v", err)
	}
	changed := files(map[string]string{"a.md": "One, changed.\n"})
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: changed})
	if ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("changed files: %v", err)
	}
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
	if n != 0 {
		t.Errorf("a refused import created %d projects", n)
	}
}

func TestEditsRenameKindSkipReorder(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"01-one.md":       "One.\n",
		"02-two.md":       "Two.\n",
		"03-three.md":     "Three.\n",
		"04-draft/a.md":   "A.\n",
		"04-draft/b.md":   "B.\n",
		"05-scratch/x.md": "X.\n",
	})
	title, kind := "Renamed Two", "scene"
	out := e.apply(f, "", func(in *imports.ApplyIn) {
		in.Title = "My Book"
		in.Kind = "novel"
		in.Edits = []imports.Edit{
			{Key: "02-two.md", Title: &title, Kind: &kind},
			{Key: "05-scratch/", Skip: true},
			{Key: "01-one.md", Skip: true},
		}
		in.Reorder = []imports.Reorder{
			{Parent: "", Keys: []string{"04-draft/", "03-three.md"}},
			{Parent: "04-draft/", Keys: []string{"04-draft/b.md"}},
		}
	})
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	byID := map[string]docs.Meta{}
	for _, m := range list.Items {
		byID[m.ID] = m
	}
	var got []string
	for _, id := range list.Order {
		got = append(got, byID[id].Kind+":"+byID[id].Title)
	}
	eq(t, got, []string{"part:Draft", "chapter:B", "chapter:A", "chapter:Three", "scene:Renamed Two"})
	p := e.must(e.w, "project.get", projects.GetIn{ID: out.ProjectID}).(projects.Item).Item
	if p.Title != "My Book" || p.Kind != "novel" {
		t.Errorf("%+v", p)
	}
	// Bad edits are refused before anything is written.
	pv := e.preview(f, "")
	bad := []imports.ApplyIn{
		{Edits: []imports.Edit{{Key: "no-such.md", Skip: true}}},
		{Edits: []imports.Edit{{Key: "01-one.md", Kind: ptr("chapterish")}}},
		{Edits: []imports.Edit{{Key: "01-one.md", Title: ptr("   ")}}},
		{Reorder: []imports.Reorder{{Parent: "", Keys: []string{"04-draft/a.md"}}}},
		{Reorder: []imports.Reorder{{Parent: "", Keys: []string{"01-one.md", "01-one.md"}}}},
	}
	for i, in := range bad {
		in.Token, in.Files = pv.Token, f
		if _, err := e.call(e.w, "import.apply", in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("bad edit %d: %v", i, err)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestNothingIsKeptWhenAnImportFailsPartWay(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{"01.md": "a b", "02.md": "c d", "03.md": "e f", "04.md": "g h"})
	pv := e.preview(f, "")
	imports.FailAfter(func(n int) error {
		if n == 3 {
			return fmt.Errorf("injected failure")
		}
		return nil
	})
	defer imports.FailAfter(nil)
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f})
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	for _, table := range []string{"projects", "documents", "doc_text", "project_sources", "document_sources", "project_members"} {
		var n int
		e.d.Read().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		if n != 0 {
			t.Errorf("%s holds %d rows after a failed import", table, n)
		}
	}
}

func TestAppendToAnExistingProject(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Existing"}).(projects.Item).Item.ID
	first := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Already here", Content: "Keep me."}).(docs.Item).Item
	before := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)

	f := files(map[string]string{"01-new-one.md": "New one.\n", "02-new-two.md": "New two.\n"})
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f}).(imports.PreviewOut)
	if pv.Documents != 2 {
		t.Fatalf("%+v", pv)
	}
	out := e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Files: f}).(imports.ApplyOut)
	if out.ProjectID != pid || out.Documents != 2 || out.Slug != "" {
		t.Errorf("%+v", out)
	}
	after := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	byID := map[string]docs.Meta{}
	for _, m := range after.Items {
		byID[m.ID] = m
	}
	var titles []string
	for _, id := range after.Order {
		titles = append(titles, byID[id].Title)
	}
	eq(t, titles, append(append([]string{}, titlesOf(before)...), "New One", "New Two"))
	// Existing documents are untouched.
	d := e.must(e.w, "doc.get", docs.DocIn{ID: first.ID}).(docs.Item).Item
	if d.Content != "Keep me." || d.Revision != 1 {
		t.Errorf("existing document changed: %+v", d.Meta)
	}
	// Into a folder.
	part := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Part", Kind: "part"}).(docs.Item).Item
	e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, ParentID: part.ID, Token: pv.Token, Files: f})
	sub := e.must(e.w, "doc.get", docs.DocIn{ID: part.ID}).(docs.Item).Item
	_ = sub
	var kids int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE parent_id = ?`, part.ID).Scan(&kids)
	if kids != 2 {
		t.Errorf("%d documents in the folder", kids)
	}
	if _, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: pid, ParentID: "doc_00000000000000000000000000", Token: pv.Token, Files: f}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a parent that does not exist: %v", err)
	}
}

func titlesOf(l docs.ListOut) []string {
	byID := map[string]docs.Meta{}
	for _, m := range l.Items {
		byID[m.ID] = m
	}
	var out []string
	for _, id := range l.Order {
		out = append(out, byID[id].Title)
	}
	return out
}

func TestImportPermissions(t *testing.T) {
	e := newEnv(t)
	reader := e.user("reader", ops.RoleReader)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Mine"}).(projects.Item).Item.ID
	f := files(map[string]string{"a.md": "x"})
	// An instance reader cannot create projects, so cannot import into a new one.
	if _, err := e.call(reader, "import.preview", imports.PreviewIn{Files: f}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("reader preview: %v", err)
	}
	// A stranger cannot see the project to append to it.
	stranger := e.user("stranger", ops.RoleWriter)
	if _, err := e.call(stranger, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("stranger: %v", err)
	}
	// A project reader cannot append.
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, stranger.UserID, "reader", db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.call(stranger, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("project reader: %v", err)
	}
}

func TestThreeHundredFilesAreFast(t *testing.T) {
	e := newEnv(t)
	m := map[string]string{}
	para := strings.Repeat("The salt wind carried the smell of tar and old rope across the harbour. ", 40)
	for i := 0; i < 300; i++ {
		m[fmt.Sprintf("%02d-part/%03d-chapter-%d.md", i/30, i%30, i)] = fmt.Sprintf("# Chapter %d\n\n%s\n\n## Scene\n\n%s\n", i, para, para)
	}
	f := files(m)
	start := time.Now()
	pv := e.preview(f, "h2")
	previewTook := time.Since(start)
	if pv.Documents != 300+300+10 {
		t.Fatalf("documents %d", pv.Documents)
	}
	start = time.Now()
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, SplitScenes: "h2"}).(imports.ApplyOut)
	applyTook := time.Since(start)
	t.Logf("300 files: preview %v, apply %v", previewTook, applyTook)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if previewTook > 3*time.Second*timingFactor || applyTook > 5*time.Second*timingFactor {
			t.Errorf("preview %v, apply %v", previewTook, applyTook)
		}
	})
	if out.Documents != 610 {
		t.Errorf("documents %d", out.Documents)
	}
}

// ---- the Scríobh layouts ------------------------------------------------------------

// build creates a project that uses every shape the export lays out.
func (e *env) buildSource() string {
	e.t.Helper()
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Dún na nGall: A/B?", Kind: "novel", AuthorName: "M. Ó Néill", Language: "ga"}).(projects.Item).Item.ID
	// Remove the seeded chapter.
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range list.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	mk := func(title, kind, content, parent string) string {
		return e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, Content: content, ParentID: parent}).(docs.Item).Item.ID
	}
	mk("Front Matter", "front_matter", "Copyright.\n", "")
	part1 := mk("Part One: The Salt Road", "part", "Part one epigraph.\n", "")
	ch1 := mk("Arrival", "chapter", "# Arrival heading kept\n\nThey came at dusk.\n", part1)
	mk("The Dock", "scene", "She waited.\r\nThe boat came.\n", ch1)
	mk("Same Title", "scene", "Scene two.\n", ch1)
	mk("Same Title", "chapter", "A chapter with the same title.\n", part1)
	part2 := mk("Part Two", "part", "", "") // a part with no text of its own
	mk("Departure", "chapter", "---\ntitle: Departure\n---\nThey left.\n", part2)
	mk("Afterword", "back_matter", "The end.\n", "")
	st := "revise"
	revID := mk("Revise me", "chapter", "Status test.\n", "")
	cur := e.must(e.w, "doc.get", docs.DocIn{ID: revID}).(docs.Item).Item
	e.must(e.w, "doc.update", docs.UpdateIn{ID: revID, BaseRevision: cur.Revision, Status: &st})
	// Notes are exported in their own section, so a note returns after the
	// manuscript whatever it was next to: it is created last here.
	mk("Research note", "note", "A note to self.\n", "")
	return pid
}

// snapshot is the tree of a project as kind/title/status/content, indented.
func (e *env) snapshot(pid string, normaliseCRLF bool) []string {
	e.t.Helper()
	l := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	byID := map[string]docs.Meta{}
	for _, m := range l.Items {
		byID[m.ID] = m
	}
	depth := map[string]int{"": 0}
	var out []string
	for _, id := range l.Order {
		m := byID[id]
		d := depth[m.ParentID]
		depth[m.ID] = d + 1
		doc := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
		c := doc.Content
		if normaliseCRLF {
			c = strings.ReplaceAll(c, "\r\n", "\n")
		}
		out = append(out, fmt.Sprintf("%s%s|%s|%s|%q", strings.Repeat("  ", d), m.Kind, m.Title, m.Status, c))
	}
	return out
}

func TestRoundTripThroughTheExportBundle(t *testing.T) {
	e := newEnv(t)
	src := e.buildSource()
	exp := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: src, Mode: "bundle", Notes: true}).(export.File)

	for _, wrap := range []bool{false, true} {
		fs, err := imports.ReadZip(exp.Body)
		if err != nil {
			t.Fatal(err)
		}
		in := imports.PreviewIn{Files: fs, Name: "ignored"}
		if wrap { // a zipped folder arrives inside a wrapper folder named after it
			entries := map[string]string{}
			for _, f := range fs {
				entries["export-2026/"+f.Path] = string(f.Data)
			}
			in = imports.PreviewIn{Zip: zipOf(t, entries), Name: "ignored"}
		}
		pv := e.must(e.w, "import.preview", in).(imports.PreviewOut)
		if pv.Layout != imports.LayoutScriobh || pv.ManifestFile != "scriobh.json" {
			t.Fatalf("layout %q manifest %q", pv.Layout, pv.ManifestFile)
		}
		if pv.Project.Title != "Dún na nGall: A/B?" || pv.Project.Kind != "novel" || pv.Project.Author != "M. Ó Néill" || pv.Project.Language != "ga" {
			t.Errorf("project: %+v", pv.Project)
		}
		if len(pv.Warnings) != 0 {
			t.Errorf("a clean export should import without warnings: %v", pv.Warnings)
		}
		out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: in.Files, Zip: in.Zip}).(imports.ApplyOut)
		want := e.snapshot(src, true)
		got := e.snapshot(out.ProjectID, true)
		// The source's seeded chapter was trashed; the import has no trash. Order, kinds,
		// titles, statuses and text are what must match.
		eq(t, got, want)
		p := e.must(e.w, "project.get", projects.GetIn{ID: out.ProjectID}).(projects.Item).Item
		if p.Title != "Dún na nGall: A/B?" || p.AuthorName != "M. Ó Néill" || p.Language != "ga" || p.Kind != "novel" {
			t.Errorf("project metadata: %+v", p)
		}
		if out.ProjectID == src {
			t.Error("an import must mint new ids")
		}
		var shared int
		e.d.Read().QueryRow(`SELECT count(*) FROM documents a JOIN documents b ON a.id = b.id WHERE a.project_id = ? AND b.project_id = ?`, src, out.ProjectID).Scan(&shared)
		if shared != 0 {
			t.Error("documents share ids with the source")
		}
	}
}

func TestScriobhManifestIsUntrusted(t *testing.T) {
	e := newEnv(t)
	manifest := `{"project":{"title":"` + strings.Repeat("T", 400) + `","kind":"<script>","language":"en us"},
	 "documents":[
	  {"id":"doc_VICTIM","path":"manuscript/01-a.md","kind":"research","title":"Evil","status":"published"},
	  {"id":"x","path":"../../etc/passwd","kind":"chapter","title":"traversal"},
	  {"path":"manuscript/99-missing.md","kind":"chapter","title":"Missing"},
	  {"path":"manuscript/02-b.md","kind":"scene","title":"` + strings.Repeat("x", 500) + `"}
	 ]}`
	f := files(map[string]string{
		"scriobh.json":       manifest,
		"manuscript/01-a.md": "A.\n",
		"manuscript/02-b.md": "B.\n",
		"manuscript/03-c.md": "C.\n",
		"characters/ada.md":  "an entity",
		"README.md":          "generated",
	})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f}).(imports.PreviewOut)
	if pv.Project.Kind != "blank" || len(pv.Project.Title) != 200 || pv.Project.Language != "en" {
		t.Errorf("project: %q %d %q", pv.Project.Kind, len(pv.Project.Title), pv.Project.Language)
	}
	var titles []string
	for _, n := range pv.Nodes {
		titles = append(titles, n.Kind+":"+n.Status+":"+n.Title[:min(len(n.Title), 6)])
	}
	// An unknown kind falls back to chapter, a status the app does not have to draft; a long title is cut.
	eq(t, titles, []string{"chapter:draft:Evil", "scene:draft:xxxxxx", "chapter:draft:C"})
	if pv.SkippedByReason["outside the manuscript"] != 1 || pv.SkippedByReason["generated README"] != 1 {
		t.Errorf("%v", pv.SkippedByReason)
	}
	joined := strings.Join(pv.Warnings, "\n")
	if !strings.Contains(joined, "not listed in the manifest") || !strings.Contains(joined, "not supplied") || !strings.Contains(joined, "shortened") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
	// Whatever ids the manifest names, the documents get new ones.
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f}).(imports.ApplyOut)
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE id = 'doc_VICTIM'`).Scan(&n)
	if n != 0 || out.Documents != 3 {
		t.Errorf("manifest ids must never be used (%d), documents %d", n, out.Documents)
	}
}

func TestProjectJSONAndBrokenManifests(t *testing.T) {
	e := newEnv(t)
	// A mirror's project.json with a project block and no document list: the tree comes from the folders.
	f := files(map[string]string{
		"project.json":                      `{"id":"prj_x","title":"From Mirror","kind":"novella","author_name":"A. Writer"}`,
		"manuscript/00-front-matter.md":     "Front.\n",
		"manuscript/01-part-one/01-ch-a.md": "A.\n",
		"manuscript/01-part-one/02-ch-b.md": "B.\n",
		"README.md":                         "generated; edits here are not read back",
	})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutScriobh || pv.ManifestFile != "project.json" || pv.Project.Title != "From Mirror" || pv.Project.Kind != "novella" || pv.Project.Author != "A. Writer" {
		t.Errorf("%+v", pv)
	}
	// Without a manifest entry, a name like 00-front-matter still says what the file is.
	eq(t, outline(pv.Nodes), []string{"front_matter:Front Matter", "part:Part One", "  chapter:Ch A", "  chapter:Ch B"})
	// A broken manifest is ignored with a warning and the folder is read generically.
	f2 := files(map[string]string{"scriobh.json": "{not json", "01-a.md": "A.\n"})
	pv = e.must(e.w, "import.preview", imports.PreviewIn{Files: f2}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutMarkdown || len(pv.Warnings) == 0 {
		t.Errorf("%+v", pv)
	}
	// Both names present: the one that lists documents wins.
	f3 := files(map[string]string{
		"project.json":       `{"title":"P"}`,
		"scriobh.json":       `{"project":{"title":"S"},"documents":[{"path":"manuscript/01-a.md","kind":"chapter","title":"From scriobh.json"}]}`,
		"manuscript/01-a.md": "A.\n",
	})
	pv = e.must(e.w, "import.preview", imports.PreviewIn{Files: f3}).(imports.PreviewOut)
	if pv.ManifestFile != "scriobh.json" || pv.Nodes[0].Title != "From scriobh.json" {
		t.Errorf("%+v", pv)
	}
}

func TestDuplicateTitlesAndOrdering(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"ch2.md": "2", "ch10.md": "10", "ch1.md": "1",
		"b/same.md": "x", "a/same.md": "y",
		"01-same.md": "z", "02-same.md": "w",
	})
	pv := e.preview(f, "")
	// Numbered entries first, then the rest by name the way a person counts.
	eq(t, outline(pv.Nodes), []string{
		"chapter:Same", "chapter:Same", "part:A", "  chapter:Same", "part:B", "  chapter:Same",
		"chapter:Ch1", "chapter:Ch2", "chapter:Ch10",
	})
	out := e.apply(f, "", nil)
	if out.Documents != 9 {
		t.Errorf("duplicate titles import: %d", out.Documents)
	}
}

// Wiki links in imported files resolve against the other imported documents,
// whichever file comes first, and the text is stamped for history.
func TestImportedWikiLinksResolveAndTextIsStamped(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{
		"01-a.md":     "See [[Bravo]] and [[Nowhere]].\n",
		"02-bravo.md": "Back to [[A]].\n",
	})
	out := e.apply(f, "", nil)
	rows, err := e.d.Read().Query(`SELECT l.link_text, l.to_id IS NOT NULL FROM document_links l JOIN documents d ON d.id = l.from_id WHERE d.project_id = ? ORDER BY l.link_text`, out.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var text string
		var resolved bool
		if err := rows.Scan(&text, &resolved); err != nil {
			t.Fatal(err)
		}
		got[text] = resolved
	}
	if len(got) != 3 || !got["Bravo"] || got["Nowhere"] {
		t.Errorf("links after import: %v (Bravo must resolve, Nowhere must stay open)", got)
	}
	var stamped int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND content_at IS NOT NULL AND content_by IS NOT NULL`, out.ProjectID).Scan(&stamped)
	if stamped < 2 {
		t.Errorf("imported documents must carry a content stamp for history: %d", stamped)
	}
}
