package pandoc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pandoc"
)

// The one test that needs a real Pandoc. It is skipped when there is none on PATH, as there was
// where this was written, so it has never run; it states what the bridge assumes of the real program.
func TestRealPandocWhenInstalled(t *testing.T) {
	path, err := exec.LookPath("pandoc")
	if err != nil {
		t.Skip("pandoc is not installed")
	}
	r := pandoc.NewRunner(pandoc.Limits{}, t.TempDir(), "")
	ctx := context.Background()
	b, err := r.Locate(ctx, path)
	if err != nil {
		t.Skipf("the installed pandoc is not accepted: %v", err) // an old version, or a file the checks refuse
	}
	t.Logf("pandoc %s at %s", b.Version, b.Resolved)

	// An import with the production arguments (--sandbox, --wrap=none).
	imp, err := r.Import(ctx, b, "rst", []byte("Title\n=====\n\nA short *test* paragraph.\n"))
	if err != nil || !strings.Contains(string(imp.Markdown), "test") {
		t.Fatalf("import: %v", err)
	}
	// An export with -o under --sandbox.
	exp, err := r.Export(ctx, b, "rst", []byte("# Title\n\nA short test paragraph.\n"))
	if err != nil || !strings.Contains(string(exp.Body), "test paragraph") {
		t.Fatalf("export rst: %v", err)
	}
	for _, f := range []string{"odt", "rtf", "latex", "mediawiki"} {
		if e, err := r.Export(ctx, b, f, []byte("# Title\n\nText.\n")); err != nil || len(e.Body) == 0 {
			t.Errorf("export %s: %v", f, err)
		}
	}
	// The sandbox must keep a document from reading a file by name.
	tex, err := r.Import(ctx, b, "latex", []byte("\\documentclass{article}\\begin{document}Before \\input{/etc/hostname} after.\\end{document}\n"))
	if err == nil && strings.Contains(string(tex.Markdown), "Before") {
		hostname := strings.TrimSpace(readFile(t, "/etc/hostname"))
		if hostname != "" && strings.Contains(string(tex.Markdown), hostname) {
			t.Error("a LaTeX \\input read a file through the sandbox")
		}
	}
	inc, err := r.Import(ctx, b, "rst", []byte("Before\n\n.. include:: /etc/hostname\n\nAfter\n"))
	if err == nil {
		if hostname := strings.TrimSpace(readFile(t, "/etc/hostname")); hostname != "" && strings.Contains(string(inc.Markdown), hostname) {
			t.Error("an rst include read a file through the sandbox")
		}
	}
	// The same input twice gives the same Markdown.
	again, err := r.Import(ctx, b, "rst", []byte("Title\n=====\n\nA short *test* paragraph.\n"))
	if err != nil || string(again.Markdown) != string(imp.Markdown) {
		t.Error("an import is not repeatable")
	}
}

// The write that CVE-2023-35936 and CVE-2023-38745 are about: an image whose data: URI carries an extension with
// encoded path components, which --extract-media turns into a file name. The floor of the bridge's version keeps
// Pandoc builds that have it out; this states what a build the bridge accepts must do. It has not been run: there
// was no Pandoc where it was written. It does not skip when the installed Pandoc is accepted, and it fails if
// anything appears outside the call's folder, or anywhere beside the folder the calls are made in.
func TestRealPandocExtractMediaWritesNothingOutsideTheCallFolder(t *testing.T) {
	path, err := exec.LookPath("pandoc")
	if err != nil {
		t.Skip("pandoc is not installed")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "calls")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	r := pandoc.NewRunner(pandoc.Limits{}, root, "")
	b, err := r.Locate(context.Background(), path)
	if err != nil {
		t.Skipf("the installed pandoc is not accepted: %v", err) // below the floor: refused, which is the control
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	for name, doc := range map[string]string{
		"docbook": `<?xml version="1.0"?><article xmlns="http://docbook.org/ns/docbook"><para><inlinemediaobject><imageobject><imagedata fileref="data:image/png%2f..%2f..%2fplanted;base64,` + png + `"/></imageobject></inlinemediaobject></para></article>`,
		"rst":     ".. image:: data:image/png%2f..%2f..%2fplanted;base64," + png + "\n",
		"org":     "[[data:image/png%2f..%2f..%2fplanted;base64," + png + "]]\n",
	} {
		format := name
		if _, err := r.ImportWith(context.Background(), b, format, []byte(doc), pandoc.ImportOptions{ExtractMedia: true}); err != nil {
			t.Logf("%s: %v", name, err) // refused is fine; a stray file is not
		}
		var stray []string
		_ = filepath.Walk(parent, func(p string, fi os.FileInfo, err error) error {
			if err == nil && p != parent && p != root {
				stray = append(stray, p)
			}
			return nil
		})
		if len(stray) != 0 {
			t.Errorf("%s: files were left outside a call folder: %v", name, stray)
		}
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
