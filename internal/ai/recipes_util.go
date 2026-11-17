package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	xtext "github.com/crueber/scriobh/internal/text"
)

// Helpers every recipe's parser shares. A model's answer is untrusted text: the
// parsers read it tolerantly (a fenced block, a preamble, a list that was cut
// off) and bound everything they return, so nothing a model writes can make a
// result larger, stranger or more trusted than the recipe allows.

// Bounds on what a parser returns. They are the same for every recipe so a
// screen can rely on them.
const (
	MaxNotes         = 60  // notes of one critique or continuity run
	MaxMarks         = 200 // model marks of one Lab run
	MaxEntitiesFound = 30  // sheets of one extraction run
	MaxAlternatives  = 5
	MaxNoteChars     = 600 // runes of a note's sentence
	MaxQuoteChars    = 300 // runes of a quote a note or mark anchors to
	MaxExampleChars  = 160 // runes of the one short example a note may carry
	MaxReasonChars   = 300
	MaxReplaceBytes  = 64 << 10 // a suggested replacement
)

var (
	boundaryEcho = regexp.MustCompile(`<<<\s*(scriobh-data|end)\b`)
	htmlTag      = regexp.MustCompile(`<[A-Za-z/!?]`)
)

// Insertable says why a model-written text may not enter a document, or "" when
// it may, for a text that replaces nothing: no control characters, no invisible
// or direction-overriding characters (internal/text.IsInvisible: every format
// character but the joiners), no private-note, ghost, alternatives or HTML
// comment markers, no raw HTML, no link, image or address at all, no echo of the
// request's boundary lines, valid UTF-8 and a bounded size. An empty text is
// insertable (a trim deletes).
func Insertable(s string) string { return InsertableAgainst(s, "") }

// InsertableAgainst is Insertable for a text that replaces original: the
// links, images and addresses original already has may stay (internal/text.LinkProblem
// holds the rule); none may be added.
func InsertableAgainst(s, original string) string {
	switch {
	case !utf8.ValidString(s):
		return "it is not valid text"
	case len(s) > MaxReplaceBytes:
		return "it is too long"
	case strings.Contains(s, "%%"):
		return "it contains %%, the private-note marker"
	case strings.Contains(s, "scriobh:alts") || strings.Contains(s, "scriobh-ghost"):
		return "it contains an alternatives or ghost marker"
	case strings.Contains(s, "<!--") || strings.Contains(s, "-->"):
		return "it contains an HTML comment"
	case htmlTag.MatchString(s):
		return "it contains HTML"
	case boundaryEcho.MatchString(s):
		return "it repeats the request's boundary lines"
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return "it contains a control character"
		case invisible(r):
			return "it contains an invisible or direction-overriding character"
		case unicode.Is(unicode.Co, r) || r == utf8.RuneError:
			return "it contains a character that cannot be shown"
		}
	}
	if why := xtext.LinkProblem(s, original); why != "" {
		return why
	}
	return ""
}

func invisible(r rune) bool { return xtext.IsInvisible(r) }

// CleanLine makes a model's phrase safe to show and to store: valid UTF-8, no
// control, direction-override or zero-width characters, white space collapsed to
// single spaces, at most max runes. It never returns markup-significant control
// bytes; HTML escaping is the screen's job (everything here is text).
func CleanLine(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == ' ':
			space = b.Len() > 0
			continue
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || invisible(r) || unicode.Is(unicode.Co, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
			n++
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// CleanContext cleans the text a model gives as the words around a quote: it
// keeps the characters as the manuscript has them (the context is looked up
// there), removes only what can never be in a manuscript line (control
// characters and the invisible and direction-overriding ones, which could make a
// note read as something it is not) and bounds the length.
func CleanContext(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\r' || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) || invisible(r) {
			continue
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String() // not trimmed: the white space next to a quote is how it is told from its repeats
}

// CleanQuote is CleanContext, trimmed: a quote is the words, not the space around them.
func CleanQuote(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\r' || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) || invisible(r) {
			continue
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

var fenceRe = regexp.MustCompile("(?s)```[A-Za-z0-9_-]*[ \t]*\n?(.*?)```")

// Unfence returns the text inside the first code fence of s, or s itself. A
// model told to answer in JSON often wraps it.
func Unfence(s string) string {
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(s)
}

// FirstJSONValue decodes the first complete JSON object or array in s,
// ignoring anything before it (a preamble, a fence marker). ok is false when
// there is none.
func FirstJSONValue(s string) (json.RawMessage, bool) {
	for _, src := range []string{Unfence(s), s} {
		for i := 0; i < len(src); i++ {
			if src[i] != '{' && src[i] != '[' {
				continue
			}
			dec := json.NewDecoder(strings.NewReader(src[i:]))
			var v json.RawMessage
			if err := dec.Decode(&v); err == nil {
				return v, true
			}
			if i > 4096 { // a manuscript quote can hold braces; do not scan forever
				break
			}
		}
	}
	return nil, false
}

// SalvageList reads the elements of the JSON array under key (or the first
// array of the answer when there is no such key), one complete element at a
// time. An answer that was cut off keeps every element that finished.
// truncated is true when the array did not reach its closing bracket. At most
// limit elements are returned; more set over.
func SalvageList(s, key string, limit int) (items []json.RawMessage, truncated, over, ok bool) {
	src := Unfence(s)
	if src == "" {
		return nil, false, false, false
	}
	start := -1
	if key != "" {
		if i := strings.Index(src, `"`+key+`"`); i >= 0 {
			if j := strings.IndexByte(src[i:], '['); j >= 0 {
				start = i + j
			}
		}
	}
	if start < 0 {
		start = strings.IndexByte(src, '[')
	}
	if start < 0 {
		return nil, false, false, false
	}
	dec := json.NewDecoder(strings.NewReader(src[start:]))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, false, false, false
	}
	for dec.More() {
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return items, true, over, true
		}
		if len(items) >= limit {
			over = true
			continue
		}
		items = append(items, v)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return items, true, over, true
	}
	return items, false, over, true
}

// Mark is what a model-judged Lab recipe returns for one range.
type Mark struct {
	Kind   string `json:"kind"`
	Quote  string `json:"quote"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Source is a fact a continuity finding cites.
type Source struct {
	Kind  string `json:"kind" enum:"entity,event,finding,doc"`
	ID    string `json:"id"`
	Field string `json:"field,omitempty"`
}

// Note is one note of a critique or a continuity audit: an observation about a
// place in the text, never replacement prose.
type Note struct {
	Quote    string   `json:"quote,omitempty"`
	Before   string   `json:"before,omitempty"`
	After    string   `json:"after,omitempty"`
	Category string   `json:"category"`
	Severity string   `json:"severity" enum:"low,medium,high"`
	Text     string   `json:"note"`
	Example  string   `json:"example,omitempty"`
	Sources  []Source `json:"sources,omitempty"`
	// Trimmed is set when a limit shortened the note or its example.
	Trimmed bool `json:"trimmed,omitempty"`
}

// NotesData is the parsed answer of a notes recipe.
type NotesData struct {
	Notes []Note `json:"notes"`
	// Dropped counts items left out: unreadable, empty, citing sources that were
	// not supplied, or over the limit.
	Dropped   int  `json:"dropped"`
	Truncated bool `json:"truncated" desc:"The answer was cut off; the notes that finished are kept."`
}

// MarksData is the parsed answer of a Lab recipe.
type MarksData struct {
	Marks     []Mark `json:"marks"`
	Dropped   int    `json:"dropped"`
	Truncated bool   `json:"truncated"`
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

var errEmpty = errors.New("empty")

// cutSentence shortens s to at most max runes at a sentence or word boundary.
func cutSentence(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	r := []rune(s)[:max]
	out := string(r)
	if i := strings.LastIndexAny(out, ".!?"); i > len(out)/2 {
		return out[:i+1], true
	}
	if i := strings.LastIndexByte(out, ' '); i > len(out)/2 {
		out = out[:i]
	}
	return strings.TrimSpace(out), true
}

var _ = bytes.MinRead
