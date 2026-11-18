package compile_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func TestCoverSpecAndPDF(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	call := func(op string, in compile.CoverIn) (any, error) { return f.reg.Call(f.ctx, f.owner, op, in) }
	// the thickness is required
	if _, err := call("compile.cover_spec", compile.CoverIn{ProjectID: f.pid}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("no thickness: %v", err)
	}
	// a given page count: the arithmetic is exact
	o, err := call("compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	s := o.(compile.CoverSpecOut)
	if s.PageSource != "given" || math.Abs(s.SpineIn-0.75) > 1e-6 || math.Abs(s.WrapWidthIn-(0.25+12+0.75)) > 1e-6 || math.Abs(s.WrapHeightIn-9.25) > 1e-6 || !s.SpineHasText {
		t.Errorf("%+v", s)
	}
	if math.Abs((s.Spine.X1-s.Spine.X0)-54) > 0.01 || math.Abs(s.Front.X1-s.WrapWidthIn*72+9) > 0.01 {
		t.Errorf("panels %+v %+v %+v", s.Back, s.Spine, s.Front)
	}
	// the layout's page count is the PDF's
	o, err = call("compile.cover_spec", compile.CoverIn{ProjectID: f.pid, Preset: "print-5x8", PageThicknessIn: 0.002})
	if err != nil {
		t.Fatal(err)
	}
	ls := o.(compile.CoverSpecOut)
	book := f.compile("print-5x8", compile_noOptions())
	doc, _ := check.ParsePDF(book.Body)
	if ls.PageSource != "layout" || ls.PageCount != len(doc.Pages) {
		t.Errorf("cover says %d pages (%s), the book has %d", ls.PageCount, ls.PageSource, len(doc.Pages))
	}
	if len(ls.Warnings) == 0 { // a short novel: few pages, a narrow spine
		t.Log("no warnings for", ls.PageCount, "pages")
	}
	// the cover PDF
	out, err := call("export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025, Guides: true, Barcode: true})
	if err != nil {
		t.Fatal(err)
	}
	co := out.(compile.Out)
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "cover.pdf"), co.Body, 0o644)
	}
	cd, probs := check.ParsePDF(co.Body)
	for _, p := range probs {
		t.Error(p)
	}
	if len(cd.Pages) != 1 || co.Format != "pdf" || !strings.HasSuffix(co.Filename, "-cover.pdf") {
		t.Fatalf("%d pages %s", len(cd.Pages), co.Filename)
	}
	// the front alone is a file of its own
	fo, err := call("export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025, Guides: true, Barcode: true, Part: "front"})
	if err != nil {
		t.Fatal(err)
	}
	fd, fprobs := check.ParsePDF(fo.(compile.Out).Body)
	for _, p := range fprobs {
		t.Error(p)
	}
	if len(fd.Pages) != 1 || !strings.HasSuffix(fo.(compile.Out).Filename, "-cover-front.pdf") {
		t.Fatalf("the front: %d pages %s", len(fd.Pages), fo.(compile.Out).Filename)
	}
	if _, err := call("export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025, Part: "proof"}); ops.AsError(err) == nil || ops.AsError(err).Data["reason"] != "cover_no_proof" {
		t.Errorf("the proof of a cover with no layout: %v", err)
	}
	if fo.(compile.Out).InputHash == co.InputHash {
		t.Error("the front and the wrap have one hash")
	}
	w, h := (0.25+12+0.75)*72, 9.25*72
	if math.Abs(cd.Pages[0].W-w) > 0.01 || math.Abs(cd.Pages[0].H-h) > 0.01 || math.Abs(fd.Pages[0].W-(6.25*72)) > 0.01 {
		t.Errorf("page sizes %vx%v, %v", cd.Pages[0].W, cd.Pages[0].H, fd.Pages[0].W)
	}
	if !strings.Contains(cd.Pages[0].Text(), "The Salt Road") || !strings.Contains(cd.Pages[0].Text(), "Máire Ní Bhriain") || !strings.Contains(fd.Pages[0].Text(), "A sub title") {
		t.Errorf("text: %q / %q", cd.Pages[0].Text(), fd.Pages[0].Text())
	}
	again, _ := call("export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025, Guides: true, Barcode: true})
	if !bytes.Equal(again.(compile.Out).Body, co.Body) {
		t.Error("two covers differ")
	}
	// a thin book has no type on the spine
	thin, err := call("export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 60, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	var narrow bool
	for _, w := range thin.(compile.Out).Warnings {
		if w.Code == "spine_narrow" {
			narrow = true
		}
	}
	if !narrow {
		t.Error("a 0.15 in spine was not reported as too narrow")
	}
	// not a print preset
	if _, err := call("compile.cover_spec", compile.CoverIn{ProjectID: f.pid, Preset: "epub3", PageCount: 100, PageThicknessIn: 0.0025}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("epub: %v", err)
	}
	// a reader cannot
	if _, err := f.reg.Call(f.ctx, f.reader, "export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 100, PageThicknessIn: 0.0025}); err == nil {
		t.Error("a reader made a cover")
	}
}

func compile_noOptions() publish.Options { return publish.Options{} }
