package comanchor

import (
	"regexp"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// Plain is an anchor as a reader sees the text rendered: the quote without its
// formatting marks and a few words either side, so a reading view can find the
// place in its own page without knowing Markdown. They are hints, never trusted
// for placement: the Markdown offsets and quote are the anchor.
//
// Nothing hidden from a reader is ever in them. The context is read only from
// inside the block of prose the anchor sits in, with every private note, ghosted
// text and HTML comment cut out of it first, so a note, a comment or a stretch
// of front matter next to the commented words cannot leak a fragment of itself
// through the context, however long it is.
type Plain struct {
	Quote  string
	Before string
	After  string
}

var reHTMLComment = regexp.MustCompile(`(?s)<!--.*?(?:-->|\z)`)

// Hidden are the byte ranges of a body that a reader never sees as text and a
// comment may not be anchored to: private notes, ghosted text with its tags,
// and HTML comments (the alternatives annotation among them).
func Hidden(body string) [][2]int {
	var spans [][2]int
	if strings.Contains(body, "%%") || strings.Contains(body, text.GhostOpen) {
		spans = append(spans, text.HiddenSpans(body)...)
	}
	if strings.Contains(body, "<!--") {
		for _, m := range reHTMLComment.FindAllStringIndex(body, -1) {
			spans = append(spans, [2]int{m[0], m[1]})
		}
	}
	if len(spans) < 2 {
		return spans
	}
	return text.MergeSpans(spans)
}

// Hinter works out plain forms for many anchors of one body, parsing it once.
type Hinter struct {
	body   string
	hidden [][2]int
	blocks []text.Block
	ok     bool
	plain  []text.PlainBlock
	pok    bool
}

// NewHinter prepares the hints of a body.
func NewHinter(body string) *Hinter {
	h := &Hinter{body: body, hidden: Hidden(body)}
	h.blocks, h.ok = text.Blocks(body)
	h.plain, h.pok = text.PlainBlocks(body)
	return h
}

// For is the plain form of body[a:b]. Without a block to hold the range it has the
// quote alone and no context.
func (h *Hinter) For(a, b int) Plain {
	// The visible text of the block, through the map from it to the source: a range that starts or ends inside
	// formatting marks (the words "quiet house" of "*quiet* house") is exactly what a reading view shows.
	if h.pok {
		for _, bl := range h.plain {
			if bl.Start <= a && b <= bl.End {
				if i, j, ok := bl.Visible(a, b); ok {
					return Plain{Quote: bl.Text[i:j], Before: tailRunes(strings.TrimRight(bl.Text[:i], " "), 24), After: headRunes(strings.TrimLeft(bl.Text[j:], " "), 24)}
				}
				break
			}
		}
	}
	p := Plain{Quote: squashed(text.Plain(h.body[a:b]))}
	if !h.ok {
		return p
	}
	for _, bl := range h.blocks {
		if bl.Start <= a && b <= bl.End {
			p.Before = tailRunes(squashed(text.Plain(visible(h.body, h.hidden, bl.Start, a))), 24)
			p.After = headRunes(squashed(text.Plain(visible(h.body, h.hidden, b, bl.End))), 24)
			break
		}
	}
	return p
}

func squashed(s string) string { return strings.Join(strings.Fields(s), " ") }

// visible is body[lo:hi] with every hidden span cut out.
func visible(body string, hidden [][2]int, lo, hi int) string {
	if lo >= hi {
		return ""
	}
	var b strings.Builder
	at := lo
	for _, h := range hidden {
		if h[1] <= at || h[0] >= hi {
			continue
		}
		if h[0] > at {
			b.WriteString(body[at:h[0]])
		}
		if h[1] > at {
			at = h[1]
		}
	}
	if at < hi {
		b.WriteString(body[at:hi])
	}
	return b.String()
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
