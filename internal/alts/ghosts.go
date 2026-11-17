package alts

import (
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// bigLimits are the limits for reading a comment that is only being copied or
// mended, never stored: wide enough for any comment the server would have kept.
var bigLimits = Limits{PerDoc: 5000, VariantsPer: 1000, MaxVariantSz: 2 << 20}

// OmitGhosts returns content with its ghosted text removed, markers included.
// The body is always stripped. The alternatives comment (if any) is then brought
// in line with the shorter text when it can be: the contexts around each anchor
// are those of the text that remains, and an alternative whose anchored text was
// inside a ghost has nowhere to stand and is written as orphaned. A comment that
// cannot be read (not valid, or hand-written without ids) is left as written; it
// is never a reason to leave ghosted text in. Line endings become LF. A text
// with no ghosts is returned as it is. It is what an export that leaves ghosted
// text out writes.
func OmitGhosts(content string) string {
	if !strings.Contains(content, text.GhostOpen) {
		return content
	}
	content = NormalizeEOL(content)
	parts := Split(content)
	spans, spansOK := text.Ghosts(parts.Body)
	if len(spans) == 0 && spansOK {
		return content
	}
	newBody := text.StripGhosts(parts.Body, true)
	if !parts.HasLine {
		return newBody
	}
	left := Parts{Body: newBody, Sep: parts.Sep, Tail: parts.Tail, HasLine: true}
	if !spansOK {
		return left.Compose(parts.Line) // the text was stripped by its tags; its comment cannot be mended
	}
	items, err := Parse(parts.Line, bigLimits)
	if err != nil {
		return left.Compose(parts.Line)
	}
	n := 0
	mo := MergeOptions{NewID: func(prefix string) string { n++; return fmt.Sprintf("%s_omit%d", prefix, n) }, Limits: bigLimits}
	if err := Freshen(items, mo, false); err != nil {
		return left.Compose(parts.Line)
	}
	o := Options{NewID: mo.NewID, Limits: bigLimits}
	placed, _ := Rebase("", parts.Body, items, nil, o)
	edits := make([]Edit, 0, len(spans))
	for _, sp := range spans {
		edits = append(edits, Edit{Start: sp.OpenStart, End: sp.CloseEnd, NewLen: 0})
	}
	final, _ := Rebase(parts.Body, newBody, placed, edits, o)
	return left.Compose(Serialize(final))
}

// BodyWithoutComment is content with its trailing alternatives comment taken
// off: the chosen text alone, as plain Markdown. A text with no comment is
// returned as it is.
func BodyWithoutComment(content string) string {
	if !strings.Contains(content, Marker) {
		return content
	}
	p := Split(NormalizeEOL(content))
	if !p.HasLine {
		return content
	}
	return p.Body
}

// Collapse finds the alternatives comments in a text that has more than one, or
// has text after its comment, so that the text can be given one canonical
// comment again. It looks at every line that begins and ends like a comment and
// reads as one (lines inside a fenced code block are code, and are left alone).
// It returns the text with all such lines taken out, and the last of them, the
// one that stands for the document's alternatives. ok is false when there is
// none, or when the only one is already the last line of the text.
func Collapse(content string, lim Limits) (body, line string, ok bool) {
	type cand struct{ from, to int } // byte range of the line including its line break
	var cands []cand
	fence, fenceLen := byte(0), 0
	checked := 0
	for from := 0; from < len(content); {
		to := strings.IndexByte(content[from:], '\n')
		next := len(content)
		l := content[from:]
		if to >= 0 {
			next = from + to + 1
			l = content[from : from+to]
		}
		if f, n, closes := fenceLineIn(l, fence, fenceLen); f != 0 {
			if fence == 0 {
				fence, fenceLen = f, n
			} else if closes {
				fence, fenceLen = 0, 0
			}
		} else if fence == 0 && strings.HasPrefix(l, Marker) && strings.HasSuffix(l, " -->") {
			// A line that reads as a comment but fails the checks is ordinary text (it
			// was kept as such), and is never taken out.
			items, err := Parse(l, lim)
			if err == nil {
				err = Freshen(items, MergeOptions{NewID: func(p string) string { checked++; return fmt.Sprintf("%s%d", p, checked) }, Limits: lim}, true)
			}
			if err == nil {
				cands = append(cands, cand{from, next})
				line = l
			}
		}
		from = next
	}
	if len(cands) == 0 {
		return "", "", false
	}
	if p := Split(content); len(cands) == 1 && p.HasLine && p.Line == line {
		return "", "", false
	}
	var b strings.Builder
	at := 0
	for _, c := range cands {
		b.WriteString(content[at:c.from])
		at = c.to
	}
	b.WriteString(content[at:])
	return b.String(), line, true
}

// fenceLineIn is the code-fence test of a line: the fence's character and
// length when it opens a block, or whether it closes the open one.
func fenceLineIn(line string, open byte, openLen int) (byte, int, bool) {
	t := strings.TrimRight(line, "\r")
	trim := strings.TrimLeft(t, " ")
	if len(t)-len(trim) >= 4 || len(trim) < 3 || (trim[0] != '`' && trim[0] != '~') {
		return 0, 0, false
	}
	ch := trim[0]
	run := len(trim) - len(strings.TrimLeft(trim, string(ch)))
	if run < 3 {
		return 0, 0, false
	}
	rest := trim[run:]
	if open == 0 {
		if ch == '`' && strings.IndexByte(rest, '`') >= 0 {
			return 0, 0, false
		}
		return ch, run, false
	}
	if ch == open && run >= openLen && strings.TrimSpace(rest) == "" {
		return ch, run, true
	}
	return 0, 0, false
}
