// Package i18n is the message catalogue of the interface: UI strings that live in Go templates and in the
// small scripts of the newest screens, looked up by a key, in the person's language, falling back to English.
//
// English is the source: internal/i18n/locales/en.json holds every key with its English text. A translation is
// another file in the same directory (a flat JSON object of the same keys; a missing key falls back to English
// one string at a time, so a half-finished translation is usable). Nothing is fetched and nothing is generated at
// runtime beyond the pseudo-locales below; there is no service and no runtime npm. A message may hold {name}
// placeholders, filled from the pairs the caller passes.
//
// Two pseudo-locales test the whole path without translating anything: "xx" accents every letter and pads and
// brackets the string (so a string that is not in the catalogue, or is cut off, or does not fit its box, shows
// at once), and "xx-rtl" does that and sets the interface right to left (so mirrored layout is checked with
// logical CSS properties and no real translation). Both are offered in Settings only in a development build.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

// Locale is a language the interface can be shown in.
type Locale struct {
	Tag  string // BCP 47, as the lang attribute takes it
	Name string // in its own language
	Dir  string // "ltr" or "rtl"
	Test bool   // a pseudo-locale, for developers
}

// Catalog is every loaded locale.
type Catalog struct {
	msgs    map[string]map[string]string
	locales []Locale
}

// meta describes a locale file's own entry: the reserved keys "@name" and "@dir".
const (
	keyName = "@name"
	keyDir  = "@dir"
)

// Default is the shared catalogue, loaded once from the embedded files.
var Default = mustLoad()

func mustLoad() *Catalog {
	c, err := Load()
	if err != nil {
		panic(err)
	}
	return c
}

// Load reads the embedded locales.
func Load() (*Catalog, error) {
	c := &Catalog{msgs: map[string]map[string]string{}}
	ents, err := files.ReadDir("locales")
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		tag := strings.TrimSuffix(e.Name(), ".json")
		raw, err := files.ReadFile("locales/" + e.Name())
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("locale %s: %w", tag, err)
		}
		l := Locale{Tag: tag, Name: m[keyName], Dir: m[keyDir]}
		if l.Name == "" {
			l.Name = tag
		}
		if l.Dir != "rtl" {
			l.Dir = "ltr"
		}
		delete(m, keyName)
		delete(m, keyDir)
		c.msgs[tag] = m
		c.locales = append(c.locales, l)
	}
	if c.msgs["en"] == nil {
		return nil, fmt.Errorf("the English catalogue is missing")
	}
	// the pseudo-locales have no file: they are made from English
	c.locales = append(c.locales, Locale{Tag: "xx", Name: "Pseudo (test)", Dir: "ltr", Test: true}, Locale{Tag: "xx-rtl", Name: "Pseudo, right to left (test)", Dir: "rtl", Test: true})
	sort.Slice(c.locales, func(i, j int) bool { return c.locales[i].Tag < c.locales[j].Tag })
	return c, nil
}

// Locales lists the languages, English first among the real ones.
func (c *Catalog) Locales() []Locale {
	out := make([]Locale, 0, len(c.locales))
	for _, l := range c.locales {
		if l.Tag == "en" {
			out = append([]Locale{l}, out...)
		} else {
			out = append(out, l)
		}
	}
	return out
}

// Known reports whether tag is a language this catalogue can show (a regional tag counts when its language does).
func (c *Catalog) Known(tag string) bool {
	_, ok := c.Resolve(tag)
	return ok
}

// Resolve is the locale a tag asks for: the tag itself, else its language ("pt-BR" asks for "pt"), else not found.
func (c *Catalog) Resolve(tag string) (Locale, bool) {
	tag = strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	for t := tag; t != ""; {
		for _, l := range c.locales {
			if strings.EqualFold(l.Tag, t) {
				return l, true
			}
		}
		i := strings.LastIndex(t, "-")
		if i < 0 {
			break
		}
		t = t[:i]
	}
	return Locale{}, false
}

// Dir is the direction of a language's interface: "rtl" or "ltr".
func (c *Catalog) Dir(tag string) string {
	if l, ok := c.Resolve(tag); ok {
		return l.Dir
	}
	return "ltr"
}

// Has reports whether English has the key.
func (c *Catalog) Has(key string) bool { _, ok := c.msgs["en"][key]; return ok }

// Keys lists every key of the English catalogue, sorted.
func (c *Catalog) Keys() []string {
	out := make([]string, 0, len(c.msgs["en"]))
	for k := range c.msgs["en"] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Raw is the text of a key in a language, before placeholders: the language's own, else English, else the key
// itself (a key nobody wrote is visible, never empty).
func (c *Catalog) Raw(lang, key string) string {
	if lang == "xx" || lang == "xx-rtl" {
		return pseudo(c.msgs["en"][key], key, lang == "xx-rtl")
	}
	if l, ok := c.Resolve(lang); ok {
		if s, ok := c.msgs[l.Tag][key]; ok && s != "" {
			return s
		}
	}
	if s, ok := c.msgs["en"][key]; ok {
		return s
	}
	return key
}

// T is the message for key in lang, with {name} placeholders replaced by the (name, value) pairs in args.
func (c *Catalog) T(lang, key string, args ...any) string {
	s := c.Raw(lang, key)
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return s
}

// N is the message for a count: key.one for 1, key.other for anything else, with {n} the count. A language with more forms
// than that gives key.few, key.many and so on in its own file and N chooses by its own rule (English and the pseudo-locales
// have two).
func (c *Catalog) N(lang, key string, n int, args ...any) string {
	form := "other"
	if n == 1 {
		form = "one"
	}
	return c.T(lang, key+"."+form, append([]any{"n", n}, args...)...)
}

// With returns the messages whose keys start with prefix (the prefix cut off), in lang, with English filling any
// gap: the data a script reads instead of carrying its own strings.
func (c *Catalog) With(lang, prefix string) map[string]string {
	out := map[string]string{}
	for k := range c.msgs["en"] {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = c.Raw(lang, k)
		}
	}
	return out
}

// pseudo makes the test form of an English string: letters accented, the string padded by about a third and
// bracketed, placeholders left as they were. A key that English does not have is shown as itself, bracketed.
func pseudo(en, key string, rtl bool) string {
	if en == "" {
		en = key
	}
	const from, to = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", "áƀçðéƒĝĥíĵķĺḿńóṕqŕśťúṽŵẋýžÁḂÇĐÉḞĜĤÍĴĶĹṀŃÓṔQŔŚŤÚṼŴẊÝŽ"
	rf, rt := []rune(from), []rune(to)
	var b strings.Builder
	in := false
	n := 0
	for _, r := range en {
		switch {
		case r == '{':
			in = true
			b.WriteRune(r)
		case r == '}':
			in = false
			b.WriteRune(r)
		case in:
			b.WriteRune(r)
		default:
			done := false
			for i, f := range rf {
				if f == r {
					b.WriteRune(rt[i])
					done = true
					break
				}
			}
			if !done {
				b.WriteRune(r)
			}
			n++
		}
	}
	pad := strings.Repeat("·", n/3)
	s := "⟦" + b.String() + pad + "⟧"
	if rtl {
		s = "‏" + s + "‏"
	}
	return s
}
