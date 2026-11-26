// Package shortcuts is the one place a keyboard shortcut is written down (F-1251). The shortcuts window, the palette's
// hints, the format bar's tooltips and the desktop shell's list of chords the web app listens for are all made from it,
// and tests compare it with the scripts and the editor's keymap, so a key that is added or removed in one place and not
// in the other fails the build.
//
// A key is a token string. "+" joins the keys pressed together; "Mod" is Control, or Command on a Mac. A chord is
// written with its modifiers first, in the order Mod, Ctrl, Alt, Shift, then the key: a single character, or one of
// the names in keyNames. The browser draws the tokens for its own platform (the caps are in js/palette.js).
package shortcuts

import (
	"fmt"
	"sort"
	"strings"
)

// Group is a section of the window. Scopes are the kinds of page where its keys work, and the sections of the page you are on
// come first. Everywhere is true for the group whose keys work on every page (the palette, this window).
type Group struct {
	ID         string
	Scopes     []string
	Everywhere bool
}

// Scopes a page can have. app is every other signed-in page. Each of the boards is its own: the corkboard, the status board, the
// research board and the timeline are different scripts with different keys.
const (
	ScopeApp      = "app"
	ScopeWrite    = "write"
	ScopeRead     = "read"
	ScopeOutline  = "outline"
	ScopeCards    = "cards"
	ScopeStatus   = "status"
	ScopeResearch = "research"
	ScopeTimeline = "timeline"
)

// AllScopes is every scope, for the tests and for the fragment's query.
var AllScopes = []string{ScopeApp, ScopeWrite, ScopeRead, ScopeOutline, ScopeCards, ScopeStatus, ScopeResearch, ScopeTimeline}

// Groups, in the order the window lists them. Each has the words kb.g.<ID>.
var Groups = []Group{
	{ID: "write", Scopes: []string{ScopeWrite}},
	{ID: "undo", Scopes: []string{ScopeWrite}},
	{ID: "alts", Scopes: []string{ScopeWrite}},
	{ID: "ai", Scopes: []string{ScopeWrite}},
	{ID: "chapters", Scopes: []string{ScopeWrite}},
	{ID: "compendium", Scopes: []string{ScopeWrite}},
	{ID: "comments", Scopes: []string{ScopeWrite}},
	{ID: "tree", Scopes: []string{ScopeWrite}},
	{ID: "outline", Scopes: []string{ScopeOutline}},
	{ID: "cards", Scopes: []string{ScopeCards}},
	{ID: "status", Scopes: []string{ScopeStatus}},
	{ID: "research", Scopes: []string{ScopeResearch}},
	{ID: "timeline", Scopes: []string{ScopeTimeline}},
	{ID: "read", Scopes: []string{ScopeRead}},
	{ID: "app", Scopes: []string{ScopeApp}, Everywhere: true},
}

// WorksOn is the scopes where a group's keys work.
func (g Group) WorksOn() []string {
	if g.Everywhere {
		return AllScopes
	}
	return g.Scopes
}

// Entry is one shortcut. Its words are the catalogue key kb.<ID>.
type Entry struct {
	ID    string
	Group string
	// Keys are the alternatives, each a chord of tokens. A row of the window shows them all.
	Keys []string
	// MacKeys are further alternatives that exist on a Mac only.
	MacKeys []string
	// Needs is "ai" (shown when AI is on for the project), "edit" (shown to a person who can edit), or empty.
	Needs string
	// Over names the entries whose key this one takes over where both work: the editor's Mod+K is a link, and not the palette. Two
	// entries of one page may share a chord only so.
	Over []string
	// Claim is, for an Alt+Shift chord, the KeyboardEvent.code that S.chords must hold in the scripts.
	Claim string
	// Palette marks a key the palette and the format bar show beside a command: its tokens go to the page.
	Palette bool
	// F is the feature the key belongs to, where it has one.
	F string
}

// Entries is every shortcut, in the order the window shows them within a group.
var Entries = []Entry{
	// Writing and format (js/format.js, web/editor/src/rules.js).
	{ID: "bold", Group: "write", Keys: []string{"Mod+B"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "italic", Group: "write", Keys: []string{"Mod+I"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "strike", Group: "write", Keys: []string{"Mod+Shift+X"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "h1", Group: "write", Keys: []string{"Mod+Alt+1"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "h2", Group: "write", Keys: []string{"Mod+Alt+2"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "h3", Group: "write", Keys: []string{"Mod+Alt+3"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "paragraph", Group: "write", Keys: []string{"Mod+Alt+0"}, Needs: "edit", F: "F-063"},
	{ID: "link", Group: "write", Keys: []string{"Mod+K"}, Over: []string{"palette_k"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "scene_break", Group: "write", Keys: []string{"Mod+Enter"}, Needs: "edit", Palette: true, F: "F-063"},
	{ID: "line_break", Group: "write", Keys: []string{"Shift+Enter"}, Needs: "edit", F: "F-063"},
	{ID: "source", Group: "write", Keys: []string{"Mod+E"}, Palette: true, F: "F-063"},
	{ID: "save", Group: "write", Keys: []string{"Mod+S"}, Needs: "edit", F: "F-063"},
	{ID: "list_indent", Group: "write", Keys: []string{"Tab"}, Needs: "edit", F: "F-063"},
	{ID: "list_outdent", Group: "write", Keys: []string{"Shift+Tab"}, Needs: "edit", F: "F-063"},

	// Undo and redo (F-1226): the editor's own history, and the two buttons that run it.
	{ID: "undo", Group: "undo", Keys: []string{"Mod+Z"}, Needs: "edit", Palette: true, F: "F-1226"},
	{ID: "redo", Group: "undo", Keys: []string{"Mod+Shift+Z", "Mod+Y"}, Needs: "edit", Palette: true, F: "F-1226"},

	// Alternatives and ghosts (js/alts.js).
	{ID: "alt_add", Group: "alts", Keys: []string{"Alt+Shift+A"}, Needs: "edit", Claim: "KeyA", Palette: true, F: "F-411"},
	{ID: "alt_next", Group: "alts", Keys: []string{"Alt+Shift+N"}, Needs: "edit", Claim: "KeyN", F: "F-411"},
	{ID: "alt_prev", Group: "alts", Keys: []string{"Alt+Shift+P"}, Needs: "edit", Claim: "KeyP", F: "F-411"},
	{ID: "alt_orig", Group: "alts", Keys: []string{"Alt+Shift+O"}, Needs: "edit", Claim: "KeyO", F: "F-411"},
	{ID: "ghost", Group: "alts", Keys: []string{"Alt+Shift+G"}, Needs: "edit", Claim: "KeyG", Palette: true, F: "F-411"},
	{ID: "stash", Group: "alts", Keys: []string{"Alt+Shift+S"}, Needs: "edit", Claim: "KeyS", Palette: true, F: "F-411"},

	// The writing partner (js/aiboot.js, js/ai.js), when AI is on for the project.
	{ID: "ai_menu", Group: "ai", Keys: []string{"Alt+Shift+I"}, Needs: "ai", Claim: "KeyI", Palette: true, F: "F-654"},
	{ID: "ai_proposal", Group: "ai", Keys: []string{"Alt+Shift+J"}, Needs: "ai", Claim: "KeyJ", Palette: true, F: "F-654"},
	{ID: "ai_accept", Group: "ai", Keys: []string{"A"}, Needs: "ai", F: "F-654"},
	{ID: "ai_keep", Group: "ai", Keys: []string{"L"}, Needs: "ai", F: "F-654"},
	{ID: "ai_reject", Group: "ai", Keys: []string{"R", "Esc"}, Over: []string{"esc"}, Needs: "ai", F: "F-654"},
	{ID: "ai_retry", Group: "ai", Keys: []string{"T"}, Needs: "ai", F: "F-654"},
	{ID: "ai_chat_send", Group: "ai", Keys: []string{"Mod+Enter"}, Over: []string{"scene_break"}, Needs: "ai", F: "F-654"},

	// Chapters (js/doc.js, js/split.js, js/palette.js, js/tree.js).
	{ID: "split", Group: "chapters", Keys: []string{"Alt+Shift+B"}, Needs: "edit", Claim: "KeyB", Palette: true, F: "F-1224"},
	{ID: "chapter_prev", Group: "chapters", Keys: []string{"Alt+PageUp"}, Palette: true, F: "F-063"},
	{ID: "chapter_next", Group: "chapters", Keys: []string{"Alt+PageDown"}, Palette: true, F: "F-063"},
	{ID: "switcher", Group: "chapters", Keys: []string{"Mod+O"}, Palette: true, F: "F-164"},
	{ID: "manuscript", Group: "chapters", Keys: []string{"Mod+\\"}},

	// The Compendium tab of the inspector (js/inspector.js claims the chord, js/lookup.js has the other keys).
	{ID: "compendium_panel", Group: "compendium", Keys: []string{"Alt+Shift+C"}, Claim: "KeyC", Palette: true, F: "F-1259"},
	{ID: "compendium_open", Group: "compendium", Keys: []string{"Enter"}, Over: []string{"tree_open"}, F: "F-1253"},
	{ID: "compendium_esc", Group: "compendium", Keys: []string{"Esc"}, Over: []string{"ai_reject", "esc"}, F: "F-1254"},

	// The Comments tab of the inspector (js/inspector.js claims the chord, js/cwrite.js writes the comment): for people who can edit.
	{ID: "comments_panel", Group: "comments", Keys: []string{"Alt+Shift+M"}, Needs: "edit", Claim: "KeyM", Palette: true, F: "F-1284"},

	// The manuscript tree in the writing view, and the outline (js/tree.js, js/outline.js).
	{ID: "tree_move", Group: "tree", Keys: []string{"Up", "Down"}},
	{ID: "tree_ends", Group: "tree", Keys: []string{"Home", "End"}},
	{ID: "tree_fold", Group: "tree", Keys: []string{"Left", "Right"}},
	{ID: "tree_open", Group: "tree", Keys: []string{"Enter"}},
	{ID: "tree_rename", Group: "tree", Keys: []string{"F2"}, Needs: "edit"},
	{ID: "tree_trash", Group: "tree", Keys: []string{"Delete"}, MacKeys: []string{"Backspace"}, Needs: "edit"},
	{ID: "tree_reorder", Group: "tree", Keys: []string{"Alt+Up", "Alt+Down"}, Needs: "edit"},
	{ID: "tree_nest", Group: "tree", Keys: []string{"Alt+Right", "Alt+Left"}, Needs: "edit"},
	{ID: "tree_jump", Group: "tree", Keys: []string{"Letter"}},
	{ID: "tree_menu", Group: "tree", Keys: []string{"Menu", "Shift+F10"}, Needs: "edit"},

	// The outline page (js/outline.js). It is loaded for people who can edit, and the keys are theirs.
	{ID: "outline_move", Group: "outline", Keys: []string{"Up", "Down", "Home", "End"}, Needs: "edit"},
	{ID: "outline_edit", Group: "outline", Keys: []string{"Enter"}, Needs: "edit"},
	{ID: "outline_reorder", Group: "outline", Keys: []string{"Alt+Up", "Alt+Down"}, Needs: "edit"},
	{ID: "outline_nest", Group: "outline", Keys: []string{"Alt+Right", "Alt+Left"}, Needs: "edit"},
	{ID: "outline_save", Group: "outline", Keys: []string{"Mod+Enter"}, Needs: "edit"},

	// The corkboard (js/boards.js).
	{ID: "cards_move", Group: "cards", Keys: []string{"Up", "Down", "Home", "End"}, Needs: "edit"},
	{ID: "cards_edit", Group: "cards", Keys: []string{"Enter"}, Needs: "edit"},
	{ID: "cards_earlier", Group: "cards", Keys: []string{"Alt+Up", "Alt+Left"}, Needs: "edit"},
	{ID: "cards_later", Group: "cards", Keys: []string{"Alt+Down", "Alt+Right"}, Needs: "edit"},
	{ID: "cards_save", Group: "cards", Keys: []string{"Mod+Enter"}, Needs: "edit"},

	// The status board (js/boards.js).
	{ID: "status_move", Group: "status", Keys: []string{"Up", "Down", "Home", "End"}, Needs: "edit"},
	{ID: "status_column", Group: "status", Keys: []string{"Left", "Right"}, Needs: "edit"},

	// The research board (js/research.js).
	{ID: "research_column", Group: "research", Keys: []string{"Left", "Right"}, Needs: "edit"},

	// The project timeline in its own order (js/timeline.js): the buttons it moves with are there only for someone who can edit.
	{ID: "event_move", Group: "timeline", Keys: []string{"Alt+Up", "Alt+Down"}, Needs: "edit"},

	// Reading mode and review (js/read.js, js/review.js), F-701.
	{ID: "read_next", Group: "read", Keys: []string{"Right", "J"}, F: "F-701"},
	{ID: "read_prev", Group: "read", Keys: []string{"Left", "K"}, F: "F-701"},
	{ID: "review_caret", Group: "read", Keys: []string{"]", "["}, F: "F-701"},
	{ID: "review_select", Group: "read", Keys: []string{"Shift+Left", "Shift+Right"}, F: "F-701"},
	{ID: "review_comment", Group: "read", Keys: []string{"C"}, F: "F-701"},
	{ID: "review_suggest", Group: "read", Keys: []string{"S"}, F: "F-701"},
	{ID: "review_margin", Group: "read", Keys: []string{"M"}, F: "F-701"},
	{ID: "review_thread", Group: "read", Keys: []string{"J", "K"}, Over: []string{"read_next", "read_prev"}, F: "F-701"},
	{ID: "review_show", Group: "read", Keys: []string{"Enter"}, F: "F-701"},
	{ID: "review_reply", Group: "read", Keys: []string{"R"}, F: "F-701"},
	{ID: "review_resolve", Group: "read", Keys: []string{"X"}, F: "F-701"},
	{ID: "review_next", Group: "read", Keys: []string{"N"}, F: "F-701"},
	{ID: "review_leave", Group: "read", Keys: []string{"Esc"}, Over: []string{"esc"}, F: "F-701"},
	{ID: "review_send", Group: "read", Keys: []string{"Mod+Enter"}, F: "F-701"},

	// The application: the palette, this window, and closing things (js/palette.js, js/shell.js).
	{ID: "palette", Group: "app", Keys: []string{"Mod+Shift+P", "Mod+/"}, F: "F-164"},
	{ID: "palette_k", Group: "app", Keys: []string{"Mod+K"}, F: "F-164"},
	{ID: "help", Group: "app", Keys: []string{"?"}, Palette: true, F: "F-166"},
	{ID: "esc", Group: "app", Keys: []string{"Esc"}, F: "F-166"},
}

// keyNames are the keys that are not one character: the names the scripts' tests use, with the words they get from the
// catalogue (kb.k.<name in lower case>). Mod, Ctrl, Alt and Shift are the modifiers.
var keyNames = map[string]bool{
	"Enter": true, "Esc": true, "Tab": true, "Delete": true, "Backspace": true, "Menu": true,
	"PageUp": true, "PageDown": true, "Up": true, "Down": true, "Left": true, "Right": true, "Home": true, "End": true,
	"F2": true, "F10": true, "Letter": true,
}

var modifiers = []string{"Mod", "Ctrl", "Alt", "Shift"}

// IsModifier reports whether a token is a modifier.
func IsModifier(tok string) bool {
	for _, m := range modifiers {
		if tok == m {
			return true
		}
	}
	return false
}

// Split is a chord's tokens: the modifiers and then the key.
func Split(chord string) []string {
	if chord == "+" {
		return []string{"+"}
	}
	// A chord can end in "+" itself (Mod++); none does today, and Valid refuses it.
	return strings.Split(chord, "+")
}

// Valid reports why a chord is not in the canonical form, or "" when it is.
func Valid(chord string) string {
	toks := Split(chord)
	if len(toks) == 0 || chord == "" {
		return "empty"
	}
	last := -1
	for i, t := range toks {
		if i == len(toks)-1 {
			if IsModifier(t) {
				return "ends in a modifier"
			}
			if !keyNames[t] && len([]rune(t)) != 1 {
				return "unknown key " + t
			}
			if len([]rune(t)) == 1 && strings.ToUpper(t) != t {
				return "a letter is upper case"
			}
			break
		}
		at := -1
		for j, m := range modifiers {
			if m == t {
				at = j
			}
		}
		if at < 0 {
			return "unknown modifier " + t
		}
		if at <= last {
			return "modifiers are in the order Mod, Ctrl, Alt, Shift, once each"
		}
		last = at
	}
	return ""
}

// ByID finds an entry.
func ByID(id string) (Entry, bool) {
	for _, e := range Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Tokens is the entry's alternatives as one string, separated by spaces: what a toolbar button's data-keys holds. An id
// that is not in the registry is an error, so the template that names it does not render.
func Tokens(id string) (string, error) {
	e, ok := ByID(id)
	if !ok {
		return "", fmt.Errorf("shortcuts: no entry %q", id)
	}
	return strings.Join(e.Keys, " "), nil
}

// AllChords is every chord the registry names, on every platform, once, sorted.
func AllChords() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range Entries {
		for _, c := range append(append([]string{}, e.Keys...), e.MacKeys...) {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// DesktopChords are the chords the desktop shell must know the web app listens for, so that no native menu accelerator
// shadows one: every chord with the Mod modifier, and every Alt+Shift chord, in the shell's own spelling (CmdOrCtrl for
// Mod), sorted. It is what menu.rs APP_CHORDS has to equal.
func DesktopChords() []string {
	var out []string
	for _, c := range AllChords() {
		if strings.HasPrefix(c, "Mod+") || strings.HasPrefix(c, "Alt+Shift+") {
			out = append(out, strings.Replace(c, "Mod+", "CmdOrCtrl+", 1))
		}
	}
	sort.Strings(out)
	return out
}

// ScopeOf is the scope of a page by its address: the sections of the window that are about the page come first.
func ScopeOf(path string) string {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) >= 3 && seg[0] == "p" && seg[2] == "d" {
		return ScopeWrite
	}
	if len(seg) >= 3 && (seg[0] == "p" || seg[0] == "c") {
		switch seg[2] {
		case "read":
			return ScopeRead
		case "outline":
			return ScopeOutline
		case "cards":
			return ScopeCards
		case "board":
			return ScopeStatus
		case "timeline":
			if len(seg) == 3 {
				return ScopeTimeline
			}
		case "research":
			if len(seg) >= 4 && seg[3] == "board" {
				return ScopeResearch
			}
		}
	}
	return ScopeApp
}
