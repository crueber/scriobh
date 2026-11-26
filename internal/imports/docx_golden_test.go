package imports_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
)

// The golden file pins what a Word file's preview and apply say, byte for byte. It covers the corpus samples, long synthetic books,
// the cut options a writer can set (one by one, the way a split adds breaks), the tracked-changes modes, and the documents an apply
// writes for the default options. A change to the reading or the cutting of a Word file must leave every line as it is, or say in
// the same commit why the writer's result changed. Regenerate with UPDATE_DOCX_GOLDEN=1 only for such a change.
const docxGolden = "testdata/docx-cut.golden"

type goldenCase struct {
	name  string
	data  []byte
	in    imports.PreviewIn
	apply bool // also apply with the default options and hash the documents written
}

func goldenCases(t *testing.T) []goldenCase {
	var cs []goldenCase
	for _, s := range corpusSamples() {
		data := corpusDoc(s.paras)
		base := "corpus/" + s.name
		cs = append(cs,
			goldenCase{name: base, data: data, apply: true},
			goldenCase{name: base + "/size", data: data, in: imports.PreviewIn{DocxSplit: "size", DocxSplitWords: 1500}},
			goldenCase{name: base + "/none", data: data, in: imports.PreviewIn{DocxSplit: "none"}},
			goldenCase{name: base + "/label", data: data, in: imports.PreviewIn{DocxSplit: "label"}},
			goldenCase{name: base + "/breaks", data: data, in: imports.PreviewIn{DocxBreaks: []int{4, 9, 17}}},
			goldenCase{name: base + "/pattern", data: data, in: imports.PreviewIn{DocxSplit: "pattern", DocxSplitPattern: `^chapter`, DocxSplitMatch: "starts"}},
		)
	}
	for _, kind := range []string{"h1", "plain"} {
		data := corpusDoc(corpusBig(kind, 60, 100))
		base := "book/" + kind
		cs = append(cs, goldenCase{name: base, data: data, apply: true})
		// Breaks of the writer's own, added one at a time: each step is the cut a split of the draft makes.
		var ords []int
		for _, o := range []int{3, 40, 120, 500, 900, 1400} {
			ords = append(ords, o)
			cs = append(cs, goldenCase{name: fmt.Sprintf("%s/breaks-%d", base, len(ords)), data: data, in: imports.PreviewIn{DocxBreaks: append([]int(nil), ords...)}})
		}
		cs = append(cs, goldenCase{name: base + "/pattern", data: data, in: imports.PreviewIn{DocxSplit: "pattern", DocxSplitPattern: `(?i)^(the|a|marsh) .* 7$`, DocxSplitMatch: "regex"}})
		cs = append(cs, goldenCase{name: base + "/size", data: data, in: imports.PreviewIn{DocxSplit: "size", DocxSplitWords: 4000}})
	}
	cs = append(cs, goldenCase{name: "book/500k", data: corpusDoc(corpusBig("h1", 1000, 20))})
	d1, d2 := "2024-03-05T10:21:00Z", "2024-04-09T08:00:00Z"
	tracked := body(
		para("Title", "The Book") + para("Heading1", "One") +
			para2(run("The boat came in at "), del("Ann Editor", d1, "dawn"), ins("Ann Editor", d1, "first light"), run(" and the harbour was quiet. Nobody "), ins("Bo Reader", d2, "ever "), run("spoke of the winter.")) +
			para2(run("A second paragraph that stays "), del("Bo Reader", d2, "very "), run("exactly as it was written, with *stars* and [brackets] and a_b.")) +
			para("Heading1", "Two") +
			para2(run("Chapter two has "), ins("Ann Editor", d1, "an added sentence. "), run("Then the rest of the text goes on for a while so it is long enough.")))
	tdoc := miniDocx(t, map[string]string{"word/document.xml": tracked, "word/styles.xml": headingStyles})
	for _, mode := range []string{"suggest", "accept", "reject"} {
		cs = append(cs, goldenCase{name: "tracked/" + mode, data: tdoc, in: imports.PreviewIn{DocxChanges: mode}})
	}
	cs = append(cs, goldenCase{name: "tracked/default", data: tdoc, apply: true})
	return cs
}

// previewLine is one case's line: its name and the hash of what its preview said, or the refusal. Previews run before any apply:
// an apply records the source, and a later preview of the same bytes says so, with ids and times that differ from run to run.
func previewLine(e *env, c goldenCase) (string, imports.PreviewOut, bool) {
	e.t.Helper()
	h := sha256.New()
	out, err := e.reg.Call(context.Background(), e.w, "import.preview", imports.PreviewIn{Docx: c.data, Name: "book.docx", DocxSplit: c.in.DocxSplit, DocxSplitStyle: c.in.DocxSplitStyle,
		DocxSplitWords: c.in.DocxSplitWords, DocxBreaks: c.in.DocxBreaks, DocxSplitPattern: c.in.DocxSplitPattern, DocxSplitMatch: c.in.DocxSplitMatch,
		DocxChanges: c.in.DocxChanges})
	if err != nil {
		e.t.Logf("%s refused: %v", c.name, err)
		fmt.Fprintf(h, "error: %v", err)
		return c.name + " " + hex.EncodeToString(h.Sum(nil)), imports.PreviewOut{}, false
	}
	pv := out.(imports.PreviewOut)
	raw, merr := json.Marshal(pv)
	if merr != nil {
		e.t.Fatal(merr)
	}
	h.Write(raw)
	return c.name + " " + hex.EncodeToString(h.Sum(nil)), pv, true
}

// applyLine is what an apply with the default options writes: the hash of every document's title and text.
func applyLine(e *env, c goldenCase, pv imports.PreviewOut) string {
	e.t.Helper()
	h := sha256.New()
	ap, err := e.reg.Call(context.Background(), e.w, "import.apply", imports.ApplyIn{Docx: c.data, Name: "book.docx", Token: pv.Token})
	if err != nil {
		fmt.Fprintf(h, "apply error: %v", err)
		return c.name + "/apply " + hex.EncodeToString(h.Sum(nil))
	}
	pid := ap.(imports.ApplyOut).ProjectID
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range lo.Items {
		content := e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item.Content
		fmt.Fprintf(h, "\n%s\n%s", m.Title, content)
	}
	return c.name + "/apply " + hex.EncodeToString(h.Sum(nil))
}

// TestDocxCutGolden holds what the preview and apply of each case say to the golden file. The first run that finds no file, or a
// different line, fails; UPDATE_DOCX_GOLDEN=1 writes the file instead.
func TestDocxCutGolden(t *testing.T) {
	e := newEnv(t)
	cases := goldenCases(t)
	var lines []string
	type applied struct {
		c  goldenCase
		pv imports.PreviewOut
	}
	var applies []applied
	for _, c := range cases {
		line, pv, ok := previewLine(e, c)
		lines = append(lines, line)
		if ok && c.apply {
			applies = append(applies, applied{c, pv})
		}
	}
	for _, a := range applies {
		lines = append(lines, applyLine(e, a.c, a.pv))
	}
	got := strings.Join(lines, "\n") + "\n"
	path := docxGolden
	if os.Getenv("UPDATE_DOCX_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (%s): %v; run with UPDATE_DOCX_GOLDEN=1 once on the code that is the reference", docxGolden, err)
	}
	if string(want) != got {
		wl := strings.Split(string(want), "\n")
		gl := strings.Split(got, "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if wl[i] != gl[i] {
				t.Errorf("line %d differs:\n want %s\n got  %s", i+1, wl[i], gl[i])
			}
		}
		if len(wl) != len(gl) {
			t.Errorf("%d lines in the golden file, %d now", len(wl), len(gl))
		}
	}
}
