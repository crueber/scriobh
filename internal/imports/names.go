package imports

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Front and back matter are recognised by file or folder name, after the
// number prefix and extension are removed, and only on an exact match. The
// list is deliberately short; anything else is a chapter, and the preview lets
// the writer change a kind.
var (
	frontMatterNames = map[string]bool{
		"front-matter": true, "frontmatter": true, "title-page": true, "copyright": true,
		"dedication": true, "epigraph": true, "foreword": true, "preface": true,
	}
	backMatterNames = map[string]bool{
		"back-matter": true, "backmatter": true, "acknowledgements": true, "acknowledgments": true,
		"appendix": true, "about-the-author": true, "afterword": true,
	}
)

var reNumbered = regexp.MustCompile(`^(\d{1,6})(?:[-_. ]+(.*))?$`)

// splitName separates a leading number from the rest of a name that has had
// its extension removed: "03-the-salt-road" is (3, true, "the-salt-road").
func splitName(name string) (num int, has bool, rest string) {
	m := reNumbered.FindStringSubmatch(name)
	if m == nil {
		return 0, false, name
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false, name
	}
	return n, true, m[2]
}

// stem removes the file extension.
func stem(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		l := strings.ToLower(name[i:])
		if l == ".md" || l == ".markdown" {
			return name[:i]
		}
	}
	return name
}

// classify returns the kind a name implies: front_matter, back_matter, or "".
func classify(rest string) string {
	k := strings.ToLower(strings.Join(strings.FieldsFunc(rest, func(r rune) bool { return r == '_' || r == ' ' || r == '-' }), "-"))
	switch {
	case frontMatterNames[k]:
		return "front_matter"
	case backMatterNames[k]:
		return "back_matter"
	}
	return ""
}

var smallWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "but": true, "by": true, "for": true, "in": true,
	"nor": true, "of": true, "on": true, "or": true, "the": true, "to": true,
}

// prettify turns a file or folder name into a title: separators become
// spaces, and a name that is all lower case gets capitals (the-salt-road
// becomes The Salt Road). A name that already has capitals is left as written.
func prettify(rest string) string {
	words := strings.FieldsFunc(rest, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	if len(words) == 0 {
		return ""
	}
	lower := true
	for _, r := range rest {
		if unicode.IsUpper(r) {
			lower = false
			break
		}
	}
	if lower {
		for i, w := range words {
			if i > 0 && i < len(words)-1 && smallWords[w] {
				continue
			}
			r, n := utf8.DecodeRuneInString(w)
			words[i] = string(unicode.ToUpper(r)) + w[n:]
		}
	}
	return strings.Join(words, " ")
}

// rank orders siblings: unnumbered front matter, then numbered entries by
// number, then other unnumbered entries by name, then unnumbered back matter.
type sortKey struct {
	rank int
	num  int
	name string
}

func (a sortKey) less(b sortKey) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.num != b.num {
		return a.num < b.num
	}
	return naturalLess(a.name, b.name)
}

func keyFor(num int, has bool, class, name string) sortKey {
	k := sortKey{name: strings.ToLower(name)}
	switch {
	case has:
		k.rank, k.num = 1, num
	case class == "front_matter":
		k.rank = 0
	case class == "back_matter":
		k.rank = 3
	default:
		k.rank = 2
	}
	return k
}

// naturalLess compares names the way a person counts: ch2 before ch10.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na, _ := strconv.Atoi(a[:min(da, 9)])
			nb, _ := strconv.Atoi(b[:min(db, 9)])
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return ra < rb
		}
		a, b = a[sa:], b[sb:]
	}
	return len(a) < len(b)
}

func digitRun(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

// ---- titles from the text -------------------------------------------------------------

var (
	reH1    = regexp.MustCompile(`^#[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	reH2    = regexp.MustCompile(`^##[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	reTitle = regexp.MustCompile(`(?i)^title[ \t]*:[ \t]*(.*?)[ \t]*$`)
	reFence = regexp.MustCompile("^ {0,3}(```|~~~)")

	reFrontKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_ .-]*:(\s|$)`)
)

// frontMatterEnd returns the byte offset just past a leading `---` block, or 0.
// It follows the rule the editor and the word counter use: the first line
// inside must look like YAML, or the opening `---` is a scene break.
func frontMatterEnd(s string) int {
	if !strings.HasPrefix(s, "---") {
		return 0
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) < 3 || strings.TrimRight(lines[0], " \t\n") != "---" {
		return 0
	}
	first := strings.TrimRight(lines[1], "\n")
	if !(strings.HasPrefix(first, "#") || strings.HasPrefix(first, "- ") || reFrontKey.MatchString(first)) {
		return 0
	}
	off := len(lines[0]) + len(lines[1])
	for _, l := range lines[2:] {
		off += len(l)
		t := strings.TrimRight(l, " \t\n")
		if t == "---" || t == "..." {
			return off
		}
	}
	return 0
}

// frontMatterTitle is the title: value of the front matter, or "".
func frontMatterTitle(fm string) string {
	for _, l := range strings.Split(fm, "\n") {
		if m := reTitle.FindStringSubmatch(l); m != nil {
			v := strings.TrimSpace(m[1])
			if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
			return v
		}
	}
	return ""
}

// leadingH1 finds a first-level heading that opens the text (after front
// matter and blank lines). It returns the heading text and the offsets of the
// heading line and of the text that follows it (blank lines skipped).
func leadingH1(s string, from int) (title string, start, end int, ok bool) {
	i := from
	for i < len(s) {
		j := strings.IndexByte(s[i:], '\n')
		line, next := s[i:], len(s)
		if j >= 0 {
			line, next = s[i:i+j], i+j+1
		}
		if strings.TrimSpace(line) == "" {
			i = next
			continue
		}
		m := reH1.FindStringSubmatch(line)
		if m == nil {
			return "", 0, 0, false
		}
		end = next
		for end < len(s) {
			k := strings.IndexByte(s[end:], '\n')
			l := s[end:]
			if k >= 0 {
				l = s[end : end+k]
			}
			if strings.TrimSpace(l) != "" {
				break
			}
			if k < 0 {
				end = len(s)
				break
			}
			end += k + 1
		}
		return m[1], i, end, true
	}
	return "", 0, 0, false
}

// h2 is a second-level heading outside code fences.
type h2 struct {
	title      string
	start, end int // the heading line, newline included
}

// secondLevelHeadings lists the H2 lines of s, skipping front matter and
// fenced code. It stops after limit headings: a file of nothing but headings
// must not become a million of them.
func secondLevelHeadings(s string, limit int) []h2 {
	var out []h2
	fence := ""
	i := frontMatterEnd(s)
	for i < len(s) {
		j := strings.IndexByte(s[i:], '\n')
		line, next := s[i:], len(s)
		if j >= 0 {
			line, next = s[i:i+j], i+j+1
		}
		if m := reFence.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case fence == m[1]:
				fence = ""
			}
		} else if fence == "" {
			if m := reH2.FindStringSubmatch(line); m != nil {
				out = append(out, h2{title: m[1], start: i, end: next})
				if len(out) >= limit {
					return out
				}
			}
		}
		i = next
	}
	return out
}

// cleanTitle makes a document title one line of at most 300 characters. It
// reports whether it had to cut.
func cleanTitle(s string) (string, bool) { return cleanLine(s, 300) }

// cleanLine makes s one line of at most max characters, reporting a cut.
func cleanLine(s string, max int) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max])), true
}
