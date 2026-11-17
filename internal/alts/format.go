package alts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/text"
)

// Limits bound one document's alternatives.
type Limits struct {
	PerDoc       int // alternatives in one document
	VariantsPer  int // variants of one alternative
	MaxVariantSz int // bytes of one variant's text
}

// DefaultLimits are the documented defaults (SCRIOBH_MAX_ALTS_PER_DOC).
var DefaultLimits = Limits{PerDoc: 200, VariantsPer: 50, MaxVariantSz: 20_000}

func (l Limits) orDefault() Limits {
	if l.PerDoc <= 0 {
		l.PerDoc = DefaultLimits.PerDoc
	}
	if l.VariantsPer <= 0 {
		l.VariantsPer = DefaultLimits.VariantsPer
	}
	if l.MaxVariantSz <= 0 {
		l.MaxVariantSz = DefaultLimits.MaxVariantSz
	}
	return l
}

// ---- the comment ----------------------------------------------------------------

// The long form is what Serialize writes. The reader also accepts the short
// hand-written form (variants as plain strings, a quote, no ids).
type jVariant struct {
	ID       string `json:"id,omitempty"`
	Text     string `json:"text"`
	Original bool   `json:"original,omitempty"`
	Source   string `json:"source,omitempty"` // absent means manual
	At       string `json:"at,omitempty"`
}

type jItem struct {
	ID       string            `json:"id,omitempty"`
	Scope    string            `json:"scope,omitempty"`
	Before   string            `json:"before,omitempty"`
	After    string            `json:"after,omitempty"`
	Chosen   string            `json:"chosen,omitempty"`
	Orphan   bool              `json:"orphan,omitempty"`
	Quote    string            `json:"quote,omitempty"` // short form only
	Variants []json.RawMessage `json:"variants"`
}

type jDoc struct {
	Version int     `json:"version"`
	Sig     string  `json:"sig,omitempty"`
	Items   []jItem `json:"items"`
}

type outVariant struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Original bool   `json:"original,omitempty"`
	Source   string `json:"source,omitempty"`
	At       string `json:"at,omitempty"`
}

type outItem struct {
	ID       string       `json:"id"`
	Scope    string       `json:"scope"`
	Before   string       `json:"before,omitempty"`
	After    string       `json:"after,omitempty"`
	Chosen   string       `json:"chosen"`
	Orphan   bool         `json:"orphan,omitempty"`
	Variants []outVariant `json:"variants"`
}

type outDoc struct {
	Version int       `json:"version"`
	Sig     string    `json:"sig"`
	Items   []outItem `json:"items"`
}

// Sig is a short fingerprint of what a comment says about its alternatives: the
// items, their scope, and each wording's id, text, kind, source and time, in
// order. It leaves out what the server keeps current by itself (anchors,
// contexts, which wording is chosen, orphan marks) and what is not in the
// comment (authors). The writer puts it in the comment; a comment whose sig
// agrees with its own content has not been edited by hand, and one that differs
// from what is stored is then an older copy, not an instruction.
func Sig(items []Item) string {
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := sha256.New()
	for _, it := range sorted {
		fmt.Fprintf(h, "i\x00%s\x00%s\n", it.ID, it.Scope)
		for _, v := range it.Variants {
			fmt.Fprintf(h, "v\x00%s\x00%s\x00%t\x00%s\x00%s\n", v.ID, v.Text, v.Original, normSource(v.Source), v.At)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Serialize writes the comment line for items (which must be valid and have
// ids). Items are written in document order. Go's encoder escapes <, > and &, so
// no variant can close the comment. No items: an empty string.
func Serialize(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	sorted := append([]Item(nil), items...)
	SortItems(sorted)
	d := outDoc{Version: Version, Sig: Sig(sorted), Items: make([]outItem, 0, len(sorted))}
	for _, it := range sorted {
		o := outItem{ID: it.ID, Scope: it.Scope, Before: it.Before, After: it.After, Chosen: it.Chosen, Orphan: it.Orphan,
			Variants: make([]outVariant, 0, len(it.Variants))}
		for _, v := range it.Variants {
			ov := outVariant{ID: v.ID, Text: v.Text, Original: v.Original, At: v.At}
			if v.Source != "" && v.Source != SourceManual {
				ov.Source = v.Source
			}
			o.Variants = append(o.Variants, ov)
		}
		d.Items = append(d.Items, o)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(d); err != nil {
		return "" // cannot happen for these types
	}
	return Marker + strings.TrimRight(b.String(), "\n") + " -->"
}

// ParseError is a comment the reader cannot use.
type ParseError struct{ Msg string }

func (e *ParseError) Error() string { return e.Msg }

// Parse reads a comment line. Items come back unplaced (Start -1) with the ids
// the comment gave them (empty when it gave none); the short form's quote
// becomes the original and chosen variant. A missing scope is inferred from the
// quote. Structural problems are a ParseError; whether the text agrees with the
// anchors is for Rebase to find out.
func Parse(line string, lim Limits) ([]Item, error) {
	items, _, err := ParseSig(line, lim)
	return items, err
}

// ParseSig is Parse that also returns the sig the comment carries ("" when it
// has none, as a hand-written one does).
func ParseSig(line string, lim Limits) ([]Item, string, error) {
	lim = lim.orDefault()
	if !strings.HasPrefix(line, Marker) || !strings.HasSuffix(line, " -->") {
		return nil, "", &ParseError{"not an alternatives comment"}
	}
	raw := line[len(Marker) : len(line)-len(" -->")]
	var d jDoc
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&d); err != nil {
		return nil, "", &ParseError{"the alternatives comment is not valid JSON: " + shortErr(err)}
	}
	if dec.More() {
		return nil, "", &ParseError{"the alternatives comment has text after its JSON"}
	}
	if d.Version != Version {
		return nil, "", &ParseError{fmt.Sprintf("the alternatives comment has version %d; this server reads version %d", d.Version, Version)}
	}
	if len(d.Items) > lim.PerDoc {
		return nil, "", &ParseError{fmt.Sprintf("the alternatives comment holds %d alternatives; the limit is %d a document", len(d.Items), lim.PerDoc)}
	}
	out := make([]Item, 0, len(d.Items))
	for i, j := range d.Items {
		it, err := j.item(i, lim)
		if err != nil {
			return nil, "", err
		}
		out = append(out, it)
	}
	return out, d.Sig, nil
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func (j jItem) item(i int, lim Limits) (Item, error) {
	it := Item{ID: j.ID, Scope: j.Scope, Start: -1, End: -1, Before: j.Before, After: j.After, Chosen: j.Chosen, Orphan: j.Orphan}
	if len(j.Variants) > lim.VariantsPer+1 {
		return it, &ParseError{fmt.Sprintf("alternative %d holds %d variants; the limit is %d", i+1, len(j.Variants), lim.VariantsPer)}
	}
	short := false
	for _, rv := range j.Variants {
		rv = bytes.TrimSpace(rv)
		if len(rv) > 0 && rv[0] == '"' {
			var s string
			if err := json.Unmarshal(rv, &s); err != nil {
				return it, &ParseError{fmt.Sprintf("alternative %d: a variant is not valid", i+1)}
			}
			it.Variants = append(it.Variants, Variant{Text: s, Source: SourceManual})
			short = true
			continue
		}
		var v jVariant
		if err := json.Unmarshal(rv, &v); err != nil {
			return it, &ParseError{fmt.Sprintf("alternative %d: a variant is not valid", i+1)}
		}
		src := v.Source
		if src == "" {
			src = SourceManual
		}
		it.Variants = append(it.Variants, Variant{ID: v.ID, Text: v.Text, Original: v.Original, Source: src, At: v.At})
	}
	if short {
		// The hand-written form: variants are the other wordings and the quote
		// (or chosen text) is what the text says now, which is the original.
		quote := j.Quote
		if quote == "" {
			quote = j.Chosen
		}
		if quote == "" {
			return it, &ParseError{fmt.Sprintf("alternative %d: give a quote, the text the alternative replaces", i+1)}
		}
		have := false
		for k := range it.Variants {
			if it.Variants[k].Text == quote {
				it.Variants[k].Original = true
				have = true
			}
		}
		if !have {
			it.Variants = append([]Variant{{Text: quote, Original: true, Source: SourceManual}}, it.Variants...)
		}
		it.Chosen = "" // the body decides
		for _, v := range it.Variants {
			if v.Original {
				it.Chosen = v.ID // empty for a new item; filled when ids are assigned
			}
		}
	}
	if it.Scope == "" {
		it.Scope = inferScope(it)
	}
	return it, nil
}

func inferScope(it Item) string {
	q := it.ChosenText()
	if q == "" {
		if v, ok := it.Original(); ok {
			q = v.Text
		} else if len(it.Variants) > 0 {
			q = it.Variants[0].Text
		}
	}
	if strings.ContainsAny(q, " \t\n") {
		return ScopeSentence
	}
	return ScopeWord
}

// ---- validation -------------------------------------------------------------

// ErrOriginal is returned when a change would alter or remove an original.
var ErrOriginal = errors.New("the original wording of an alternative cannot be changed or removed")

// Invalid is a problem with the content of an alternative, not its format.
type Invalid struct{ Msg string }

func (e *Invalid) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &Invalid{fmt.Sprintf(format, a...)} }

// CheckVariantText says whether text may be a variant of an alternative of the
// scope: trimmed, not empty, no blank line, no alternatives marker or ghost
// marker, within the size limit; a word's and a sentence's variants are one
// line; a paragraph's is one paragraph.
func CheckVariantText(txt, scope string, lim Limits) error {
	lim = lim.orDefault()
	switch {
	case txt == "":
		return bad("A variant cannot be empty.")
	case len(txt) > lim.MaxVariantSz:
		return bad("A variant is limited to %d bytes.", lim.MaxVariantSz)
	case !utf8.ValidString(txt):
		return bad("A variant must be valid UTF-8.")
	case jsTrim(txt) != txt:
		return bad("A variant cannot start or end with a space or a line break.")
	case strings.Contains(txt, "\r"):
		return bad("A variant cannot hold a carriage return.")
	case strings.Contains(txt, "\x00"):
		return bad("A variant cannot hold a NUL character.")
	case strings.Contains(txt, "\n\n"):
		return bad("A variant cannot hold a blank line: it would split the paragraph.")
	case strings.Contains(txt, "scriobh:alts") || strings.Contains(txt, text.GhostOpen) || strings.Contains(txt, text.GhostClose):
		return bad("A variant cannot hold an alternatives or ghost marker.")
	}
	if scope == ScopeWord && strings.Contains(txt, "\n") {
		return bad("A word variant is one line.")
	}
	if scope == ScopeParagraph {
		bl, ok := text.Blocks(txt)
		if !ok || len(bl) != 1 || bl[0].Start != 0 || bl[0].End != len(txt) || !bl[0].Top {
			return bad("A paragraph variant must be one paragraph of plain txt, without list, quote, heading or code markers.")
		}
	}
	return nil
}

// ValidateItem checks the structure of one item: a scope, a unique original, a
// chosen variant, unique wordings within the limits.
func ValidateItem(it Item, lim Limits) error {
	lim = lim.orDefault()
	switch it.Scope {
	case ScopeWord, ScopeSentence, ScopeParagraph:
	default:
		return bad("Unknown scope %q: use word, sentence or paragraph.", it.Scope)
	}
	if len(it.Variants) == 0 {
		return bad("An alternative needs at least its original wording.")
	}
	if len(it.Variants) > lim.VariantsPer {
		return bad("An alternative holds at most %d variants.", lim.VariantsPer)
	}
	originals := 0
	seenText := map[string]bool{}
	seenID := map[string]bool{}
	for _, v := range it.Variants {
		if v.Original {
			originals++
		}
		if seenText[v.Text] {
			return bad("Two variants say the same thing: %s", Clip(v.Text))
		}
		seenText[v.Text] = true
		if v.ID != "" {
			if seenID[v.ID] {
				return bad("Two variants share an id.")
			}
			seenID[v.ID] = true
		}
		if v.Source != SourceManual && v.Source != SourceGenerated {
			return bad("A variant's source is manual or generated.")
		}
		if err := CheckVariantText(v.Text, it.Scope, lim); err != nil {
			return err
		}
	}
	if originals != 1 {
		return bad("An alternative has exactly one original wording; found %d.", originals)
	}
	return nil
}

// Clip shortens text for a message.
func Clip(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// ---- comparing and merging with what is stored -------------------------------

// Same reports whether two sets of items say the same thing: the same items
// and variants, in the same order. Anchors, contexts and the chosen mark are
// not compared: an editor may hold a stale copy of those, and they are the
// server's to maintain.
func Same(a, b []Item) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]Item, len(a))
	for _, it := range a {
		byID[it.ID] = it
	}
	for _, y := range b {
		x, ok := byID[y.ID]
		if !ok || y.ID == "" || x.Scope != y.Scope {
			return false
		}
		// The chosen wording is the server's to keep (the text decides it, and an
		// edit in place can have added or reworded it since the editor loaded), and
		// the author is not in the comment: neither is compared.
		xs, ys := withoutID(x.Variants, x.Chosen), withoutID(y.Variants, x.Chosen)
		if len(xs) != len(ys) {
			return false
		}
		for i := range xs {
			p, q := xs[i], ys[i]
			if p.ID != q.ID || p.Text != q.Text || p.Original != q.Original || normSource(p.Source) != normSource(q.Source) || p.At != q.At {
				return false
			}
		}
	}
	return true
}

func withoutID(vs []Variant, id string) []Variant {
	out := make([]Variant, 0, len(vs))
	for _, v := range vs {
		if v.ID != id {
			out = append(out, v)
		}
	}
	return out
}

func normSource(s string) string {
	if s == "" {
		return SourceManual
	}
	return s
}

// MergeOptions say who and when for the variants a merge adds.
type MergeOptions struct {
	Actor string
	Now   string
	NewID func(prefix string) string
	Limits
	// KeepMissing makes a comment only add and change: an item or a wording it
	// does not name is kept, not removed. It is for a comment that cannot be shown
	// to have seen what is stored (an older copy that someone then edited).
	KeepMissing bool
}

// Merge applies a comment that differs from the stored items: items are
// matched by id (the stored anchor stays), an unknown or missing id is a new
// item (unplaced), a stored item the comment leaves out is resolved. A stored
// item's scope and original wording cannot change, nor can the chosen variant
// be removed or reworded here (the text of the document says which is chosen).
func Merge(stored, incoming []Item, o MergeOptions) ([]Item, error) {
	byID := make(map[string]Item, len(stored))
	for _, it := range stored {
		byID[it.ID] = it
	}
	seen := map[string]bool{}
	out := make([]Item, 0, len(incoming))
	for _, in := range incoming {
		cur, known := byID[in.ID]
		if in.ID == "" || !known || seen[in.ID] {
			// A new item: it gets ids of its own (an id in a comment could be another
			// document's), and is placed by its text.
			fresh := []Item{in.Clone()}
			if err := Freshen(fresh, o, true); err != nil {
				return nil, err
			}
			seen[fresh[0].ID] = true
			out = append(out, fresh[0])
			continue
		}
		seen[in.ID] = true
		it := cur.Clone()
		if in.Scope != cur.Scope {
			return nil, bad("The scope of an alternative cannot change; delete it and make another.")
		}
		stay := map[string]bool{}
		next := make([]Variant, 0, len(in.Variants))
		for _, v := range in.Variants {
			old, ok := cur.Find(v.ID)
			if v.ID == "" || !ok {
				if v.Original {
					return nil, bad("An alternative already has its original wording.")
				}
				v.ID = o.NewID("alv")
				if v.By == "" {
					v.By = o.Actor
				}
				if v.At == "" || len(v.At) > 40 {
					v.At = o.Now
				}
				if v.Source == "" {
					v.Source = SourceManual
				}
				next = append(next, v)
				continue
			}
			stay[v.ID] = true
			if v.ID == cur.Chosen {
				next = append(next, old) // the text says which wording is chosen and what it says
				continue
			}
			if v.Text != old.Text || v.Original != old.Original {
				if old.Original || v.Original {
					return nil, ErrOriginal
				}
			}
			old.Text = v.Text
			next = append(next, old)
		}
		for j, v := range cur.Variants {
			if stay[v.ID] {
				continue
			}
			switch {
			case v.Original && !o.KeepMissing:
				return nil, ErrOriginal
			case v.ID == cur.Chosen || o.KeepMissing: // a stale comment lacks it: keep it where it was
				at := min(j, len(next))
				next = append(next[:at], append([]Variant{v}, next[at:]...)...)
			}
		}
		it.Variants = next
		if err := ValidateItem(it, o.Limits); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if o.KeepMissing {
		for _, it := range stored {
			if !seen[it.ID] {
				out = append(out, it.Clone())
			}
		}
	}
	if len(out) > o.Limits.orDefault().PerDoc {
		return nil, bad("A document holds at most %d alternatives.", o.Limits.orDefault().PerDoc)
	}
	return out, nil
}

// Freshen gives a new item (one read from a comment) ids and authorship and
// validates it.
func freshen(it *Item, o MergeOptions) error {
	if it.ID == "" {
		it.ID = o.NewID("alt")
	}
	if len(it.Before) > 4*ContextRunes*4 {
		it.Before = ""
	}
	if len(it.After) > 4*ContextRunes*4 {
		it.After = ""
	}
	chosenText := ""
	if v, ok := it.Find(it.Chosen); ok && it.Chosen != "" {
		chosenText = v.Text
	}
	for i := range it.Variants {
		v := &it.Variants[i]
		if v.ID == "" {
			v.ID = o.NewID("alv")
		}
		if v.Source == "" {
			v.Source = SourceManual
		}
		if v.By == "" {
			v.By = o.Actor
		}
		if v.At == "" || len(v.At) > 40 {
			v.At = o.Now
		}
		if v.Original && it.Chosen == "" {
			it.Chosen = v.ID
		}
	}
	_ = chosenText
	if it.Chosen != "" {
		if _, ok := it.Find(it.Chosen); !ok {
			it.Chosen = ""
		}
	}
	if it.Chosen == "" {
		if v, ok := it.Original(); ok {
			it.Chosen = v.ID
		}
	}
	return ValidateItem(*it, o.Limits)
}

// Freshen is for the code that reads a comment into a new document or replaces
// a document's alternatives from one: it assigns ids, fills authorship and
// validates. With fresh, every id the comment gave is replaced by a new one (a
// new document must not reuse the ids of the one the Markdown came from); the
// chosen variant is kept by position.
func Freshen(items []Item, o MergeOptions, fresh bool) error {
	for i := range items {
		it := &items[i]
		it.Start, it.End = -1, -1
		if fresh {
			chosen := -1
			for j, v := range it.Variants {
				if v.ID != "" && v.ID == it.Chosen {
					chosen = j
				}
				it.Variants[j].ID = ""
			}
			it.ID, it.Chosen = "", ""
			if err := freshen(it, o); err != nil {
				return err
			}
			if chosen >= 0 {
				it.Chosen = it.Variants[chosen].ID
			}
			continue
		}
		if err := freshen(it, o); err != nil {
			return err
		}
	}
	if len(items) > o.Limits.orDefault().PerDoc {
		return bad("A document holds at most %d alternatives.", o.Limits.orDefault().PerDoc)
	}
	return nil
}

// Reid gives items and their variants new ids, keeping which variant is chosen
// (a document split in two gives the second half's alternatives their own ids).
func Reid(items []Item, newID func(prefix string) string) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		it = it.Clone()
		chosen := -1
		for j := range it.Variants {
			if it.Variants[j].ID == it.Chosen {
				chosen = j
			}
			it.Variants[j].ID = newID("alv")
		}
		it.ID = newID("alt")
		if chosen >= 0 {
			it.Chosen = it.Variants[chosen].ID
		}
		out[i] = it
	}
	return out
}

// ---- small shared helpers -----------------------------------------------------

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }
