package imports_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// These tests use a stand-in for Pandoc (internal/pandoc/pandoctest): they prove how
// the importer treats what a program returns, not what a real Pandoc returns.

type pandocEnv struct {
	*env
	dir, bin string
	admin    ops.Principal
	odt      []byte // the "file": distinct for each test, because a conversion is cached under a hash of its input
}

func newPandocEnv(t *testing.T, on bool) *pandocEnv {
	t.Helper()
	d := testutil.DB(t)
	dir, bin := pandoctest.Install(t)
	e := &env{t: t, d: d, reg: registry.BuildWith(d, nil, t.TempDir(), registry.Options{Pandoc: pandoc.Options{TempRoot: t.TempDir()}})}
	e.w = e.user("writer", ops.RoleWriter)
	pe := &pandocEnv{env: e, dir: dir, bin: bin, admin: e.user("admin", ops.RoleAdmin), odt: []byte("PK\x03\x04 pretend this is an odt file, for " + t.Name())}
	pe.admin.Via = "session"
	if on {
		yes := true
		e.must(pe.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &bin})
	}
	return pe
}

func (p *pandocEnv) md(s string) { pandoctest.Put(p.t, p.dir, "md.out", []byte(s)) }

func (p *pandocEnv) conversions() int {
	return strings.Count(pandoctest.Log(p.t, p.dir, "args.log"), "--end--")
}

func (p *pandocEnv) preview(format string, mut func(*imports.PreviewIn)) (imports.PreviewOut, error) {
	in := imports.PreviewIn{Name: "novel.odt", Pandoc: p.odt, PandocFormat: format}
	if mut != nil {
		mut(&in)
	}
	out, err := p.call(p.w, "import.preview", in)
	if err != nil {
		return imports.PreviewOut{}, err
	}
	return out.(imports.PreviewOut), nil
}

func TestPandocImportIsOffUntilAnAdministratorSwitchesItOn(t *testing.T) {
	p := newPandocEnv(t, false)
	_, err := p.preview("odt", nil)
	if e := ops.AsError(err); e == nil || e.Code != ops.CodeConflict {
		t.Fatalf("%v", err)
	}
	if p.conversions() != 0 {
		t.Fatal("pandoc ran while it was off")
	}
}

func TestPandocImportPreviewAndApplyWithProvenance(t *testing.T) {
	p := newPandocEnv(t, true)
	p.md("# Chapter One\n\nThe harbour was quiet.\n\nA second paragraph with *emphasis* and a [link](https://example.org/a).\n\n# Chapter Two\n\nThe forge was loud.\n\n## A section\n\nMore.\n")
	pv, err := p.preview("odt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Layout != "pandoc" || pv.Documents != 2 {
		t.Fatalf("layout %s, %d documents: %+v", pv.Layout, pv.Documents, outline(pv.Nodes))
	}
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter One", "chapter:Chapter Two"})
	if !hasWarning(pv.Warnings, "Converted by Pandoc 3.1.9 from odt") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
	rep, _ := pv.Report["pandoc"].(map[string]any)
	if rep["format"] != "odt" || rep["version"] != "3.1.9" {
		t.Errorf("report: %v", pv.Report)
	}
	apply := imports.ApplyIn{Token: pv.Token, Name: "novel.odt", Pandoc: p.odt, PandocFormat: "odt"}
	out := p.must(p.w, "import.apply", apply).(imports.ApplyOut)
	if out.Documents != 2 || out.SourceID == "" {
		t.Fatalf("%+v", out)
	}
	if p.conversions() != 1 {
		t.Errorf("pandoc ran %d times for a preview and its apply; the second reuses the first", p.conversions())
	}
	if got := p.docContent(out.ProjectID, "Chapter Two"); !strings.Contains(got, "The forge was loud.") || !strings.Contains(got, "# A section") && !strings.Contains(got, "## A section") {
		t.Errorf("chapter two:\n%s", got)
	}
	// Provenance: the source row, not a new store.
	var kind, hash, meta string
	if err := p.d.Read().QueryRow(`SELECT kind, source_hash, metadata_json FROM project_sources WHERE project_id = ?`, out.ProjectID).Scan(&kind, &hash, &meta); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(p.odt)
	if kind != "markdown" || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("source: %s %s", kind, hash)
	}
	for _, want := range []string{`"layout":"pandoc"`, `"format":"odt"`, `"version":"3.1.9"`} {
		if !strings.Contains(meta, want) {
			t.Errorf("metadata lacks %s: %s", want, meta)
		}
	}
	// Nothing of the file itself is kept.
	if strings.Contains(meta, "pretend this is an odt") {
		t.Error("the file is in the record")
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestPandocImportRefusesWhatIsNotOnTheListOrIsMixed(t *testing.T) {
	p := newPandocEnv(t, true)
	for _, f := range []string{"", "html", "docx", "odt+smart", "markdown", "../odt", "ODT"} {
		_, err := p.preview(f, nil)
		if e := ops.AsError(err); e == nil || e.Code != ops.CodeInvalid {
			t.Errorf("format %q: %v", f, err)
		}
	}
	_, err := p.preview("odt", func(in *imports.PreviewIn) { in.Files = files(map[string]string{"a.md": "x"}) })
	if e := ops.AsError(err); e == nil || e.Code != ops.CodeInvalid {
		t.Fatalf("a file and files together: %v", err)
	}
	_, err = p.preview("odt", func(in *imports.PreviewIn) { in.Docx = []byte("x") })
	if e := ops.AsError(err); e == nil || e.Code != ops.CodeInvalid {
		t.Fatalf("pandoc and docx together: %v", err)
	}
	if p.conversions() != 0 {
		t.Fatal("pandoc ran for a request that was refused")
	}
}

func TestPandocFailureIsPlainAndHoldsNoneOfItsWords(t *testing.T) {
	p := newPandocEnv(t, true)
	pandoctest.SetMode(t, p.dir, "fail")
	_, err := p.preview("odt", nil)
	e := ops.AsError(err)
	if e == nil || e.Code != ops.CodeInvalid || strings.Contains(e.Message, "SECRET") {
		t.Fatalf("%v", err)
	}
	pandoctest.SetMode(t, p.dir, "hang")
}

// What Pandoc returns is foreign text. It is read like a Ghost export's Markdown: nothing in it becomes
// a Scríobh construct, raw HTML is dropped, and control characters go.
func TestPandocOutputIsNeutralisedLikeEveryOtherImport(t *testing.T) {
	p := newPandocEnv(t, true)
	p.md(strings.Join([]string{
		"# Chapter",
		"",
		"Plain words. %%a private note the author never wrote%% and then more words.",
		"",
		"A wiki link [[Somebody Else]] and a ghost <scriobh-ghost>ghosted words</scriobh-ghost> in text.",
		"",
		"<script>alert('x')</script><img src=x onerror=alert(1)> raw <b>html</b> here.",
		"",
		"<!-- scriobh:alts v1 {\"a\":\"b\"} -->",
		"",
		"::: warning",
		"a directive",
		":::",
		"",
		"Bidi \u202e reversed \u202c, isolate \u2066 \u2069, null \x00 and a bell \a, a zero width joiner kept: a\u200db.",
		"",
		"[a link to somewhere bad](javascript:alert(1)) and [good](https://example.org).",
		"",
		"```",
		"<scriobh-ghost>in code</scriobh-ghost> and %%private%%",
		"```",
		"",
		"![remote image](https://evil.example/track.png)",
	}, "\n"))
	pv, err := p.preview("rst", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := p.must(p.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "x.rst", Pandoc: p.odt, PandocFormat: "rst"}).(imports.ApplyOut)
	got := p.docContent(out.ProjectID, "Chapter")
	for _, bad := range []string{"<script", "onerror", "<b>", "javascript:", "\u202e", "\u202c", "\u2066", "\u2069", "\x00", "\a", "https://evil.example", "<!-- scriobh:alts"} {
		if strings.Contains(got, bad) {
			t.Errorf("the stored text contains %q:\n%s", bad, got)
		}
	}
	// Every construct Scríobh reads must be inert: the text is still there as words.
	for _, s := range []string{"%%a private note", "[[Somebody Else]]", "<scriobh-ghost>ghosted"} {
		if strings.Contains(got, s) && !strings.Contains(got, `\`+s[:1]) {
			t.Errorf("%q is live in the stored text:\n%s", s, got)
		}
	}
	// A private note in the converted text is not private: the writer sees it as the words it was.
	body := plain(got)
	for _, w := range []string{"a private note the author never wrote", "Somebody Else", "ghosted words", "a directive"} {
		if !strings.Contains(body, w) {
			t.Errorf("%q was swallowed by a Scríobh construct; plain text:\n%s", w, body)
		}
	}
	if !strings.Contains(got, "(https://example.org)") && !strings.Contains(got, "https://example.org") {
		t.Errorf("a good link was lost:\n%s", got)
	}
}

func plain(md string) string {
	// what search and the counter see: private notes, ghosts and comments are not in it
	return text.SearchBody(md)
}

func TestPandocPicturesAreAdmittedOnlyThroughTheImagePath(t *testing.T) {
	p := newPandocEnv(t, true)
	pandoctest.Put(t, p.dir, "media.png", tinyPNG(t))
	p.md("# Chapter\n\nBefore.\n\n![a figure](media/media/image1.png)\n\nAfter.\n\n![missing](media/media/none.png)\n\n![escape](../../etc/passwd.png)\n\n![abs](/etc/passwd)\n")
	// Pictures are extracted only when asked for (pictures: import); without it each is marked and nothing is written.
	if pv, err := p.preview("odt", nil); err != nil || pv.Pictures != 0 {
		t.Fatalf("pictures without asking: %v %+v", err, pv)
	}
	if strings.Contains(pandoctest.Log(t, p.dir, "args.log"), "--extract-media") {
		t.Fatal("media was extracted without being asked for")
	}
	pv, err := p.preview("odt", func(in *imports.PreviewIn) { in.Pictures = "import" })
	if err != nil {
		t.Fatal(err)
	}
	if pv.Pictures != 1 || pv.PictureBytes <= 0 {
		t.Fatalf("pictures %d (%d bytes): warnings %v", pv.Pictures, pv.PictureBytes, pv.Warnings)
	}
	out := p.must(p.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "x.odt", Pandoc: p.odt, PandocFormat: "odt", Pictures: "import"}).(imports.ApplyOut)
	if out.Pictures != 1 || p.count(`SELECT count(*) FROM attachments WHERE project_id = ? AND kind = 'image'`, out.ProjectID) != 1 {
		t.Fatalf("%+v", out)
	}
	got := p.docContent(out.ProjectID, "Chapter")
	if !strings.Contains(got, "![a figure](") || strings.Contains(got, "media/media") || strings.Contains(got, "passwd") {
		t.Errorf("text:\n%s", got)
	}
	if n := strings.Count(got, `\[image: `); n < 3 {
		t.Errorf("the pictures that were not kept are marked [image]: %d\n%s", n, got)
	}
	// A file that is not a picture, under a picture's name, is not stored.
	q := newPandocEnv(t, true)
	q.odt = append(q.odt, " second"...)
	pandoctest.Put(t, q.dir, "media.png", []byte("<html><script>not a picture</script></html>"))
	q.md("# Chapter\n\n![x](media/media/image1.png)\n")
	pv, err = q.preview("odt", func(in *imports.PreviewIn) { in.Pictures = "import" })
	if err != nil {
		t.Fatal(err)
	}
	if pv.Pictures != 0 {
		t.Fatalf("a text file was admitted as a picture: %v %v", pv.Warnings, pv.Report)
	}
	if !hasWarning(pv.Warnings, "picture") {
		t.Errorf("no word about the picture that was refused: %v", pv.Warnings)
	}
}

func TestPandocOutputOverTheImportLimitsIsRefused(t *testing.T) {
	p := newPandocEnv(t, true)
	p.md(strings.Repeat("A line of words that goes on.\n\n", 1_200_000)) // ~36 MB of Markdown
	start := time.Now()
	_, err := p.preview("odt", nil)
	if e := ops.AsError(err); e == nil || e.Code != ops.CodeInvalid {
		t.Fatalf("%v", err)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 30*time.Second {
			t.Errorf("took %v", took)
		}
	})
}

func TestPandocOutputThatWouldSlowAParserIsBrokenUp(t *testing.T) {
	p := newPandocEnv(t, true)
	p.md("# Chapter\n\n" + strings.Repeat(">", 60_000) + " deep quote\n\n" + strings.Repeat("[a](", 40_000) + "\n\n" + strings.Repeat("- ", 50_000) + "item\n")
	start := time.Now()
	pv, err := p.preview("odt", nil)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second {
			t.Errorf("took %v", took)
		}
	})
	if !hasWarning(pv.Warnings, "broken up") {
		t.Errorf("warnings %v", pv.Warnings)
	}
}

func TestPandocImportIntoAnExistingProject(t *testing.T) {
	p := newPandocEnv(t, true)
	pid := p.must(p.w, "import.apply", imports.ApplyIn{Token: p.mustPreviewFiles(), Name: "base", Files: files(map[string]string{"one.md": "# One\n\nFirst.\n"})}).(imports.ApplyOut).ProjectID
	p.md("# Added by Pandoc\n\nNew words.\n")
	pv := p.must(p.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "more.rst", Pandoc: p.odt, PandocFormat: "rst"}).(imports.PreviewOut)
	if pv.Layout != "pandoc" {
		t.Fatalf("%s", pv.Layout)
	}
	out := p.must(p.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Name: "more.rst", Pandoc: p.odt, PandocFormat: "rst"}).(imports.ApplyOut)
	if out.Documents != 1 {
		t.Fatalf("%+v", out)
	}
	lo := p.must(p.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	var titles []string
	for _, m := range lo.Items {
		titles = append(titles, m.Title)
	}
	if !strings.Contains(strings.Join(titles, ","), "Added by Pandoc") {
		t.Fatalf("%v", titles)
	}
}

func (p *pandocEnv) mustPreviewFiles() string {
	return p.preview0(files(map[string]string{"one.md": "# One\n\nFirst.\n"}))
}

func (p *pandocEnv) preview0(f []imports.File) string {
	return p.must(p.w, "import.preview", imports.PreviewIn{Name: "base", Files: f}).(imports.PreviewOut).Token
}

var _ = db.Now
