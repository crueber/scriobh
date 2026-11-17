// Package redact replaces names with stable placeholders before text is sent to
// a provider and puts the names back in what comes back.
//
// A Map is built from terms (the names and aliases of the project's entities,
// grouped by entity) and, optionally, from the capitalised names found in the
// texts themselves. Placeholders look like {{CHAR_1}}. The numbering is a
// function of the terms alone (sorted by group), never of the text or of the
// order texts are applied in, so the same project gives the same placeholders
// run after run and the preview matches the send.
//
// Within one group the first term is the primary name and gets {{CHAR_1}}; its
// other surface forms (aliases) get {{CHAR_1b}}, {{CHAR_1c}} and so on, so a
// restored answer says exactly the form the text used. Matching is whole-word
// and case-insensitive; a restored name is the spelling the bible holds, so a
// name the text wrote in lower case comes back capitalised.
//
// What it is not: a guarantee. It finds the names the bible knows and, in
// capitalised mode, capitalised words in mid-sentence; a name the bible lacks
// and that is only ever written at the start of a sentence, or a nickname, is
// not found. The preview lists every placeholder so the person can see what was
// and was not replaced. A manuscript that itself contains text shaped like a
// placeholder is the one case restoration can get wrong.
package redact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Term is one name to hide.
type Term struct {
	Surface string
	// Group ties the surface forms of one entity together; the first term of a
	// group is its primary name.
	Group string
	// Kind picks the placeholder's prefix: character, place, item, org,
	// concept; anything else is NAME.
	Kind string
}

// Entry is one row of the mapping, for the preview.
type Entry struct {
	Placeholder string `json:"placeholder"`
	Original    string `json:"original"`
	Kind        string `json:"kind"`
}

// Options select the modes.
type Options struct {
	// Capitalised also hides capitalised words found mid-sentence in the texts
	// given to Learn.
	Capitalised bool
}

type surface struct {
	runes       []rune
	placeholder string
	original    string
	kind        string
}

// Map is a built mapping. It is safe for concurrent Apply and Restore after
// Learn has finished.
type Map struct {
	opt     Options
	groups  map[string][]Term
	kinds   map[string]string
	surf    []surface
	byFirst map[rune][]int // first rune, lower-cased, to indexes of surf, longest first
	back    map[string]string
	learned bool

	mu      sync.Mutex
	used    map[string]bool // placeholders Apply actually put into a text
	literal map[string]bool // placeholders that stood, literally, in a text given to Apply
}

var prefixes = map[string]string{"character": "CHAR", "place": "PLACE", "item": "ITEM", "org": "ORG", "concept": "CONCEPT"}

// New builds a Map from terms. Empty and one-character surfaces are ignored.
func New(terms []Term, o Options) *Map {
	m := &Map{opt: o, groups: map[string][]Term{}, kinds: map[string]string{}, used: map[string]bool{}, literal: map[string]bool{}}
	order := []string{}
	for _, t := range terms {
		t.Surface = strings.TrimSpace(t.Surface)
		if len([]rune(t.Surface)) < 2 {
			continue
		}
		if _, ok := m.groups[t.Group]; !ok {
			order = append(order, t.Group)
			m.kinds[t.Group] = t.Kind
		}
		m.groups[t.Group] = append(m.groups[t.Group], t)
	}
	sort.Strings(order)
	m.assign(order)
	return m
}

func variant(i int) string {
	switch {
	case i == 0:
		return ""
	case i < 26:
		return string(rune('a' + i))
	}
	return fmt.Sprintf("_%d", i)
}

// slugVariant is the placeholder variant of a name written as a URL slug
// ("maire-nolan"): restoring it gives the slug back, not the name with a space.
const slugVariant = "_u"

func (m *Map) addSurface(placeholder, original string, runes []rune, kind string) {
	m.surf = append(m.surf, surface{runes: runes, placeholder: placeholder, original: original, kind: kind})
	m.back[placeholder] = original
}

func (m *Map) assign(order []string) {
	m.surf = nil
	m.back = map[string]string{}
	count := map[string]int{}
	seen := map[string]bool{}
	for _, g := range order {
		pre, ok := prefixes[m.kinds[g]]
		if !ok {
			pre = "NAME"
		}
		count[pre]++
		vi := 0
		for ti, t := range m.groups[g] {
			cr := canonical(t.Surface)
			key := string(cr)
			if len(cr) < 2 || seen[key] { // the same words under two entities: the first group keeps them
				continue
			}
			seen[key] = true
			m.addSurface(fmt.Sprintf("{{%s_%d%s}}", pre, count[pre], variant(vi)), t.Surface, cr, m.kinds[g])
			vi++
			if ti != 0 {
				continue
			}
			// The primary name also as a URL slug: a link or a file name carries it too.
			if slug := slugOf(t.Surface); slug != "" && !seen[slug] && strings.Contains(slug, "-") {
				seen[slug] = true
				m.addSurface(fmt.Sprintf("{{%s_%d%s}}", pre, count[pre], slugVariant), slug, canonical(slug), m.kinds[g])
			}
		}
	}
	m.index()
}

func (m *Map) index() {
	m.byFirst = map[rune][]int{}
	for i, s := range m.surf {
		m.byFirst[s.runes[0]] = append(m.byFirst[s.runes[0]], i)
	}
	for _, l := range m.byFirst {
		sort.SliceStable(l, func(a, b int) bool { return len(m.surf[l[a]].runes) > len(m.surf[l[b]].runes) })
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// Learn finds capitalised names in the texts and adds them, when the map was
// built with Options.Capitalised. Call it once with every text that will be
// applied, before the first Apply; the result does not depend on the order of
// the texts.
func (m *Map) Learn(texts ...string) {
	if !m.opt.Capitalised || m.learned {
		return
	}
	m.learned = true
	found := map[string]string{}
	for _, t := range texts {
		for _, p := range capitalisedPhrases(t) {
			found[strings.ToLower(p)] = p
		}
	}
	known := map[string]bool{}
	for _, s := range m.surf {
		known[string(s.runes)] = true
	}
	var names []string
	for _, v := range found {
		if k := string(canonical(v)); !known[k] {
			names = append(names, v)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := strings.ToLower(names[i]), strings.ToLower(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	n := 0
	for _, s := range m.surf { // numbering of NAME placeholders continues after entity ones
		if strings.HasPrefix(s.placeholder, "{{NAME_") {
			n++
		}
	}
	for _, nm := range names {
		n++
		m.addSurface(fmt.Sprintf("{{NAME_%d}}", n), nm, canonical(nm), "")
	}
	m.index()
}

// capitalisedPhrases returns runs of capitalised words that do not start a
// sentence ("Máire Nolan" in "Then Máire Nolan left").
func capitalisedPhrases(s string) []string {
	rs := []rune(s)
	var out []string
	startOfSentence := true
	i := 0
	for i < len(rs) {
		r := rs[i]
		if !isWordRune(r) {
			switch r {
			case '.', '!', '?', '\n', '…', ':', '#', '>', '*':
				startOfSentence = true
			case '"', '\'', '(', '[', '“', '‘', '«', '-', '_', ' ', '\t', '—', '–':
				// opening punctuation and spaces do not end the sentence start state
			default:
				startOfSentence = false
			}
			i++
			continue
		}
		j := i
		for j < len(rs) && (isWordRune(rs[j])) {
			j++
		}
		w := rs[i:j]
		if unicode.IsUpper(w[0]) && len(w) >= 2 && !allUpper(w) && !startOfSentence {
			// extend over following capitalised words separated by single spaces
			end := j
			for k := j; k+1 < len(rs) && rs[k] == ' ' && unicode.IsUpper(rs[k+1]); {
				e := k + 1
				for e < len(rs) && isWordRune(rs[e]) {
					e++
				}
				if e-(k+1) < 2 {
					break
				}
				end, k = e, e
			}
			out = append(out, string(rs[i:end]))
			i = end
			startOfSentence = false
			continue
		}
		startOfSentence = false
		i = j
	}
	return out
}

func allUpper(w []rune) bool {
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// Apply replaces every name in s with its placeholder, and remembers which
// placeholders it used: only those are ever restored (see Restore). Names are
// found on the canonical view of the text (see match.go), whatever way they
// are typed; the text around a name is returned as it was.
func (m *Map) Apply(s string) string {
	if s == "" {
		return s
	}
	// A placeholder-shaped token that is already in the text is the person's own
	// text, not ours: it is never restored, so it cannot be told apart from, or
	// made to collide with, one this map hands out.
	var lit []string
	for _, sub := range placeholderRe.FindAllStringSubmatch(s, -1) {
		lit = append(lit, "{{"+sub[1]+"}}")
	}
	if len(lit) > 0 {
		m.mu.Lock()
		for _, l := range lit {
			m.literal[l] = true
		}
		m.mu.Unlock()
	}
	if len(m.surf) == 0 {
		return s
	}
	rs := []rune(s)
	v := buildView(rs)
	var hit []string
	var out strings.Builder
	out.Grow(len(s))
	oi := 0 // the next original rune not yet written
	for ci := 0; ci < len(v.canon); {
		if isWordRune(v.canon[ci]) && (ci == 0 || !isWordRune(v.canon[ci-1])) {
			matched := false
			for _, k := range m.byFirst[v.canon[ci]] {
				sf := m.surf[k]
				n := len(sf.runes)
				last := ci + n - 1
				if last >= len(v.canon) || (last+1 < len(v.canon) && (isWordRune(v.canon[last+1]) || v.start[last+1] == v.start[last])) {
					continue
				}
				ok := true
				for q := 0; q < n; q++ {
					if v.canon[ci+q] != sf.runes[q] {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
				out.WriteString(string(rs[oi:v.start[ci]]))
				out.WriteString(sf.placeholder)
				hit = append(hit, sf.placeholder)
				oi = v.end[last]
				ci = last + 1
				matched = true
				break
			}
			if matched {
				continue
			}
		}
		ci++
	}
	out.WriteString(string(rs[oi:]))
	if len(hit) > 0 {
		m.mu.Lock()
		for _, h := range hit {
			m.used[h] = true
		}
		m.mu.Unlock()
	}
	return out.String()
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Z]+_\d+(?:[a-z]|_u|_\d+)?)\s*\}\}`)

// Restore puts the names back, but only for placeholders that Apply put into a
// text: the model can say any placeholder, and one that stood for nothing in
// this request must not turn into a name (it could be the name of an entity
// the person has no access to). Any other placeholder, a made-up one or one
// that was never sent, is left as it is and counted.
func (m *Map) Restore(s string) (string, int) {
	unknown := 0
	out := placeholderRe.ReplaceAllStringFunc(s, func(h string) string {
		sub := placeholderRe.FindStringSubmatch(h)
		key := "{{" + sub[1] + "}}"
		m.mu.Lock()
		ok := m.used[key] && !m.literal[key]
		m.mu.Unlock()
		if o, found := m.back[key]; found && ok {
			return o
		}
		unknown++
		return h
	})
	return out, unknown
}

// Entries lists the mapping in placeholder order, for the preview.
func (m *Map) Entries() []Entry {
	out := make([]Entry, 0, len(m.surf))
	for _, s := range m.surf {
		out = append(out, Entry{Placeholder: s.placeholder, Original: s.original, Kind: s.kind})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Placeholder < out[j].Placeholder })
	return out
}

// UsedEntries lists the mapping rows whose placeholder Apply actually put into a
// text (and that the writer's own text did not also contain): what a preview may
// show. The other rows name entities this request does not mention.
func (m *Map) UsedEntries() []Entry {
	var out []Entry
	for _, e := range m.Entries() {
		m.mu.Lock()
		ok := m.used[e.Placeholder] && !m.literal[e.Placeholder]
		m.mu.Unlock()
		if ok {
			out = append(out, e)
		}
	}
	return out
}

// Len is how many placeholders the map holds.
func (m *Map) Len() int { return len(m.surf) }

// StreamRestorer restores placeholders in a stream of chunks. A placeholder can
// be split across chunks, so text from the last "{{" that has not closed yet is
// held back until the rest arrives (at most 48 characters; beyond that it is
// not a placeholder and is let through).
type StreamRestorer struct {
	m       *Map
	pending string
	Unknown int
}

// NewStreamRestorer starts a restorer over m.
func (m *Map) NewStreamRestorer() *StreamRestorer { return &StreamRestorer{m: m} }

// Write takes the next chunk and returns the text that is safe to send on.
func (r *StreamRestorer) Write(chunk string) string {
	s := r.pending + chunk
	cut := len(s)
	if i := strings.LastIndex(s, "{{"); i >= 0 && !strings.Contains(s[i:], "}}") && len(s)-i <= 48 {
		cut = i
	} else if strings.HasSuffix(s, "{") {
		cut = len(s) - 1 // a lone brace might be the start of "{{"
	}
	r.pending = s[cut:]
	out, n := r.m.Restore(s[:cut])
	r.Unknown += n
	return out
}

// Flush returns whatever is still held back.
func (r *StreamRestorer) Flush() string {
	out, n := r.m.Restore(r.pending)
	r.Unknown += n
	r.pending = ""
	return out
}
