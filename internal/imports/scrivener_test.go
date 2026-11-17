package imports

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readTree lists a fixture folder the way the command line does: every file, by
// its path under the folder.
func readTree(t testing.TB, root string) []File {
	t.Helper()
	var out []File
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, File{Path: filepath.ToSlash(rel), Data: b, Size: int64(len(b))})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dump(d []*cdoc, depth int, b *strings.Builder) {
	for _, x := range d {
		b.WriteString(strings.Repeat("  ", depth) + x.kind + ":" + x.title + " [" + x.status + "]")
		if x.exclude {
			b.WriteString(" (no compile)")
		}
		if x.synopsis != "" {
			b.WriteString(" syn=" + x.synopsis)
		}
		b.WriteString("\n")
		dump(x.kids, depth+1, b)
	}
}

func TestScrivenerDump(t *testing.T) {
	res, err := scrivenerConvert(readTree(t, "testdata"), "Salt Road.scriv", convOpts{})
	if err == nil {
		t.Log("two projects under one folder is fine here")
	}
	_ = res
	res, err = scrivenerConvert(readTree(t, "testdata/Salt Road.scriv"), "Salt Road.scriv", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	dump(res.docs, 0, &b)
	t.Log("\n" + b.String())
	var walk func(ds []*cdoc)
	walk = func(ds []*cdoc) {
		for _, d := range ds {
			t.Logf("--- %s\n%s", d.title, d.body)
			walk(d.kids)
		}
	}
	walk(res.docs)
	for _, n := range res.research {
		t.Logf("research %s parent=%s %s %q tags=%v cand=%s\n%s", n.key, n.parentKey, n.kind, n.title, n.tags, n.candidate, n.body)
	}
	for _, w := range res.warnings {
		t.Log("warning:", w)
	}
}
