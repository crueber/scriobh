package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// The same through a zip upload, with a manifest over the 2 MiB a document may be.
func TestALargeManifestInAZipIsStillRead(t *testing.T) {
	e := newEnv(t)
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Zip: zipOf(t, bigManifest(300, 10_000)), Name: "x"}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutScriobh || pv.Project.Title != "Big" || pv.Documents != 300 {
		t.Fatalf("layout %q title %q documents %d warnings %v skipped %+v", pv.Layout, pv.Project.Title, pv.Documents, pv.Warnings, pv.Skipped)
	}
}
