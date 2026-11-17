package compile_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
	"github.com/crueber/scriobh/internal/publish"
)

// FAILS by design (TestReviewASlowPandocExportDoesNotHoldTheCompileGate). export.compile holds one of the compile gate's slots (shared with every other compile and with site builds)
// for the whole of a Pandoc run, which may take the runner's full minute. Two slow Pandoc exports by anyone with
// editor rights on any project make every other compile on the instance answer 429 for as long as they last. The
// runner has its own limit of two, so the gate adds nothing for Pandoc but the starvation.
func TestReviewASlowPandocExportDoesNotHoldTheCompileGate(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.novel()
	script, _ := os.ReadFile(filepath.Join(dir, "pandoc"))
	if err := os.WriteFile(filepath.Join(dir, "pandoc"), []byte(strings.ReplaceAll(string(script), "slow) sleep 1;;", "slow) sleep 5;;")), 0o755); err != nil {
		t.Fatal(err)
	}
	pandoctest.SetMode(t, dir, "slow")
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.try(publish.Options{Format: "odt"}) }()
	}
	time.Sleep(700 * time.Millisecond) // both are inside Pandoc
	start := time.Now()
	_, err := f.try(publish.Options{Format: "md"}) // no Pandoc in this one
	if err != nil {
		t.Errorf("a plain Markdown compile was refused while two Pandoc exports ran (after %v): %v", time.Since(start).Round(100*time.Millisecond), err)
	}
	wg.Wait()
}

// FAILS by design (TestReviewPresetAndPreviewFormatEnumsListThePandocFormats): the schema of the previews and
// presets still lists the five old formats.
func TestReviewPresetAndPreviewFormatEnumsListThePandocFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{{"PreviewOut", reflect.TypeOf(compile.PreviewOut{})}, {"Preset", reflect.TypeOf(compile.Preset{})}} {
		sf, _ := tc.typ.FieldByName("Format")
		enum := sf.Tag.Get("enum")
		for _, f := range pandoc.ExportFormats() {
			if !strings.Contains(","+enum+",", ","+f+",") {
				t.Errorf("%s.Format enum %q lacks %q, which a custom preset may now hold", tc.name, enum, f)
			}
		}
	}
}

// FAILS by design, and a POLICY guard, not a demonstrated exploit (TestReviewBinaryInsideTheDataDirectoryIsRefused):
// an administrator may point the bridge at any executable the server's user owns, including one inside the data
// directory. The check is owner and mode only. No server path that writes an executable file there was found; the
// rule is cheap insurance for one.
func TestReviewBinaryInsideTheDataDirectoryIsRefused(t *testing.T) {
	f, _, admin := pandocFx(t, false)
	inside := filepath.Join(f.dir, "files", "x")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(inside, "pandoc")
	if err := os.WriteFile(bin, []byte(pandoctest.Script), 0o755); err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := f.reg.Call(context.Background(), admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &bin}); err == nil {
		t.Error("the bridge was switched on with a program that lives inside the data directory")
	} else if oe := ops.AsError(err); oe == nil {
		t.Errorf("%v", err)
	}
}

// GUARD (passes today): an export format that is not one of the named words is refused before Pandoc is started.
func TestReviewExportRefusesHostileFormatNamesBeforePandocStarts(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.novel()
	for _, name := range []string{"-", "--", "=", " odt", "odt ", "odt\x00", "оdt", "ODT", "rst+raw_html", "latex+raw_tex", "gfm+lua", "odt\n--lua-filter=x", "custom.lua", "docx+x", "pdf"} {
		if _, err := f.try(publish.Options{Format: name}); err == nil && name != "pdf" {
			t.Errorf("format %q was accepted", name)
		}
	}
	if pandoctest.Log(t, dir, "args.log") != "" {
		t.Fatal("Pandoc was started for a name that is not on the list")
	}
}
