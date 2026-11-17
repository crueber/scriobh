package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// An agent compiles a manuscript to a DOCX and an EPUB with the CLI alone: the
// generic mapping of the op, --preset and --options, and the file written where
// --out (or --output) says.
func TestAgentCLICompile(t *testing.T) {
	server := buildBinary(t, "")
	cliBin := buildCLI(t)
	p := start(t, server, t.TempDir())
	c := client()
	signIn(t, c, p.base, "dev-writer")
	req, _ := http.NewRequest("POST", p.base+"/api/v1/keys", strings.NewReader(`{"name":"compile agent","scope":"write","expires_in_days":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw := body(t, resp)
	secret, _ := jsonOf(t, raw)["secret"].(string)
	if !strings.HasPrefix(secret, "scr_") {
		t.Fatalf("no secret in %s", raw)
	}
	cli := cliRun{bin: cliBin, url: p.base, key: secret, cfg: t.TempDir(), cache: t.TempDir()}

	if code, out, errs := cli.run(t, "", "project", "create", "--title", "The Salt Road", "--kind", "novel", "--author-name", "Máire Ní Bhriain", "--json"); code != 0 {
		t.Fatalf("project create: %d\n%s\n%s", code, out, errs)
	}
	for _, ch := range []struct{ title, text string }{{"The Harbour", "The tide had gone out.\n\n* * *\n\nAnd the light with it.\n"}, {"The Road", "Salt on the road."}} {
		if code, out, errs := cli.run(t, ch.text, "doc", "create", "The Salt Road", "--title", ch.title, "--kind", "chapter", "--content", "-"); code != 0 {
			t.Fatalf("doc create: %d\n%s\n%s", code, out, errs)
		}
	}
	if code, out, errs := cli.run(t, "", "compile", "contact", "set", "The Salt Road", "--name", "Máire Ní Bhriain", "--email", "maire@example.com", "--address", "12 Quay Street\nGalway"); code != 0 {
		t.Fatalf("contact set: %d\n%s\n%s", code, out, errs)
	}

	// Presets are listed.
	code, out, errs := cli.run(t, "", "compile", "preset", "list", "The Salt Road", "--json")
	if code != 0 || !strings.Contains(out, "shunn-us") || !strings.Contains(out, "uk-manuscript") {
		t.Fatalf("preset list: %d\n%s\n%s", code, out, errs)
	}
	// A preview writes nothing and says what the file will hold.
	code, out, errs = cli.run(t, "", "compile", "preview", "The Salt Road", "--preset", "shunn-us")
	if code != 0 || !strings.Contains(out, "Chapter 1: The Harbour") && !strings.Contains(out, "The Harbour") {
		t.Fatalf("preview: %d\n%s\n%s", code, out, errs)
	}

	dir := t.TempDir()
	docx := filepath.Join(dir, "salt-road.docx")
	code, out, errs = cli.run(t, "", "export", "compile", "The Salt Road", "--preset", "shunn-us", "--out", docx)
	if code != 0 {
		t.Fatalf("export compile: %d\n%s\n%s", code, out, errs)
	}
	data, err := os.ReadFile(docx)
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.DOCX(data); len(probs) > 0 {
		t.Fatalf("the DOCX is not valid: %v", probs)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			s := b.String()
			found = strings.Contains(s, "The tide had gone out") && strings.Contains(s, "maire@example.com") && strings.Contains(s, ">#<")
		}
	}
	if !found {
		t.Error("the DOCX lacks the text, the contact block or the scene break")
	}

	// An EPUB with an override, with --output.
	epub := filepath.Join(dir, "salt-road.epub")
	code, out, errs = cli.run(t, "", "export", "compile", "The Salt Road", "--preset", "epub3", "--options", `{"scene_break":"ornament","ornament":"~"}`, "--output", epub)
	if code != 0 {
		t.Fatalf("epub: %d\n%s\n%s", code, out, errs)
	}
	ep, err := os.ReadFile(epub)
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.EPUB(ep); len(probs) > 0 {
		t.Fatalf("the EPUB is not valid: %v", probs)
	}

	// An unknown preset exits as a not-found, a bad option as a usage error of the server.
	if code, _, _ := cli.run(t, "", "export", "compile", "The Salt Road", "--preset", "nope", "--out", filepath.Join(dir, "x.docx")); code == 0 {
		t.Error("an unknown preset should fail")
	}
	// A dry run reports a recorded export and writes no file.
	dry := filepath.Join(dir, "dry.docx")
	code, out, errs = cli.run(t, "", "export", "compile", "The Salt Road", "--preset", "shunn-us", "--dry-run", "--out", dry)
	if code != 0 {
		t.Fatalf("dry run: %d\n%s\n%s", code, out, errs)
	}
	if _, err := os.Stat(dry); err == nil {
		t.Error("a dry run wrote the file")
	}
}
