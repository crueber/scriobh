package compile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish"
)

// The preview keeps the same pictures as the export when their total is over
// the cap (192 MB), so the page count, the outline and the input hash agree.
func TestPreviewAndExportAgreeOnTheImageTotal(t *testing.T) {
	f := newFx(t, "Heavy")
	var md strings.Builder
	var ids []string
	for i := 1; i <= 10; i++ {
		id := f.upload(f.pid, uint8(i))
		ids = append(ids, id)
		fmt.Fprintf(&md, "Picture %d.\n\n![p](/files/%s)\n\n", i, id)
	}
	// the rows say 20 MB each: ten of them are over the 192 MB cap together
	if _, err := f.d.Writer().Exec(`UPDATE attachments SET bytes = ? WHERE project_id = ?`, 20<<20, f.pid); err != nil {
		t.Fatal(err)
	}
	f.doc("One", "chapter", "", md.String())
	for _, preset := range []string{"print-6x9", "epub3"} {
		p := f.preview(preset, publish.Options{})
		out := f.compile(preset, publish.Options{})
		if p.InputHash != out.InputHash {
			t.Errorf("%s: the preview and the export keep different pictures (hashes differ)", preset)
		}
		skipped := func(ws []string) bool {
			for _, w := range ws {
				if strings.Contains(w, "together") {
					return true
				}
			}
			return false
		}
		var pw, ew []string
		for _, w := range p.Warnings {
			pw = append(pw, w.Message)
		}
		for _, w := range out.Warnings {
			ew = append(ew, w.Message)
		}
		if !skipped(pw) || !skipped(ew) {
			t.Errorf("%s: the cap was not reported (preview %v, export %v)", preset, skipped(pw), skipped(ew))
		}
		if preset == "print-6x9" && p.PageCount == 0 {
			t.Error("no page count")
		}
	}
	_ = ids
}
