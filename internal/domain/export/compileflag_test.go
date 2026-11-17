package export_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
)

// exclude marks a document "do not compile".
func (f *fx) exclude(d docs.Doc) {
	f.t.Helper()
	no := false
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, IncludeInCompile: &no}); err != nil {
		f.t.Fatal(err)
	}
}

// A document marked not to compile, and everything inside it, is left out of
// the Markdown and HTML exports unless asked for; the manifest says what.
func TestExportsHonourIncludeInCompile(t *testing.T) {
	f := newFx(t, "Flags")
	d := f.novel()
	f.exclude(d["c1"]) // the chapter, and with it its scenes
	f.exclude(d["back"])

	md := f.must("single", false)
	s := string(md.Body)
	for _, gone := range []string{"ch1mark", "s1mark", "s2mark", "backmark"} {
		if strings.Contains(s, gone) {
			t.Errorf("markdown holds %q of an excluded document", gone)
		}
	}
	for _, kept := range []string{"ch2mark", "ch3mark", "frontmark"} {
		if !strings.Contains(s, kept) {
			t.Errorf("markdown lost %q", kept)
		}
	}
	// the chapter, its two scenes, its note and the afterword
	if md.Skipped != 5 {
		t.Errorf("skipped = %d, want 5 (chapter, two scenes, note, afterword)", md.Skipped)
	}

	var m struct {
		Docs []struct{ ID string } `json:"documents"`
		Left []string              `json:"not_compiled"`
	}
	var manifest string
	if err := f.d.Read().QueryRow(`SELECT manifest_json FROM exports WHERE id = ?`, md.ExportID).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Left) != md.Skipped {
		t.Errorf("manifest not_compiled %v", m.Left)
	}
	for _, h := range m.Docs {
		if h.ID == d["c1"].ID || h.ID == d["s1"].ID || h.ID == d["back"].ID {
			t.Errorf("the manifest holds an excluded document %s", h.ID)
		}
	}

	// A bundle is the full copy: it holds everything by default, so a restore
	// loses nothing, and filters only when asked to.
	b := f.must("bundle", false)
	names, _, _ := unzip(t, b.Body)
	have := strings.Join(names, " ")
	if !strings.Contains(have, "arrival") || !strings.Contains(have, "afterword") || b.Skipped != 0 {
		t.Errorf("the bundle left something out by default: %s (skipped %d)", have, b.Skipped)
	}
	no := false
	fb, err := f.reg.Call(f.ctx, f.owner, "export.markdown", export.MarkdownIn{ProjectID: f.pid, Mode: "bundle", IncludeExcluded: &no})
	if err != nil {
		t.Fatal(err)
	}
	names, _, _ = unzip(t, fb.(export.File).Body)
	for _, n := range names {
		if strings.Contains(n, "arrival") || strings.Contains(n, "afterword") || strings.Contains(n, "harbour") {
			t.Errorf("the filtered bundle holds %s", n)
		}
	}
	if fb.(export.File).Skipped != 5 {
		t.Errorf("filtered bundle skipped %d", fb.(export.File).Skipped)
	}

	// A single file asking for everything brings them back.
	yes := true
	out, err := f.reg.Call(f.ctx, f.owner, "export.markdown", export.MarkdownIn{ProjectID: f.pid, Mode: "single", IncludeExcluded: &yes})
	if err != nil {
		t.Fatal(err)
	}
	all := out.(export.File)
	if all.Skipped != 0 || !strings.Contains(string(all.Body), "ch1mark") || !strings.Contains(string(all.Body), "backmark") {
		t.Errorf("include_excluded left things out: skipped=%d", all.Skipped)
	}

	// HTML too.
	hout, err := f.reg.Call(f.ctx, f.owner, "export.html", export.HTMLIn{ProjectID: f.pid, Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}
	h := hout.(export.File)
	if strings.Contains(string(h.Body), "ch1mark") || !strings.Contains(string(h.Body), "ch2mark") || h.Skipped == 0 {
		t.Errorf("html did not honour the flag (skipped %d)", h.Skipped)
	}
	hout, err = f.reg.Call(f.ctx, f.owner, "export.html", export.HTMLIn{ProjectID: f.pid, Mode: "single", IncludeExcluded: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hout.(export.File).Body), "ch1mark") {
		t.Error("html include_excluded left the chapter out")
	}
}
