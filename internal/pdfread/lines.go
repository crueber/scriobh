package pdfread

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFragsPerPage bounds the pieces of text a page may hold for the layout.
const MaxFragsPerPage = 30000

// pline is a line of text on a page: frags that share a baseline, in one column.
type pline struct {
	page       int
	col        int // 0 or 1
	region     int // lines of one region are laid out together; a region is a column between full-width lines
	x0, x1, y  float64
	size       float64
	frags      []Frag
	runs       []Run
	text       string
	bold, ital bool
	gaps       int     // wide gaps inside the line (cells of a table, tabs, leaders)
	cut        float64 // x of the first cell after the first wide gap
	mono       bool    // set wholly in a monospaced face
	leader     bool
	removed    string
	fnote      bool
	sups       []supMark
	idx        int // reading order in the document
}

// supMark is a raised small run that may be a footnote marker.
type supMark struct {
	text string
}

func baseY(f Frag) float64 { return f.Y - f.Rise }

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p * float64(len(s)-1))
	return s[i]
}

// bodySize is the font size that carries the most characters in the document.
func bodySize(pages []*Page) float64 {
	hist := map[int]int{}
	for _, p := range pages {
		for _, f := range p.Frags {
			if f.Invisible && len(p.Frags) == 0 {
				continue
			}
			n := utf8.RuneCountInString(f.Text)
			hist[int(math.Round(f.Size*2))] += n
		}
	}
	best, bn := 0, 0
	for k, n := range hist {
		if n > bn || n == bn && k > best {
			best, bn = k, n
		}
	}
	if bn == 0 {
		return 10
	}
	return float64(best) / 2
}

// gutter looks for the vertical gutter of a two-column page. It returns the x
// of the gutter's middle, or ok=false. Frags that cross it (a heading across
// both columns, a running head) are allowed up to a few.
func gutter(frags []Frag, body float64) (gx float64, spanners int, ok bool, ambiguous bool) {
	var main []Frag
	for _, f := range frags {
		if f.Size >= 0.7*body && f.X1 > f.X0 {
			main = append(main, f)
		}
	}
	if len(main) < 14 {
		return 0, 0, false, false
	}
	xs0, xs1 := make([]float64, len(main)), make([]float64, len(main))
	for i, f := range main {
		xs0[i], xs1[i] = f.X0, f.X1
	}
	lo, hi := percentile(xs0, 0.02), percentile(xs1, 0.98)
	w := hi - lo
	if !(w >= 6*body && w <= 20000) { // narrower than 6 em is not a text block, and wider than a page can be is not a page
		return 0, 0, false, false
	}
	n := int(math.Ceil(w)) + 2
	diff := make([]int, n+1)
	for _, f := range main {
		a := int(math.Floor(f.X0 - lo))
		b := int(math.Ceil(f.X1 - lo))
		a, b = max(a, 0), min(b, n)
		if a < b {
			diff[a]++
			diff[b]--
		}
	}
	allow := max(2, len(main)*6/100)
	cover := make([]int, n)
	c := 0
	for i := 0; i < n; i++ {
		c += diff[i]
		cover[i] = c
	}
	zlo, zhi := int(0.3*w), int(0.7*w)
	bestA, bestB := -1, -1
	for i := zlo; i < zhi && i < n; {
		if cover[i] > allow {
			i++
			continue
		}
		j := i
		for j < zhi && j < n && cover[j] <= allow {
			j++
		}
		if j-i > bestB-bestA {
			bestA, bestB = i, j
		}
		i = j
	}
	if bestA < 0 || float64(bestB-bestA) < math.Max(8, 0.9*body) {
		return 0, 0, false, false
	}
	gx = lo + float64(bestA+bestB)/2
	var left, right []Frag
	for _, f := range main {
		switch {
		case f.X1 <= gx+1:
			left = append(left, f)
		case f.X0 >= gx-1:
			right = append(right, f)
		default:
			spanners++
		}
	}
	if len(left) < 6 || len(right) < 6 || float64(min(len(left), len(right))) < 0.2*float64(len(left)+len(right)) {
		return 0, 0, false, false
	}
	// A column is text that fills most of its width; a table's left cells do not.
	colW := gx - float64(bestB-bestA)/2 - lo
	var widths []float64
	for _, f := range left {
		widths = append(widths, f.X1-f.X0)
	}
	if colW <= 0 || percentile(widths, 0.5) < 0.6*colW {
		return 0, 0, false, true
	}
	return gx, spanners, true, spanners > len(main)/20
}

// pageLines turns a page's frags into lines in reading order and says how many
// columns it found.
func pageLines(p *Page, body float64) (lines []*pline, cols int, ambiguous bool) {
	if len(p.Frags) > MaxFragsPerPage { // a page of that many pieces is not text a person set: the first are read
		p.Frags = p.Frags[:MaxFragsPerPage]
		p.Truncated = true
	}
	frags := make([]Frag, 0, len(p.Frags))
	visible := false
	for _, f := range p.Frags {
		if !f.Invisible {
			visible = true
		}
	}
	for _, f := range p.Frags {
		if f.Invisible && visible {
			continue
		}
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		frags = append(frags, f)
	}
	if len(frags) == 0 {
		return nil, 0, false
	}
	gx, _, two, amb := gutter(frags, body)
	ambiguous = amb
	if !two {
		return clusterLines(frags, p.Index, 0, 0, body), 1, ambiguous
	}
	// Full-width frags split the page into bands; each band's columns are read left then right.
	var spans []Frag
	var rest []Frag
	for _, f := range frags {
		if f.X0 < gx-2 && f.X1 > gx+2 {
			spans = append(spans, f)
		} else {
			rest = append(rest, f)
		}
	}
	sort.Slice(spans, func(i, j int) bool { return baseY(spans[i]) > baseY(spans[j]) })
	band := func(f Frag) int {
		n := 0
		for _, s := range spans {
			if baseY(s) > baseY(f)+0.5*f.Size {
				n++
			}
		}
		return n
	}
	groups := map[[2]int][]Frag{}
	for _, f := range rest {
		side := 0
		if f.X0 >= gx-1 {
			side = 1
		}
		k := [2]int{band(f), side}
		groups[k] = append(groups[k], f)
	}
	region := 0
	for b := 0; b <= len(spans); b++ {
		for side := 0; side < 2; side++ {
			if g := groups[[2]int{b, side}]; len(g) > 0 {
				lines = append(lines, clusterLines(g, p.Index, side, region, body)...)
				region++
			}
		}
		if b < len(spans) {
			lines = append(lines, clusterLines([]Frag{spans[b]}, p.Index, 0, region, body)...)
			region++
		}
	}
	return lines, 2, ambiguous
}

// clusterLines groups frags on one baseline into lines, top to bottom.
func clusterLines(frags []Frag, page, col, region int, body float64) []*pline {
	fs := append([]Frag(nil), frags...)
	sort.SliceStable(fs, func(i, j int) bool {
		bi, bj := baseY(fs[i]), baseY(fs[j])
		if math.Abs(bi-bj) > 0.01 {
			return bi > bj
		}
		return fs[i].X0 < fs[j].X0
	})
	var lines []*pline
	for _, f := range fs {
		by := baseY(f)
		var hit *pline
		for k := len(lines) - 1; k >= 0 && k >= len(lines)-12; k-- {
			l := lines[k]
			tol := 0.3 * math.Min(f.Size, l.size)
			if math.Abs(by-l.y) <= tol {
				hit = l
				break
			}
		}
		if hit == nil {
			hit = &pline{page: page, col: col, region: region, y: by, size: f.Size}
			lines = append(lines, hit)
		}
		hit.frags = append(hit.frags, f)
		if f.Size > hit.size {
			hit.size, hit.y = f.Size, by
		}
	}
	// Small raised or lowered text with no baseline of its own (a footnote marker, an exponent) joins the line it touches.
	lines = absorbSatellites(lines)
	for _, l := range lines {
		l.build(body)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].y > lines[j].y })
	return lines
}

func absorbSatellites(lines []*pline) []*pline {
	span := func(l *pline) (a, b float64) {
		a, b = math.Inf(1), math.Inf(-1)
		for _, f := range l.frags {
			a, b = math.Min(a, f.X0), math.Max(b, f.X1)
		}
		return
	}
	keep := lines[:0:0]
	gone := map[*pline]bool{}
	for si, s := range lines {
		chars := 0
		for _, f := range s.frags {
			chars += utf8.RuneCountInString(f.Text)
		}
		if chars > 6 {
			continue
		}
		sa, sb := span(s)
		var best *pline
		bd := math.Inf(1)
		// the lines are in order down the page: what a raised mark belongs to is a few lines away
		for mi := max(0, si-12); mi < len(lines) && mi <= si+12; mi++ {
			m := lines[mi]
			if m == s || gone[m] || s.size > 0.8*m.size {
				continue
			}
			d := math.Abs(s.y - m.y)
			if d > 0.8*m.size || d < 0.05*m.size {
				continue
			}
			ma, mb := span(m)
			if sa >= ma-1 && sb <= mb+1 && d < bd {
				best, bd = m, d
			}
		}
		if best != nil {
			for i := range s.frags {
				s.frags[i].Rise = s.y - best.y // remembered as raised text
				if s.frags[i].Rise == 0 {
					s.frags[i].Rise = 0.01
				}
			}
			best.frags = append(best.frags, s.frags...)
			gone[s] = true
		}
	}
	for _, l := range lines {
		if !gone[l] {
			keep = append(keep, l)
		}
	}
	return keep
}

var leaderRe = func(s string) bool {
	d := 0
	for _, r := range s {
		if r == '.' || r == '·' || r == '…' || r == '•' {
			d++
		}
	}
	return d >= 5 && strings.Contains(s, ". .") || strings.Contains(s, "....") || strings.Contains(s, "…..") || strings.Contains(s, "· ·")
}

// build makes the line's runs and measures it.
func (l *pline) build(body float64) {
	sort.SliceStable(l.frags, func(i, j int) bool { return l.frags[i].X0 < l.frags[j].X0 })
	l.x0, l.x1 = math.Inf(1), math.Inf(-1)
	allBold, allItal, allMono := true, true, true
	var runs []Run
	var prev *Frag
	chars := 0
	var sizeChars = map[int]int{}
	for i := range l.frags {
		f := &l.frags[i]
		t := normalise(f.Text)
		if t == "" {
			continue
		}
		l.x0, l.x1 = math.Min(l.x0, f.X0), math.Max(l.x1, f.X1)
		n := utf8.RuneCountInString(t)
		raised := f.Rise > 0.15*f.Size && f.Size < 0.85*l.size
		if !raised {
			sizeChars[int(math.Round(f.Size*2))] += n
		}
		if prev != nil {
			gap := f.X0 - prev.X1
			s := math.Min(f.Size, prev.Size)
			if gap > 2.5*s && s > 0 {
				l.gaps++
				if l.gaps == 1 {
					l.cut = f.X0
				}
				if len(runs) > 0 && !strings.HasPrefix(runs[len(runs)-1].Text, "\x00") {
					runs[len(runs)-1].Text += "\x01"
				}
			}
			if gap > 0.2*s && len(runs) > 0 && !strings.HasSuffix(runs[len(runs)-1].Text, " ") && !strings.HasPrefix(t, " ") {
				runs[len(runs)-1].Text += " "
			}
		}
		if raised {
			l.sups = append(l.sups, supMark{text: strings.TrimSpace(t)})
			runs = append(runs, Run{Text: "\x00sup:" + strings.TrimSpace(t) + "\x00"})
		} else {
			runs = append(runs, Run{Text: t, Bold: f.Font != nil && f.Font.Bold, Italic: f.Font != nil && f.Font.Italic, Mono: f.Font != nil && f.Font.Mono})
			allBold = allBold && f.Font != nil && f.Font.Bold
			allItal = allItal && f.Font != nil && f.Font.Italic
			allMono = allMono && f.Font != nil && f.Font.Mono
		}
		chars += n
		prev = f
	}
	if math.IsInf(l.x0, 1) {
		l.x0, l.x1 = 0, 0
	}
	best, bn := 0, -1
	for k, n := range sizeChars {
		if n > bn || n == bn && k > best {
			best, bn = k, n
		}
	}
	if bn > 0 {
		l.size = float64(best) / 2
	}
	l.bold, l.ital = allBold && chars > 0, allItal && chars > 0
	l.mono = allMono && chars > 0
	l.runs = mergeRuns(runs)
	var sb strings.Builder
	for _, r := range l.runs {
		if strings.HasPrefix(r.Text, "\x00sup:") {
			continue
		}
		sb.WriteString(strings.ReplaceAll(r.Text, "\x01", " "))
	}
	l.text = strings.TrimSpace(sb.String())
	l.leader = leaderRe(l.text)
}

func mergeRuns(runs []Run) []Run {
	var out []Run
	var sb strings.Builder // the text of the last run, when it takes more
	open := false
	closeRun := func() {
		if open {
			out[len(out)-1].Text = sb.String()
			sb.Reset()
			open = false
		}
	}
	for _, r := range runs {
		if n := len(out); n > 0 && out[n-1].Bold == r.Bold && out[n-1].Italic == r.Italic && out[n-1].Mono == r.Mono && out[n-1].Note == r.Note &&
			!strings.HasPrefix(r.Text, "\x00") && !strings.HasPrefix(out[n-1].Text, "\x00") {
			if !open {
				sb.WriteString(out[n-1].Text)
				open = true
			}
			sb.WriteString(r.Text)
			continue
		}
		closeRun()
		out = append(out, r)
	}
	closeRun()
	return out
}

var supOpen = "\x00sup:"

func isSupRun(r Run) (string, bool) {
	if strings.HasPrefix(r.Text, supOpen) && strings.HasSuffix(r.Text, "\x00") && len(r.Text) > len(supOpen)+1 {
		return r.Text[len(supOpen) : len(r.Text)-1], true
	}
	return "", false
}

func isRomanNumeral(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("ivxlcdmIVXLCDM", r) {
			return false
		}
	}
	return true
}

func isNumberOnly(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if isRomanNumeral(s) {
		return true
	}
	digits := 0
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits++
		case r == '-' || r == '–' || r == '—' || r == ' ' || r == '.' || r == '(' || r == ')' || r == '[' || r == ']':
		default:
			return false
		}
	}
	return digits > 0 && digits <= 5
}

func pageWord(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"page ", "p. ", "p ", "pg. "} {
		if strings.HasPrefix(l, p) && isNumberOnly(strings.TrimPrefix(l, p)) {
			return true
		}
	}
	// "3 of 120", "3 / 120"
	for _, sep := range []string{" of ", " / ", "/"} {
		if i := strings.Index(l, sep); i > 0 && isNumberOnly(l[:i]) && isNumberOnly(l[i+len(sep):]) {
			return true
		}
	}
	return false
}

// shape normalises a line for comparing running heads: digits are one mark.
func shape(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsDigit(r):
			if !strings.HasSuffix(b.String(), "#") {
				b.WriteByte('#')
			}
		case unicode.IsSpace(r):
			if !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// rawText is the line's text as it is set, with the separators of wide gaps.
func (l *pline) rawText() string {
	var sb strings.Builder
	for _, r := range l.runs {
		if strings.HasPrefix(r.Text, "\x00") {
			continue
		}
		sb.WriteString(r.Text)
	}
	return sb.String()
}
