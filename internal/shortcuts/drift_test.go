package shortcuts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// These tests read the shipped scripts, the editor's keymap, the templates and the desktop shell's chord list, and compare
// them with the registry. A key added, changed or removed in one place and not in the registry fails here, and the message
// names the key and the file. (F-1251.)

const repo = "../.."

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func scripts(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repo, "web/static/js/*.js"))
	if err != nil || len(files) < 30 {
		t.Fatalf("the shipped scripts: %v, %d found", err, len(files))
	}
	out := map[string]string{}
	for _, f := range files {
		name := filepath.Base(f)
		if name == "htmx.min.js" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func sorted(m map[string]bool) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	sort.Strings(o)
	return o
}

func registryChords() map[string]string { // chord -> the entry that has it
	m := map[string]string{}
	for _, e := range Entries {
		for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
			m[c] = e.ID
		}
	}
	return m
}

var (
	reClaimAssign = regexp.MustCompile(`chords\.(Key[A-Z])\s*=[^=]`)
	reClaimCall   = regexp.MustCompile(`claim\("(Key[A-Z])"`)
	reKeysTable   = regexp.MustCompile(`var KEYS = \{([^}]*)\}`)
	reKeysPair    = regexp.MustCompile(`(Key[A-Z]):`)
	reAltShiftSrc = regexp.MustCompile(`e\.code === "(Key[A-Z])"`)
)

// claimsInScripts is every Alt+Shift chord the scripts hold, by the file that holds it.
func claimsInScripts(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for name, src := range scripts(t) {
		for _, m := range reClaimAssign.FindAllStringSubmatch(src, -1) {
			got[m[1]] = name
		}
		for _, m := range reClaimCall.FindAllStringSubmatch(src, -1) {
			got[m[1]] = name
		}
		for _, tb := range reKeysTable.FindAllStringSubmatch(src, -1) {
			for _, m := range reKeysPair.FindAllStringSubmatch(tb[1], -1) {
				got[m[1]] = name
			}
		}
		for _, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "altKey") && strings.Contains(line, "shiftKey") {
				for _, m := range reAltShiftSrc.FindAllStringSubmatch(line, -1) {
					got[m[1]] = name
				}
			}
		}
	}
	if len(got) < 8 {
		t.Fatalf("only %d Alt+Shift claims found in the scripts (%v): is the scan stale?", len(got), got)
	}
	return got
}

// A chord that a script claims and the registry does not have fails; a registry Alt+Shift entry that no script claims fails.
func TestEveryChordIsDocumented(t *testing.T) {
	claimed := claimsInScripts(t)
	have := map[string]string{}
	for _, e := range Entries {
		if e.Claim != "" {
			have[e.Claim] = e.ID
		}
	}
	for code, file := range claimed {
		if have[code] == "" {
			t.Errorf("%s claims Alt+Shift+%s (%s), and the registry has no entry for it: add one to internal/shortcuts/registry.go, with its words kb.<id> in the catalogue and the chord in desktop menu.rs APP_CHORDS", file, strings.TrimPrefix(code, "Key"), code)
		}
	}
	for code, id := range have {
		if claimed[code] == "" {
			t.Errorf("the registry entry %s says Alt+Shift+%s, and no script claims %s: a key the page does not have", id, strings.TrimPrefix(code, "Key"), code)
		}
	}
}

// pmKey turns a ProseMirror key name (Shift-Mod-z) into the registry's canonical chord (Mod+Shift+Z).
func pmKey(name string) string {
	parts := strings.Split(name, "-")
	key := parts[len(parts)-1]
	mods := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		mods[p] = true
	}
	var out []string
	for _, m := range modifiers {
		if mods[m] {
			out = append(out, m)
		}
	}
	if len([]rune(key)) == 1 {
		key = strings.ToUpper(key)
	}
	return strings.Join(append(out, key), "+")
}

var reKeymapLine = regexp.MustCompile(`(?m)^\s*(?:"([A-Za-z0-9-]+)"|(Enter|Tab|Backspace)):`)

func editorKeymap(t *testing.T) map[string]bool {
	t.Helper()
	src := read(t, "web/editor/src/rules.js")
	a := strings.Index(src, "keymap({")
	if a < 0 {
		t.Fatal("rules.js has no keymap({")
	}
	body := src[a:]
	body = body[:strings.Index(body, "keymap(baseKeymap)")]
	out := map[string]bool{}
	for _, m := range reKeymapLine.FindAllStringSubmatch(body, -1) {
		name := m[1] + m[2]
		if name == "Enter" || name == "Backspace" { // the editor's own behaviour, not a key a writer is told about
			continue
		}
		out[pmKey(name)] = true
	}
	if len(out) < 12 {
		t.Fatalf("only %d chords in the editor's keymap: %v", len(out), sorted(out))
	}
	return out
}

func TestTheEditorKeymapIsDocumented(t *testing.T) {
	have := registryChords()
	for c := range editorKeymap(t) {
		if have[c] == "" {
			t.Errorf("web/editor/src/rules.js binds %s, and the registry has no entry for it", c)
		}
	}
}

var (
	// A test for the key and a test for Control or Command in one && chain, in either order. The chain is read as a chord, so a line
	// that tests several unrelated things (a handler for two keys, one with Control) is not read as a chord for each.
	reModKeyFirst = regexp.MustCompile(`\bkey(?:\.toLowerCase\(\))? === "((?:[^"\\]|\\.)+)" && \(e\.(?:ctrl|meta)Key \|\| e\.(?:ctrl|meta)Key\)`)
	reModKeyLast  = regexp.MustCompile(`\(e\.(?:ctrl|meta)Key \|\| e\.(?:ctrl|meta)Key\)((?: && !?e\.(?:alt|shift)Key)*) && e\.key(?:\.toLowerCase\(\))? === "((?:[^"\\]|\\.)+)"`)
	rePaletteK    = regexp.MustCompile(`\bk === "(.)"`)
	// Not bindings: a script that notes Select All as a move of the selection.
	notBound = map[string]bool{"alts.js Mod+A": true}
)

// modChordsByFile is every Control or Command chord a script tests for, found on one line (the way every handler writes it), by
// file. share.js is the page a share link opens, which has no header, palette or window, and its Mod+P is that page's print; the
// editor's own synthetic Mod+K (format.js) is marked // chord-ok.
func modChordsByFile(t *testing.T) map[string]map[string]bool {
	t.Helper()
	got := map[string]map[string]bool{}
	add := func(file, c string) {
		if got[file] == nil {
			got[file] = map[string]bool{}
		}
		got[file][c] = true
	}
	n := 0
	for name, src := range scripts(t) {
		if name == "share.js" {
			continue
		}
		for _, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "chord-ok") {
				continue
			}
			if name == "palette.js" {
				for _, m := range rePaletteK.FindAllStringSubmatch(line, -1) {
					c := "Mod+" + strings.ToUpper(m[1])
					if strings.Contains(line, "e.shiftKey") {
						c = "Mod+Shift+" + strings.ToUpper(m[1])
					}
					add(name, c)
					n++
				}
				continue
			}
			if !strings.Contains(line, "ctrlKey") || !strings.Contains(line, "metaKey") {
				continue
			}
			note := func(chain, key string) {
				key = strings.ReplaceAll(key, `\\`, `\`)
				if len([]rune(key)) == 1 {
					key = strings.ToUpper(key)
				}
				c := "Mod+" + key
				if strings.Contains(chain, " && e.shiftKey") {
					c = "Mod+Shift+" + key
				}
				if !notBound[name+" "+c] {
					add(name, c)
					n++
				}
			}
			for _, m := range reModKeyFirst.FindAllStringSubmatch(line, -1) {
				note("", m[1])
			}
			for _, m := range reModKeyLast.FindAllStringSubmatch(line, -1) {
				note(m[1], m[2])
			}
		}
	}
	if n < 8 {
		t.Fatalf("only %d Mod chords found in the scripts: %v (is the scan stale?)", n, got)
	}
	return got
}

// The groups of the registry that document each file's Control and Command chords. A chord a file listens for must be a row of one
// of its groups, not just of any group: Mod+Enter in boards.js is saving a card, not a scene break.
var modGroups = map[string][]string{
	"rules.js": {"write", "undo"}, "doc.js": {"write"}, "aichat.js": {"ai"}, "tree.js": {"chapters"}, "palette.js": {"app", "chapters"},
	"outline.js": {"outline"}, "boards.js": {"cards"}, "review.js": {"read"}, "suggest.js": {"read"},
}

func groupChords(groups ...string) map[string]bool {
	out := map[string]bool{}
	for _, e := range Entries {
		for _, g := range groups {
			if e.Group == g {
				for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
					out[c] = true
				}
			}
		}
	}
	return out
}

func TestEveryModChordIsDocumentedAndEveryDocumentedOneExists(t *testing.T) {
	by := modChordsByFile(t)
	by["rules.js"] = map[string]bool{}
	for k := range editorKeymap(t) {
		if strings.HasPrefix(k, "Mod+") {
			by["rules.js"][k] = true
		}
	}
	found := map[string]string{}
	for file, chords := range by {
		groups, ok := modGroups[file]
		if !ok {
			t.Errorf("%s listens for %v, and is in no group of modGroups: add the file, with the group of the registry that documents its keys", file, sorted(chords))
			continue
		}
		have := groupChords(groups...)
		for c := range chords {
			found[c] = file
			if !have[c] {
				t.Errorf("%s listens for %s, and no row of its group(s) %v has it", file, c, groups)
			}
		}
	}
	// the other way: a Mod chord in the registry must be bound by the editor or listened for by a script
	for c, id := range registryChords() {
		if strings.HasPrefix(c, "Mod+") && found[c] == "" {
			t.Errorf("the registry entry %s says %s, and no script or keymap binds it", id, c)
		}
	}
}

// The keys the per-page handlers test, by the file, and the groups of the registry that document them. Arrow keys, Home, End,
// Enter, Escape and Tab are not checked one by one (they are how every list works); every other named key and every letter is.
var pageFiles = map[string][]string{
	"tree.js": {"tree"}, "outline.js": {"outline"}, "boards.js": {"cards", "status"}, "timeline.js": {"timeline"}, "research.js": {"research"},
	"read.js": {"read"}, "review.js": {"read"}, "ai.js": {"ai"},
}

// elsewhere are the bare keys that scripts outside pageFiles (and the ones in it, beyond what they document) test for, and why none
// is a shortcut. A new one fails TestEveryKeyAScriptTestsForIsAccountedFor until it is a row or is listed here with its reason.
var elsewhere = map[string]string{
	"alts.js Backspace":     "Backspace inside an alternative's mark edits the mark, as Backspace does in any text",
	"mentions.js Backspace": "Backspace after a name's link takes the link off, as Backspace does in any text",
	"palette.js ?":          "the help key, which is the registry's help row",
	"selbar.js Shift":       "the selection bar shows when a Shift+arrow selection is let go; Shift there extends a selection, as it does in any text",
	"share.js [":            "the share-link page: no header, palette or window (its keys are the reading view's)",
	"share.js ]":            "the share-link page",
	"share.js C":            "the share-link page",
	"share.js P":            "the share-link page",
}

var (
	reEKey      = regexp.MustCompile(`\be\.key(?:\.toLowerCase\(\))? (?:===|!==) "((?:[^"\\]|\\.)+)"`)
	reAliasKey  = regexp.MustCompile(`\bk (?:===|!==) "((?:[^"\\]|\\.)+)"`)
	genericKeys = map[string]bool{"arrowup": true, "arrowdown": true, "arrowleft": true, "arrowright": true, "home": true, "end": true, "enter": true, "escape": true, "tab": true, " ": true, "pageup": true, "pagedown": true}
)

func pageToken(lit string) string {
	lit = strings.ReplaceAll(lit, `\\`, `\`)
	if lit == "ContextMenu" {
		return "Menu"
	}
	if len([]rune(lit)) == 1 {
		return strings.ToUpper(lit)
	}
	return lit
}

// regionOf is the part of a file between two markers; a file that names its key variable k only there is read for k only there.
func regionOf(file, text, from, to string) string {
	a := strings.Index(text, from)
	if a < 0 {
		panic(file + ": the scan's start marker " + from + " is not there")
	}
	b := strings.Index(text[a:], to)
	if b < 0 {
		panic(file + ": the scan's end marker " + to + " is not there")
	}
	return text[a : a+b]
}

var aliasRegions = map[string][2]string{
	"tree.js": {"// ---- keyboard", `slot.addEventListener("focusin"`},
	"ai.js":   {"var k = e.key.toLowerCase();", "});"},
}

// pageKeys is the specific keys (not the generic ones) a file tests for, as registry tokens.
func pageKeys(file, text string) map[string]bool {
	out := map[string]bool{}
	scan := func(re *regexp.Regexp, src string) {
		for _, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "ctrlKey") && strings.Contains(line, "metaKey") {
				continue // a Mod chord: TestEveryModChordIsDocumented
			}
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				if !genericKeys[strings.ToLower(m[1])] {
					out[pageToken(m[1])] = true
				}
			}
		}
	}
	scan(reEKey, text)
	if r, ok := aliasRegions[file]; ok {
		scan(reAliasKey, regionOf(file, text, r[0], r[1]))
	}
	return out
}

func documentedKeys(groups []string) map[string]bool {
	doc := map[string]bool{}
	for _, e := range Entries {
		for _, g := range groups {
			if e.Group != g {
				continue
			}
			for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
				s := Split(c)
				doc[s[len(s)-1]] = true
			}
		}
	}
	return doc
}

func TestThePerPageHandlersKeysAreDocumented(t *testing.T) {
	src := scripts(t)
	tested := map[string]map[string]bool{} // group -> keys some file of it tests
	for file, groups := range pageFiles {
		text, ok := src[file]
		if !ok {
			t.Fatalf("%s is not among the shipped scripts", file)
		}
		doc := documentedKeys(groups)
		for tok := range pageKeys(file, text) {
			for _, g := range groups {
				if tested[g] == nil {
					tested[g] = map[string]bool{}
				}
				tested[g][tok] = true
			}
			if !doc[tok] && elsewhere[file+" "+tok] == "" {
				t.Errorf("%s tests for the key %s, and the rows of its group(s) %v (internal/shortcuts/registry.go) do not have it", file, tok, groups)
			}
		}
	}
	// the other way: a key with no modifier that a row documents, other than the arrows, Home, End, Enter, Esc, Tab and A to Z, is
	// tested by a file of the group (an Alt+Shift chord is claimed, which TestEveryChordIsDocumented checks)
	generic := map[string]bool{"Up": true, "Down": true, "Left": true, "Right": true, "Home": true, "End": true, "Enter": true, "Esc": true, "Tab": true, "Letter": true}
	for _, e := range Entries {
		if e.Group == "write" || e.Group == "undo" || e.Group == "alts" || e.Group == "chapters" || e.Group == "app" || e.Group == "timeline" {
			continue
		}
		for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
			if s := Split(c); len(s) == 1 && !generic[s[0]] && !tested[e.Group][s[0]] {
				t.Errorf("the registry entry %s documents %s, and no script of the %q group tests for it", e.ID, s[0], e.Group)
			}
		}
	}
	if len(tested["tree"]) < 4 || len(tested["read"]) < 8 || len(tested["ai"]) < 4 {
		t.Errorf("the scan found too little: %v", tested)
	}
}

// The other blind spot: a script that handles a key and is in no list. Every file of the shipped scripts is read for a bare key it
// tests for (not an arrow, Home, End, Enter, Escape or Tab, and not on a line with Control or Command), and a key that is neither in
// pageFiles nor in elsewhere, with its reason, fails: either it is a shortcut, and is a row, or it is not, and says why.
func TestEveryKeyAScriptTestsForIsAccountedFor(t *testing.T) {
	for file, text := range scripts(t) {
		if _, ok := pageFiles[file]; ok {
			continue
		}
		for tok := range pageKeys(file, text) {
			if elsewhere[file+" "+tok] != "" {
				continue
			}
			t.Errorf("%s tests for the key %s: if it is a shortcut, make it a row of the registry and add the file to pageFiles; if it is not, list it in elsewhere with the reason", file, tok)
		}
	}
	// a key listed in elsewhere is still tested for
	for k := range elsewhere {
		parts := strings.SplitN(k, " ", 2)
		if !strings.Contains(scripts(t)[parts[0]], `"`+strings.ToLower(parts[1])+`"`) && !strings.Contains(scripts(t)[parts[0]], `"`+parts[1]+`"`) {
			t.Errorf("elsewhere lists %q, which the script no longer tests for: remove it", k)
		}
	}
}

// Alt+Shift chords named in the scripts' own sentences (the writing partner's notices) must be ones the registry has.
func TestChordsNamedInSentencesExist(t *testing.T) {
	re := regexp.MustCompile(`Alt\+Shift\+([A-Z])\b`)
	have := registryChords()
	n := 0
	for name, src := range scripts(t) {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			n++
			if have["Alt+Shift+"+m[1]] == "" {
				t.Errorf("%s names Alt+Shift+%s, which is not a key in the registry", name, m[1])
			}
		}
	}
	if n < 5 {
		t.Errorf("only %d mentions found", n)
	}
}

// No hint is typed by hand: the palette script, the format bar and the templates draw every key from the registry.
func TestNoHandTypedHintsRemain(t *testing.T) {
	src := scripts(t)
	pal := src["palette.js"]
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`\bkey:\s*"`),                                        // key: "Alt Shift B"
		regexp.MustCompile(`\bkey:\s*MOD\b`),                                    // key: MOD + " O"
		regexp.MustCompile(`"(Alt|Ctrl|Shift)[ +](Shift|Alt|PgUp|PgDn|[A-Z])"`), // "Alt Shift I"
		regexp.MustCompile(`\bMOD\s*[+=]`),                                      // the hand-built modifier
	} {
		if m := bad.FindString(pal); m != "" {
			t.Errorf("palette.js has a hand-typed key: %s", m)
		}
	}
	for name, s := range src {
		if name == "palette.js" {
			continue
		}
		for _, bad := range []*regexp.Regexp{regexp.MustCompile(`replace\(/Mod/g`), regexp.MustCompile(`"⌘"`), regexp.MustCompile(`dataset\.pc\b`), regexp.MustCompile(`data-mod-key`)} {
			if m := bad.FindString(s); m != "" {
				t.Errorf("%s draws a key itself (%s): use S.keys.text from palette.js", name, m)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(repo, "web/templates/*.html"))
	more, _ := filepath.Glob(filepath.Join(repo, "web/templates/partials/*.html"))
	n := 0
	for _, f := range append(files, more...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		n += strings.Count(s, "{{keys ")
		for _, bad := range []*regexp.Regexp{
			regexp.MustCompile(`data-keys="[^{]`), regexp.MustCompile(`data-pc=`), regexp.MustCompile(`data-mod-key`),
			regexp.MustCompile(`aria-keyshortcuts="[^{"]*"`),
		} {
			for _, m := range bad.FindAllString(s, -1) {
				// the palette's hint button is named in ARIA's own words (Control, Meta), which a registry token is not
				if strings.Contains(m, "Control+K Meta+K") || strings.Contains(m, `aria-keyshortcuts="Shift+/"`) {
					continue
				}
				t.Errorf("%s has a hand-typed key: %s", filepath.Base(f), m)
			}
		}
	}
	if n < 15 {
		t.Errorf("only %d {{keys}} in the templates: is the scan stale?", n)
	}
}

// menu.rs APP_CHORDS, the list of chords the web app listens for so that a native menu accelerator never shadows one, equals the
// registry's Mod and Alt+Shift chords (Mod as CmdOrCtrl). It is Rust, so the Go test reads the source.
func TestDesktopAppChordsEqualTheRegistry(t *testing.T) {
	src := read(t, "desktop/src-tauri/src/menu.rs")
	m := regexp.MustCompile(`(?s)pub const APP_CHORDS: &\[&str\] = &\[(.*?)\];`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("menu.rs has no APP_CHORDS")
	}
	rust := map[string]bool{}
	for _, s := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(m[1], -1) {
		rust[strings.ReplaceAll(s[1], `\\`, `\`)] = true
	}
	reg := map[string]bool{}
	for _, c := range DesktopChords() {
		reg[c] = true
	}
	for c := range reg {
		if !rust[c] {
			t.Errorf("the registry has %s and desktop/src-tauri/src/menu.rs APP_CHORDS does not: a native accelerator could shadow it", c)
		}
	}
	for c := range rust {
		if !reg[c] {
			t.Errorf("menu.rs APP_CHORDS has %s, which no registry entry has: a stale chord, or a key the registry lacks", c)
		}
	}
}

// aria-keyshortcuts is read by assistive technology in ARIA's own words: Control and Meta, never the registry's Mod, and the key a
// ? is made with is Shift+/. A template that puts a registry id with a Mod chord there says Mod to a screen reader.
func TestAriaKeyshortcutsNeverSayMod(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(repo, "web/templates/*.html"))
	more, _ := filepath.Glob(filepath.Join(repo, "web/templates/partials/*.html"))
	re := regexp.MustCompile(`aria-keyshortcuts="((?:\{\{[^}]*\}\}|[^"])*)"`) // a template action has quotes of its own
	idRe := regexp.MustCompile(`\{\{keys "([a-z0-9_]+)"\}\}`)
	n := 0
	for _, f := range append(files, more...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n++
			if strings.Contains(m[1], "Mod") {
				t.Errorf("%s: aria-keyshortcuts=%q says Mod", filepath.Base(f), m[1])
			}
			for _, id := range idRe.FindAllStringSubmatch(m[1], -1) {
				e, ok := ByID(id[1])
				if !ok {
					t.Errorf("%s: aria-keyshortcuts names the unknown entry %s", filepath.Base(f), id[1])
					continue
				}
				for _, c := range e.Keys {
					if strings.Contains(c, "Mod") {
						t.Errorf("%s: aria-keyshortcuts uses %s, whose chord %s has Mod: ARIA wants Control and Meta", filepath.Base(f), e.ID, c)
					}
				}
			}
			if m[1] == "?" {
				t.Errorf("%s: aria-keyshortcuts=\"?\": the key is Shift+/", filepath.Base(f))
			}
		}
	}
	if n < 4 {
		t.Errorf("only %d aria-keyshortcuts found", n)
	}
}

// A key the person reads is drawn from the registry for their computer (U5): a sentence that names a modifier ("Ctrl+K", "Ctrl or
// Command with E", "Alt or Ctrl") is the wrong key on the other platform. kb.lede says what the two modifiers are, and the key
// names are the catalogue's own (kb.k.*). aria-keyshortcuts is the ARIA format, and comments are not shown to anyone.
var reTypedModifier = regexp.MustCompile(`\b(Ctrl|Cmd|Command|Control)(\+|\s+(or|and|with)\b)|\b(or|and)\s+(Ctrl|Cmd|Command)\b`)
var (
	reARIAKeys     = regexp.MustCompile(`aria-keyshortcuts="[^"]*"`)
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`(?m)^\s*//.*$`)
)

func TestNoTypedModifierKeysInText(t *testing.T) {
	var en map[string]string
	if err := json.Unmarshal([]byte(read(t, "internal/i18n/locales/en.json")), &en); err != nil {
		t.Fatal(err)
	}
	for k, v := range en {
		if k == "kb.lede" || strings.HasPrefix(k, "kb.k.") {
			continue
		}
		if m := reTypedModifier.FindString(v); m != "" {
			t.Errorf("the catalogue's %s names a modifier (%q): say the action, or draw the key from the registry. Text: %s", k, m, v)
		}
	}
	tpls, _ := filepath.Glob(filepath.Join(repo, "web/templates/*.html"))
	for _, f := range tpls {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := reARIAKeys.ReplaceAllString(string(b), "")
		if m := reTypedModifier.FindString(src); m != "" {
			t.Errorf("%s names a modifier in its text (%q): draw the key from the registry (data-keys), or say the action", filepath.Base(f), m)
		}
	}
	for name, src := range scripts(t) {
		if name == "editor.js" {
			continue
		}
		src = reLineComment.ReplaceAllString(reBlockComment.ReplaceAllString(src, ""), "")
		if m := reTypedModifier.FindString(src); m != "" {
			t.Errorf("%s names a modifier in its text (%q): say the action, or draw the key from the registry (S.keys.text)", name, m)
		}
	}
}
