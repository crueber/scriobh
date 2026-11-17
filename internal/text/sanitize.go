package text

import "bytes"

// goldmark, like most Markdown parsers, can be driven into quadratic work by
// text that is nothing like prose: a megabyte of unmatched `[` or `*`, or a line
// of ten thousand `> ` markers. The counter runs inside every save, on text up
// to 2 MiB, so it must stay fast on whatever it is given. sanitize bounds the
// work before parsing, in one linear pass, by escaping the characters that
// start the expensive structures once a paragraph has more of them than any
// writing has, and by cutting a run of container markers short.
//
// An escaped character is literal text, which is what an unmatched one is
// anyway, so word counts of such text are unchanged except where a real link or
// emphasis was in the part past the limit. Ordinary documents are never touched
// and are returned as they are, without a copy.
const (
	// maxRisky is how many delimiter characters (`[ ] * _ ~` and backticks) one
	// run of lines without a blank line may hold before the rest are escaped.
	maxRisky = 2000
	// A destination is read to the end of its line when it never closes, so on a
	// line longer than longLine the number of link openers (`](`) is limited to
	// linkBudget / the line's length (at least minLongLinks); the rest are
	// escaped. Short lines, however many, cost little.
	longLine     = 4096
	linkBudget   = 1 << 26
	minLongLinks = 8
	// maxIndent is how far a line may be indented. Each deeper level of a nested
	// list is another container to walk, so a staircase of ten thousand lines
	// is quadratic; no list is nested thirty levels deep.
	maxIndent = 64
	// maxMarkers is how many characters of list and quote markers a line may
	// start with before the first is escaped (a legitimate line has a handful).
	maxMarkers = 64
)

func risky(c byte) bool {
	switch c {
	case '[', ']', '*', '_', '~', '`':
		return true
	}
	return false
}

func isMarkerByte(c byte) bool {
	switch c {
	case ' ', '\t', '>', '-', '*', '+', '.', ')':
		return true
	}
	return c >= '0' && c <= '9'
}

// sanitize returns src, or a copy with the expensive structures escaped.
func sanitize(src []byte) []byte {
	var out []byte // allocated on the first change
	copied := 0    // how much of src is already in out
	last := -1     // where the last backslash went in, so no place gets two
	escape := func(at int) {
		if at == last {
			return
		}
		last = at
		if out == nil {
			out = make([]byte, 0, len(src)+64)
		}
		out = append(out, src[copied:at]...)
		out = append(out, '\\')
		copied = at
	}
	// cut replaces the n bytes of indentation at `at` with keep spaces.
	cut := func(at, n, keep int) {
		if out == nil {
			out = make([]byte, 0, len(src))
		}
		out = append(out, src[copied:at]...)
		out = append(out, bytes.Repeat([]byte{' '}, keep)...)
		copied = at + n
	}
	count := 0
	var fence byte // the character of the open fenced code block, or 0
	fenceLen := 0
	for start := 0; start < len(src); {
		end := bytes.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start
		}
		line := src[start:end]
		// Fenced code is literal and cheap to parse, so its lines are neither
		// counted nor escaped; escaping a closing fence would swallow the rest of
		// the document into the block.
		if f, n, closes := fenceLine(line, fence, fenceLen); f != 0 {
			if fence == 0 {
				fence, fenceLen = f, n
			} else if closes {
				fence, fenceLen = 0, 0
			}
			count = 0
			start = end + 1
			continue
		} else if fence != 0 {
			start = end + 1
			continue
		}
		if len(bytes.TrimLeft(line, " \t\r")) == 0 {
			count = 0
		} else {
			if lead := len(line) - len(bytes.TrimLeft(line, " \t")); lead > maxIndent {
				cut(start, lead, maxIndent)
			}
			// The markers at the start of the line: every `>` is a level of quote,
			// and every space-separated token (`-`, `1.`, `*`) a level of list.
			n, first := 0, -1
			for i := 0; i < len(line) && isMarkerByte(line[i]); i++ {
				c := line[i]
				if c == ' ' || c == '\t' {
					continue
				}
				if first < 0 {
					first = i
				}
				if c == '>' || i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == '>' {
					n++
				}
			}
			if n > maxMarkers && first >= 0 {
				escape(start + first)
				// The marker run is literal text now; count it as part of the line.
			}
			backslashes := 0
			links := 0
			allowed := max(minLongLinks, linkBudget/max(len(line), 1))
			for i := 0; i < len(line); i++ {
				c := line[i]
				if c == '\\' {
					backslashes++
					continue
				}
				if risky(c) {
					count++
					if count > maxRisky && backslashes%2 == 0 {
						escape(start + i)
					}
				}
				if c == '(' && i > 0 && line[i-1] == ']' && len(line) > longLine && backslashes%2 == 0 {
					if links++; links > allowed {
						escape(start + i)
					}
				}
				backslashes = 0
			}
		}
		start = end + 1
	}
	if out == nil {
		return src
	}
	return append(out, src[copied:]...)
}

// fenceLine reports whether line is a code fence. Outside a block (open == 0)
// it returns the fence character and length of an opening fence; inside one
// it returns the open fence's character and whether this line closes it. It
// returns 0 when the line is not a fence line.
func fenceLine(line []byte, open byte, openLen int) (c byte, n int, closes bool) {
	t := bytes.TrimRight(line, "\r")
	trim := bytes.TrimLeft(t, " ")
	if len(t)-len(trim) >= 4 || len(trim) < 3 || (trim[0] != '`' && trim[0] != '~') {
		return 0, 0, false
	}
	ch := trim[0]
	run := len(trim) - len(bytes.TrimLeft(trim, string(ch)))
	if run < 3 {
		return 0, 0, false
	}
	rest := trim[run:]
	if open == 0 {
		if ch == '`' && bytes.IndexByte(rest, '`') >= 0 {
			return 0, 0, false // not a fence: a backtick info string
		}
		return ch, run, false
	}
	if ch == open && run >= openLen && len(bytes.TrimSpace(rest)) == 0 {
		return ch, run, true
	}
	return 0, 0, false
}
