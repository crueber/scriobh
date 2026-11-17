package imports_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
)

// A DOCX that another program wrote: LibreOffice, when it is installed and
// asked for, opens the manuscript this program wrote and saves it again as a
// DOCX of its own (different styles, different XML), and that file imports to
// the same chapters and text. Off by default: it takes seconds.
func TestDocxWrittenByLibreOffice(t *testing.T) {
	if os.Getenv("SCRIOBH_LIBREOFFICE") == "" {
		t.Skip("set SCRIOBH_LIBREOFFICE=1 to run")
	}
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		if _, e2 := os.Stat("/opt/homebrew/bin/soffice"); e2 != nil {
			t.Skip("soffice is not installed")
		}
		soffice = "/opt/homebrew/bin/soffice"
	}
	e := newEnv(t)
	out := e.must(e.w, "project.create", projects.CreateIn{Title: "The Salt Road", AuthorName: "Máire Ní Bhriain", Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	for _, m := range e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut).Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "The Arrival", Kind: "chapter", Content: "The boat came in at *dawn*, and **loud**.[^1]\n\n* * *\n\nAfter the break.\n\n- one\n- two\n\n[^1]: A footnote.\n"})
	e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "The Road", Kind: "chapter", Content: "Salt on the road.\n"})
	c := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "shunn-us"}).(compile.Out)

	dir := t.TempDir()
	src := filepath.Join(dir, "in.docx")
	os.WriteFile(src, c.Body, 0o644)
	conv := filepath.Join(dir, "out")
	cmd := exec.Command(soffice, "--headless", "--convert-to", "docx:MS Word 2007 XML", "--outdir", conv, src)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	done := make(chan error, 1)
	go func() { _, e := cmd.CombinedOutput(); done <- e }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(120 * time.Second):
		cmd.Process.Kill()
		t.Fatal("soffice timed out")
	}
	lo, err := os.ReadFile(filepath.Join(conv, "in.docx"))
	if err != nil {
		t.Fatal(err)
	}
	pv := e.previewDocx(lo, imports.PreviewIn{})
	var titles []string
	for _, n := range pv.Nodes {
		titles = append(titles, n.Title)
	}
	if strings.Join(titles, "|") != "The Arrival|The Road" {
		t.Fatalf("chapters %v\nwarnings %v", titles, pv.Warnings)
	}
	ap := e.applyDocx(lo, pv, imports.ApplyIn{})
	got := e.docContent(ap.ProjectID, "The Arrival")
	for _, want := range []string{"*dawn*", "**loud**", "[^1]", "[^1]: A footnote.", "* * *", "After the break.", "- one"} {
		if !strings.Contains(got, want) {
			t.Errorf("the text lacks %q:\n%s", want, got)
		}
	}
}
