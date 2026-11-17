package mirror_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// A Scrivener project imported with its research reaches the mirror whole: the
// manuscript, and the research notes with their tags, nested under their folders.
// (Imported notes emit one project event, import.applied; the mirror rebuilds the
// project's projection from it.)
func TestImportedResearchIsMirrored(t *testing.T) {
	e := newEnv(t)
	e.start()
	root := "../imports/testdata/Salt Road.scriv"
	var listing []imports.File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		listing = append(listing, imports.File{Path: filepath.ToSlash(rel), Data: b, Size: int64(len(b))})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pv := e.call(e.owner, "import.preview", imports.PreviewIn{Name: "Salt Road.scriv", Files: listing}).(imports.PreviewOut)
	ap := e.call(e.owner, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "Salt Road.scriv", Files: listing}).(imports.ApplyOut)
	if ap.ResearchNotes != 7 {
		t.Fatalf("research notes written: %d", ap.ResearchNotes)
	}
	e.settle(ap.ProjectID)
	got := files(t, e.root(ap.ProjectID))
	var content, mara string
	for k, v := range got {
		if strings.HasPrefix(k, "research/") && strings.Contains(v, "Mara, a courier of salt") {
			mara = k
			content = v
		}
	}
	if mara == "" {
		t.Fatalf("Mara's note is not in the mirror: %v", keys(got))
	}
	if !strings.Contains(content, "protagonist") || !strings.Contains(content, "kind: note") {
		t.Errorf("the note's front matter lacks its tag:\n%s", content)
	}
	if !strings.Contains(mara, "characters--") {
		t.Errorf("the note is not under its folder: %s", mara)
	}
	var manuscript int
	for k := range got {
		if strings.HasPrefix(k, "manuscript/") {
			manuscript++
		}
	}
	if manuscript < 11 {
		t.Errorf("the manuscript has %d files", manuscript)
	}
}
