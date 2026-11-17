package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func TestPrintPresetsProduceValidPDFs(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	for _, id := range []string{"print-6x9", "print-5.5x8.5", "print-5x8", "print-a5", "proof-letter", "proof-a4"} {
		out := f.compile(id, publish.Options{})
		if out.Format != "pdf" || out.ContentType != "application/pdf" || !strings.HasSuffix(out.Filename, "-"+id+".pdf") {
			t.Errorf("%s: %+v", id, out.Filename)
		}
		if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "compile-"+id+".pdf"), out.Body, 0o644)
		}
		doc, probs := check.ParsePDF(out.Body)
		for _, p := range probs {
			t.Errorf("%s: %s", id, p)
		}
		txt := doc.Text()
		for _, want := range []string{"The Salt Road", "The Arrival", "ch1mark", "s1mark", "s2mark", "backmark", "dedmark", "ch3mark", "notemark"} {
			if !strings.Contains(txt, want) {
				t.Errorf("%s: %q is not in the PDF's text", id, want)
			}
		}
		// what a compile never holds
		for _, never := range []string{"privmark", "ghostmark", "skipmark", "kidmark", "gonemark", "n1mark", "n2mark", "other wording"} {
			if strings.Contains(txt, never) {
				t.Errorf("%s: %q is in the PDF", id, never)
			}
		}
		if doc.Title != "The Salt Road" || doc.Author != "Máire Ní Bhriain" || doc.Lang == "" {
			t.Errorf("%s: metadata %q %q %q", id, doc.Title, doc.Author, doc.Lang)
		}
		// the preview and the export agree on the hash and the page count, and the page count is real
		p := f.preview(id, publish.Options{})
		if p.InputHash != out.InputHash {
			t.Errorf("%s: the preview's input hash differs from the export's", id)
		}
		if p.PageCount != len(doc.Pages) || p.PageEstimated || p.Print == nil {
			t.Errorf("%s: preview says %d pages (estimated %v), the file has %d", id, p.PageCount, p.PageEstimated, len(doc.Pages))
		}
		if p.Print != nil && (p.Print.BodyStart < 1 || len(p.Print.Pages) != 3) {
			t.Errorf("%s: %+v", id, p.Print)
		}
		// the outline's pages are the pages the headings are on
		for _, o := range p.Outline {
			if o.Kind == "chapter" && o.Page > 0 {
				if !strings.Contains(doc.Pages[o.Page-1].Text(), strings.ToUpper(strings.Fields(o.Heading)[0])) && !strings.Contains(doc.Pages[o.Page-1].Text(), o.Title) {
					t.Errorf("%s: chapter %q is said to start on page %d, whose text is %q", id, o.Title, o.Page, doc.Pages[o.Page-1].Text()[:min(60, len(doc.Pages[o.Page-1].Text()))])
				}
			}
		}
		// deterministic
		again := f.compile(id, publish.Options{})
		if !bytes.Equal(out.Body, again.Body) || out.SHA256 != again.SHA256 {
			t.Errorf("%s: two compiles differ", id)
		}
	}
}

func TestPagedHTMLPreset(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	out := f.compile("paged-html", publish.Options{})
	if out.ContentType != "text/html; charset=utf-8" || !strings.HasSuffix(out.Filename, ".html") || out.Format != "html" {
		t.Errorf("%+v", out.Filename)
	}
	h := string(out.Body)
	if !strings.Contains(h, "@page") || !strings.Contains(h, "ch1mark") || strings.Contains(h, "privmark") || strings.Contains(h, "<script") {
		t.Error("the paged HTML is wrong")
	}
	p := f.preview("paged-html", publish.Options{})
	if p.InputHash != out.InputHash || p.PageCount != 0 || p.Print != nil {
		t.Errorf("%+v", p.PageCount)
	}
}

func TestPrintOptionsChangeTheHashAndTheFile(t *testing.T) {
	f := newFx(t, "Options")
	f.novel()
	base := f.compile("print-6x9", publish.Options{})
	for name, over := range map[string]publish.Options{
		"trim":     {PageSize: "5x8"},
		"size":     {FontSize: 12},
		"margin":   {MarginInside: publish.F(1)},
		"dropcap":  {DropCap: publish.True()},
		"ornament": {SceneBreak: "stars"},
		"heads":    {RunningHeads: publish.False()},
		"toc":      {TOC: publish.False()},
		"notes":    {Footnotes: "chapter"},
		"imprint":  {Imprint: "My own line"},
		"crop":     {CropMarks: publish.True()},
		"justify":  {Justify: publish.False()},
	} {
		o := f.compile("print-6x9", over)
		if o.InputHash == base.InputHash || bytes.Equal(o.Body, base.Body) {
			t.Errorf("%s did not change the hash and the file", name)
		}
		if _, probs := check.ParsePDF(o.Body); len(probs) > 0 {
			t.Errorf("%s: %v", name, probs)
		}
	}
	// the imprint is on the copyright page
	imp := f.compile("print-6x9", publish.Options{Imprint: "Copyright 2026 The Writer\nISBN 000-0-00-000000-0"})
	doc, _ := check.ParsePDF(imp.Body)
	if !strings.Contains(doc.Text(), "ISBN 000-0-00-000000-0") {
		t.Error("the imprint is missing")
	}
	// hyphenation is not built
	if _, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9", Options: publish.Options{Hyphenate: publish.True()}}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("hyphenate: %v", err)
	}
}

func TestPrintRefusesRightToLeft(t *testing.T) {
	f := newFx(t, "Right to left")
	f.doc("One", "chapter", "", "Words.")
	_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9", Options: publish.Options{Language: "ar"}})
	if ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "right-to-left") {
		t.Errorf("%v", err)
	}
	p := f.preview("print-6x9", publish.Options{Language: "he"})
	var coded bool
	for _, w := range p.Warnings {
		if w.Code == "layout_refused" {
			coded = true
		}
	}
	if !coded {
		t.Errorf("the preview does not say so: %+v", p.Warnings)
	}
}
