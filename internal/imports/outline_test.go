package imports_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/mirror"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }
func bp(b bool) *bool     { return &b }

// outlineSource builds a project whose documents carry outline metadata, and
// returns its id.
func (e *env) outlineSource() string {
	e.t.Helper()
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Outline Book", Kind: "novel"}).(projects.Item).Item.ID
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range list.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	mk := func(title, kind, content, parent string, upd docs.UpdateIn) string {
		d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, Content: content, ParentID: parent}).(docs.Item).Item
		upd.ID, upd.BaseRevision = d.ID, d.Revision
		e.must(e.w, "doc.update", upd)
		return d.ID
	}
	part := mk("Part One", "part", "Epigraph.\n", "", docs.UpdateIn{SynopsisShort: sp("The crossing."), TargetWords: ip(30000)})
	ch := mk("Arrival", "chapter", "They came at dusk.\n", part, docs.UpdateIn{Status: sp("revise"), SynopsisShort: sp("A boat at dusk."),
		SynopsisLong: sp("Line one.\n\nLine three, with <angle> & \"quotes\" and Máire."), TargetWords: ip(4000)})
	mk("The Dock", "scene", "She waited.\n", ch, docs.UpdateIn{Status: sp("outline"), Pov: sp("Máire"), Location: sp("The dock"),
		StoryDate: sp("1847-03-02"), StoryDateEnd: sp("1847-03-03"), TargetWords: ip(1200), IncludeInCompile: bp(false)})
	mk("Plain", "chapter", "Nothing set.\n", "", docs.UpdateIn{SynopsisLong: sp("Two  spaces,\r\nand a trailing newline.\n")})
	mk("Research note", "note", "A note to self.\n", "", docs.UpdateIn{SynopsisShort: sp("Why the boat is late."), SynopsisLong: sp("Notes keep their outline too."), TargetWords: ip(250)})
	return pid
}

// outlineOf lists every document's outline data in tree order.
func (e *env) outlineOf(pid string) []string {
	e.t.Helper()
	l := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	var out []string
	for _, id := range l.Order {
		d := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
		out = append(out, strings.Join([]string{d.Kind, d.Title, d.Status, "syn=" + d.SynopsisShort, "long=" + fmt.Sprintf("%q", d.SynopsisLong),
			"pov=" + d.Pov, "loc=" + d.Location, "from=" + d.StoryDate, "to=" + d.StoryDateEnd,
			"target=" + strconv.Itoa(d.TargetWords), "compile=" + btoa(d.IncludeInCompile)}, " / "))
	}
	return out
}

func btoa(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// mirrorFiles writes a project's mirror and reads it back as a folder of files,
// as a clone from Git or a copied folder would arrive.
func (e *env) mirrorFiles(pid string) []imports.File {
	e.t.Helper()
	dir := e.t.TempDir()
	m := mirror.New(mirror.Options{DataDir: dir, DB: e.d})
	if _, err := m.Rebuild(context.Background(), pid); err != nil {
		e.t.Fatal(err)
	}
	folders, _ := filepath.Glob(filepath.Join(dir, "mirror", "*--*"))
	if len(folders) != 1 {
		e.t.Fatalf("mirror folders: %v", folders)
	}
	var out []imports.File
	err := filepath.WalkDir(folders[0], func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(folders[0], p)
		b, err := os.ReadFile(p)
		out = append(out, imports.File{Path: filepath.ToSlash(rel), Data: b})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// The outline survives mirror -> folder -> import: synopses, status, target,
// point of view, location, story dates and the compile flag, in the same order.
func TestOutlineRoundTripsThroughTheMirror(t *testing.T) {
	e := newEnv(t)
	src := e.outlineSource()
	want := e.outlineOf(src)
	if joined := strings.Join(want, "\n"); !strings.Contains(joined, `note / Research note / draft / syn=Why the boat is late. / long="Notes keep their outline too." / pov= / loc= / from= / to= / target=250`) ||
		!strings.Contains(joined, `long="Two  spaces,\r\nand a trailing newline.\n"`) {
		t.Fatalf("the source lacks the note or the awkward whitespace:\n%s", joined)
	}
	if !strings.Contains(strings.Join(want, "\n"), "pov=Máire / loc=The dock / from=1847-03-02 / to=1847-03-03 / target=1200 / compile=no") {
		t.Fatalf("the source is not what the test expects:\n%s", strings.Join(want, "\n"))
	}
	for _, manifest := range []string{"project.json", "scriobh.json"} { // the mirror's name and the Git layout's
		fs := e.mirrorFiles(src)
		if manifest == "scriobh.json" {
			for i := range fs {
				if fs[i].Path == "project.json" {
					fs[i].Path = "scriobh.json"
				}
			}
		}
		pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: fs, Name: "x"}).(imports.PreviewOut)
		if pv.Layout != imports.LayoutScriobh || pv.ManifestFile != manifest {
			t.Fatalf("layout %q manifest %q", pv.Layout, pv.ManifestFile)
		}
		out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: fs, Name: "x"}).(imports.ApplyOut)
		got := e.outlineOf(out.ProjectID)
		eq(t, got, want)
	}
}

// A manifest from before the outline fields, and one with hostile values.
func TestOutlineFieldsAreOptionalAndUntrusted(t *testing.T) {
	e := newEnv(t)
	// Older manifests: no outline fields at all means the defaults, compiled.
	old := files(map[string]string{
		"project.json":       `{"schema_version":1,"project":{"title":"Old"},"documents":[{"path":"manuscript/01-a.md","kind":"chapter","title":"A","status":"draft"}]}`,
		"manuscript/01-a.md": "Text.\n",
	})
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: e.must(e.w, "import.preview", imports.PreviewIn{Files: old, Name: "x"}).(imports.PreviewOut).Token, Files: old, Name: "x"}).(imports.ApplyOut)
	eq(t, e.outlineOf(out.ProjectID), []string{"chapter / A / draft / syn= / long=\"\" / pov= / loc= / from= / to= / target=0 / compile=yes"})

	long := strings.Repeat("x", 25_000)
	// Wrong types: only the bad fields are lost; the title, status and the tree
	// still come from the manifest.
	wrong := files(map[string]string{
		"project.json": `{"project":{"title":"W"},"documents":[{"path":"manuscript/01-a.md","kind":"chapter","title":"Titled by the manifest","status":"revise",
			"synopsis_short":5,"synopsis_long":"kept","pov":["x"],"target_words":"many","include_in_compile":"maybe","story_date":"1900"}]}`,
		"manuscript/01-a.md": "Text.\n",
	})
	wpv := e.must(e.w, "import.preview", imports.PreviewIn{Files: wrong, Name: "x"}).(imports.PreviewOut)
	for _, w := range wpv.Warnings {
		if strings.Contains(w, "not valid JSON") {
			t.Errorf("one bad field made the whole manifest unreadable: %v", wpv.Warnings)
		}
	}
	wo := e.must(e.w, "import.apply", imports.ApplyIn{Token: wpv.Token, Files: wrong, Name: "x"}).(imports.ApplyOut)
	eq(t, e.outlineOf(wo.ProjectID), []string{`chapter / Titled by the manifest / revise / syn= / long="kept" / pov= / loc= / from=1900 / to= / target=0 / compile=yes`})

	hostile := files(map[string]string{
		"project.json": `{"project":{"title":"H"},"documents":[{"path":"manuscript/01-a.md","kind":"chapter","title":"A","status":"draft",
			"synopsis_short":"` + strings.Repeat("s", 400) + `","synopsis_long":"` + long + `\u0000\u0007ok","pov":"` + strings.Repeat("p", 300) + `",
			"location":"a\nb","story_date":"` + strings.Repeat("d", 100) + `","target_words":-5}]}`,
		"manuscript/01-a.md": "Text.\n",
	})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: hostile, Name: "x"}).(imports.PreviewOut)
	ao := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: hostile, Name: "x"}).(imports.ApplyOut)
	l := e.must(e.w, "doc.list", docs.ListIn{ProjectID: ao.ProjectID}).(docs.ListOut)
	d := e.must(e.w, "doc.get", docs.DocIn{ID: l.Order[0]}).(docs.Item).Item
	if len([]rune(d.SynopsisShort)) != 300 || len([]rune(d.Pov)) != 200 || len([]rune(d.StoryDate)) != 64 || d.Location != "a b" || d.TargetWords != 0 {
		t.Errorf("limits not applied: syn %d pov %d date %d loc %q target %d", len([]rune(d.SynopsisShort)), len([]rune(d.Pov)), len([]rune(d.StoryDate)), d.Location, d.TargetWords)
	}
	if len([]rune(d.SynopsisLong)) != 20_000 || strings.ContainsAny(d.SynopsisLong, "\x00\x07") {
		t.Errorf("long synopsis: %d runes", len([]rune(d.SynopsisLong)))
	}
	if w := strings.Join(pv.Warnings, " "); !strings.Contains(w, "shortened") {
		t.Errorf("a cut value should be reported: %v", pv.Warnings)
	}
}

// bigManifest builds a folder of n documents whose manifest lists a long
// synopsis for each, so the manifest is much larger than the Markdown.
func bigManifest(n, synopsisBytes int) map[string]string {
	m := map[string]string{}
	var b strings.Builder
	b.WriteString(`{"schema_version":2,"project":{"title":"Big"},"documents":[`)
	syn := strings.Repeat("s", synopsisBytes)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		path := fmt.Sprintf("manuscript/%04d-chapter.md", i+1)
		fmt.Fprintf(&b, `{"path":%q,"kind":"chapter","title":"Chapter %d","status":"draft","synopsis_short":"short %d","synopsis_long":%q,"target_words":%d,"include_in_compile":true}`,
			path, i+1, i+1, syn, i+1)
		m[path] = "Text.\n"
	}
	b.WriteString(`]}`)
	m["project.json"] = b.String()
	return m
}

// A manifest over a megabyte (a project with long synopses) is read in full:
// the outline, the titles and the kinds are not lost to a size limit.
func TestALargeManifestIsStillRead(t *testing.T) {
	e := newEnv(t)
	f := files(bigManifest(400, 4000)) // about 1.6 MB of manifest
	var size int
	for _, x := range f {
		if x.Path == "project.json" {
			size = len(x.Data)
		}
	}
	if size < 1<<20 {
		t.Fatalf("the manifest is only %d bytes; the test needs more than 1 MiB", size)
	}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f, Name: "x"}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutScriobh || pv.ManifestFile != "project.json" || pv.Project.Title != "Big" {
		t.Fatalf("a large manifest was not read: layout %q file %q title %q warnings %v", pv.Layout, pv.ManifestFile, pv.Project.Title, pv.Warnings)
	}
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, Name: "x"}).(imports.ApplyOut)
	l := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	if len(l.Order) != 400 {
		t.Fatalf("%d documents", len(l.Order))
	}
	last := e.must(e.w, "doc.get", docs.DocIn{ID: l.Order[399]}).(docs.Item).Item
	if last.Title != "Chapter 400" || last.SynopsisShort != "short 400" || last.TargetWords != 400 || len(last.SynopsisLong) != 4000 {
		t.Errorf("the last document lost its outline: %q %q %d %d", last.Title, last.SynopsisShort, last.TargetWords, len(last.SynopsisLong))
	}
}

// A manifest over the limit is refused loudly, never read as if it were not there.
func TestAnOversizedManifestIsReported(t *testing.T) {
	e := newEnv(t)
	f := files(bigManifest(30, 300_000)) // about 9 MB
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f, Name: "x"}).(imports.PreviewOut)
	if !strings.Contains(strings.Join(pv.Warnings, " "), "larger than 8 MiB and was ignored") {
		t.Errorf("no warning for the ignored manifest: %v", pv.Warnings)
	}
	listed := false
	for _, sk := range pv.Skipped {
		if sk.Path == "project.json" && strings.Contains(sk.Reason, "larger than the limit") {
			listed = true
		}
	}
	if !listed {
		t.Errorf("the manifest is not in the skipped list: %+v", pv.Skipped)
	}
}
