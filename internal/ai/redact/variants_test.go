package redact_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ai/redact"
)

func nameMap() *redact.Map {
	return redact.New([]redact.Term{
		{Surface: "Máire Nolan", Group: "a", Kind: "character"},
		{Surface: "Máire", Group: "a", Kind: "character"},
		{Surface: "O'Brien", Group: "b", Kind: "character"},
		{Surface: "Anne-Marie Fox", Group: "c", Kind: "character"},
	}, redact.Options{})
}

// However a name is typed, it is found; and what surrounds it is left exactly as it was.
func TestNamesAreFoundHoweverTheyAreTyped(t *testing.T) {
	for name, text := range map[string]string{
		"a hard-wrapped line":        "Then Máire\nNolan left.",
		"a carriage return":          "Then Máire\r\nNolan left.",
		"a no-break space":           "Then Máire Nolan left.",
		"a double space":             "Then Máire  Nolan left.",
		"a tab":                      "Then Máire\tNolan left.",
		"an ideographic space":       "Then Máire　Nolan left.",
		"a decomposed accent":        "Then Máire Nolan left.",
		"a zero-width space inside":  "Then Má​ire No⁠lan left.",
		"a byte order mark inside":   "Then Máire\uFEFF Nolan left.",
		"a soft hyphen":              "Then Má­ire Nolan left.",
		"upper case":                 "Then MÁIRE NOLAN left.",
		"a bidi control around it":   "Then ‮Máire Nolan‬ left.",
		"a decomposed capital":       "Then MÁIRE NOLAN left.",
		"a wrapped, decomposed name": "Then Máire \n  Nolan left.",
	} {
		m := nameMap()
		got := m.Apply(text)
		if strings.Contains(strings.ToLower(got), "nolan") || strings.Contains(strings.ToLower(got), "ire") && !strings.Contains(got, "{{") {
			t.Errorf("%s: %q", name, got)
		}
		if !strings.Contains(got, "{{CHAR_1}}") && !strings.Contains(got, "{{CHAR_2}}") {
			t.Errorf("%s: no placeholder in %q", name, got)
		}
		if !strings.HasPrefix(got, "Then ") && !strings.HasPrefix(got, "Then ‮") || !strings.HasSuffix(got, " left.") && !strings.HasSuffix(got, "‬ left.") {
			t.Errorf("%s: the text around the name changed: %q", name, got)
		}
		if back, _ := m.Restore(got); !strings.Contains(back, "Máire Nolan") {
			t.Errorf("%s: restored %q", name, back)
		}
	}
	// Exactly the original characters of the name are replaced, nothing more.
	m := nameMap()
	if got := m.Apply("A\nMáire  Nolan\nB"); got != "A\n{{CHAR_1}}\nB" {
		t.Errorf("%q", got)
	}
}

func TestApostropheAndHyphenVariants(t *testing.T) {
	m := nameMap()
	for _, text := range []string{"O'Brien came.", "O’Brien came.", "O‘Brien came.", "OʼBrien came.", "O`Brien came.", "o’brien came."} {
		if got := m.Apply(text); got != "{{CHAR_2}} came." {
			t.Errorf("%q gave %q", text, got)
		}
	}
	for _, text := range []string{"Anne-Marie Fox", "Anne‐Marie Fox", "Anne–Marie Fox", "Anne‑Marie Fox"} {
		if got := m.Apply(text); got != "{{CHAR_3}}" {
			t.Errorf("%q gave %q", text, got)
		}
	}
	// A possessive keeps its ending.
	if got := m.Apply("O’Brien’s hat"); got != "{{CHAR_2}}’s hat" {
		t.Errorf("%q", got)
	}
}

// A name in a URL or a file name is in its slug form; it is replaced by its own placeholder,
// and restoring that gives the slug back, not the name with a space in a link.
func TestSlugForms(t *testing.T) {
	m := nameMap()
	text := "See https://example.org/people/maire-nolan and notes/Maire-Nolan.md, and Anne-Marie-Fox too."
	got := m.Apply(text)
	if strings.Contains(strings.ToLower(got), "nolan") || strings.Contains(strings.ToLower(got), "fox") {
		t.Fatalf("a slug survived: %s", got)
	}
	if !strings.Contains(got, "{{CHAR_1_u}}") || !strings.Contains(got, "{{CHAR_3_u}}") {
		t.Errorf("%s", got)
	}
	back, unknown := m.Restore(got)
	if unknown != 0 || !strings.Contains(back, "people/maire-nolan and") || !strings.Contains(back, "anne-marie-fox too") {
		t.Errorf("restored %q (%d)", back, unknown)
	}
	// The slug form of a one-word name is just the word, found as a word.
	if got := m.Apply("Máire and maire."); strings.Contains(strings.ToLower(got), "maire") && !strings.Contains(got, "{{") {
		t.Errorf("%q", got)
	}
}

// A placeholder-shaped token that is in the writer's own text is not ours: it is neither listed nor
// restored, whatever the numbering of the map.
func TestLiteralPlaceholdersAreNeverRestored(t *testing.T) {
	m := nameMap()
	got := m.Apply("The note says {{CHAR_1}} and {{CHAR_2}}. Máire Nolan read it.")
	// CHAR_2 really stands for Máire Nolan here, but the text had its own {{CHAR_2}}: it cannot be told apart, so it stays.
	back, _ := m.Restore(got)
	if strings.Contains(back, "O'Brien") {
		t.Errorf("a literal placeholder was restored to a name: %q", back)
	}
	// A request that carries only a literal placeholder uses no name at all.
	m2 := nameMap()
	m2.Apply("repeat {{CHAR_1}} and {{CHAR_2}}")
	out, unknown := m2.Restore("{{CHAR_1}} {{CHAR_2}}")
	if out != "{{CHAR_1}} {{CHAR_2}}" || unknown != 2 {
		t.Errorf("%q %d", out, unknown)
	}
}

// The first thing a re-probe tries: the accents dropped, from a keyboard that has none.
func TestAccentsDroppedStillFindTheName(t *testing.T) {
	for _, text := range []string{"Maire Nolan left.", "MAIRE NOLAN left.", "maire\nnolan left.", "Maire  Nolan left.", "M\u00e1ire Nolan left.", "Ma\u0301ire Nolan left.", "MA\u0301IRE\u00a0NOLAN left."} {
		m := nameMap()
		got := m.Apply(text)
		if got != "{{CHAR_1}} left." {
			t.Errorf("%q gave %q", text, got)
		}
		if back, _ := m.Restore(got); back != "Máire Nolan left." {
			t.Errorf("%q restored to %q", text, back)
		}
	}
	m := nameMap()
	if got := m.Apply("Marie Nolan and Maira left."); strings.Contains(got, "{{") {
		t.Errorf("a different name was taken for it: %q", got)
	}
	// An accented name given to the map matches the plain spelling in the text, and the other way round.
	plain := redact.New([]redact.Term{{Surface: "Zoe Hart", Group: "z", Kind: "character"}}, redact.Options{})
	if got := plain.Apply("Zoë Hart and ZOE HART"); got != "{{CHAR_1}} and {{CHAR_1}}" {
		t.Errorf("%q", got)
	}
}
