package publish

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// SCRIOBH_PDFTOOLS=1 runs the files through whichever of qpdf, pdfinfo,
// pdftotext and mutool are installed, and compares the text they extract with
// the validator's. Off by default; when a tool is missing it is skipped and the
// test says so (the tools were not present when this was written, so this test
// has not been run by its author).
func TestPDFWithExternalTools(t *testing.T) {
	if os.Getenv("SCRIOBH_PDFTOOLS") != "1" {
		t.Skip("set SCRIOBH_PDFTOOLS=1 to run qpdf, pdfinfo, pdftotext and mutool over the output")
	}
	out, _, err := WritePDF(featureBook(), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, _ := check.ParsePDF(out)
	ran := 0
	run := func(tool string, args ...string) (string, bool) {
		bin, err := exec.LookPath(tool)
		if err != nil {
			t.Logf("%s is not installed", tool)
			return "", false
		}
		ran++
		o, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Errorf("%s %v: %v\n%s", tool, args, err, o)
		}
		return string(o), true
	}
	run("qpdf", "--check", path)
	if info, ok := run("pdfinfo", path); ok && !strings.Contains(info, "Pages:") {
		t.Errorf("pdfinfo: %s", info)
	}
	if txt, ok := run("pdftotext", "-layout", path, "-"); ok {
		squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
		if !strings.Contains(squash(txt), squash(doc.Pages[len(doc.Pages)/2].Text())[:40]) {
			t.Error("pdftotext does not find the validator's text")
		}
	}
	run("mutool", "info", path)
	if ran == 0 {
		t.Skip("none of the tools is installed")
	}
}
