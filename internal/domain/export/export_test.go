package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/yuin/goldmark"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t        *testing.T
	dir      string
	d        *db.DB
	reg      *ops.Registry
	ctx      context.Context
	owner    ops.Principal
	reader   ops.Principal
	outsider ops.Principal
	pid      string
}

func newFx(t *testing.T, title string) *fx {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	f := &fx{t: t, dir: dir, d: d, ctx: context.Background(), reg: registry.Build(d, events.NewStore(d, nil), dir)}
	f.owner = ops.Principal{UserID: testutil.User(t, d, "writer", "o@t.local"), Role: ops.RoleWriter}
	f.reader = ops.Principal{UserID: testutil.User(t, d, "reader", "r@t.local"), Role: ops.RoleReader}
	f.outsider = ops.Principal{UserID: testutil.User(t, d, "writer", "x@t.local"), Role: ops.RoleWriter}
	out, err := f.reg.Call(f.ctx, f.owner, "project.create", projects.CreateIn{Title: title, Subtitle: "A sub *title*", AuthorName: "Máire Ní Bhriain", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	f.pid = out.(projects.Item).Item.ID
	if _, err := d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, f.pid, f.reader.UserID, "reader", db.Now()); err != nil {
		t.Fatal(err)
	}
	// The seeded first page ("Chapter 1") is not part of these trees.
	f.trashSeed()
	return f
}

func (f *fx) trashSeed() {
	out, err := f.reg.Call(f.ctx, f.owner, "doc.list", docs.ListIn{ProjectID: f.pid})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, m := range out.(docs.ListOut).Items {
		if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: m.ID}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fx) doc(title, kind, parent, content string) docs.Doc {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, f.owner, "doc.create", docs.CreateIn{ProjectID: f.pid, Title: title, Kind: kind, ParentID: parent, Content: content})
	if err != nil {
		f.t.Fatalf("create %q: %v", title, err)
	}
	return out.(docs.Item).Item
}

func (f *fx) export(p ops.Principal, mode string, notes bool) (export.File, error) {
	out, err := f.reg.Call(f.ctx, p, "export.markdown", export.MarkdownIn{ProjectID: f.pid, Mode: mode, Notes: notes})
	if err != nil {
		return export.File{}, err
	}
	return out.(export.File), nil
}

func (f *fx) must(mode string, notes bool) export.File {
	f.t.Helper()
	file, err := f.export(f.owner, mode, notes)
	if err != nil {
		f.t.Fatal(err)
	}
	return file
}

const alts = "<!-- scriobh:alts {\"v\":1,\"alts\":[{\"at\":3,\"text\":\"other wording\"}]} -->"

// novel builds a manuscript with every kind of thing an export must decide
// about: front matter, parts, chapters and scenes, a part with no text, notes
// (top level and inside a chapter), an untitled scene, a trashed chapter, and a
// document that carries the alternatives comment.
func (f *fx) novel() map[string]docs.Doc {
	d := map[string]docs.Doc{}
	d["front"] = f.doc("Front Matter", "front_matter", "", "Dedication text frontmark.")
	d["p1"] = f.doc("Part One", "part", "", "Epigraph of part one.")
	d["c1"] = f.doc("The Arrival", "chapter", d["p1"].ID, "Chapter one opens here, ch1mark.")
	d["s1"] = f.doc("Harbour", "scene", d["c1"].ID, "Scene one on the quay, s1mark.\n\n"+alts+"\n")
	d["s2"] = f.doc("", "scene", d["c1"].ID, "An untitled scene, s2mark.")
	d["n1"] = f.doc("Research", "note", d["c1"].ID, "A private research note, n1mark.")
	d["c2"] = f.doc("Máire's Boat: *a* [story] #", "chapter", d["p1"].ID, "Chapter two, ch2mark.")
	d["p2"] = f.doc("Part Two", "part", "", "")
	d["c3"] = f.doc("The End", "chapter", d["p2"].ID, "Last words, ch3mark.")
	d["n2"] = f.doc("Plot Notes", "note", "", "Top level note, n2mark.")
	d["gone"] = f.doc("Cut", "chapter", "", "Trashed text, gonemark.")
	d["back"] = f.doc("Afterword", "back_matter", "", "Thanks, backmark.")
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: d["gone"].ID}); err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *fx) updatedAt() time.Time {
	var s string
	if err := f.d.Read().QueryRow(`SELECT updated_at FROM projects WHERE id = ?`, f.pid).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	t, err := db.Parse(s)
	if err != nil {
		f.t.Fatal(err)
	}
	return t
}

func unzip(t *testing.T, body []byte) (names []string, files map[string][]byte, zr *zip.Reader) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	files = map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, f.Name)
		files[f.Name] = b
	}
	return names, files, zr
}

func TestSingleFileIsTheTitleBlockThenEveryDocumentInOrder(t *testing.T) {
	f := newFx(t, "The Long Tide")
	f.novel()
	file := f.must("single", false)
	if file.Filename != "the-long-tide.md" || file.ContentType != "text/markdown; charset=utf-8" {
		t.Errorf("%s %s", file.Filename, file.ContentType)
	}
	want := "# The Long Tide\n\n*A sub \\*title\\**\n\nby Máire Ní Bhriain\n" +
		"\n# Front Matter\n\nDedication text frontmark.\n" +
		"\n# Part One\n\nEpigraph of part one.\n" +
		"\n## The Arrival\n\nChapter one opens here, ch1mark.\n" +
		"\n### Harbour\n\nScene one on the quay, s1mark.\n\n" + alts + "\n" +
		"\nAn untitled scene, s2mark.\n" +
		"\n## Máire's Boat: \\*a\\* \\[story\\] \\#\n\nChapter two, ch2mark.\n" +
		"\n# Part Two\n" +
		"\n## The End\n\nLast words, ch3mark.\n" +
		"\n# Afterword\n\nThanks, backmark.\n"
	if string(file.Body) != want {
		t.Fatalf("single export\n got:\n%s\nwant:\n%s", file.Body, want)
	}
	for _, absent := range []string{"n1mark", "n2mark", "gonemark", "Research", "Plot Notes"} {
		if strings.Contains(string(file.Body), absent) {
			t.Errorf("the export holds %q", absent)
		}
	}
	if file.Documents != 9 {
		t.Errorf("documents = %d, want 9", file.Documents)
	}
}

func TestNotesAreOptIn(t *testing.T) {
	f := newFx(t, "Notes")
	f.novel()
	file := f.must("single", true)
	s := string(file.Body)
	i := strings.Index(s, "\n# Notes\n")
	if i < 0 {
		t.Fatalf("no notes section:\n%s", s)
	}
	manuscript, notes := s[:i], s[i:]
	if strings.Contains(manuscript, "n1mark") || strings.Contains(manuscript, "n2mark") {
		t.Error("a note is inside the manuscript part")
	}
	if !strings.Contains(notes, "## Research\n\nA private research note, n1mark.") || !strings.Contains(notes, "## Plot Notes\n\nTop level note, n2mark.") {
		t.Errorf("notes section:\n%s", notes)
	}
	if strings.Index(notes, "Research") > strings.Index(notes, "Plot Notes") {
		t.Error("notes are not in reading order")
	}
	if file.Documents != 11 {
		t.Errorf("documents = %d, want 11", file.Documents)
	}
}

func TestExportOrderIsTheOrderDocListReports(t *testing.T) {
	f := newFx(t, "Order")
	d := f.novel()
	// Move things about so the order is not creation order.
	after := d["c2"].ID
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.move", docs.MoveIn{ID: d["c3"].ID, ParentID: d["p1"].ID, After: &after}); err != nil {
		t.Fatal(err)
	}
	top := ""
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.move", docs.MoveIn{ID: d["back"].ID, ParentID: "", After: &top}); err != nil {
		t.Fatal(err)
	}
	listed, err := f.reg.Call(f.ctx, f.owner, "doc.list", docs.ListIn{ProjectID: f.pid})
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	byID := map[string]docs.Meta{}
	for _, m := range listed.(docs.ListOut).Items {
		byID[m.ID] = m
	}
	for _, id := range listed.(docs.ListOut).Order {
		if byID[id].Kind != "note" {
			want = append(want, id)
		}
	}
	file := f.must("bundle", false)
	_, files, _ := unzip(t, file.Body)
	var m struct {
		Documents []struct{ ID, Path string }
	}
	if err := json.Unmarshal(files["scriobh.json"], &m); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range m.Documents {
		got = append(got, x.ID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("bundle order\n got %v\nwant %v", got, want)
	}
}

func TestBundleMirrorsTheTree(t *testing.T) {
	f := newFx(t, "The Long Tide")
	d := f.novel()
	file := f.must("bundle", false)
	if file.Filename != "the-long-tide.zip" || file.ContentType != "application/zip" {
		t.Errorf("%s %s", file.Filename, file.ContentType)
	}
	names, files, _ := unzip(t, file.Body)
	want := []string{
		"manuscript/00-front-matter.md",
		"manuscript/01-part-one/00-part-one.md",
		"manuscript/01-part-one/01-the-arrival/00-the-arrival.md",
		"manuscript/01-part-one/01-the-arrival/01-harbour.md",
		"manuscript/01-part-one/01-the-arrival/02-untitled.md",
		"manuscript/01-part-one/02-maire-s-boat-a-story.md",
		"manuscript/02-part-two/00-part-two.md",
		"manuscript/02-part-two/01-the-end.md",
		"manuscript/03-afterword.md",
		"scriobh.json",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bundle entries (sorted)\n got:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	// A document's file is its stored text, byte for byte, alternatives comment included.
	if got := string(files["manuscript/01-part-one/01-the-arrival/01-harbour.md"]); got != "Scene one on the quay, s1mark.\n\n"+alts+"\n" {
		t.Errorf("scene file %q", got)
	}
	if !strings.Contains(string(files["manuscript/01-part-one/01-the-arrival/01-harbour.md"]), alts) {
		t.Error("the alternatives comment did not survive")
	}
	if len(files["manuscript/02-part-two/00-part-two.md"]) != 0 {
		t.Error("a part with no text should have an empty file")
	}

	var m struct {
		SchemaVersion int `json:"schema_version"`
		Format        string
		Project       struct{ ID, Title, Kind, Author, Language string }
		ExportedAt    string `json:"exported_at"`
		IncludesNotes bool   `json:"includes_notes"`
		Documents     []struct {
			ID, Path, Kind, Title, Status string
			Revision                      int
			WordCount                     int `json:"word_count"`
			SHA256                        string
		}
	}
	if err := json.Unmarshal(files["scriobh.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 1 || m.Project.ID != f.pid || m.Project.Title != "The Long Tide" || m.Project.Kind != "novel" || m.Project.Author != "Máire Ní Bhriain" || m.Project.Language != "en" {
		t.Errorf("manifest header %+v", m)
	}
	if m.ExportedAt != db.Format(f.updatedAt()) {
		t.Errorf("exported_at %q is not the project's updated_at %q", m.ExportedAt, db.Format(f.updatedAt()))
	}
	if len(m.Documents) != len(want)-1 || m.IncludesNotes {
		t.Fatalf("manifest lists %d documents", len(m.Documents))
	}
	for _, x := range m.Documents {
		data, ok := files[x.Path]
		if !ok {
			t.Errorf("manifest path %s is not in the zip", x.Path)
			continue
		}
		sum := sha256.Sum256(data)
		if x.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: manifest hash does not match the file", x.Path)
		}
	}
	byID := map[string]int{}
	for i, x := range m.Documents {
		byID[x.ID] = i
	}
	h := m.Documents[byID[d["s1"].ID]]
	if h.Title != "Harbour" || h.Kind != "scene" || h.Status != "draft" || h.Revision != 1 || h.WordCount != 6 {
		t.Errorf("manifest entry %+v", h)
	}
	for _, id := range []string{d["n1"].ID, d["n2"].ID, d["gone"].ID} {
		if _, ok := byID[id]; ok {
			t.Errorf("manifest lists %s", id)
		}
	}
}

func TestBundleWithNotes(t *testing.T) {
	f := newFx(t, "N")
	d := f.novel()
	names, files, _ := unzip(t, f.must("bundle", true).Body)
	var notes []string
	for _, n := range names {
		if strings.HasPrefix(n, "notes/") {
			notes = append(notes, n)
		}
	}
	if strings.Join(notes, ",") != "notes/01-research.md,notes/02-plot-notes.md" {
		t.Errorf("notes: %v", notes)
	}
	if string(files["notes/01-research.md"]) != "A private research note, n1mark." {
		t.Errorf("note file %q", files["notes/01-research.md"])
	}
	var m struct {
		IncludesNotes bool `json:"includes_notes"`
		Documents     []struct{ ID string }
	}
	if err := json.Unmarshal(files["scriobh.json"], &m); err != nil {
		t.Fatal(err)
	}
	if !m.IncludesNotes || len(m.Documents) != 11 {
		t.Errorf("manifest: notes %v, %d documents", m.IncludesNotes, len(m.Documents))
	}
	_ = d
	// Without the flag, no notes/ folder at all.
	names, _, _ = unzip(t, f.must("bundle", false).Body)
	for _, n := range names {
		if strings.HasPrefix(n, "notes/") {
			t.Errorf("unexpected %s", n)
		}
	}
}

func TestBundleIsByteStable(t *testing.T) {
	f := newFx(t, "Stable")
	f.novel()
	a, b := f.must("bundle", true), f.must("bundle", true)
	if a.SHA256 != b.SHA256 || !bytes.Equal(a.Body, b.Body) {
		t.Fatalf("two exports of an unchanged project differ: %s vs %s", a.SHA256, b.SHA256)
	}
	if a.ExportID == b.ExportID {
		t.Error("each export is its own record")
	}
	s1, s2 := f.must("single", true), f.must("single", true)
	if s1.SHA256 != s2.SHA256 {
		t.Error("single exports differ")
	}
	// Shape of the zip: stored, sorted, no extra fields, a fixed time, no owner.
	names, _, zr := unzip(t, a.Body)
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("entries are not sorted: %q before %q", names[i-1], names[i])
		}
	}
	at := f.updatedAt()
	for _, e := range zr.File {
		if e.Method != zip.Store {
			t.Errorf("%s: method %d, want stored", e.Name, e.Method)
		}
		if len(e.Extra) != 0 {
			t.Errorf("%s: %d bytes of extra fields", e.Name, len(e.Extra))
		}
		if e.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v", e.Name, e.Mode())
		}
		if d := e.Modified.Sub(at.Truncate(2 * time.Second)); d < -2*time.Second || d > 2*time.Second {
			t.Errorf("%s: modified %v, want the project's updated_at %v", e.Name, e.Modified, at)
		}
	}
	// A change to the project changes the export.
	f.doc("Another", "chapter", "", "More words.")
	if c := f.must("bundle", true); c.SHA256 == a.SHA256 {
		t.Error("the export did not change when the project did")
	}
}

func TestEveryExportIsRecorded(t *testing.T) {
	f := newFx(t, "Recorded")
	d := f.novel()
	file := f.must("bundle", false)
	var preset, format, filename, sum, manifest, userID string
	var bytesN int
	if err := f.d.Read().QueryRow(`SELECT preset, format, filename, bytes, sha256, manifest_json, user_id FROM exports WHERE id = ? AND project_id = ?`, file.ExportID, f.pid).
		Scan(&preset, &format, &filename, &bytesN, &sum, &manifest, &userID); err != nil {
		t.Fatal(err)
	}
	if preset != "bundle" || format != "md" || filename != "recorded.zip" || bytesN != len(file.Body) || sum != file.SHA256 || userID != f.owner.UserID {
		t.Errorf("row: %s %s %s %d %s %s", preset, format, filename, bytesN, sum, userID)
	}
	var m struct {
		Documents []struct{ ID, SHA256 string }
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Documents) != 9 {
		t.Errorf("recorded manifest holds %d documents", len(m.Documents))
	}
	want := sha256.Sum256([]byte("Scene one on the quay, s1mark.\n\n" + alts + "\n"))
	found := false
	for _, x := range m.Documents {
		if x.ID == d["s1"].ID {
			found = x.SHA256 == hex.EncodeToString(want[:])
		}
	}
	if !found {
		t.Error("the recorded manifest does not carry the scene's content hash")
	}
	f.must("single", false)
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM exports WHERE project_id = ?`, f.pid).Scan(&n); err != nil || n != 2 {
		t.Errorf("exports rows: %d %v", n, err)
	}
	var audits int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'export.run' AND target_id = ?`, f.pid).Scan(&audits); err != nil || audits != 2 {
		t.Errorf("audit rows: %d %v", audits, err)
	}
	var detail string
	if err := f.d.Read().QueryRow(`SELECT detail_json FROM audit_log WHERE action = 'export.run' ORDER BY id LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "mark") || strings.Contains(detail, "Scene") {
		t.Errorf("the audit entry carries manuscript text: %s", detail)
	}
}

func TestWhoMayExport(t *testing.T) {
	f := newFx(t, "Access")
	f.novel()
	if _, err := f.export(f.reader, "single", false); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader must be refused (403), got %v", err)
	}
	if _, err := f.export(f.outsider, "single", false); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("an outsider must get not_found, got %v", err)
	}
	if _, err := f.export(ops.Principal{}, "single", false); ops.AsError(err).Code != ops.CodeUnauthenticated {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := f.export(f.owner, "pdf", false); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("unknown mode: %v", err)
	}
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM exports`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused export left %d rows (%v)", n, err)
	}
	// Default mode is single.
	if file, err := f.export(f.owner, "", false); err != nil || !strings.HasSuffix(file.Filename, ".md") {
		t.Errorf("default mode: %+v %v", file.Filename, err)
	}
}

func TestLimits(t *testing.T) {
	f := newFx(t, "Big")
	a := f.doc("A", "chapter", "", "words here")
	if _, err := f.d.Writer().Exec(`UPDATE documents SET word_count = 2000001 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.export(f.owner, "single", false); ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "2000000") {
		t.Errorf("too many words: %v", err)
	}
	if _, err := f.d.Writer().Exec(`UPDATE documents SET word_count = 10 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.d.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < export.MaxDocuments; i++ {
		if _, err := tx.Exec(`INSERT INTO documents(id, project_id, kind, title, position, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			db.NewID("doc"), f.pid, "scene", fmt.Sprintf("s%d", i), float64(i+100), db.Now(), db.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.export(f.owner, "bundle", false); ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "5000") {
		t.Errorf("too many documents: %v", err)
	}
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM exports`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused export left %d rows", n)
	}
}

func TestDeterministicNames(t *testing.T) {
	f := newFx(t, "../../etc/passwd")
	if file := f.must("single", false); file.Filename != "etc-passwd.md" {
		t.Errorf("filename %q", file.Filename)
	}
	p1 := f.doc("Part", "part", "", "")
	f.doc("Same Title", "chapter", p1.ID, "one")
	f.doc("Same Title", "chapter", p1.ID, "two")
	f.doc("../../Ünïcode — Ænima & ß: «x»?", "chapter", p1.ID, "three")
	f.doc("  ", "chapter", p1.ID, "four")
	f.doc(strings.Repeat("long ", 40), "chapter", p1.ID, "five")
	names, _, _ := unzip(t, f.must("bundle", false).Body)
	want := []string{
		"manuscript/01-part/00-part.md",
		"manuscript/01-part/01-same-title.md",
		"manuscript/01-part/02-same-title.md",
		"manuscript/01-part/03-unicode-aenima-ss-x.md",
		"manuscript/01-part/04-untitled.md",
		"manuscript/01-part/05-long-long-long-long-long-long-long-long-long.md",
		"scriobh.json",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("names\n got:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	for _, n := range names {
		if strings.Contains(n, "..") || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\\x00") {
			t.Errorf("unsafe entry name %q", n)
		}
	}
}

func TestNumbersWidenPastNinetyNine(t *testing.T) {
	f := newFx(t, "Many")
	for i := 0; i < 101; i++ {
		f.doc(fmt.Sprintf("Scene %d", i), "scene", "", "x")
	}
	names, _, _ := unzip(t, f.must("bundle", false).Body)
	if names[0] != "manuscript/001-scene-0.md" || names[100] != "manuscript/101-scene-100.md" {
		t.Errorf("first %q last %q", names[0], names[100])
	}
}

// The single file is ordinary Markdown: a CommonMark parser reads it, and every
// document's text is in the result once.
func TestSingleFileParsesAndHoldsEachDocumentOnce(t *testing.T) {
	f := newFx(t, "Parsed")
	f.novel()
	file := f.must("single", false)
	var html bytes.Buffer
	if err := goldmark.New().Convert(file.Body, &html); err != nil {
		t.Fatal(err)
	}
	for _, mark := range []string{"frontmark", "ch1mark", "s1mark", "s2mark", "ch2mark", "ch3mark", "backmark"} {
		if n := strings.Count(html.String(), mark); n != 1 {
			t.Errorf("%s appears %d times in the parsed export", mark, n)
		}
	}
	if !strings.Contains(html.String(), "<h1>Parsed</h1>") || !strings.Contains(html.String(), "<h3>Harbour</h3>") {
		t.Errorf("headings:\n%s", html.String())
	}
	// A title's punctuation is text, not formatting.
	if !strings.Contains(html.String(), "Boat: *a* [story] #</h2>") {
		t.Errorf("escaped title:\n%s", html.String())
	}
	// The alternatives comment is a comment: invisible, intact.
	if !strings.Contains(string(file.Body), alts) {
		t.Error("the alternatives comment did not survive")
	}
}

// And the editor's own parser reads it, loses none of the words, and settles on
// a fixed point. Needs Node; skipped without it unless CI is set.
func TestSingleFileSurvivesTheEditorsParser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("node is required in CI: %v", err)
		}
		t.Skip("node not found; skipping (it fails when CI is set)")
	}
	f := newFx(t, "Editor")
	f.novel()
	file := f.must("single", true)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "export.md"), file.Body, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("web", "editor", "roundtrip.mjs"), "--impl", "bundle", dir)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("roundtrip.mjs: %v\n%s", err, stderr.String())
	}
	var res struct {
		Files []struct {
			Name     string
			Out      string
			Out2     string
			Fallback bool
			Error    *string
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Fallback || res.Files[0].Error != nil {
		t.Fatalf("the editor could not parse the export: %+v", res.Files)
	}
	out := res.Files[0].Out
	if out != res.Files[0].Out2 {
		t.Error("the editor's serialization of the export is not a fixed point")
	}
	if !words(t, string(file.Body), out) {
		t.Errorf("a word of the export was lost by the editor's parse/serialize:\n%s\n---\n%s", file.Body, out)
	}
	for _, mark := range []string{"frontmark", "ch1mark", "s1mark", "n1mark", "n2mark", "backmark"} {
		if !strings.Contains(out, mark) {
			t.Errorf("%s was lost", mark)
		}
	}
	if !strings.Contains(out, alts) {
		t.Error("the alternatives comment did not survive the editor round trip")
	}
}

// words reports whether every word of in appears in out in order.
func words(t *testing.T, in, out string) bool {
	t.Helper()
	split := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	}
	have := split(out)
	j := 0
	for _, w := range split(in) {
		for j < len(have) && have[j] != w {
			j++
		}
		if j == len(have) {
			t.Logf("missing word %q", w)
			return false
		}
		j++
	}
	return true
}

func TestDeepNestingIsNotDropped(t *testing.T) {
	f := newFx(t, "Export Fixture")
	f.trashSeed()
	parent := ""
	const depth = 80
	for i := 0; i < depth; i++ {
		parent = f.doc(fmt.Sprintf("Level %d", i), "part", parent, fmt.Sprintf("text %d", i)).ID
	}
	file := f.must("bundle", false)
	zr, err := zip.NewReader(bytes.NewReader(file.Body), int64(len(file.Body)))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range zr.File {
		if strings.HasSuffix(e.Name, ".md") {
			n++
		}
	}
	if n != depth {
		t.Errorf("exported %d of %d nested documents", n, depth)
	}
}

func TestExportRefusesHugeTextThatCountsAsFewWords(t *testing.T) {
	f := newFx(t, "Export Fixture")
	d := f.doc("Big", "chapter", "", "x")
	if _, err := f.d.Writer().Exec(`UPDATE documents SET content = zeroblob(?) WHERE id = ?`, export.MaxBytes+1, d.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.export(f.owner, "single", false)
	if e := ops.AsError(err); e == nil || e.Code != ops.CodeInvalid {
		t.Errorf("a project over the byte cap must be refused, got %v", err)
	}
}
