package publish

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

func keep(t *testing.T, name string, data []byte) {
	t.Helper()
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSamplePDFIsValid(t *testing.T) {
	b := bookOf(12000)
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	keep(t, "sample.pdf", out)
	for _, w := range warns {
		t.Logf("warning %s: %s", w.Code, w.Message)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	t.Logf("%d bytes, %d pages", len(out), len(doc.Pages))
}
