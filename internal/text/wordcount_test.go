package text

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The count is defined by the editor: countWords(parseMarkdown(md)), run in
// Node over web/editor/src (wordcount.mjs). The committed golden holds that
// oracle's answer for every corpus file, so the Go counter is checked against
// the editor without Node on every run, and a second test fails when the golden
// no longer matches what Node says (so the golden cannot go stale quietly).
//
// Keys are "corpus/<name>" (the round-trip corpus, inputs and canonical
// goldens) and "extra/<name>" (inputs added for the counter: constructs the
// round-trip corpus does not exercise).

const (
	testdata   = "../editor/testdata"
	goldenFile = testdata + "/wordcount.golden.json"
)

var corpusDirs = [][2]string{
	{"corpus", testdata + "/corpus"},
	{"extra", testdata + "/wordcount-extra"},
}

// Inputs the Go counter is known to count differently from the editor, with
// the reason. The goal is an empty map.
var exceptions = map[string]string{
	// The editor's table cannot start on a line that is less indented than the
	// list item (or footnote) it follows, so there the rows are lazy paragraph
	// text and count. goldmark reads them as a table, which the counter holds as
	// source. Writers separate a table from a list with a blank line; the
	// editor's own serializer always does.
	"extra/limit-lazy-table.md": "table directly after an unindented lazy line inside a list item",

	// The rest are places where goldmark and markdown-it (the editor's parser)
	// draw a block boundary differently. Each is a construct a writer does not
	// produce by accident, found by the differential test; none is counted
	// wrongly in ordinary prose.
	"extra/limit-table-delimiter-setext.md":             "`b|` over `--` is a table in the editor (the delimiter row need not hold a pipe) and a setext heading in goldmark",
	"extra/limit-reference-then-indented.md":            "text left after a reference definition and indented four spaces is an indented code block in the editor, a paragraph in goldmark",
	"extra/limit-reference-destination-on-next-line.md": "a definition whose destination sits on the next line, and looks like a list marker, is not a definition in the editor",
	"extra/limit-html-block-nbsp.md":                    "an unknown tag followed only by a no-break space is an HTML block in the editor (its \\s includes U+00A0) and a paragraph in goldmark",
	"extra/limit-directive-after-lazy-list-line.md":     "a `:::` block on an unindented line right after a list item's text is a lazy continuation in the editor, a directive in goldmark",
	"extra/limit-empty-item-then-indented-directive.md": "an empty list item, a blank line, then indented text: the editor keeps the text in the item, goldmark ends the list",
	"extra/limit-footnote-after-table-in-quote.md":      "a footnote definition straight after a table row inside a quote is another table row in the editor",
}

func loadGolden(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]int
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func readCase(t *testing.T, key string) string {
	t.Helper()
	label, name, _ := strings.Cut(key, "/")
	for _, d := range corpusDirs {
		if d[0] == label {
			raw, err := os.ReadFile(filepath.Join(d[1], name))
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}
	}
	t.Fatalf("unknown corpus label in %q", key)
	return ""
}

func TestWordCountMatchesEditor(t *testing.T) {
	golden := loadGolden(t)
	if len(golden) < 200 {
		t.Fatalf("the golden has only %d entries", len(golden))
	}
	keys := make([]string, 0, len(golden))
	for k := range golden {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var bad []string
	agree, inputs, inputsAgree := 0, 0, 0
	for _, k := range keys {
		got := WordCount(readCase(t, k))
		isInput := strings.HasPrefix(k, "corpus/") && !strings.HasSuffix(k, ".golden.md")
		if isInput {
			inputs++
			if got == golden[k] {
				inputsAgree++
			}
		}
		if got == golden[k] {
			agree++
			if why, ok := exceptions[k]; ok {
				t.Errorf("%s now agrees; remove its exception (%s)", k, why)
			}
			continue
		}
		if _, ok := exceptions[k]; ok {
			continue
		}
		bad = append(bad, k+": editor "+itoa(golden[k])+", Go "+itoa(got))
	}
	t.Logf("round-trip corpus inputs: %d of %d agree with the editor", inputsAgree, inputs)
	t.Logf("%d of %d inputs agree with the editor (%d documented exceptions)", agree, len(keys), len(exceptions))
	if len(bad) > 0 {
		t.Errorf("%d disagreements with the editor:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func inCI() bool { return os.Getenv("CI") != "" }

func TestWordCountGoldenIsCurrent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if inCI() {
			t.Fatalf("node is required to check the word-count golden in CI: %v", err)
		}
		t.Skip("node not found; skipping the golden regeneration check (it fails when CI is set)")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{filepath.Join("web", "editor", "wordcount.mjs"), "--impl", "bundle"}
	for _, d := range corpusDirs {
		args = append(args, d[0]+"="+filepath.Join("internal", "editor", "testdata", strings.TrimPrefix(d[1], testdata+"/")))
	}
	cmd := exec.Command(node, args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("wordcount.mjs failed: %v\n%s", err, stderr.String())
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("%s is stale: regenerate it with\n  node web/editor/wordcount.mjs --impl bundle corpus=internal/editor/testdata/corpus extra=internal/editor/testdata/wordcount-extra > internal/editor/testdata/wordcount.golden.json", goldenFile)
	}
}
