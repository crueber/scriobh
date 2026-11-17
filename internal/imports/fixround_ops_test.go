package imports_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Reading a source is done a few at a time for the whole process: preview, apply
// and the dry run of an apply are all conversions, and a conversion that cannot
// have a place within a few seconds is told to try again (429).
func TestImportConversionsAreLimitedForTheWholeProcess(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, "testdata/Salt Road.scriv")
	pv := e.previewConv(imports.PreviewIn{Name: "p.zip", Zip: z}) // a place is free: this works
	imports.ClearCache()                                          // so that what follows must read the source again

	defer imports.SetConcurrency(1, 150*time.Millisecond)()
	release := imports.HoldPlace()
	busy := func(what string, err error) {
		t.Helper()
		if err == nil || ops.AsError(err).Code != ops.CodeRateLimited || ops.AsError(err).RetryAfterSeconds == 0 {
			t.Errorf("%s while the place is taken: %v", what, err)
		}
	}
	start := time.Now()
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "p.zip", Zip: z})
	busy("a preview", err)
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("it did not wait for a place first")
	}
	_, err = e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "p.zip", Zip: z})
	busy("an apply", err)
	_, err = e.reg.CallDry(context.Background(), e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "p.zip", Zip: z})
	busy("a dry run of an apply", err)
	_, err = e.call(e.w, "import.preview", imports.PreviewIn{Name: "f", Files: files(map[string]string{"01-a.md": "# A\n\nText.\n"})}) // a folder is a conversion too
	busy("a folder preview", err)
	release()
	// a Word document and an Obsidian vault too
	release = imports.HoldPlace()
	_, err = e.call(e.w, "import.preview", imports.PreviewIn{Name: "v", Files: files(map[string]string{"a.md": "x"}), Layout: "obsidian"})
	busy("an Obsidian preview", err)
	_, err = e.call(e.w, "import.preview", imports.PreviewIn{Name: "b.docx", Docx: miniDocx(t, map[string]string{"word/document.xml": body(para("", "x"))})})
	busy("a Word preview", err)
	release()

	// Free again: all of them work, and the dry run converts and then rolls back.
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "p.zip", Zip: z}); err != nil {
		t.Errorf("preview after the place is free: %v", err)
	}
	dry, err := e.reg.CallDry(context.Background(), e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "p.zip", Zip: z})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if out, ok := dry.Out.(imports.ApplyOut); !ok || out.Documents != 11 {
		t.Errorf("dry run: %+v", dry.Out)
	}
	if n := rowCount(e, `SELECT count(*) FROM projects WHERE title = 'The Salt Road'`); n != 0 {
		t.Errorf("a dry run kept %d projects", n)
	}

	// A caller that goes while it waits is released at once, not after the wait.
	imports.ClearCache()
	imports.SetConcurrency(1, time.Minute)
	release = imports.HoldPlace()
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = e.reg.Call(ctx, e.w, "import.preview", imports.PreviewIn{Name: "p.zip", Zip: z})
	gone := time.Since(start)
	if err == nil {
		t.Errorf("a caller that went away: %v after %v", err, gone)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if gone > 5*time.Second {
			t.Errorf("a caller that went away: %v after %v", err, gone)
		}
	})
}

// The apply that follows a preview does not read the source again: what the source
// was read into is kept for a few minutes under a hash of the bytes, the layout and
// the options. A source that differs by a byte, or an option that changes the
// reading, is read afresh; the apply still checks its token.
func TestApplyReusesTheConversionOfItsPreview(t *testing.T) {
	e := newEnv(t)
	imports.ClearCache()
	files := bundle(t, "testdata/Salt Road.scriv")
	before := imports.Conversions()
	pv := e.previewConv(imports.PreviewIn{Name: "p", Files: files})
	if n := imports.Conversions() - before; n != 1 {
		t.Fatalf("a preview read the source %d times", n)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "p", Files: files})
	if n := imports.Conversions() - before; n != 1 {
		t.Errorf("the apply read the source again (%d reads in all)", n)
	}
	if ap.Documents != 11 || ap.ResearchNotes != 7 {
		t.Errorf("apply %+v", ap)
	}
	// the preview again, edited: still the one reading
	e.previewConv(imports.PreviewIn{Name: "p", Files: files, Edits: []imports.Edit{{Key: pv.Research[0].Key, Skip: true}}})
	if n := imports.Conversions() - before; n != 1 {
		t.Errorf("an edit made the source be read again (%d)", n)
	}
	// another option, another name, another byte: each is another reading
	e.previewConv(imports.PreviewIn{Name: "p", Files: files, ScrivResearch: "skip"})
	e.previewConv(imports.PreviewIn{Name: "other", Files: files})
	changed := bundle(t, "testdata/Salt Road.scriv")
	for i := range changed {
		if strings.HasSuffix(changed[i].Path, "000000000053/content.rtf") {
			changed[i].Data = bytes.ReplaceAll(changed[i].Data, []byte("two roads"), []byte("three roads"))
		}
	}
	pv2 := e.previewConv(imports.PreviewIn{Name: "p", Files: changed})
	if n := imports.Conversions() - before; n != 4 {
		t.Errorf("%d readings for four different sources", n)
	}
	if pv2.Token == pv.Token {
		t.Errorf("a changed source has the same token")
	}
	// the old token is refused for the changed source, cached or not
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "p", Files: changed}); err == nil {
		t.Errorf("an old token was accepted for another source")
	}
	if imports.CacheLen() > 2 {
		t.Errorf("%d sources kept", imports.CacheLen())
	}
	imports.ClearCache()
	if n := imports.CacheLen(); n != 0 {
		t.Errorf("not cleared: %d", n)
	}
}
