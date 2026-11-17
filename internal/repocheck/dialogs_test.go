package repocheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The application never asks with the browser's own panels: confirm(), alert() and prompt() show nothing in some
// windows (the desktop application's webview returns "no" from confirm() without asking), so an action behind one
// cannot be done there. Questions are asked in the page (scriobh.confirm in js/shell.js, which uses js/arm.js; the
// typed confirmations; the dialogs). window.print() is not in this list: it is kept as one way of printing among
// others, and the page says so when it does nothing (js/share.js).
var nativeDialog = regexp.MustCompile(`(^|[^.\w$])(confirm|alert|prompt)\s*\(|\b(window|win|self|globalThis|top|parent)\s*\.\s*(confirm|alert|prompt)\s*\(`)

// vendoredJS are the files this project did not write. htmx calls confirm() only for an hx-confirm attribute, which no
// template may use (checked below).
var vendoredJS = map[string]string{
	"web/static/js/htmx.min.js": "htmx: its confirm() is behind hx-confirm and its prompt() behind hx-prompt, neither of which a template uses",
}

func TestNoBrowserDialogs(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	for _, dir := range []string{"web/static/js", "web/static/sw", "web/editor/src"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if ext := filepath.Ext(path); (ext != ".js" && ext != ".mjs" && ext != ".ts") || vendoredJS[rel] != "" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for i, line := range strings.Split(string(raw), "\n") {
				if len(line) > 4000 {
					// A bundle's one long line: look at it whole, and show only the place.
					if loc := nativeDialog.FindStringIndex(line); loc != nil {
						t.Errorf("%s:%d: a browser dialog: …%s…", rel, i+1, line[max(0, loc[0]-30):min(len(line), loc[1]+30)])
					}
					continue
				}
				if nativeDialog.MatchString(line) {
					t.Errorf("%s:%d: a browser dialog; ask in the page instead (scriobh.confirm): %.140s", rel, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 40 {
		t.Fatalf("only %d script files were scanned; has the scan stopped seeing them?", scanned)
	}
	for f := range vendoredJS {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("vendoredJS lists %s, which is not there", f)
		}
	}
	// The rule has teeth.
	for _, bad := range []string{`if (confirm("x")) go();`, `window.alert("x")`, `var n = win.prompt("name")`, `x = !confirm ("y")`} {
		if !nativeDialog.MatchString(bad) {
			t.Errorf("the scan does not see %q", bad)
		}
	}
	for _, fine := range []string{`win.scriobh.confirm(li, {})`, `S.arm.confirm(x)`, `// confirmation(s)`, `t.prompt_text(x)`, `unconfirm(x)`} {
		if nativeDialog.MatchString(fine) {
			t.Errorf("the scan objects to %q", fine)
		}
	}
	// htmx's own confirm and prompt are reached only through these attributes.
	err := filepath.WalkDir(filepath.Join(root, "web", "templates"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if regexp.MustCompile(`hx-(confirm|prompt)\b`).Match(raw) {
			t.Errorf("%s uses hx-confirm or hx-prompt, which ask with the browser's own panel", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
