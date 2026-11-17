package text

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// ph builds phrases whose Ref is the index in the list.
func ph(names ...string) []Phrase {
	out := make([]Phrase, len(names))
	for i, n := range names {
		out[i] = Phrase{Text: n, Ref: i + 1, Rank: i}
	}
	return out
}

// surfaces runs a matcher over s and returns the matched text and winning ref.
func surfaces(m *Matcher, s string) []string {
	var out []string
	m.Find(s, func(h Match) {
		out = append(out, s[h.Start:h.End])
		out[len(out)-1] += "=" + string(rune('0'+h.Cands[0].Ref))
	})
	return out
}

func TestMatcherTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		phrases []string
		text    string
		want    []string
	}{
		{"plain", []string{"Elara"}, "Elara walked. Then Elara ran.", []string{"Elara=1", "Elara=1"}},
		{"case folds", []string{"Elara"}, "ELARA and elara? Elara", []string{"ELARA=1", "Elara=1"}}, // lower-case text fails the capital guard
		{"whole word only", []string{"Ann"}, "Anne and Annabel met Ann.", []string{"Ann=1"}},
		{"unicode fold", []string{"Straße"}, "Die STRASSE war lang. Straße!", []string{"STRASSE=1", "Straße=1"}},
		{"diacritics matter", []string{"Máire"}, "Maire and Máire and MÁIRE", []string{"Máire=1", "MÁIRE=1"}},
		{"nfc and nfd agree", []string{"Máire"}, "Máire waved", []string{"Máire=1"}},
		{"possessive", []string{"Máire"}, "Máire's boat and Máire’s oar", []string{"Máire=1", "Máire=1"}},
		{"possessive of a multiword name", []string{"Máire Ní Bhriain"}, "She was Máire Ní Bhriain's friend.", []string{"Máire Ní Bhriain=1"}},
		{"plural possessive", []string{"Brians"}, "the Brians' house", []string{"Brians=1"}},
		{"hyphenated name", []string{"Jean-Luc"}, "Jean-Luc smiled. Jean Luc did not.", []string{"Jean-Luc=1"}},
		{"hyphen is not a word break", []string{"Mary"}, "Mary-Anne came with Mary.", []string{"Mary=1"}},
		{"apostrophe name, either quote", []string{"O'Neill"}, "O'Neill and O’Neill, and O'Neill's hat", []string{"O'Neill=1", "O’Neill=1", "O'Neill=1"}},
		{"multiword", []string{"Máire Ní Bhriain"}, "Máire\nNí Bhriain, then Máire Ní Bhriain.", []string{"Máire Ní Bhriain=1"}}, // the line break ends the first
		{"soft space", []string{"Ní Bhriain"}, "Ní   Bhriain stood.", []string{"Ní   Bhriain=1"}},
		{"longest wins", []string{"Máire", "Máire Ní Bhriain"}, "Máire Ní Bhriain met Máire.", []string{"Máire Ní Bhriain=2", "Máire=1"}},
		{"longest wins either order", []string{"Máire Ní Bhriain", "Máire"}, "Máire Ní Bhriain met Máire.", []string{"Máire Ní Bhriain=1", "Máire=2"}},
		{"overlap is leftmost then longest", []string{"Port Salt", "Salt Road"}, "Port Salt Road", []string{"Port Salt=1"}},
		{"capital guard", []string{"Will"}, "She will go. Will went. WILL!", []string{"Will=1", "WILL=1"}},
		{"lower-case name matches any case", []string{"the old mill"}, "The Old Mill and the old mill", []string{"The Old Mill=1", "the old mill=1"}},
		{"punctuation separates", []string{"Elara"}, "(Elara), \"Elara\"—Elara...", []string{"Elara=1", "Elara=1", "Elara=1"}},
		{"digits", []string{"Route 66"}, "On Route 66 and route 66", []string{"Route 66=1"}},
		{"han has no spaces", []string{"李明"}, "昨天李明来了,李明走了", []string{"李明=1", "李明=1"}},
		{"kana", []string{"サクラ"}, "サクラが来た", []string{"サクラ=1"}},
		{"empty text", []string{"Elara"}, "", nil},
		{"alias equal to name of another resolves by rank", []string{"Bran", "Bran"}, "Bran came.", []string{"Bran=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := surfaces(NewMatcher(ph(tc.phrases...)), tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatcherAmbiguityIsReportedNotDropped(t *testing.T) {
	m := NewMatcher([]Phrase{
		{Text: "Bran", Ref: 7, Rank: 2},
		{Text: "Bran", Ref: 3, Rank: 0},
		{Text: "bran", Ref: 9, Rank: 1, Alias: true},
	})
	var hits []Match
	m.Find("Bran sat.", func(h Match) { hits = append(hits, h) })
	if len(hits) != 1 {
		t.Fatalf("hits %d", len(hits))
	}
	refs := []int{}
	for _, c := range hits[0].Cands {
		refs = append(refs, c.Ref)
	}
	if !reflect.DeepEqual(refs, []int{3, 9, 7}) {
		t.Errorf("candidates %v, want winner first by rank: [3 9 7]", refs)
	}
	// The lower-case alias is not guarded by the capital rule, the capitalised names are: "bran" finds only it.
	hits = nil
	m.Find("the bran muffin", func(h Match) { hits = append(hits, h) })
	if len(hits) != 1 || len(hits[0].Cands) != 1 || hits[0].Cands[0].Ref != 9 {
		t.Errorf("lower-case text: %+v", hits)
	}
}

func TestMatcherSameEntityNameAndAliasIsOneCandidate(t *testing.T) {
	m := NewMatcher([]Phrase{{Text: "Bran", Ref: 1, Alias: true}, {Text: "Bran", Ref: 1}})
	var cands []Candidate
	m.Find("Bran", func(h Match) { cands = h.Cands })
	if len(cands) != 1 || cands[0].Alias {
		t.Errorf("cands %+v, want one, as a name", cands)
	}
}

func TestLookupTextIgnoresCaseAndPunctuation(t *testing.T) {
	m := NewMatcher(ph("Máire Ní Bhriain", "will"))
	if c := m.LookupText("  MÁIRE ní   bhriain "); len(c) != 1 || c[0].Ref != 1 {
		t.Errorf("%+v", c)
	}
	if c := m.LookupText("Máire"); c != nil {
		t.Errorf("a prefix is not the name: %+v", c)
	}
	if c := m.LookupText("Will"); len(c) != 1 { // the capital guard does not apply to an explicit link
		t.Errorf("%+v", c)
	}
}

func TestFoldWordAgreesWithFoldTitle(t *testing.T) {
	for _, w := range []string{"Straße", "MÁIRE", "Máire", "ǅ", "ÀÉÎ", "plain", "MiXeD", "Σίσυφος", "ΣΑΣ", "ŉ"} {
		if got, want := foldWord(w), FoldTitle(w); got != want {
			t.Errorf("foldWord(%q) = %q, FoldTitle = %q", w, got, want)
		}
	}
}

func TestSpansPlaceMatchesInTheSource(t *testing.T) {
	m := NewMatcher(ph("Máire", "Elara", "Máire Ní Bhriain"))
	for _, tc := range []struct {
		name string
		md   string
		want []string // the text each span covers
	}{
		{"plain", "Máire ran.\n\nElara did not.", []string{"Máire", "Elara"}},
		{"crlf is one character", "Máire ran.\r\n\r\nElara did not.", []string{"Máire", "Elara"}},
		{"front matter", "---\ntitle: x\n---\n\nMáire ran.", []string{"Máire"}},
		{"emoji before", "😀😀 Máire and 🎉 Elara", []string{"Máire", "Elara"}},
		{"emphasis keeps the text", "A *brave* Máire and **Elara**.", []string{"Máire", "Elara"}},
		{"entity before", "Fish &amp; chips and Máire.", []string{"Máire"}},
		{"escape before", "A \\* star and Máire.", []string{"Máire"}},
		{"soft break inside a name", "Máire Ní\nBhriain waited.", []string{"Máire Ní\nBhriain"}},
		{"heading", "# Máire\n\nbody", []string{"Máire"}},
		{"list and quote", "- Máire\n- > Elara\n\n> Máire", []string{"Máire", "Elara", "Máire"}},
		{"private note is not prose", "Before %%Máire is secret%% after Elara.", []string{"Elara"}},
		{"fenced code is skipped", "```\nMáire\n```\n\nElara", []string{"Elara"}},
		{"indented code is skipped", "    Máire\n\nElara", []string{"Elara"}},
		{"html block is skipped", "<div>Máire</div>\n\nElara", []string{"Elara"}},
		{"inline code counts as prose", "Use `Máire` here", []string{"Máire"}},
		{"task item", "- [ ] Máire to call", []string{"Máire"}},
		{"after a wiki link", "[[Somewhere]] then Máire", []string{"Máire"}},
		{"inside a wiki link the whole link is the span", "[[Máire]] waved", []string{"[[Máire]]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spans, complete := Spans(tc.md, m)
			norm := strings.ReplaceAll(strings.ReplaceAll(tc.md, "\r\n", "\n"), "\r", "\n")
			var got []string
			u16 := utf16.Encode([]rune(norm))
			for _, s := range spans {
				got = append(got, string(utf16.Decode(u16[s.Start:s.End])))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("spans cover %q, want %q (complete=%v)", got, tc.want, complete)
			}
			// The index counts from the search text must equal the spans.
			var n int
			m.Find(SearchBody(tc.md), func(Match) { n++ })
			if n != len(spans) {
				t.Errorf("index would count %d, spans %d", n, len(spans))
			}
		})
	}
}

func TestSpansPlacesRewrittenTextByItsWholeToken(t *testing.T) {
	m := NewMatcher(ph("Elara"))
	md := "Say &#69;lara now, and Elara."
	spans, complete := Spans(md, m)
	if len(spans) != 2 || !complete {
		t.Fatalf("spans %d complete %v", len(spans), complete)
	}
	if got := md[spans[0].Start:spans[0].End]; got != "&#69;lara" {
		t.Errorf("first span %q", got)
	}
	if got := md[spans[1].Start:spans[1].End]; got != "Elara" {
		t.Errorf("second span %q", got)
	}
}

func TestSpansGivesUpOnSanitisedText(t *testing.T) {
	m := NewMatcher(ph("Elara"))
	md := "Elara " + strings.Repeat("[", 3000) + " Elara"
	spans, complete := Spans(md, m)
	if complete || spans != nil {
		t.Errorf("a document the parser had to sanitise is not placed: %d spans, complete=%v", len(spans), complete)
	}
}
