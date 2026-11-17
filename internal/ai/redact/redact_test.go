package redact_test

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ai/redact"
)

func terms() []redact.Term {
	return []redact.Term{
		{Surface: "Máire Nolan", Group: "ent_b", Kind: "character"},
		{Surface: "Máire", Group: "ent_b", Kind: "character"},
		{Surface: "Mrs Nolan", Group: "ent_b", Kind: "character"},
		{Surface: "Tomás Quill", Group: "ent_a", Kind: "character"},
		{Surface: "Dunmore", Group: "ent_c", Kind: "place"},
	}
}

func TestApplyAndRestoreRoundTrip(t *testing.T) {
	m := redact.New(terms(), redact.Options{})
	text := "Máire Nolan walked to Dunmore. Máire's coat was wet; Mrs Nolan said nothing. Tomás Quill, however, said plenty. máire nolan."
	red := m.Apply(text)
	for _, name := range []string{"Máire", "Nolan", "Dunmore", "Tomás", "Quill"} {
		if strings.Contains(strings.ToLower(red), strings.ToLower(name)) {
			t.Errorf("%q survived: %s", name, red)
		}
	}
	// Numbered by group order (ent_a before ent_b), longest name first, the alias as a variant.
	for _, want := range []string{"{{CHAR_1}}", "{{CHAR_2}}", "{{CHAR_2b}}", "{{CHAR_2c}}", "{{PLACE_1}}"} {
		if !strings.Contains(red, want) {
			t.Errorf("%s missing from %s", want, red)
		}
	}
	back, unknown := m.Restore(red)
	if unknown != 0 {
		t.Errorf("unknown %d", unknown)
	}
	// Case is the bible's, everything else is exactly as written.
	want := "Máire Nolan walked to Dunmore. Máire's coat was wet; Mrs Nolan said nothing. Tomás Quill, however, said plenty. Máire Nolan."
	if back != want {
		t.Errorf("\n got %s\nwant %s", back, want)
	}
}

func TestWholeWordsOnly(t *testing.T) {
	m := redact.New([]redact.Term{{Surface: "Ann", Group: "a", Kind: "character"}, {Surface: "Dun", Group: "b", Kind: "place"}}, redact.Options{})
	got := m.Apply("Annie and Joanne met Ann at Dunmore, not at Dun.")
	if got != "Annie and Joanne met {{CHAR_1}} at Dunmore, not at {{PLACE_1}}." {
		t.Errorf("%s", got)
	}
}

func TestPlaceholdersAreAFunctionOfTheTermsNotTheText(t *testing.T) {
	a := redact.New(terms(), redact.Options{})
	shuffled := terms()
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	// Shuffling the order terms arrive in must not change the numbering of groups; within a
	// group the first term is the primary, so keep each group's own order.
	order := map[string][]redact.Term{}
	for _, tm := range terms() {
		order[tm.Group] = append(order[tm.Group], tm)
	}
	var arranged []redact.Term
	for _, g := range []string{"ent_c", "ent_b", "ent_a"} {
		arranged = append(arranged, order[g]...)
	}
	b := redact.New(arranged, redact.Options{})
	ea, eb := a.Entries(), b.Entries()
	if len(ea) != len(eb) {
		t.Fatal("different sizes")
	}
	for i := range ea {
		if ea[i] != eb[i] {
			t.Errorf("%v vs %v", ea[i], eb[i])
		}
	}
	// Applying texts in a different order changes nothing.
	t1, t2 := "Máire Nolan spoke.", "Tomás Quill spoke."
	x1, x2 := a.Apply(t1), a.Apply(t2)
	y2, y1 := b.Apply(t2), b.Apply(t1)
	if x1 != y1 || x2 != y2 {
		t.Error("the result depends on the order texts are applied in")
	}
}

func TestCapitalisedNames(t *testing.T) {
	m := redact.New(terms(), redact.Options{Capitalised: true})
	texts := []string{"Then Siobhán Keane arrived. Siobhán Keane waved. Then it rained. Friday came, and Máire Nolan left for Cork.", "Later, Keane spoke."}
	m.Learn(texts...)
	red := m.Apply(texts[0])
	for _, name := range []string{"Siobhán", "Keane", "Cork", "Máire"} {
		if strings.Contains(red, name) {
			t.Errorf("%q survived: %s", name, red)
		}
	}
	if !strings.Contains(red, "Then it rained") || !strings.Contains(red, "Then {{") {
		t.Errorf("sentence-initial ordinary words must stay: %s", red)
	}
	back, _ := m.Restore(red)
	if back != texts[0] {
		t.Errorf("\n got %s\nwant %s", back, texts[0])
	}
	// Learning is not order dependent.
	m2 := redact.New(terms(), redact.Options{Capitalised: true})
	m2.Learn(texts[1], texts[0])
	if m2.Apply(texts[0]) != red {
		t.Error("learning depends on the order of the texts")
	}
}

func TestRestoreIsTolerantAndHonest(t *testing.T) {
	m := redact.New(terms(), redact.Options{})
	m.Apply("Tomás Quill and Máire Nolan were there.") // only these two stand in the request
	got, unknown := m.Restore("{{ CHAR_1 }} and {{CHAR_2}} and {{CHAR_77}} and {{PLACE_1}} and {{PLACE_9}} and {not} and {{lower_1}}")
	// {{PLACE_1}} is a real placeholder of the map, but nothing sent used it: a model that
	// echoes it must not get the name of an entity the request never mentioned.
	if got != "Tomás Quill and Máire Nolan and {{CHAR_77}} and {{PLACE_1}} and {{PLACE_9}} and {not} and {{lower_1}}" || unknown != 3 {
		t.Errorf("%q (%d)", got, unknown)
	}
}

func TestStreamRestorerHandlesSplitPlaceholders(t *testing.T) {
	m := redact.New(terms(), redact.Options{})
	m.Apply("Máire Nolan met Tomás Quill at Dunmore.")
	full := "{{CHAR_2}} met {{CHAR_1}} at {{PLACE_1}}; {single} and {{ not closed"
	want, _ := m.Restore(full)
	if !strings.HasPrefix(want, "Máire Nolan met Tomás Quill at Dunmore;") {
		t.Fatalf("%q", want)
	}
	for size := 1; size <= 12; size++ {
		r := m.NewStreamRestorer()
		var out strings.Builder
		for i := 0; i < len(full); i += size {
			end := min(i+size, len(full))
			out.WriteString(r.Write(full[i:end]))
		}
		out.WriteString(r.Flush())
		if out.String() != want {
			t.Errorf("chunks of %d: %q, want %q", size, out.String(), want)
		}
	}
}

func TestEmptyAndShortTerms(t *testing.T) {
	m := redact.New([]redact.Term{{Surface: "", Group: "x"}, {Surface: "A", Group: "y"}, {Surface: "  ", Group: "z"}}, redact.Options{})
	if m.Len() != 0 || m.Apply("A cat") != "A cat" {
		t.Error("empty and one-letter terms must be ignored")
	}
}
