package shortcuts

import (
	"encoding/json"
	"strings"
)

// Cap is one key cap of the window. Text is what a computer that is not a Mac shows (the words in the person's language,
// from the catalogue); Mod is Ctrl there. Key is the modifier token (Mod, Ctrl, Alt, Shift) when the browser should draw
// a Mac's sign instead (js/palette.js), and empty for every other key. Label names a cap that is only a sign (an arrow).
type Cap struct {
	Text, Key, Label string
	// Code is the KeyboardEvent.code of the letter of an Alt+Shift chord (KeyB): those chords are by position, so the browser
	// shows the letter that is under that key on this keyboard layout, and Text is the US letter when it cannot tell.
	Code string
}

// Alt is one alternative of a row. Mac is true for a key that exists on a Mac only: it is drawn hidden, and the browser
// shows it there.
type Alt struct {
	Mac  bool
	Caps []Cap
}

// Row is one line of the window.
type Row struct {
	ID    string
	Words string
	Alts  []Alt
}

// Section is a group of rows, with its heading. Here is true for the sections that are about the page the person is on.
type Section struct {
	ID, Name string
	Here     bool
	Rows     []Row
}

// glyph is a key whose cap is a sign, on every computer.
var glyph = map[string]string{"Up": "↑", "Down": "↓", "Left": "←", "Right": "→"}

// Translator looks a catalogue key up.
type Translator func(key string) string

// hasWord is true for a key that is drawn as its name from the catalogue (not a character, not a function key).
func hasWord(tok string) bool { return keyNames[tok] && tok != "F2" && tok != "F10" }

func keyWord(t Translator, name string) string { return t("kb.k." + strings.ToLower(name)) }

// Caps is a chord as key caps.
func Caps(t Translator, chord string) []Cap {
	var out []Cap
	altShift := strings.HasPrefix(chord, "Alt+Shift+") && len(Split(chord)) == 3
	for _, tok := range Split(chord) {
		switch {
		case tok == "Mod":
			out = append(out, Cap{Text: keyWord(t, "ctrl"), Key: "Mod"})
		case IsModifier(tok):
			out = append(out, Cap{Text: keyWord(t, tok), Key: tok})
		case glyph[tok] != "":
			out = append(out, Cap{Text: glyph[tok], Label: keyWord(t, tok)})
		case hasWord(tok):
			out = append(out, Cap{Text: keyWord(t, tok)})
		case altShift && len(out) == 2:
			out = append(out, Cap{Text: tok, Code: "Key" + tok})
		default:
			out = append(out, Cap{Text: tok})
		}
	}
	return out
}

// Build is the window for a page: the sections about the page first, then the others, in the registry's order. ai is
// whether AI is on for the project, and edit whether the person can edit: a row that needs one the page lacks is left
// out, and a section with no rows is left out.
func Build(t Translator, scope string, ai, edit bool) []Section {
	here := map[string]bool{}
	for _, g := range Groups {
		for _, s := range g.Scopes {
			if s == scope {
				here[g.ID] = true
			}
		}
	}
	var first, rest []Section
	for _, g := range Groups {
		sec := Section{ID: g.ID, Name: t("kb.g." + g.ID), Here: here[g.ID]}
		for _, e := range Entries {
			if e.Group != g.ID || (e.Needs == "ai" && !ai) || (e.Needs == "edit" && !edit) {
				continue
			}
			row := Row{ID: e.ID, Words: t("kb." + e.ID)}
			for _, k := range e.Keys {
				row.Alts = append(row.Alts, Alt{Caps: Caps(t, k)})
			}
			for _, k := range e.MacKeys {
				row.Alts = append(row.Alts, Alt{Mac: true, Caps: Caps(t, k)})
			}
			sec.Rows = append(sec.Rows, row)
		}
		if len(sec.Rows) == 0 {
			continue
		}
		if sec.Here {
			first = append(first, sec)
		} else {
			rest = append(rest, sec)
		}
	}
	return append(first, rest...)
}

// Hints is what the palette and the format bar need to draw a key where they show one: the tokens of every entry marked
// Palette, and the words of the keys among them that are not a character, as JSON for the palette dialog's data-keys.
// It is the same on every page of a language, a few hundred bytes.
func Hints(t Translator) string {
	keys := map[string]string{}
	names := map[string]string{"Mod": keyWord(t, "ctrl")}
	for _, e := range Entries {
		if !e.Palette {
			continue
		}
		keys[e.ID] = strings.Join(e.Keys, " ")
		for _, c := range e.Keys {
			for _, tok := range Split(c) {
				if tok != "Mod" && (IsModifier(tok) || hasWord(tok)) {
					names[tok] = keyWord(t, tok)
				}
			}
		}
	}
	// the names of the modifiers on a Mac, for the tooltip of a sign
	mac := map[string]string{"Mod": keyWord(t, "command"), "Shift": keyWord(t, "shift"), "Alt": keyWord(t, "option"), "Ctrl": keyWord(t, "control")}
	b, _ := json.Marshal(map[string]map[string]string{"k": keys, "n": names, "m": mac})
	return string(b)
}
