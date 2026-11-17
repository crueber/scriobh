package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// A converted source can be added to a project that exists: the documents after
// what is there, the research after the research there, nothing changed.
func TestConvertedSourcesAppend(t *testing.T) {
	e := newEnv(t)
	base := e.apply(files(map[string]string{"01-first.md": "# First\n\nText.\n"}), "", nil)
	e.must(e.w, "project.research.create", research.CreateIn{ID: base.ProjectID, Kind: "note", Title: "Mine", Body: "kept"})

	z := zipDir(t, "testdata/Salt Road.scriv")
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: base.ProjectID, Name: "Salt Road.scriv.zip", Zip: z}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutScrivener || pv.Documents != 11 || pv.ResearchTotal != 7 {
		t.Fatalf("preview: %q %d %d", pv.Layout, pv.Documents, pv.ResearchTotal)
	}
	ap := e.must(e.w, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "Salt Road.scriv.zip", Zip: z}).(imports.ApplyOut)
	if ap.Documents != 11 || ap.ResearchNotes != 7 {
		t.Fatalf("append %+v", ap)
	}
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: base.ProjectID}).(docs.ListOut)
	if len(lo.Items) != 12 || lo.Items[0].Title != "First" {
		t.Errorf("documents: %d, first %q", len(lo.Items), lo.Items[0].Title)
	}
	rl := e.must(e.w, "project.research.list", research.ListIn{ID: base.ProjectID}).(research.ListOut)
	if len(rl.Items) != 8 {
		t.Fatalf("research notes: %d", len(rl.Items))
	}
	top := []string{}
	for _, n := range rl.Items {
		if n.ParentID == "" {
			top = append(top, n.Title)
		}
	}
	if top[0] != "Mine" || len(top) != 6 {
		t.Errorf("top-level research: %v", top)
	}
	// A writer who cannot edit the project cannot append to it.
	reader := e.user("reader", ops.RoleReader)
	if _, err := e.call(reader, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "x", Zip: z}); err == nil {
		t.Errorf("a stranger appended")
	}
}

// A converted source with nothing in it is refused plainly.
func TestConvertedSourcesRefuseWhatIsNotTheirs(t *testing.T) {
	e := newEnv(t)
	for name, in := range map[string]imports.PreviewIn{
		"not a zip":            {Name: "x", Zip: []byte("hello"), Layout: "scrivener"},
		"no scrivx":            {Name: "x", Layout: "scrivener", Files: []imports.File{{Path: "a/b.rtf", Data: []byte("{\\rtf1 x}")}}},
		"not a wordpress file": {Name: "x", Layout: "wordpress", Files: []imports.File{{Path: "a.xml", Data: []byte("<html/>")}}},
		"no xml at all":        {Name: "x", Layout: "wordpress", Files: []imports.File{{Path: "a.md", Data: []byte("# x")}}},
		"not a ghost file":     {Name: "x", Layout: "ghost", Files: []imports.File{{Path: "a.json", Data: []byte(`{"hello":1}`)}}},
		"ghost with no posts":  {Name: "x", Layout: "ghost", Files: []imports.File{{Path: "a.json", Data: []byte(`{"db":[{"data":{"posts":[]}}]}`)}}},
		"ghost not json":       {Name: "x", Layout: "ghost", Files: []imports.File{{Path: "a.json", Data: []byte(`<xml/>`)}}},
		"wxr with a DOCTYPE":   {Name: "x", Layout: "wordpress", Files: []imports.File{{Path: "a.xml", Data: []byte(`<!DOCTYPE rss [<!ENTITY a "b">]><rss><channel><item><title>&a;</title></item></channel></rss>`)}}},
	} {
		_, err := e.call(e.w, "import.preview", in)
		if err == nil || ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A folder of Markdown with a stray XML file is still a folder of Markdown.
	pv := e.previewConv(imports.PreviewIn{Name: "x", Files: files(map[string]string{"a.md": "# A\n\nText.\n", "export.xml": "<rss/>"})})
	if pv.Layout != imports.LayoutMarkdown {
		t.Errorf("layout %q", pv.Layout)
	}
	// A listing whose file is named in the wrong case or path style is still found.
	pv = e.previewConv(imports.PreviewIn{Name: "x", Files: []imports.File{{Path: "Export\\WP.XML", Data: wxrFile(t)[0].Data}}})
	if pv.Layout != imports.LayoutWordPress {
		t.Errorf("layout %q", pv.Layout)
	}
	_ = strings.TrimSpace
}

// The Git import hands the importer a repository's files and never gets a
// converted source, whatever the repository holds.
func TestRepositoriesAreNeverConvertedSources(t *testing.T) {
	pv, err := imports.PreviewFiles(bundle(t, "testdata/Salt Road.scriv"), "repo", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Layout != imports.LayoutMarkdown || pv.Documents != 0 {
		t.Errorf("a repository of RTF was read as %q with %d documents", pv.Layout, pv.Documents)
	}
}
