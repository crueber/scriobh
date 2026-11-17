package imports

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
)

var update = flag.Bool("update", false, "rewrite the golden files of the converters")

// dumpResult writes everything a converter made as text: the tree with each
// document's fields and Markdown, the research notes, the report and the warnings.
func dumpResult(res *cresult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project: %+v\nlayout: %s\n\n", res.project, res.layout)
	var walk func(ds []*cdoc, depth int)
	walk = func(ds []*cdoc, depth int) {
		for _, d := range ds {
			pad := strings.Repeat("  ", depth)
			fmt.Fprintf(&b, "%s== %s: %s [status %s, compile %v, date %q, source %q]\n", pad, d.kind, d.title, d.status, !d.exclude, d.storyDate, d.source)
			if d.synopsis != "" || d.synopsisLong != "" {
				fmt.Fprintf(&b, "%s   synopsis: %q long: %q\n", pad, d.synopsis, d.synopsisLong)
			}
			for _, l := range strings.Split(strings.TrimRight(d.body, "\n"), "\n") {
				fmt.Fprintf(&b, "%s   | %s\n", pad, l)
			}
			walk(d.kids, depth+1)
		}
	}
	walk(res.docs, 0)
	for _, n := range res.research {
		fmt.Fprintf(&b, "\n-- research %s (in %q) %s %q tags=%v candidate=%q\n", n.key, n.parentKey, n.kind, n.title, n.tags, n.candidate)
		for _, l := range strings.Split(strings.TrimRight(n.body, "\n"), "\n") {
			fmt.Fprintf(&b, "   | %s\n", l)
		}
	}
	meta, _ := json.MarshalIndent(res.meta, "", " ")
	fmt.Fprintf(&b, "\nreport: %s\n\nwarnings:\n", meta)
	for _, w := range res.warnings {
		fmt.Fprintf(&b, "- %s\n", w)
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to make it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden file (run with -update, and read the diff):\n%s", name, firstDiff(string(want), got))
	}
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d\n  want: %s\n  got:  %s", i+1, a, b)
		}
	}
	return ""
}

func TestConverterGoldens(t *testing.T) {
	s3, err := scrivenerConvert(readTree(t, "testdata/Salt Road.scriv"), "Salt Road.scriv", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "scrivener3", dumpResult(s3))
	s2, err := scrivenerConvert(readTree(t, "testdata/Old Book.scriv"), "Old Book.scriv", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "scrivener2", dumpResult(s2))
	wp, _ := os.ReadFile("testdata/wordpress-1.2.xml")
	w, err := wordpressConvert([]File{{Path: "e.xml", Data: wp}}, "x", convOpts{wpStatus: "all", wpTypes: "both"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "wordpress", dumpResult(w))
	for _, n := range []string{"ghost-4", "ghost-5"} {
		g, _ := os.ReadFile("testdata/" + n + ".json")
		r, err := ghostConvert([]File{{Path: n + ".json", Data: g}}, "x", convOpts{ghostStatus: "all", ghostTypes: "both"})
		if err != nil {
			t.Fatal(err)
		}
		golden(t, n, dumpResult(r))
	}
}

// The same source is the same preview, token, files and report, every time.
func TestConvertersAreDeterministic(t *testing.T) {
	wp, _ := os.ReadFile("testdata/wordpress-1.2.xml")
	g5, _ := os.ReadFile("testdata/ghost-5.json")
	for name, s := range map[string]source{
		"scrivener": {name: "x", files: readTree(t, "testdata/Salt Road.scriv")},
		"wordpress": {name: "x", files: []File{{Path: "e.xml", Data: wp}}, conv: convOpts{wpStatus: "all", wpTypes: "both"}},
		"ghost":     {name: "x", files: []File{{Path: "e.json", Data: g5}}},
	} {
		var first []byte
		var token string
		for i := 0; i < 4; i++ {
			pv, err := preview(s, nil, nil, alts.DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(pv)
			if i == 0 {
				first, token = b, pv.Token
				continue
			}
			if string(b) != string(first) || pv.Token != token {
				t.Errorf("%s: preview %d differs from the first", name, i)
			}
		}
	}
}
