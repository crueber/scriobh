package imports

import (
	"os"
	"path/filepath"
	"testing"
)

// The private notes the converters write are in the editor's own corpus
// (internal/editor/testdata/corpus/import-private-*.md), where the round-trip
// test checks that the editor reads and writes each of them back unchanged. This
// test checks the other end: the converter writes exactly those files from the
// RTF they stand for. If either side moves, one of the two tests fails.
func TestImportedPrivateNotesAreInTheEditorCorpus(t *testing.T) {
	cases := map[string]string{
		"import-private-inline":       `Before {§*§Scrv_annot Check the timeline: is it still August?}§b Bold§b0  words and §i italic§i0  ones.§par`,
		"import-private-escapes":      `50% {§*§Scrv_annot Note with %% inside, *stars*, [[brackets]]} and 100%% literal.§par`,
		"import-private-percent-edge": `a%{§*§Scrv_annot secret}%b§par`,
		"import-private-whole":        `{§*§Scrv_annot A whole paragraph that is a note, with %% in it}§par Visible§par`,
		"import-private-blocks": `{§stylesheet{§s1 Quote;}}§pard§ls1§ilvl0 {§listtext •§tab}item {§*§Scrv_annot note one} more§par ` +
			`{§listtext •§tab}{§*§Scrv_annot only a note}§par §pard§s1 quote {§*§Scrv_annot note} end§par`,
		"import-private-footnote": `Text{§footnote Footnote text {§*§Scrv_annot private in a footnote} end.} with a note.{§*§Scrv_annot private}§par`,
	}
	for name, src := range cases {
		want, err := os.ReadFile(filepath.Join("..", "editor", "testdata", "corpus", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if name == "import-private-whole" {
			src = src[:len(src)-len("Visible§par")] + "Visible.§par"
		}
		if got := rtfMD(t, "{§rtf1§ansi "+src+"}"); got != string(want) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, want)
		}
	}
}
