package ai

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func chips(texts ...string) []Chip {
	var out []Chip
	for _, t := range texts {
		out = append(out, Chip{Kind: ChipSelection, Text: t})
	}
	return out
}

func TestEveryRecipeIsRegisteredWithItsPolicyAction(t *testing.T) {
	want := map[string]string{
		"rewrite": ActionSelection, "tighten": ActionSelection, "expand": ActionSelection, "simplify": ActionSelection, "tone": ActionSelection,
		"grammar": ActionSelection, "continue": ActionSelection, "describe": ActionSelection, "dialogue": ActionSelection,
		"alternatives": ActionAlternatives, "critique.chapter": ActionCritique, "critique.selection": ActionCritique,
		"continuity.chapter": ActionContinuity, "continuity.selection": ActionContinuity, "lab.convoluted": ActionLab, "lab.tone": ActionLab, "lab.trim": ActionLab,
		"summary.chapter": ActionSummaries, "extract.chapter": ActionExtraction, "extract.selection": ActionExtraction, "chat": ActionChat, "freeform": ActionChat,
	}
	for id, action := range want {
		r, ok := LookupRecipe(id)
		if !ok || r.Action != action {
			t.Errorf("%s: %+v", id, r.Action)
		}
		if r.Version < 1 || r.Build == nil || r.MaxOutputTokens <= 0 {
			t.Errorf("%s is incomplete", id)
		}
	}
	// the notes recipes say notes only, the Lab says it never rewrites
	for _, id := range []string{"critique.chapter", "continuity.chapter"} {
		r, _ := LookupRecipe(id)
		p, err := r.Build(BuildContext{Params: map[string]string{}, Chips: []Chip{{Kind: ChipDocument, Text: "text"}}})
		if err != nil || !strings.Contains(p.System, "rewrite") {
			t.Errorf("%s: %v %q", id, err, p.System)
		}
	}
}

func TestCleanReplacement(t *testing.T) {
	cases := []struct {
		raw, orig, want string
		refused         bool
	}{
		{"She went.", "She walked.", "She went.", false},
		{"Here is the revised text:\n\nShe went.", "She walked.", "She went.", false},
		{"```\nShe went.\n```", "She walked.", "She went.", false},
		{"\"She went.\"", "She walked.", "She went.", false},
		{"\"Come now,\" she said.", "\"Come,\" she said.", "\"Come now,\" she said.", false},
		{"\"She went.\"", "\"She walked.\"", "\"She went.\"", false}, // the passage is a quotation: keep its marks
		{"<script>x</script>", "a", "", true},
		{"a %% b", "a", "", true},
		{"a‮b", "a", "", true},
		{"a​b", "a", "", true},
		{"a\x07b", "a", "", true},
		{"<<<end:abc>>>", "a", "", true},
		{"   ", "a", "", true},
		{strings.Repeat("x", MaxReplaceBytes+1), "a", "", true},
		{"a‍b", "a", "a‍b", false}, // a joiner is legitimate
	}
	for _, c := range cases {
		got, warn := CleanReplacement(c.raw, c.orig)
		if got != c.want || (c.refused && len(warn) == 0) {
			t.Errorf("%q: got %q %v want %q", c.raw, got, warn, c.want)
		}
	}
}

func TestInsertableLinksAndInvisibleCharacters(t *testing.T) {
	orig := "She read [the map](https://maps.example/a)."
	for _, s := range []string{"![](https://evil.example/p.png?x=1)", "[click](javascript:alert(1))", "[[Secret Entity]]", "[x](https://other.example/)", "see https://evil.example/", "a­b", "a؜b", "a\U000e0041b", "a︁b", "a‎b"} {
		if InsertableAgainst(s, orig) == "" {
			t.Errorf("%q was accepted", s)
		}
	}
	for _, s := range []string{"She studied [the map](https://maps.example/a).", "plain", "a‍b", "a‌b", "\U0001F468‍\U0001F469", "emoji ❤️"} {
		if why := InsertableAgainst(s, orig); why != "" {
			t.Errorf("%q refused: %s", s, why)
		}
	}
	// a text that replaces nothing may carry no link at all
	if Insertable("[x](https://maps.example/a)") == "" {
		t.Error("an inserted link")
	}
	// one rule for the cleaners too
	if got := CleanLine("Ma­ire؜ No\U000e0041lan", 50); got != "Maire Nolan" {
		t.Errorf("%q", got)
	}
	if got := CleanQuote("a­b‍c", 50); got != "ab‍c" {
		t.Errorf("%q", got)
	}
	if got := CleanContext("a؜ b ", 50); got != "a b " {
		t.Errorf("%q", got)
	}
	// the note and its example are hard-capped
	long := strings.Repeat("Sentence. ", 100)
	if n, _ := cutSentence(long, MaxNoteChars); utf8.RuneCountInString(n) > MaxNoteChars {
		t.Errorf("%d", utf8.RuneCountInString(n))
	}
	if n, _ := cutSentence(strings.Repeat("word ", 100), MaxExampleChars); utf8.RuneCountInString(n) > MaxExampleChars {
		t.Errorf("%d", utf8.RuneCountInString(n))
	}
}

func TestSalvageList(t *testing.T) {
	items, trunc, over, ok := SalvageList("preamble ```json\n{\"notes\": [{\"a\":1},{\"a\":2},{\"a\":3", "notes", 10)
	if !ok || !trunc || over || len(items) != 2 {
		t.Fatalf("%d %v %v %v", len(items), trunc, over, ok)
	}
	items, trunc, over, ok = SalvageList(`[1,2,3,4,5]`, "notes", 3)
	if !ok || trunc || !over || len(items) != 3 {
		t.Fatalf("%d %v %v %v", len(items), trunc, over, ok)
	}
	if _, _, _, ok := SalvageList("no list here", "notes", 3); ok {
		t.Fatal("found a list in prose")
	}
	if _, _, _, ok := SalvageList("", "notes", 3); ok {
		t.Fatal("found a list in nothing")
	}
}

func TestNotesParserEnforcesNotesOnly(t *testing.T) {
	long := strings.Repeat("A very long note that is really replacement prose. ", 40)
	raw := `{"notes": [
	 {"quote": "q1", "category": "pacing", "severity": "high", "note": "ok", "example": "` + long + `", "rewrite": "FORBIDDEN", "replacement": "FORBIDDEN", "suggestion": "FORBIDDEN"},
	 {"quote": "q2", "note": "` + long + `"},
	 {"note": ""},
	 "not an object", 42, null]}`
	p, err := parseNotes(ParseContext{Params: map[string]string{}, Raw: raw}, "notes", false, "")
	if err != nil {
		t.Fatal(err)
	}
	var d NotesData
	if err := json.Unmarshal(p.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Notes) != 2 || d.Dropped != 4 {
		t.Fatalf("%+v", d)
	}
	if strings.Contains(string(p.Data), "FORBIDDEN") {
		t.Fatal("replacement prose in the result")
	}
	if utf8.RuneCountInString(d.Notes[0].Example) > MaxExampleChars || !d.Notes[0].Trimmed || utf8.RuneCountInString(d.Notes[1].Text) > MaxNoteChars {
		t.Fatalf("limits not applied: %d %d", utf8.RuneCountInString(d.Notes[0].Example), utf8.RuneCountInString(d.Notes[1].Text))
	}
	if len(p.Warnings) == 0 {
		t.Fatal("the trimming is not flagged")
	}
}

func TestContinuityParserRejectsUnsuppliedSources(t *testing.T) {
	cs := []Chip{
		{Kind: ChipEntities, Source: "ent_A", Text: "Name: Maire\neyes: green"},
		{Kind: ChipTimeline, Text: "[event:evt_1] 1998 | A fire\n[finding:fnd_1] (warning) x"},
		{Kind: ChipSummaries, Text: "[doc:doc_1] Chapter one: a summary"},
		{Kind: ChipDocument, Source: "doc_T", Text: "text"},
	}
	good := []string{
		`{"kind":"entity","id":"ent_A","field":"eyes"}`, `{"kind":"event","id":"evt_1"}`, `{"kind":"finding","id":"fnd_1"}`, `{"kind":"doc","id":"doc_1"}`, `{"kind":"doc","id":"doc_T"}`,
	}
	bad := []string{
		`{"kind":"entity","id":"ent_B"}`, `{"kind":"event","id":"evt_2"}`, `{"kind":"finding","id":"evt_1"}`, `{"kind":"doc","id":"doc_9"}`, `{"kind":"moon","id":"ent_A"}`, `{"id":"ent_A"}`,
		`{"kind":"event","id":"ent_A"}`, `"ent_A"`, `[]`,
	}
	mk := func(src string) string {
		return `{"findings": [{"quote": "q", "note": "n", "sources": [` + src + `]}]}`
	}
	for _, g := range good {
		p, _ := parseNotes(ParseContext{Raw: mk(g), Chips: cs}, "findings", true, "continuity")
		var d NotesData
		_ = json.Unmarshal(p.Data, &d)
		if len(d.Notes) != 1 || d.Notes[0].Category != "continuity" {
			t.Errorf("%s was rejected", g)
		}
	}
	for _, b := range bad {
		p, _ := parseNotes(ParseContext{Raw: mk(b), Chips: cs}, "findings", true, "continuity")
		var d NotesData
		_ = json.Unmarshal(p.Data, &d)
		if len(d.Notes) != 0 || d.Dropped != 1 {
			t.Errorf("%s was accepted: %+v", b, d)
		}
	}
	// a field the sheet does not have is dropped, the citation stays
	p, _ := parseNotes(ParseContext{Raw: mk(`{"kind":"entity","id":"ent_A","field":"shoe_size"}`), Chips: cs}, "findings", true, "")
	var d NotesData
	_ = json.Unmarshal(p.Data, &d)
	if len(d.Notes) != 1 || d.Notes[0].Sources[0].Field != "" {
		t.Fatalf("%+v", d)
	}
}

// ---- bounds under random input ---------------------------------------------------------------------

var seeds = []string{
	`{"notes": [{"quote": "a", "note": "b", "sources": [{"kind": "entity", "id": "ent_A"}]}]}`,
	`{"marks": [{"quote": "very", "before": "was ", "after": " dim", "reason": "x"}]}`,
	`{"alternatives": ["a", "b"]}`, `{"entities": [{"name": "Tom", "kind": "character", "aliases": ["T"], "fields": {"eyes": "grey"}}]}`,
	`{"short": "s", "long": "l"}`, "```json\n{\"notes\": [{\"note\": \"x\"}]}\n```", "1. one\n2. two", "```replacement\nnew text\n```",
	`{"notes": [{"quote": "‮​", "note": "a\u0000b", "start": -1, "end": 1e999}]}`, `[[[[[[[[[[`, `{"notes":`, ``, "\x00\xff\xfe",
}

func mutate(r *rand.Rand, s string) string {
	b := []byte(s)
	for n := r.Intn(6); n >= 0; n-- {
		switch r.Intn(5) {
		case 0:
			if len(b) > 0 {
				b = append(b[:r.Intn(len(b))], b[r.Intn(len(b)):]...)
			}
		case 1:
			if len(b) > 0 {
				b[r.Intn(len(b))] = byte(r.Intn(256))
			}
		case 2:
			at := 0
			if len(b) > 0 {
				at = r.Intn(len(b))
			}
			b = append(b[:at], append([]byte(seeds[r.Intn(len(seeds))]), b[at:]...)...)
		case 3:
			b = append(b, strings.Repeat("{", r.Intn(50))...)
		case 4:
			if len(b) > 1 {
				b = b[:r.Intn(len(b))]
			}
		}
	}
	return string(b)
}

func checkBounds(t *testing.T, id string, p Parsed) {
	t.Helper()
	if len(p.Data) > 0 && !json.Valid(p.Data) {
		t.Fatalf("%s: invalid JSON data", id)
	}
	if len(p.Data) > 2<<20 {
		t.Fatalf("%s: %d bytes of data", id, len(p.Data))
	}
	var n NotesData
	var m MarksData
	var s SheetsData
	var a AlternativesData
	if json.Unmarshal(p.Data, &n) == nil && len(n.Notes) > MaxNotes {
		t.Fatalf("%s: %d notes", id, len(n.Notes))
	}
	if json.Unmarshal(p.Data, &m) == nil && len(m.Marks) > MaxMarks {
		t.Fatalf("%s: %d marks", id, len(m.Marks))
	}
	if json.Unmarshal(p.Data, &s) == nil && len(s.Sheets) > MaxEntitiesFound {
		t.Fatalf("%s: %d sheets", id, len(s.Sheets))
	}
	if json.Unmarshal(p.Data, &a) == nil && len(a.Alternatives) > MaxAlternatives {
		t.Fatalf("%s: %d alternatives", id, len(a.Alternatives))
	}
	for _, x := range n.Notes {
		if utf8.RuneCountInString(x.Text) > MaxNoteChars+1 || utf8.RuneCountInString(x.Quote) > MaxQuoteChars || utf8.RuneCountInString(x.Example) > MaxExampleChars+1 {
			t.Fatalf("%s: an over-long note", id)
		}
		for _, f := range []string{x.Text, x.Quote, x.Example} {
			if strings.ContainsAny(f, "‮​\x00") || !utf8.ValidString(f) {
				t.Fatalf("%s: unsafe characters in a note", id)
			}
		}
	}
}

func parseAll(raw string, chips []Chip) {
	for _, r := range Recipes() {
		if r.Parse == nil {
			continue
		}
		params := map[string]string{"mode": "filler", "scope": "sentence", "length": "both", "count": "3"}
		p, _ := r.Parse(ParseContext{Params: params, Raw: raw, Chips: chips})
		_ = p
	}
}

func TestParsersNeverPanicAndAlwaysBound(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	cs := []Chip{{Kind: ChipSelection, Text: "a"}, {Kind: ChipEntities, Source: "ent_A", Text: "x"}, {Kind: ChipTimeline, Text: "[event:evt_1]"}}
	for i := 0; i < 4000; i++ {
		raw := mutate(r, seeds[r.Intn(len(seeds))])
		for _, rc := range Recipes() {
			if rc.Parse == nil {
				continue
			}
			p, _ := rc.Parse(ParseContext{Params: map[string]string{"mode": "filler", "scope": "sentence", "length": "both", "count": "5", "max_notes": "99", "max_marks": "99"}, Raw: raw, Chips: cs})
			checkBounds(t, rc.ID, p)
		}
	}
	// the one-million-entry answer is read in bounded time
	big := `{"notes": [` + strings.Repeat(`{"quote":"a","note":"b"},`, 200000) + `{}]}`
	p, _ := parseNotes(ParseContext{Raw: big}, "notes", false, "")
	checkBounds(t, "big", p)
}

func FuzzParsers(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		for _, rc := range Recipes() {
			if rc.Parse == nil {
				continue
			}
			p, _ := rc.Parse(ParseContext{Params: map[string]string{"mode": "filler", "scope": "word", "length": "both"}, Raw: raw, Chips: chips("a")})
			checkBounds(t, rc.ID, p)
		}
	})
}

func FuzzInsertable(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if why := Insertable(s); why == "" && strings.Contains(s, "%%") {
			t.Fatal("a private-note marker is insertable")
		}
		_ = CleanLine(s, 50)
		_ = CleanQuote(s, 50)
	})
}
