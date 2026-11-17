package text

import (
	"reflect"
	"strings"
	"testing"
)

func targets(ls []WikiLink) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Target)
	}
	return out
}

func TestWikiLinks(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want []string
	}{
		{"none", "No links here.", nil},
		{"plain", "She met [[Elara]] at the gate.", []string{"Elara"}},
		{"alias", "She met [[Elara Vance|the captain]] at the gate.", []string{"Elara Vance"}},
		{"trimmed", "See [[  Elara  ]] and [[Elara|  her ]].", []string{"Elara"}},
		{"order and distinct", "[[B]] then [[A]] then [[B]] again.", []string{"B", "A"}},
		{"case variants are distinct entries", "[[Elara]] and [[elara]].", []string{"Elara", "elara"}},
		{"fenced code", "Before.\n\n```\n[[Hidden]]\n```\n\nAfter [[Shown]].", []string{"Shown"}},
		{"indented code", "Para.\n\n    [[Hidden]]\n\nAfter [[Shown]].", []string{"Shown"}},
		{"inline code", "Use `[[Hidden]]` but see [[Shown]].", []string{"Shown"}},
		{"private note", "Visible %%secret [[Hidden]] note%% and [[Shown]].", []string{"Shown"}},
		{"raw html comment", "<!-- [[Hidden]] -->\n\n[[Shown]]", []string{"Shown"}},
		{"front matter", "---\ntitle: [[Hidden]]\n---\n\n[[Shown]]", []string{"Shown"}},
		{"heading", "# About [[Elara]]\n\ntext", []string{"Elara"}},
		{"list item", "- one [[A]]\n- two [[B]]", []string{"A", "B"}},
		{"empty target", "[[]] and [[|alias]]", nil},
		{"unterminated", "[[Never closed and more", nil},
		{"nested bracket", "[[a [[b]]", []string{"b"}},
		{"crlf", "One [[A]]\r\n\r\nTwo [[B]]", []string{"A", "B"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := targets(WikiLinks(c.md))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("WikiLinks(%q) = %v, want %v", c.md, got, c.want)
			}
		})
	}
}

func TestWikiLinkFields(t *testing.T) {
	ls := WikiLinks("First [[Elara|the captain]] spoke.\n\nLater [[Elara]] and [[Elara|she]] left.")
	if len(ls) != 1 {
		t.Fatalf("links %+v", ls)
	}
	l := ls[0]
	if l.Target != "Elara" || l.Alias != "the captain" || l.Count != 3 {
		t.Errorf("fields %+v", l)
	}
	if l.Snippet != "First the captain spoke." {
		t.Errorf("snippet %q", l.Snippet)
	}
}

func TestWikiLinkSnippetIsBoundedAndKeepsPrivateTextOut(t *testing.T) {
	long := strings.Repeat("word ", 60) + "[[Target]] " + strings.Repeat("more ", 60)
	ls := WikiLinks(long)
	if len(ls) != 1 {
		t.Fatal(ls)
	}
	if n := len([]rune(ls[0].Snippet)); n > snippetRunes+4 {
		t.Errorf("snippet is %d runes", n)
	}
	if !strings.Contains(ls[0].Snippet, "Target") || !strings.HasPrefix(ls[0].Snippet, "…") || !strings.HasSuffix(ls[0].Snippet, "…") {
		t.Errorf("snippet %q", ls[0].Snippet)
	}
	ls = WikiLinks("Open %%do not show this%% [[Target]] close.")
	if len(ls) != 1 || strings.Contains(ls[0].Snippet, "do not show") {
		t.Errorf("private text must not reach a snippet: %+v", ls)
	}
}

func TestFoldTitle(t *testing.T) {
	for _, c := range [][2]string{
		{"Elara", "elara"}, {"  Elara   Vance ", "elara vance"}, {"MÁIRE", "máire"}, {"Máire", "máire"}, {"Straße", "strasse"}, {"STRASSE", "strasse"}, {"", ""},
		{"Café", "café"}, {"CAFÉ", "café"}, {"Σίσυφος", "σίσυφοσ"}, {"ΣΊΣΥΦΟΣ", "σίσυφοσ"},
		{"ǅ", "ǆ"}, {"ﬁsh", "fish"}, {"İstanbul", "i̇stanbul"}, {"Ångström", "å" + "ngström"},
	} {
		if got := FoldTitle(c[0]); got != c[1] {
			t.Errorf("FoldTitle(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	if FoldTitle("Máire") != FoldTitle("MÁIRE") {
		t.Error("accents must fold the same way in both cases")
	}
	// Precomposed and decomposed spellings are one title (macOS file names are decomposed).
	for _, pair := range [][2]string{
		{"Café", "Café"}, {"Máire", "Máire"}, {"Ångström", "Ångström"}, {"STRASSE", "Straße"}, {"Ǆ", "ǆ"},
	} {
		if FoldTitle(pair[0]) != FoldTitle(pair[1]) {
			t.Errorf("%q and %q must be the same title", pair[0], pair[1])
		}
	}
	// And a plain mismatch stays a mismatch.
	if FoldTitle("Cafe") == FoldTitle("Café") {
		t.Error("an accent is a difference")
	}
}

func TestFoldTitleIsSafeInParallel(t *testing.T) {
	done := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			var last string
			for k := 0; k < 500; k++ {
				last = FoldTitle("STRASSE Café ΣΊΣΥΦΟΣ")
			}
			done <- last
		}()
	}
	want := FoldTitle("Straße Café σίσυφοσ")
	for i := 0; i < 8; i++ {
		if got := <-done; got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
