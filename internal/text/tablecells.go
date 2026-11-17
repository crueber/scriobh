package text

import (
	"bytes"
	"regexp"
	"strings"
)

// Table rows, for what the paragraph-by-paragraph rules do not see.
//
// The ghost and private-note rules work on text blocks (paragraphs and headings); the reading view drops the cells of
// a row beyond the header's columns, and a cell is not a block. So a ghost, an HTML comment or a %%private note%% in
// a table cell could survive into what a reader (a share link, a public repository, an edition) is handed. These
// functions sweep what is left, row by row, after the structural pass: only lines that are table rows (they start
// with a pipe) and not inside a fenced code block, and only when a marker is there at all, so a text without markers
// comes back byte for byte. What they cannot pair (an opener with no closer in the row) is cut to the end of the cell:
// failing closed.

var (
	// A ghost, a comment or a note may open in one cell and close in another (a selection across cells in
	// hand-written Markdown): a pair is removed whole, pipes and all, which fails closed (the row loses a cell).
	reRowGhost   = regexp.MustCompile(`<scriobh-ghost>.*?</scriobh-ghost>`)
	reRowComment = regexp.MustCompile(`<!--[^\n]*?-->`)
	reRowPrivate = regexp.MustCompile(`%%[^\n]*?%%`)
	// A delimiter row of a table: cells of dashes (with optional colons), at least one pipe.
	reDelimRow = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)
)

// unquote strips the block-quote markers and the white space before a line's content.
func unquote(l string) string {
	t := strings.TrimSpace(l)
	for strings.HasPrefix(t, ">") {
		t = strings.TrimSpace(t[1:])
	}
	return t
}

// tableLines says which lines are rows of a table: a line that starts with a pipe, and every line of a block that
// has a delimiter row (so a table with no leading pipes, and one inside a block quote, are rows too), outside
// fenced code.
func tableLines(lines []string) []bool {
	row := make([]bool, len(lines))
	fence := ""
	for i, l := range lines {
		t := unquote(l)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			continue
		}
		if strings.HasPrefix(t, "|") {
			row[i] = true
		}
		if strings.Contains(t, "|") && strings.Contains(t, "-") && reDelimRow.MatchString(t) && i > 0 && strings.Contains(lines[i-1], "|") && unquote(lines[i-1]) != "" {
			row[i-1], row[i] = true, true
			for j := i + 1; j < len(lines) && unquote(lines[j]) != ""; j++ {
				row[j] = true
			}
		}
	}
	return row
}

// rows applies f to every table row outside a fenced code block.
func rows(md string, f func(line string) string) string {
	lines := strings.Split(md, "\n")
	is := tableLines(lines)
	for i, l := range lines {
		if is[i] {
			lines[i] = f(l)
		}
	}
	return strings.Join(lines, "\n")
}

// cutToCell removes from the first occurrence of open to the next pipe or the end of the line.
func cutToCell(l, open string) string {
	for {
		i := strings.Index(l, open)
		if i < 0 {
			return l
		}
		j := strings.IndexByte(l[i:], '|')
		if j < 0 {
			return l[:i]
		}
		l = l[:i] + l[i+j:]
	}
}

// stripGhostsInRows removes ghosted text in table cells (omit) or only the markers (keep).
func stripGhostsInRows(md string, omit bool) string {
	if !strings.Contains(md, GhostOpen) && !strings.Contains(md, GhostClose) {
		return md
	}
	return rows(md, func(l string) string {
		if omit {
			l = reRowGhost.ReplaceAllString(l, "")
			l = cutToCell(l, GhostOpen)
			return strings.ReplaceAll(l, GhostClose, "")
		}
		l = reRowGhost.ReplaceAllStringFunc(l, func(m string) string { return m[len(GhostOpen) : len(m)-len(GhostClose)] })
		return strings.ReplaceAll(strings.ReplaceAll(l, GhostOpen, ""), GhostClose, "")
	})
}

// cutHiddenInRows removes HTML comments and %%private notes%% from table rows.
func cutHiddenInRows(md string) string {
	if !strings.Contains(md, "<!--") && !strings.Contains(md, "%%") {
		return md
	}
	return rows(md, func(l string) string {
		l = reRowComment.ReplaceAllString(l, "")
		l = cutToCell(l, "<!--")
		l = reRowPrivate.ReplaceAllString(l, "")
		return cutToCell(l, "%%")
	})
}

// dropSpanningNotes removes a %%private note%% that opens in one cell of a table row and closes in a later one. A cell
// is parsed on its own, so the two halves would otherwise be read as two unpaired marks and kept. Only a pair whose
// text crosses a pipe is touched, and only in a table row, so a text without such a note comes back byte for byte.
func dropSpanningNotes(src []byte) []byte {
	if !bytes.Contains(src, []byte("%%")) || !bytes.Contains(src, []byte("|")) {
		return src
	}
	lines := strings.Split(string(src), "\n")
	is := tableLines(lines)
	changed := false
	for i, l := range lines {
		if !is[i] || !strings.Contains(l, "%%") || !strings.Contains(l, "|") {
			continue
		}
		n := reRowPrivate.ReplaceAllStringFunc(l, func(m string) string {
			if strings.Contains(m, "|") {
				return strings.Repeat(" ", len(m)) // the same length: offsets into the source stay where they were
			}
			return m
		})
		if n != l {
			lines[i], changed = n, true
		}
	}
	if !changed {
		return src
	}
	return []byte(strings.Join(lines, "\n"))
}
