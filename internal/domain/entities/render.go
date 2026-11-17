package entities

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// One renderer writes an entity as a Markdown file with front matter. The
// mirror's compendium/ projection and the Compendium export both use it, so the
// same entity is the same bytes in both. Nothing in a file depends on when it
// was written: no timestamps, sorted keys, every string JSON-quoted (which is
// valid YAML), so a rewrite of unchanged data changes no byte.

// FileEntity is what one entity file is made from.
type FileEntity struct {
	ID       string
	Kind     string
	Name     string
	SortKey  string
	Summary  string
	Body     string
	Aliases  []string
	Fields   map[string]string
	Color    string
	Relation []string // "mother of: Sorcha", already worded and sorted by the loader

	// Projection only.
	Compendium string // the Compendium it comes from
	Reference  bool   // from a Compendium the project only references, or a single link
	Overridden bool
	BookNotes  string
}

// File is one rendered file and where it goes.
type File struct {
	Path     string
	Content  []byte
	SHA256   string
	EntityID string
	Kind     string
	Name     string
}

var kindDirs = map[string]string{"character": "characters", "place": "places", "item": "items", "org": "organisations", "concept": "concepts"}

// KindDir is the folder an entity kind is written under.
func KindDir(kind string) string {
	if d, ok := kindDirs[kind]; ok {
		return d
	}
	return "other"
}

func q(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}

// Markdown renders one entity.
func (e FileEntity) Markdown() []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	b.WriteString("id: " + q(e.ID) + "\n")
	b.WriteString("name: " + q(e.Name) + "\n")
	b.WriteString("kind: " + e.Kind + "\n")
	if e.Compendium != "" {
		b.WriteString("compendium: " + q(e.Compendium) + "\n")
		if e.Reference {
			b.WriteString("reference: true\n")
		}
	}
	if len(e.Aliases) > 0 {
		b.WriteString("aliases:\n")
		for _, a := range e.Aliases {
			b.WriteString("  - " + q(a) + "\n")
		}
	}
	if e.Summary != "" {
		b.WriteString("summary: " + q(e.Summary) + "\n")
	}
	if e.Color != "" {
		b.WriteString("color: " + q(strings.ToLower(e.Color)) + "\n")
	}
	if len(e.Fields) > 0 {
		b.WriteString("fields:\n")
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("  " + k + ": " + q(e.Fields[k]) + "\n")
		}
	}
	if len(e.Relation) > 0 {
		b.WriteString("relations:\n")
		for _, r := range e.Relation {
			b.WriteString("  - " + q(r) + "\n")
		}
	}
	if e.Overridden {
		b.WriteString("overridden: true\n")
	}
	if e.BookNotes != "" {
		b.WriteString("book_notes: " + q(e.BookNotes) + "\n")
	}
	b.WriteString("---\n")
	body := strings.TrimRight(strings.ReplaceAll(e.Body, "\r\n", "\n"), "\n")
	if body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return b.Bytes()
}

// Layout renders entities into files under prefix (which ends in a slash or is
// empty): prefix + kind folder + a slug of the name + "--" + the last six
// characters of the id. Files are in path order. If two paths still collide
// (six characters of a random id are not a guarantee) the later entity gets a
// longer suffix, so no entity ever overwrites another; the order that decides
// is the entities' (kind, sort key, id), which does not depend on anything else.
func Layout(items []FileEntity, prefix string) []File {
	sorted := append([]FileEntity(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.SortKey != b.SortKey {
			return a.SortKey < b.SortKey
		}
		return a.ID < b.ID
	})
	used := map[string]bool{}
	out := make([]File, 0, len(sorted))
	for _, e := range sorted {
		base := prefix + KindDir(e.Kind) + "/" + FileSlug(e.Name) + "--"
		id := cleanID(e.ID)
		path := ""
		for _, n := range []int{6, 12, len(id)} {
			if n > len(id) {
				n = len(id)
			}
			path = base + id[len(id)-n:] + ".md"
			if !used[path] {
				break
			}
		}
		used[path] = true
		data := e.Markdown()
		sum := sha256.Sum256(data)
		out = append(out, File{Path: path, Content: data, SHA256: hex.EncodeToString(sum[:]), EntityID: e.ID, Kind: e.Kind, Name: e.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func cleanID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

var slugFold = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ā': "a", 'ą': "a", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ę': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ī': "i", 'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o", 'ō': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ý': "y", 'ÿ': "y", 'ñ': "n", 'ń': "n", 'ç': "c", 'ć': "c", 'č': "c",
	'š': "s", 'ś': "s", 'ž': "z", 'ź': "z", 'ż': "z", 'ł': "l", 'ř': "r", 'ğ': "g", 'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d",
}

// FileSlug makes a file-name-safe word of a name: diacritics folded, lower
// case, everything else a single hyphen, at most 48 characters; "entity" when
// nothing is left.
func FileSlug(name string) string {
	var b strings.Builder
	dash := true
	emit := func(s string) {
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				dash = false
			} else if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	for _, r := range name {
		r = unicode.ToLower(r)
		switch {
		case slugFold[r] != "":
			emit(slugFold[r])
		case unicode.IsMark(r):
		default:
			emit(string(r))
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		return "entity"
	}
	return s
}
