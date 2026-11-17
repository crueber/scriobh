package text

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reFrontOpen  = regexp.MustCompile(`^---[ \t]*$`)
	reFrontClose = regexp.MustCompile(`^(?:---|\.\.\.)[ \t]*$`)
	reFrontKey   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_ .-]*:`)
)

// yamlish reports whether the first line inside a front matter block looks
// like YAML (key: value, a comment, a list item). Otherwise a leading `---` is
// a scene break that happens to open the document.
func yamlish(l string) bool {
	if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "- ") {
		return true
	}
	loc := reFrontKey.FindStringIndex(l)
	if loc == nil {
		return false
	}
	rest := l[loc[1]:]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return isJSSpace(r)
}

// frontMatterEnd returns the byte offset just past a leading `---` ... `---`
// (or `...`) block, or 0 when the document does not open with one. The rules
// are the editor's (frontMatterRule in md-plugins.js).
func frontMatterEnd(s string) int {
	lines := strings.SplitAfter(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // a final newline does not start another line
	}
	if len(lines) < 3 {
		return 0
	}
	line := func(i int) string { return strings.TrimSuffix(lines[i], "\n") }
	if !reFrontOpen.MatchString(line(0)) || !yamlish(line(1)) {
		return 0
	}
	off := len(lines[0]) + len(lines[1])
	for i := 2; i < len(lines); i++ {
		off += len(lines[i])
		if reFrontClose.MatchString(line(i)) {
			return off
		}
	}
	return 0
}
