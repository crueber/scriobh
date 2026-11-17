package editor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// The round trip is a Go test that drives the JavaScript parser/serializer with
// Node over a shared corpus. See docs/EDITOR-SPIKE.md.
//
//	canonical input x      serialize(parse(x)) == x
//	non-canonical x        serialize(parse(x)) == x.golden.md, and the golden is stable
//	every file             the whole-document fallback is never used
//	every file             no word of the input vanishes (see textExceptions)
//
// Node missing: skip locally, fail when CI is set. node_modules missing: the
// source run skips locally and fails under CI; the bundle run never needs it.

const corpusDir = "testdata/corpus"

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func inCI() bool { return os.Getenv("CI") != "" }

func needNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		if inCI() {
			t.Fatalf("node is required to run the editor round trip in CI, but was not found in PATH: %v", err)
		}
		t.Skip("node not found in PATH; skipping the editor round trip (it fails when CI is set)")
	}
	return node
}

func needModules(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "web", "editor", "node_modules", "prosemirror-model")); err != nil {
		if inCI() {
			t.Fatalf("web/editor/node_modules is missing; run `npm ci` in web/editor before the tests in CI")
		}
		t.Skip("web/editor/node_modules is missing (run `npm ci` in web/editor); skipping the source-level check")
	}
}

type fileResult struct {
	Name     string  `json:"name"`
	Out      string  `json:"out"`
	Out2     string  `json:"out2"`
	Fallback bool    `json:"fallback"`
	Error    *string `json:"error"`
}

func runRoundTrip(t *testing.T, node, root, impl string) []fileResult {
	t.Helper()
	cmd := exec.Command(node, filepath.Join("web", "editor", "roundtrip.mjs"), "--impl", impl, filepath.Join("internal", "editor", corpusDir))
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node roundtrip.mjs --impl %s failed: %v\n%s", impl, err, stderr.String())
	}
	var res struct {
		Impl  string       `json:"impl"`
		Files []fileResult `json:"files"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decoding roundtrip output: %v", err)
	}
	return res.Files
}

// Words dropped from the input before the text-preservation check, per corpus
// file. Each entry is an intentional, documented change of spelling, not a loss.
var textExceptions = map[string][]string{
	// Named and numeric entities are decoded to the characters they spell.
	"n-entities.md": {"amp", "lt", "gt", "35", "copy", "nbsp"},
	// A link whose text equals its destination becomes an autolink: the words
	// appear once, not twice, and "mailto:" is implied by <a@b.co>.
	"n-autolink-explicit.md": {"http", "example", "com", "mailto", "a", "b", "co"},
	// Character references inside a destination are decoded: &Tab; is a tab, &#106; is j.
	"n-link-obfuscated-scheme.md": {"tab", "106", "avascript"},
	// List markers are renumbered; the numbers are not text.
	"n-list-renumber.md":       {"1"},
	"n-list-renumber-start.md": {"7", "2"},
	// A reference link becomes an inline link; its label survives once, in the
	// definition that is kept in place.
	"n-reference-definitions.md": {"ref", "other"},
	"n-link-reference.md":        {"a", "collapsed", "shortcut"},
	"n-image-reference.md":       {"img"},
}

// Files where words legitimately change position (reference labels move to
// their definitions). They are checked as a multiset instead of a sequence.
var textReordered = map[string]bool{
	"n-reference-definitions.md": true,
	"n-link-reference.md":        true,
	"n-image-reference.md":       true,
}

func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// isSubmultiset reports whether every word of want occurs in have at least as
// often. It returns the first word that does not.
func isSubmultiset(want, have []string) (bool, string) {
	count := map[string]int{}
	for _, w := range have {
		count[w]++
	}
	for _, w := range want {
		if count[w] == 0 {
			return false, w
		}
		count[w]--
	}
	return true, ""
}

// isSubsequence reports whether every element of want appears in have, in order.
// It returns the first missing element when not.
func isSubsequence(want, have []string) (bool, string) {
	j := 0
	for _, w := range want {
		for j < len(have) && have[j] != w {
			j++
		}
		if j == len(have) {
			return false, w
		}
		j++
	}
	return true, ""
}

func TestCorpusIsLargeEnough(t *testing.T) {
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	var inputs, goldens int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".golden.md"):
			goldens++
		case strings.HasSuffix(e.Name(), ".md"):
			inputs++
		}
	}
	if inputs < 100 {
		t.Fatalf("corpus has %d inputs; expected at least 100", inputs)
	}
	t.Logf("corpus: %d inputs, %d goldens", inputs, goldens)
}

func TestRoundTrip(t *testing.T) {
	node := needNode(t)
	root := repoRoot(t)
	for _, impl := range []string{"source", "bundle"} {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			if impl == "source" {
				needModules(t, root)
			} else if _, err := os.Stat(filepath.Join(root, "web", "static", "js", "editor.js")); err != nil {
				t.Fatalf("committed bundle web/static/js/editor.js is missing: %v", err)
			}
			results := runRoundTrip(t, node, root, impl)
			if len(results) < 100 {
				t.Fatalf("round trip saw %d files", len(results))
			}
			var canonical, golden int
			for _, r := range results {
				r := r
				t.Run(strings.TrimSuffix(r.Name, ".md"), func(t *testing.T) {
					input, err := os.ReadFile(filepath.Join(corpusDir, r.Name))
					if err != nil {
						t.Fatal(err)
					}
					want := string(input)
					goldenPath := filepath.Join(corpusDir, strings.TrimSuffix(r.Name, ".md")+".golden.md")
					if g, err := os.ReadFile(goldenPath); err == nil {
						want = string(g)
					} else if strings.HasPrefix(r.Name, "n-") {
						t.Fatalf("%s is marked non-canonical (n- prefix) but has no golden file", r.Name)
					}
					if r.Fallback {
						t.Fatalf("whole-document fallback was used: %v", r.Error)
					}
					if r.Out != want {
						t.Errorf("serialize(parse(input)) mismatch\n input: %q\n  want: %q\n   got: %q", input, want, r.Out)
					}
					if r.Out2 != r.Out {
						t.Errorf("not idempotent\n first: %q\nsecond: %q", r.Out, r.Out2)
					}
					drop := map[string]bool{}
					for _, w := range textExceptions[r.Name] {
						drop[w] = true
					}
					var inWords []string
					for _, w := range words(string(input)) {
						if !drop[w] {
							inWords = append(inWords, w)
						}
					}
					check := isSubsequence
					if textReordered[r.Name] {
						check = isSubmultiset
					}
					if ok, missing := check(inWords, words(r.Out)); !ok {
						t.Errorf("text vanished: word %q from the input is not in the output\n input: %q\n output: %q", missing, input, r.Out)
					}
				})
				if _, err := os.Stat(filepath.Join(corpusDir, strings.TrimSuffix(r.Name, ".md")+".golden.md")); err == nil {
					golden++
				} else {
					canonical++
				}
			}
			t.Logf("%s: %d files (%d canonical, %d with goldens)", impl, len(results), canonical, golden)
		})
	}
}

// Every corpus file must have its golden or be canonical: no orphan goldens.
func TestNoOrphanGoldens(t *testing.T) {
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".golden.md") && !have[strings.TrimSuffix(n, ".golden.md")+".md"] {
			t.Errorf("golden %s has no input", n)
		}
	}
}

func TestBundleIsUpToDate(t *testing.T) {
	node := needNode(t)
	root := repoRoot(t)
	needModules(t, root)
	tmp := filepath.Join(t.TempDir(), "editor.js")
	cmd := exec.Command(node, "build.mjs", "--out", tmp)
	cmd.Dir = filepath.Join(root, "web", "editor")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the editor bundle: %v\n%s", err, out)
	}
	built, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join(root, "web", "static", "js", "editor.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(built, committed) {
		t.Fatalf("web/static/js/editor.js is stale: run `npm run build` in web/editor and commit the result (committed %d bytes, built %d bytes)", len(committed), len(built))
	}
}

// Literal text must survive a trip through the serializer in every place a
// paragraph, heading, quote, list item, or footnote can hold it. The generator
// lives in web/editor/props.mjs and runs against the committed bundle, so it
// needs Node but not node_modules.
func TestLiteralTextProperty(t *testing.T) {
	node := needNode(t)
	root := repoRoot(t)
	cmd := exec.Command(node, filepath.Join("web", "editor", "props.mjs"), "--impl", "bundle")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("literal-text property failed:\n%s", out)
	}
	t.Logf("%s", strings.TrimSpace(string(out)))
}
