package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// A note too large to send (listed by size only, as a zip upload lists it) is
// skipped with the importer's own reason, not turned into an empty document.
func TestObsidianLeavesAnUnsentNoteToTheIntake(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{"Vault/Ok.md": "Fine.\n"})
	f = append(f, imports.File{Path: "Vault/Big.md", Size: 3 << 20}, imports.File{Path: "Vault/Unsent.md", Size: 100})
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Name: "Vault", Files: f, Layout: "obsidian"}).(imports.PreviewOut)
	for _, n := range pv.Nodes {
		if n.Title == "Big" || n.Title == "Unsent" {
			t.Errorf("%s became a document", n.Title)
		}
	}
	reasons := map[string]string{}
	for _, s := range pv.Skipped {
		reasons[s.Path] = s.Reason
	}
	if reasons["Vault/Big.md"] != "larger than the 2 MiB file limit" || reasons["Vault/Unsent.md"] != "contents were not sent" {
		t.Errorf("reasons: %v", reasons)
	}
}
