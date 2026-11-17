package compile_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// The Pandoc-backed formats, with a stand-in for Pandoc (internal/pandoc/pandoctest).
// What is checked is what is sent to it, what is recorded, and what happens when it is off or
// misbehaves; nothing here says how a real Pandoc styles a real document.

func pandocFx(t *testing.T, on bool) (*fx, string, ops.Principal) {
	t.Helper()
	f := newFx(t, "The Salt Road")
	// A registry with the bridge, over the same database.
	dir, bin := pandoctest.Install(t)
	f.reg = registry.BuildWith(f.d, events.NewStore(f.d, nil), f.dir, registry.Options{Pandoc: pandoc.Options{TempRoot: t.TempDir()}})
	admin := ops.Principal{UserID: testutil.User(t, f.d, "admin", "adm@t.local"), Role: ops.RoleAdmin, Via: "session"}
	if on {
		yes := true
		if _, err := f.reg.Call(context.Background(), admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &bin}); err != nil {
			t.Fatal(err)
		}
	}
	return f, dir, admin
}

func (f *fx) try(over publish.Options) (compile.Out, *ops.Error) {
	out, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "plain-markdown", Options: over})
	if err != nil {
		return compile.Out{}, ops.AsError(err)
	}
	return out.(compile.Out), nil
}

func TestPandocFormatsAreOffUntilTheBridgeIsOn(t *testing.T) {
	f, dir, _ := pandocFx(t, false)
	f.novel()
	for _, format := range []string{"odt", "rtf", "latex", "rst", "mediawiki"} {
		_, err := f.try(publish.Options{Format: format})
		if err == nil || err.Code != ops.CodeConflict {
			t.Errorf("%s with the bridge off: %v", format, err)
		}
	}
	if pandoctest.Log(t, dir, "args.log") != "" {
		t.Fatal("pandoc ran while the bridge was off")
	}
	// The built-in formats do not need it.
	if _, err := f.try(publish.Options{Format: "md"}); err != nil {
		t.Fatal(err)
	}
	// A format that is not on the list is not one.
	if _, err := f.try(publish.Options{Format: "pdf+x"}); err == nil || err.Code != ops.CodeInvalid {
		t.Fatalf("%v", err)
	}
}

func TestPandocExportSendsTheCompileMarkdownAndRecordsTheVersion(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.novel()
	pandoctest.Put(t, dir, "export.out", []byte("ODT-FILE-BYTES"))
	out, err := f.try(publish.Options{Format: "odt"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Format != "odt" || string(out.Body) != "ODT-FILE-BYTES" || !strings.HasSuffix(out.Filename, ".odt") || !strings.Contains(out.ContentType, "opendocument") {
		t.Fatalf("%+v", out)
	}
	// What Pandoc was given is the compile Markdown: the chosen wording, not the private parts.
	in := pandoctest.Log(t, dir, "input.log")
	for _, want := range []string{"dedmark", "ch1mark", "s1mark"} {
		if !strings.Contains(in, want) {
			t.Errorf("the compile Markdown lacks %s", want)
		}
	}
	for _, gone := range []string{"ghostmark", "privmark", "skipmark", "scriobh:alts", "other wording"} {
		if strings.Contains(in, gone) {
			t.Errorf("Pandoc was given %q", gone)
		}
	}
	// The warning says who wrote it; the export row records the format and the version.
	found := false
	for _, w := range out.Warnings {
		found = found || (w.Code == "pandoc" && strings.Contains(w.Message, "3.1.9"))
	}
	if !found {
		t.Errorf("warnings: %+v", out.Warnings)
	}
	var format, manifest string
	if err := f.d.Read().QueryRow(`SELECT format, manifest_json FROM exports WHERE id = ?`, out.ExportID).Scan(&format, &manifest); err != nil {
		t.Fatal(err)
	}
	if format != "odt" || !strings.Contains(manifest, `"pandoc":{"format":"odt","version":"3.1.9"}`) || !strings.Contains(manifest, `"input_hash"`) {
		t.Fatalf("%s %s", format, manifest)
	}
	// A format whose file extension is not its name is recorded by its name.
	l, err := f.try(publish.Options{Format: "latex"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Format != "latex" || !strings.HasSuffix(l.Filename, ".tex") {
		t.Fatalf("%s %s", l.Format, l.Filename)
	}
	if err := f.d.Read().QueryRow(`SELECT format FROM exports WHERE id = ?`, l.ExportID).Scan(&format); err != nil || format != "latex" {
		t.Fatalf("%q %v", format, err)
	}
	// The same text and the same Pandoc: the same hash; another Pandoc: another hash.
	again, _ := f.try(publish.Options{Format: "odt"})
	if again.InputHash != out.InputHash || again.SHA256 != out.SHA256 {
		t.Errorf("the same input gave another hash: %s %s", again.InputHash, out.InputHash)
	}
	script, _ := os.ReadFile(filepath.Join(dir, "pandoc"))
	if err := os.WriteFile(filepath.Join(dir, "pandoc"), []byte(strings.ReplaceAll(string(script), "pandoc 3.1.9", "pandoc 3.2.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	newer, err := f.try(publish.Options{Format: "odt"})
	if err != nil {
		t.Fatal(err)
	}
	if newer.InputHash == out.InputHash {
		t.Error("the input hash did not change with the Pandoc version")
	}
	// Plain Markdown, which Pandoc does not touch, does not carry the version.
	md, err := f.try(publish.Options{Format: "md"})
	if err != nil {
		t.Fatal(err)
	}
	var mm string
	f.d.Read().QueryRow(`SELECT manifest_json FROM exports WHERE id = ?`, md.ExportID).Scan(&mm)
	if strings.Contains(mm, "pandoc") {
		t.Errorf("%s", mm)
	}
}

func TestPandocExportFailureIsPlainAndStoresNothing(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.novel()
	var before int
	f.d.Read().QueryRow(`SELECT count(*) FROM exports`).Scan(&before)
	pandoctest.SetMode(t, dir, "fail")
	_, err := f.try(publish.Options{Format: "rtf"})
	if err == nil || err.Code != ops.CodeInvalid || strings.Contains(err.Message, "SECRET") {
		t.Fatalf("%v", err)
	}
	var after int
	f.d.Read().QueryRow(`SELECT count(*) FROM exports`).Scan(&after)
	if after != before {
		t.Fatal("a failed export was recorded")
	}
}

func TestPandocExportIsNotRunForADryRun(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.novel()
	_, err := f.reg.CallDry(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "plain-markdown", Options: publish.Options{Format: "odt"}})
	if err == nil {
		t.Fatal("a dry run converted")
	}
	if pandoctest.Log(t, dir, "args.log") != "" {
		t.Fatal("pandoc ran for a dry run")
	}
}

// A project's own text can hold a YAML block, raw LaTeX and raw HTML. Pandoc reads the compile Markdown with raw HTML
// and the YAML metadata block turned off (-f gfm-raw_html-yaml_metadata_block), and is given no option that takes
// metadata, a template, a filter or an include from the request, so none of them is read as metadata or let through
// as raw output. Checked here against the fake: the arguments and the Markdown it was handed. What a real Pandoc
// does with this Markdown under those flags is not shown by this test.
func TestPandocExportReadsTheMarkdownWithNoMetadataAndNoRawOutput(t *testing.T) {
	f, dir, _ := pandocFx(t, true)
	f.doc("Opening", "chapter", "", "---\ntitle: hijacked\nauthor: someone else\nheader-includes: x\n---\n\nbody hardmark.\n\n```{=latex}\n\\write18{evil}\n```\n\n<div style=\"x\">raw html hardmark2</div>\n\n<script>alert(1)</script>\n")
	if _, err := f.try(publish.Options{Format: "latex"}); err != nil {
		t.Fatal(err)
	}
	args := strings.Split(pandoctest.Log(t, dir, "args.log"), "\n")
	reader := ""
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			reader = args[i+1]
		}
		for _, bad := range []string{"--metadata", "-M", "--variable", "-V", "--template", "--lua-filter", "-L", "--filter", "-F", "--include-in-header", "-H", "--include-before-body", "-B", "--include-after-body", "-A", "--metadata-file", "--defaults", "-d", "--reference-doc", "--css", "--syntax-definition", "--resource-path", "--extract-media"} {
			if a == bad || strings.HasPrefix(a, bad+"=") {
				t.Errorf("Pandoc was given %s", a)
			}
		}
	}
	if reader != "gfm-raw_html-yaml_metadata_block" {
		t.Fatalf("the reader is %q", reader)
	}
	// The text reaches Pandoc as text, in a file, unchanged by this program.
	in := pandoctest.Log(t, dir, "input.log")
	if !strings.Contains(in, "hardmark.") || !strings.Contains(in, "hardmark2") {
		t.Errorf("the compile Markdown lacks the text:\n%s", in)
	}
}
