package publish

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

func testFaces(t testing.TB) *typeface {
	t.Helper()
	ts, err := newTypeface("")
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func lineText(p *para, lr lineRec) string {
	var b strings.Builder
	for i := lr.from; i < lr.to; i++ {
		it := p.items[i]
		switch it.kind {
		case iBox:
			for r := it.r0; r < it.r1; r++ {
				b.WriteString(string(p.runs[r].runes))
			}
		case iGlue:
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

func paraOf(ts *typeface, s string, size mpt, justify bool, maxChunk mpt) *para {
	p, _ := buildPara(ts, []text.Inline{{Kind: text.IText, Text: s}}, 0, size, justify, maxChunk, false)
	return p
}

func fixed(w mpt) lineFn { return func(int) (mpt, mpt) { return 0, w } }

const lorem = "It was the best of times, it was the worst of times, it was the age of wisdom, it was the age of foolishness, it was the epoch of belief, it was the epoch of incredulity, it was the season of Light, it was the season of Darkness, it was the spring of hope, it was the winter of despair."

func TestBreakingFillsLinesAndKeepsTheText(t *testing.T) {
	ts := testFaces(t)
	for _, justify := range []bool{true, false} {
		p := paraOf(ts, lorem, 11*pt, justify, 200*pt)
		lines, loose, overfull := breakParagraph(p.items, fixed(280*pt), 0, justify)
		if len(lines) < 4 || len(lines) > 7 {
			t.Fatalf("justify=%v: %d lines", justify, len(lines))
		}
		var got []string
		for i, lr := range lines {
			got = append(got, lineText(p, lr))
			if justify && !lr.last {
				// natural width plus the stretch is the measure, to within a milli-point per space
				var y, z mpt
				for k := lr.from; k < lr.to; k++ {
					y += p.items[k].y
					z += p.items[k].z
				}
				var w mpt
				if lr.r >= 0 {
					w = lr.nat + y*mpt(lr.r)/1000
				} else {
					w = lr.nat + z*mpt(lr.r)/1000
				}
				if d := w - lr.width; d > 40 || d < -40 {
					t.Errorf("line %d is %d wide in a measure of %d (r=%d)", i, w, lr.width, lr.r)
				}
			}
			if lr.nat > lr.width+lr.width/20 {
				t.Errorf("line %d is overfull: %d in %d", i, lr.nat, lr.width)
			}
		}
		if strings.Join(got, " ") != lorem {
			t.Errorf("the text changed:\n%q", got)
		}
		if loose > 1 || overfull != 0 {
			t.Errorf("justify=%v: %d loose, %d overfull lines", justify, loose, overfull)
		}
	}
}

func TestAnUnbreakableWordIsCutNotOverflowed(t *testing.T) {
	ts := testFaces(t)
	word := strings.Repeat("Supercalifragilistic", 30)
	p := paraOf(ts, "before "+word+" after", 11*pt, true, 250*pt)
	lines, _, overfull := breakParagraph(p.items, fixed(280*pt), 0, true)
	if overfull != 0 {
		t.Errorf("%d overfull lines", overfull)
	}
	var all []string
	for _, lr := range lines {
		if lr.nat > lr.width {
			t.Errorf("a line is %d wide in %d", lr.nat, lr.width)
		}
		all = append(all, lineText(p, lr))
	}
	if strings.ReplaceAll(strings.Join(all, ""), " ", "") != "before"+word+"after" {
		t.Errorf("the text changed")
	}
}

func TestHardBreakEndsALine(t *testing.T) {
	ts := testFaces(t)
	in := []text.Inline{{Kind: text.IText, Text: "first line"}, {Kind: text.IBreak}, {Kind: text.IText, Text: "second line here"}}
	p, _ := buildPara(ts, in, 0, 11*pt, true, 200*pt, false)
	lines, _, _ := breakParagraph(p.items, fixed(280*pt), 0, true)
	if len(lines) != 2 || lineText(p, lines[0]) != "first line" || lineText(p, lines[1]) != "second line here" {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0].r != 0 || !lines[0].forced {
		t.Errorf("a line before a hard break is set at its natural width: r=%d forced=%v", lines[0].r, lines[0].forced)
	}
}

func TestVariableMeasureForADropCap(t *testing.T) {
	ts := testFaces(t)
	p := paraOf(ts, strings.Repeat(lorem+" ", 2), 11*pt, true, 150*pt)
	lf := func(i int) (mpt, mpt) {
		if i < 3 {
			return 30 * pt, 250 * pt
		}
		return 0, 280 * pt
	}
	lines, _, _ := breakParagraph(p.items, lf, 3, true)
	for i, lr := range lines {
		_, L := lf(i)
		if lr.width != L || lr.nat > L+L/20 {
			t.Errorf("line %d: %d in %d", i, lr.nat, L)
		}
	}
}

func TestBreakingIsFastAtBookScale(t *testing.T) {
	testutil.Timing(t)
	ts := testFaces(t)
	// 150,000 words in paragraphs of about 90 words.
	words := strings.Fields(lorem)
	var paras []*para
	n := 0
	start := time.Now()
	for n < 150_000 {
		var b strings.Builder
		for i := 0; i < 90; i++ {
			b.WriteString(words[(n+i)%len(words)])
			b.WriteByte(' ')
		}
		n += 90
		paras = append(paras, paraOf(ts, b.String(), 11*pt, true, 200*pt))
	}
	shaped := time.Since(start)
	start = time.Now()
	lines := 0
	for _, p := range paras {
		ls, _, _ := breakParagraph(p.items, fixed(300*pt), 0, true)
		lines += len(ls)
	}
	t.Logf("150,000 words: shaping %v, breaking %v into %d lines", shaped, time.Since(start), lines)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("breaking took %v", d)
	}
}

func TestOnePaginatedHugeParagraphIsBrokenFirstFit(t *testing.T) {
	ts := testFaces(t)
	big := strings.Repeat("word ", 40_000) // more items than the Knuth-Plass limit
	p := paraOf(ts, big, 11*pt, true, 200*pt)
	if len(p.items) <= maxKPItems {
		t.Fatalf("only %d items", len(p.items))
	}
	start := time.Now()
	lines, _, _ := breakParagraph(p.items, fixed(300*pt), 0, true)
	if len(lines) < 1000 {
		t.Errorf("%d lines", len(lines))
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second {
			t.Errorf("took %v", took)
		}
	})
}
