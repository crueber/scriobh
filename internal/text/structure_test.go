package text

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func irText(d Doc) string {
	s := PlainText(d.Nodes)
	for _, f := range d.Notes {
		s += "\n" + PlainText(f.Nodes)
	}
	return s
}

// Every word RenderHTML shows from the editor's corpus is in the structure the
// DOCX and EPUB writers read.
func TestStructureCoversTheCorpus(t *testing.T) {
	files, err := filepath.Glob("../editor/testdata/corpus/*.md")
	if err != nil || len(files) < 50 {
		t.Fatalf("corpus not found: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".golden.md") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		md := StripGhosts(string(raw), false) // a compile never meets the markers
		t.Run(filepath.Base(f), func(t *testing.T) {
			ir := " " + strings.Join(strings.Fields(irText(Structure(md, StructOptions{}))), " ") + " "
			shown := visible(RenderHTML(md, HTMLOptions{WikiHref: func(string) (string, bool) { return "#x", true }}))
			for _, w := range strings.Fields(shown) {
				if !strings.Contains(ir, w) {
					t.Errorf("word %q is in the HTML but not in the structure:\nsource:\n%s\nstructure:\n%s", w, md, ir)
					break
				}
			}
		})
	}
}

func TestStructureDropsWhatMustNotLeave(t *testing.T) {
	md := "---\ntitle: x\n---\n\nVisible %%secret%% text.\n\n<!-- scriobh:alts {\"v\":1} -->\n\n<script>alert(1)</script>\n\n[a](javascript:alert(1)) and [b](https://example.com/x?a=1&b=2)\n"
	d := Structure(md, StructOptions{})
	all := irText(d)
	for _, bad := range []string{"secret", "scriobh:alts", "title: x"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q leaked: %s", bad, all)
		}
	}
	if !strings.Contains(all, "<script>alert(1)</script>") {
		t.Errorf("raw HTML should be shown as text: %s", all)
	}
	found := false
	for _, n := range d.Nodes {
		for _, in := range n.Inl {
			if in.Kind == ILink && strings.Contains(in.Text, "javascript:") {
				t.Errorf("an unsafe link kept its address: %+v", in)
			}
			if in.Kind == ILink && in.Text == "https://example.com/x?a=1&b=2" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the https link lost its address: %+v", d.Nodes)
	}
	if got := Structure("a %%x%% b", StructOptions{Private: true}); !strings.Contains(irText(got), "x") {
		t.Error("Private should keep the note text")
	}
}

func TestStructureFootnotesNumberFromStart(t *testing.T) {
	d := Structure("One[^a] two[^b].\n\n[^a]: First.\n[^b]: Second.\n", StructOptions{NoteStart: 4})
	if len(d.Notes) != 2 || d.Notes[0].Num != 5 || d.Notes[1].Num != 6 {
		t.Fatalf("notes: %+v", d.Notes)
	}
	if d.Nodes[0].Inl[1].Kind != INote || d.Nodes[0].Inl[1].Num != 5 {
		t.Errorf("reference: %+v", d.Nodes[0].Inl)
	}
}

func TestStructureBoundedOnHostileText(t *testing.T) {
	deep := strings.Repeat("> ", 5000) + "text"
	d := Structure(deep, StructOptions{})
	depth := 0
	n := d.Nodes
	for len(n) > 0 && n[0].Kind == NQuote {
		depth++
		n = n[0].Kids
	}
	if depth > maxStructDepth+2 {
		t.Errorf("nesting %d is not bounded", depth)
	}
	if !strings.Contains(irText(d), "text") {
		t.Error("the text of a deeply nested quote was lost")
	}
}
