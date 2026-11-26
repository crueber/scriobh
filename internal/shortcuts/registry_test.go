package shortcuts

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/i18n"
)

func en(k string) string { return i18n.Default.T("en", k) }

func TestRegistryIsWellFormed(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range Groups {
		if groups[g.ID] {
			t.Errorf("group %s is listed twice", g.ID)
		}
		groups[g.ID] = true
		if len(g.Scopes) == 0 {
			t.Errorf("group %s works on no page", g.ID)
		}
		for _, s := range g.Scopes {
			if !contains(AllScopes, s) {
				t.Errorf("group %s has the unknown scope %q", g.ID, s)
			}
		}
	}
	seen := map[string]bool{}
	claims := map[string]string{}
	for _, e := range Entries {
		if seen[e.ID] {
			t.Errorf("entry %s is listed twice", e.ID)
		}
		seen[e.ID] = true
		if !groups[e.Group] {
			t.Errorf("entry %s is in the group %q, which does not exist", e.ID, e.Group)
		}
		if len(e.Keys) == 0 {
			t.Errorf("entry %s has no keys", e.ID)
		}
		switch e.Needs {
		case "", "ai", "edit":
		default:
			t.Errorf("entry %s needs %q", e.ID, e.Needs)
		}
		altShift := false
		for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
			if why := Valid(c); why != "" {
				t.Errorf("entry %s: chord %q: %s", e.ID, c, why)
			}
			if strings.HasPrefix(c, "Alt+Shift+") {
				altShift = true
				if want := "Key" + strings.TrimPrefix(c, "Alt+Shift+"); e.Claim != want {
					t.Errorf("entry %s: the chord %s is claimed as %q, want %q", e.ID, c, e.Claim, want)
				}
			}
		}
		if e.Claim != "" && !altShift {
			t.Errorf("entry %s claims %s but has no Alt+Shift chord", e.ID, e.Claim)
		}
		if e.Claim != "" {
			if other := claims[e.Claim]; other != "" {
				t.Errorf("entries %s and %s both claim %s", other, e.ID, e.Claim)
			}
			claims[e.Claim] = e.ID
		}
	}
	if len(Entries) < 50 {
		t.Errorf("only %d entries", len(Entries))
	}
}

func contains(l []string, x string) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}

// Two entries that work on one page do not share a chord, unless one of them says it takes the other's over (Over): Mod+K is a
// link in the editor and the palette outside it. A key that does two things, with nothing saying which wins, is a key the
// window cannot honestly describe.
func TestNoTwoEntriesOfAPageShareAChordUnlessOneOverridesTheOther(t *testing.T) {
	group := map[string]Group{}
	for _, g := range Groups {
		group[g.ID] = g
	}
	for _, e := range Entries {
		for _, o := range e.Over {
			if _, ok := ByID(o); !ok {
				t.Errorf("%s overrides %q, which is not an entry", e.ID, o)
			}
		}
	}
	for _, sc := range AllScopes {
		have := map[string]Entry{}
		for _, e := range Entries {
			if !contains(group[e.Group].WorksOn(), sc) {
				continue
			}
			for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
				o, dup := have[c]
				if dup && !contains(o.Over, e.ID) && !contains(e.Over, o.ID) {
					t.Errorf("on a %s page %s and %s both have %s, and neither overrides the other", sc, o.ID, e.ID, c)
				}
				if !dup {
					have[c] = e
				}
			}
		}
	}
	// the override really is a shared chord
	for _, e := range Entries {
		for _, name := range e.Over {
			o, _ := ByID(name)
			shared := false
			for _, a := range e.Keys {
				for _, b := range o.Keys {
					shared = shared || a == b
				}
			}
			if !shared {
				t.Errorf("%s overrides %s, and they share no chord", e.ID, name)
			}
		}
	}
}

func TestValidRefusesWhatIsNotCanonical(t *testing.T) {
	for _, c := range []string{"", "mod+b", "Mod+b", "Shift+Mod+Z", "Alt+Alt+B", "Mod+", "Mod+Shift", "Hyper+B", "Mod+Foo", "Mod+Shift+Alt+B"} {
		if Valid(c) == "" {
			t.Errorf("%q passed", c)
		}
	}
	for _, c := range []string{"Mod+Shift+Z", "Mod+Alt+1", "Alt+PageUp", "?", "Mod+/", "Mod+\\", "Shift+F10", "Esc", "]"} {
		if why := Valid(c); why != "" {
			t.Errorf("%q: %s", c, why)
		}
	}
}

// Every row has its words in the catalogue, and so do the sections and the keys that are drawn as words.
func TestEveryEntryHasItsWords(t *testing.T) {
	c := i18n.Default
	for _, k := range []string{"kb.title", "kb.open", "kb.lede", "kb.close", "kb.here", "kb.or", "kb.none"} {
		if !c.Has(k) {
			t.Errorf("the catalogue lacks %s", k)
		}
	}
	for _, g := range Groups {
		if !c.Has("kb.g." + g.ID) {
			t.Errorf("no name for the group %s", g.ID)
		}
	}
	for _, e := range Entries {
		if !c.Has("kb." + e.ID) {
			t.Errorf("no words for %s (kb.%s)", e.ID, e.ID)
		}
	}
	for name := range keyNames {
		if hasWord(name) && !c.Has("kb.k."+strings.ToLower(name)) {
			t.Errorf("no word for the key %s", name)
		}
	}
	for _, m := range modifiers {
		if m != "Mod" && !c.Has("kb.k."+strings.ToLower(m)) {
			t.Errorf("no word for the key %s", m)
		}
	}
	if !c.Has("kb.k.ctrl") {
		t.Error("no word for Ctrl")
	}
	// the catalogue holds no kb.* that nothing uses
	used := map[string]bool{"kb.title": true, "kb.open": true, "kb.lede": true, "kb.close": true, "kb.here": true, "kb.or": true, "kb.none": true}
	for _, g := range Groups {
		used["kb.g."+g.ID] = true
	}
	for _, e := range Entries {
		used["kb."+e.ID] = true
	}
	for name := range keyNames {
		used["kb.k."+strings.ToLower(name)] = true
	}
	for _, m := range modifiers {
		used["kb.k."+strings.ToLower(m)] = true
	}
	for _, m := range []string{"command", "option", "control"} { // the Mac's names of the modifiers, for the sign's tooltip
		used["kb.k."+m] = true
		if !c.Has("kb.k." + m) {
			t.Errorf("no word for %s", m)
		}
	}
	for _, k := range c.Keys() {
		if strings.HasPrefix(k, "kb.") && !used[k] {
			t.Errorf("the catalogue has %s, and nothing uses it", k)
		}
	}
}

func TestBuildPutsThePageFirstAndLeavesOutWhatTheProjectLacks(t *testing.T) {
	ids := func(s []Section) []string {
		var o []string
		for _, x := range s {
			o = append(o, x.ID)
		}
		return o
	}
	w := Build(en, ScopeWrite, true, true)
	if got := ids(w); got[0] != "write" || got[len(got)-1] != "app" {
		t.Errorf("the writing view: %v", got)
	}
	for _, s := range w {
		want := s.ID == "write" || s.ID == "undo" || s.ID == "alts" || s.ID == "ai" || s.ID == "chapters" || s.ID == "compendium" || s.ID == "comments" || s.ID == "tree"
		if s.Here != want {
			t.Errorf("%s: Here is %v", s.ID, s.Here)
		}
	}
	if got := ids(Build(en, ScopeWrite, false, true)); strings.Contains(strings.Join(got, " "), "ai") {
		t.Errorf("AI off still lists the writing partner: %v", got)
	}
	noEdit := Build(en, ScopeWrite, true, false)
	for _, s := range noEdit {
		for _, r := range s.Rows {
			if e, _ := ByID(r.ID); e.Needs == "edit" {
				t.Errorf("%s needs edit and is shown to a reader", r.ID)
			}
		}
	}
	if got := ids(Build(en, ScopeRead, false, false)); got[0] != "read" {
		t.Errorf("the reading view: %v", got)
	}
	for sc, want := range map[string]string{ScopeOutline: "outline", ScopeCards: "cards", ScopeStatus: "status", ScopeResearch: "research", ScopeTimeline: "timeline"} {
		if got := ids(Build(en, sc, false, true)); got[0] != want {
			t.Errorf("%s: %v", sc, got)
		}
		// for a person who cannot edit there is no section of keys that are for editing
		for _, id := range ids(Build(en, sc, false, false)) {
			if id == want {
				t.Errorf("%s: a reader is shown the %s section", sc, want)
			}
		}
	}
	if got := ids(Build(en, ScopeApp, false, true)); got[0] != "app" {
		t.Errorf("every other page: %v", got)
	}
	// The same words in the person's language: a pseudo-locale changes every one.
	px := Build(func(k string) string { return i18n.Default.T("xx", k) }, ScopeApp, false, true)
	if px[0].Rows[0].Words == w[len(w)-1].Rows[0].Words {
		t.Error("the words did not follow the language")
	}
}

func TestCapsAreDrawnForOtherThanAMac(t *testing.T) {
	caps := Caps(en, "Mod+Shift+Z")
	if len(caps) != 3 || caps[0].Text != "Ctrl" || caps[0].Key != "Mod" || caps[1].Text != "Shift" || caps[1].Key != "Shift" || caps[2].Text != "Z" || caps[2].Key != "" {
		t.Errorf("%+v", caps)
	}
	if c := Caps(en, "Alt+PageUp"); c[1].Text != "Page Up" || c[0].Key != "Alt" {
		t.Errorf("%+v", c)
	}
	if c := Caps(en, "Up"); c[0].Text != "↑" || c[0].Label != "Up arrow" {
		t.Errorf("%+v", c)
	}
	if c := Caps(en, "Shift+F10"); c[1].Text != "F10" {
		t.Errorf("%+v", c)
	}
	// A Mac-only key is in the row, marked.
	for _, s := range Build(en, ScopeWrite, true, true) {
		for _, r := range s.Rows {
			if r.ID == "tree_trash" && (len(r.Alts) != 2 || r.Alts[0].Mac || !r.Alts[1].Mac) {
				t.Errorf("trash: %+v", r.Alts)
			}
		}
	}
}

func TestHintsAreForEveryPaletteEntryAndSmall(t *testing.T) {
	h := Hints(en)
	for _, e := range Entries {
		if e.Palette != strings.Contains(h, `"`+e.ID+`":`) {
			t.Errorf("%s: Palette is %v, and the hints say otherwise", e.ID, e.Palette)
		}
	}
	for _, want := range []string{`"redo":"Mod+Shift+Z Mod+Y"`, `"PageUp":"Page Up"`, `"Mod":"Ctrl"`, `"split":"Alt+Shift+B"`, `"m":{`, `"Alt":"Option"`} {
		if !strings.Contains(h, want) {
			t.Errorf("the hints lack %s: %s", want, h)
		}
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(h))
	zw.Close()
	// It is on every signed-in page: under 1 KB gzip, as the plan says.
	if buf.Len() > 1024 || len(h) > 1500 {
		t.Errorf("the hints are %d bytes, %d gzip", len(h), buf.Len())
	}
	t.Logf("palette hints: %d bytes, %d gzip", len(h), buf.Len())
}

func TestScopeOf(t *testing.T) {
	for path, want := range map[string]string{
		"/": ScopeApp, "/settings": ScopeApp, "/p/book/d/abc": ScopeWrite, "/p/book/read": ScopeRead, "/p/book/read/abc": ScopeRead,
		"/p/book/outline": ScopeOutline, "/p/book/cards": ScopeCards, "/p/book/board": ScopeStatus, "/p/book/timeline": ScopeTimeline,
		"/p/book/timeline/events/new": ScopeApp, "/p/book/research/board": ScopeResearch, "/p/book/research": ScopeApp, "/c/lore/timeline": ScopeTimeline, "/p/book": ScopeApp, "/p/book/entities": ScopeApp,
	} {
		if got := ScopeOf(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

func TestDesktopChordsAreTheModAndAltShiftOnes(t *testing.T) {
	got := strings.Join(DesktopChords(), " ")
	for _, want := range []string{"CmdOrCtrl+Z", "CmdOrCtrl+Shift+Z", "CmdOrCtrl+Y", "CmdOrCtrl+/", "CmdOrCtrl+Shift+P", "CmdOrCtrl+\\", "Alt+Shift+B", "Alt+Shift+J"} {
		if !strings.Contains(" "+got+" ", " "+want+" ") {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	for _, not := range []string{"Alt+PageUp", "Tab", "Esc", "F2"} {
		if strings.Contains(" "+got+" ", " "+not+" ") {
			t.Errorf("%s is not a modifier chord", not)
		}
	}
}
