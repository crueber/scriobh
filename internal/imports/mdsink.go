package imports

import (
	"strings"
)

// The one emitter. The converters that read Scrivener, WordPress and Ghost
// (and the DOCX converter before them) never write Markdown by hand: they build
// paragraphs out of segments and hand them to a sink, which escapes every
// character that could be read as Markdown or as a Scríobh construct, pairs the
// private notes of each block, and renumbers footnotes. What a foreign file
// says is therefore always text, whatever it holds.

var noteSpaces = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "\u00a0", " ")

// codeDefuse keeps the words that mark Scríobh's own constructs from being written
// in code, where no backslash can escape them: the opening of a ghost, its closing,
// and the alternatives comment. A zero-width space after the bracket is enough; what
// a person sees and copies is the same.
var codeDefuse = strings.NewReplacer(
	"<scriobh-ghost", "<\u200bscriobh-ghost",
	"</scriobh-ghost", "</\u200bscriobh-ghost",
	"<!-- scriobh:", "<!--\u200b scriobh:",
)

// privateNote is text as one %%private note%%: nothing in it can end the note
// early, and nothing about it is visible. Empty text is no note (%%%% would be
// literal text).
func privateNote(text string) string {
	t := strings.Join(strings.Fields(noteSpaces.Replace(text)), " ")
	if t == "" {
		return ""
	}
	return "%%" + escMDx(t, true, true) + "%%"
}

// sink collects the blocks of one document at a time.
type sink struct {
	c        *docxConv
	noteID   int
	notesLen int // bytes of footnote text kept for the document being built
}

func newSink() *sink {
	return &sink{c: newDocxConv()} // the tracked-change and comment state files() reads is made too
}

// overflow reports that more Markdown was made than the import may hold.
func (s *sink) overflow() bool { return s.c.overflow }

// size is the bytes of Markdown made so far, over every document.
func (s *sink) size() int { return s.c.total + s.notesLen }

// para adds a paragraph of segments.
func (s *sink) para(segs []seg) {
	if r, notes := s.renderSegs(segs); strings.TrimSpace(r) != "" {
		s.c.add(mdBlock{kind: bPara, text: escLines(r), notes: notes}, 0)
	}
}

// quote adds a paragraph of a block quote.
func (s *sink) quote(segs []seg) {
	if r, notes := s.renderSegs(segs); strings.TrimSpace(r) != "" {
		s.c.add(mdBlock{kind: bQuote, text: escLines(r), notes: notes}, 0)
	}
}

// item adds a list item at a level (0 is the outermost). list names the list
// the item belongs to, so a new list starts again; start is an ordered item's number.
func (s *sink) item(segs []seg, level int, ordered bool, start int, list string) {
	if r, notes := s.renderSegs(segs); strings.TrimSpace(r) != "" {
		s.c.add(mdBlock{kind: bList, text: escLines(r), ordered: ordered, listLvl: min(max(level, 0), 8), listNum: list, start: max(start, 1), notes: notes}, 0)
	}
}

// heading adds a heading of plain text. A private note is not allowed in one
// (it would be inside the heading's own text), so the caller moves it to a
// paragraph of its own.
func (s *sink) heading(level int, text string) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return
	}
	s.c.add(mdBlock{kind: bHeading, level: min(max(level, 1), 6), text: text}, 0)
}

// code adds a fenced block of text exactly as given.
func (s *sink) code(text string) {
	s.c.add(mdBlock{kind: bCode, text: codeDefuse.Replace(strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", " "))}, 0)
}

// codeLines adds a fenced block of several lines.
func (s *sink) codeLines(text string) {
	for _, l := range strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n") {
		s.c.add(mdBlock{kind: bCode, text: codeDefuse.Replace(l)}, 0)
	}
}

// scene adds a scene break.
func (s *sink) scene() { s.c.add(mdBlock{kind: bScene}, 0) }

// table adds a pipe table from rows of already rendered cells.
func (s *sink) table(rows [][]string, notes []string) {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if len(rows) == 0 || cols == 0 || cols > 63 {
		return
	}
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(cells) {
				v = escPipes(strings.TrimSpace(strings.ReplaceAll(cells[i], "\n", " ")))
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	line(rows[0])
	b.WriteString("|")
	for i := 0; i < cols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, r := range rows[1:] {
		line(r)
	}
	s.c.tables++
	s.c.add(mdBlock{kind: bTable, text: strings.TrimRight(b.String(), "\n"), notes: notes}, 0)
}

// inline renders segments as one line of Markdown, for a table cell or the like.
// It returns the footnotes the line refers to.
func (s *sink) inline(segs []seg) (string, []string) {
	r, notes := s.renderSegs(segs)
	return strings.TrimSpace(strings.ReplaceAll(r, "\n", " ")), notes
}

func (s *sink) renderSegs(segs []seg) (string, []string) {
	s.c.noteSeen = nil
	r := s.c.render(segs)
	notes := s.c.noteSeen
	s.c.noteSeen = nil
	return r, notes
}

// maxNoteBytes bounds the footnote text kept for one document.
const maxNoteBytes = 4 << 20

// notesFull says the document's footnotes are as many, or as large, as they may be.
func (s *sink) notesFull() bool { return s.notesLen >= maxNoteBytes || s.noteID >= maxNotes }

// note stores a footnote of one or more paragraphs and returns the id a segment
// refers to it by (a plain number; the document's footnotes are renumbered by
// first reference when it is composed). An empty note, or one past the budget,
// is not kept and returns "".
func (s *sink) note(paras [][]seg) string {
	var out []string
	size := 0
	s.c.inNote = true
	for _, p := range paras {
		r := strings.TrimSpace(strings.ReplaceAll(s.c.render(p), "\n", " "))
		if r == "" {
			continue
		}
		r = escLines(r)
		size += len(r)
		out = append(out, r)
	}
	s.c.inNote = false
	if len(out) == 0 || s.notesLen+size > maxNoteBytes || s.noteID >= maxNotes {
		return ""
	}
	s.noteID++
	id := itoa(s.noteID)
	s.c.noteText[id] = out
	s.notesLen += size
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// take composes the blocks made since the last call into one document's
// Markdown (no title: the title is the document's own field) and starts again.
func (s *sink) take() string {
	text := s.c.compose("", s.c.blocks)
	s.c.blocks = nil
	s.c.haveLast = false
	s.c.noteText = map[string][]string{}
	for k := range s.c.listCounters {
		delete(s.c.listCounters, k)
	}
	s.noteID, s.notesLen = 0, 0
	return text
}

// empty reports that nothing has been added since the last take.
func (s *sink) empty() bool { return len(s.c.blocks) == 0 }
