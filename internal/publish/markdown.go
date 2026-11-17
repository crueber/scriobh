package publish

import (
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// WriteMarkdown writes the compiled book as plain Markdown from the same
// structure the DOCX and EPUB are built from: no front matter, no private
// notes, no alternatives comment, no ghost markers, scene breaks as * * *, and
// every construct spelled the one way. Images become a bracketed note (an
// attachment's address means nothing outside the project).
func WriteMarkdown(b *Book, o Options) []byte {
	m := &mdWriter{b: b, o: o}
	if On(o.TitlePage) {
		m.line("# " + m.esc(CleanLine(b.Title)))
		if s := CleanLine(b.Subtitle); s != "" {
			m.blank()
			m.line("*" + m.esc(s) + "*")
		}
		if a := CleanLine(b.Byline); a != "" {
			m.blank()
			m.line("by " + m.esc(a))
		}
	}
	for _, s := range b.Sections {
		m.section(s)
	}
	if Set(o.TheEnd) {
		m.blank()
		m.line("THE END")
	}
	out := strings.TrimLeft(m.out.String(), "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

type mdWriter struct {
	b   *Book
	o   Options
	out strings.Builder
}

func (m *mdWriter) line(s string) { m.out.WriteString(s + "\n") }
func (m *mdWriter) blank() {
	if s := m.out.String(); s != "" && !strings.HasSuffix(s, "\n\n") {
		m.out.WriteString("\n")
	}
}

func (m *mdWriter) section(s Section) {
	heading := HeadingText(s, m.o)
	level := s.Level
	if level < 1 {
		level = 1
	}
	hl := 2
	if s.Kind == SecChapter && level >= 2 {
		hl = 3
	}
	m.blank()
	if heading != "" {
		m.line(strings.Repeat("#", hl) + " " + m.esc(heading))
		m.blank()
	}
	var buf strings.Builder
	m.nodes(&buf, s.Nodes, "", hl)
	m.out.WriteString(strings.TrimRight(buf.String(), "\n") + "\n")
	used := map[int]bool{}
	noteRefs(s.Nodes, used)
	for _, f := range s.Notes {
		if !used[f.Num] {
			continue // a note whose reference was left out (a ghost, a private note) is left out too
		}
		var nb strings.Builder
		m.nodes(&nb, f.Nodes, "", 6)
		body := strings.TrimRight(nb.String(), "\n")
		m.blank()
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			if i == 0 {
				m.line(fmt.Sprintf("[^%d]: %s", f.Num, l))
			} else if l == "" {
				m.line("")
			} else {
				m.line("    " + l)
			}
		}
	}
}

func (m *mdWriter) nodes(w *strings.Builder, nodes []text.Node, prefix string, hl int) {
	for i, n := range nodes {
		if i > 0 {
			w.WriteString(strings.TrimRight(prefix, " ") + "\n")
		}
		switch n.Kind {
		case text.NPara:
			w.WriteString(m.prefixed(prefix, m.inl(n.Inl)) + "\n")
		case text.NHeading:
			lv := hl + n.Level
			if lv > 6 {
				lv = 6
			}
			w.WriteString(prefix + strings.Repeat("#", lv) + " " + strings.ReplaceAll(m.inl(n.Inl), "\n", " ") + "\n")
		case text.NRule:
			if m.o.SceneBreak == "ornament" && strings.TrimSpace(m.o.Ornament) != "" {
				w.WriteString(prefix + m.esc(SceneBreakText(m.o)) + "\n")
			} else {
				w.WriteString(prefix + "* * *\n")
			}
		case text.NQuote:
			m.nodes(w, n.Kids, prefix+"> ", hl)
		case text.NList:
			for k, it := range n.Items {
				marker := "- "
				if n.Ordered {
					marker = fmt.Sprintf("%d. ", n.Start+k)
				}
				pad := strings.Repeat(" ", len(marker))
				var ib strings.Builder
				m.nodes(&ib, it, "", hl)
				lines := strings.Split(strings.TrimRight(ib.String(), "\n"), "\n")
				for j, l := range lines {
					switch {
					case j == 0:
						w.WriteString(prefix + marker + l + "\n")
					case l == "":
						w.WriteString(strings.TrimRight(prefix, " ") + "\n")
					default:
						w.WriteString(prefix + pad + l + "\n")
					}
				}
			}
		case text.NCode, text.NSource:
			fence := "```"
			for strings.Contains(n.Text, fence) {
				fence += "`"
			}
			w.WriteString(prefix + fence + "\n")
			for _, l := range strings.Split(n.Text, "\n") {
				w.WriteString(strings.TrimRight(prefix+l, " ") + "\n")
			}
			w.WriteString(prefix + fence + "\n")
		case text.NTable:
			m.table(w, n, prefix)
		}
	}
}

func (m *mdWriter) prefixed(prefix, s string) string {
	if prefix == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m *mdWriter) table(w *strings.Builder, n text.Node, prefix string) {
	cols := 0
	for _, r := range n.Rows {
		cols = max(cols, len(r.Cells))
	}
	if cols == 0 {
		return
	}
	cell := func(r text.Row, i int) string {
		if i >= len(r.Cells) {
			return ""
		}
		return strings.ReplaceAll(strings.ReplaceAll(m.inl(r.Cells[i].Inl), "\n", " "), "|", `\|`)
	}
	row := func(r text.Row) string {
		var parts []string
		for i := 0; i < cols; i++ {
			parts = append(parts, cell(r, i))
		}
		return "| " + strings.Join(parts, " | ") + " |"
	}
	rows := n.Rows
	var head text.Row
	if len(rows) > 0 && rows[0].Head {
		head, rows = rows[0], rows[1:]
	} else {
		head = text.Row{Cells: make([]text.Cell, cols)}
	}
	w.WriteString(prefix + row(head) + "\n")
	var sep []string
	for i := 0; i < cols; i++ {
		a := ""
		if len(n.Rows) > 0 && i < len(n.Rows[0].Cells) {
			a = n.Rows[0].Cells[i].Align
		}
		switch a {
		case "right":
			sep = append(sep, "---:")
		case "center":
			sep = append(sep, ":---:")
		case "left":
			sep = append(sep, ":---")
		default:
			sep = append(sep, "---")
		}
	}
	w.WriteString(prefix + "| " + strings.Join(sep, " | ") + " |\n")
	for _, r := range rows {
		w.WriteString(prefix + row(r) + "\n")
	}
}

func (m *mdWriter) inl(in []text.Inline) string {
	var s strings.Builder
	for _, x := range in {
		switch x.Kind {
		case text.IText:
			s.WriteString(m.esc(strings.ReplaceAll(x.Text, "\n", " ")))
		case text.ICode:
			fence := "`"
			for strings.Contains(x.Text, fence) {
				fence += "`"
			}
			pad := ""
			if strings.HasPrefix(x.Text, "`") || strings.HasSuffix(x.Text, "`") {
				pad = " "
			}
			s.WriteString(fence + pad + x.Text + pad + fence)
		case text.IBreak:
			s.WriteString("\\\n")
		case text.IEmph:
			s.WriteString("*" + m.inl(x.Kids) + "*")
		case text.IStrong:
			s.WriteString("**" + m.inl(x.Kids) + "**")
		case text.IStrike:
			s.WriteString("~~" + m.inl(x.Kids) + "~~")
		case text.ILink:
			dest := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E").Replace(x.Text)
			s.WriteString("[" + m.inl(x.Kids) + "](" + dest + ")")
		case text.IImage:
			label := "[image"
			if a := strings.TrimSpace(x.Alt); a != "" {
				label += ": " + a
			}
			s.WriteString(m.esc(label + " (not included)]"))
		case text.INote:
			s.WriteString(fmt.Sprintf("[^%d]", x.Num))
		}
	}
	return s.String()
}

// esc escapes the characters that would turn plain text into Markdown.
func (m *mdWriter) esc(s string) string {
	s = CleanXML(s)
	var b strings.Builder
	for i, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '~', '|':
			b.WriteByte('\\')
		case '#', '+', '-', '=':
			if i == 0 {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	out := b.String()
	// a line that would read as an ordered list item
	if j := strings.IndexAny(out, ".)"); j > 0 && j < 10 && strings.Trim(out[:j], "0123456789") == "" && (len(out) == j+1 || out[j+1] == ' ') {
		out = out[:j] + `\` + out[j:]
	}
	return out
}
