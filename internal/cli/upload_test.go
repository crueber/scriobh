package cli_test

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
)

func pngFile(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 7, 5))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// An upload is a file on the command line: --file @path (or a bare path, or -
// for standard input), sent as the request body, named after the file unless
// --filename says otherwise, found again by download, and refused as a preview.
func TestUploadAndDownloadAnAttachmentFromTheCLI(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "The Salt Road")
	dir := t.TempDir()
	path := filepath.Join(dir, "harbour map.png")
	want := pngFile(t)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.ok("project", "attachment", "upload", "The Salt Road", "--file", "@"+path, "--alt", "The harbour")
	item := r.data(t)["item"].(map[string]any)
	if item["filename"] != "harbour map.png" || item["mime"] != "image/png" || item["alt"] != "The harbour" || item["width"].(float64) != 7 {
		t.Errorf("upload: %s", r.stdout)
	}
	id := item["id"].(string)
	// The same file again is the same attachment.
	if again := e.ok("project", "attachment", "upload", "The Salt Road", "--file", path).data(t); again["duplicate"] != true {
		t.Errorf("a repeat upload: %v", again)
	}
	// Download it to a file: the bytes come back as they went.
	out := filepath.Join(dir, "back.png")
	e.ok("project", "attachment", "download", "The Salt Road", id, "--output", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, want) {
		t.Errorf("downloaded %d bytes (%v), want %d", len(got), err, len(want))
	}
	// Standard input works, with a name.
	r2 := e.runWith(e.key, string(append([]byte("# Notes\n"), bytes.Repeat([]byte("words "), 20)...)), "project", "attachment", "upload", "The Salt Road", "--file", "-", "--filename", "notes.md")
	if r2.code != 0 || r2.data(t)["item"].(map[string]any)["mime"] != "text/markdown; charset=utf-8" {
		t.Errorf("stdin upload: %d %s %s", r2.code, r2.stdout, r2.stderr)
	}
	// A preview is refused before anything is sent, and says why.
	before := len(e.seen())
	_ = before
	d := e.run("project", "attachment", "upload", "The Salt Road", "--file", path, "--dry-run")
	if d.code != cli.ExitValidation || d.fail(t)["code"] != "dry_run_unsupported" {
		t.Errorf("dry run: %d %s", d.code, d.stderr)
	}
	// A missing flag and a missing file are usage errors; the server's refusal of a type comes through.
	if m := e.run("project", "attachment", "upload", "The Salt Road"); m.code != cli.ExitUsage || !strings.Contains(m.stderr, "file") {
		t.Errorf("no --file: %d %s", m.code, m.stderr)
	}
	if m := e.run("project", "attachment", "upload", "The Salt Road", "--file", filepath.Join(dir, "nope.png")); m.code != cli.ExitUsage {
		t.Errorf("a missing file: %d %s", m.code, m.stderr)
	}
	svg := filepath.Join(dir, "x.svg")
	_ = os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600)
	if m := e.run("project", "attachment", "upload", "The Salt Road", "--file", svg); m.code == 0 || m.fail(t)["code"] != "unsupported_type" {
		t.Errorf("an svg: %d %s", m.code, m.stderr)
	}
	// The help lists the flag.
	if h := e.run("project", "attachment", "upload", "--help"); !strings.Contains(h.stdout, "--file") {
		t.Errorf("help: %s", h.stdout)
	}
}
