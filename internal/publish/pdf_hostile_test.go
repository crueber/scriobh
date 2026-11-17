package publish

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// hostile writes a book of one chapter of the given Markdown and checks that
// the PDF is sound. It returns how long the write took; the caller asserts the
// time bound in a subtest that calls testutil.Timing.
func hostileBook(t *testing.T, name, md string, o Options) (*Book, []byte, []Warn, time.Duration) {
	t.Helper()
	b := bookFrom("Hostile", md)
	start := time.Now()
	out, warns, err := WritePDF(b, o)
	if errors.Is(err, ErrTruncated) && name == "a note referenced often" {
		return b, nil, nil, time.Since(start) // the notes are over the layout's limit: refused, in bounded time
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	took := time.Since(start)
	if out != nil {
		if probs := check.PDF(out); len(probs) > 0 {
			for i, p := range probs {
				if i < 5 {
					t.Errorf("%s: %s", name, p)
				}
			}
		}
	}
	return b, out, warns, took
}

// hostileLimit is the time every hostile book must be written in.
const hostileLimit = 20 * time.Second

func assertHostileTime(t *testing.T, name string, took time.Duration) {
	t.Helper()
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > hostileLimit*timingFactor {
			t.Errorf("%s took %v (limit %v)", name, took, hostileLimit*timingFactor)
		}
	})
}

func TestHostilePDFInputs(t *testing.T) {
	words := func(n int) string { return strings.Repeat("alpha beta gamma delta ", n/4) }
	nested := func(n int, open, prefix string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(strings.Repeat(prefix, i+1))
			b.WriteString(open + "level text\n")
			b.WriteString(strings.Repeat(prefix, i+1) + "\n")
		}
		return b.String()
	}
	huge := strings.Repeat("W", 100_000)
	var table strings.Builder
	table.WriteString("| a | b | c | d | e |\n|---|---|---|---|---|\n")
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&table, "| r%d | some words in a cell | %d | x | a longer cell with enough words to wrap in a narrow column |\n", i, i)
	}
	var wide strings.Builder
	for c := 0; c < 80; c++ {
		wide.WriteString("| col" + fmt.Sprint(c) + " ")
	}
	wide.WriteString("|\n" + strings.Repeat("|---", 80) + "|\n" + strings.Repeat("| cell ", 80) + "|\n")
	cases := map[string]string{
		"one giant paragraph":     words(120_000),
		"an unbreakable word":     "before " + huge + " after",
		"many long words":         strings.Repeat(strings.Repeat("Z", 400)+" ", 300),
		"deep quotes":             nested(40, "text ", "> "),
		"deep lists":              nested(30, "- ", "  "),
		"a huge table":            table.String(),
		"a wide table":            wide.String(),
		"heading storm":           strings.Repeat("# heading\n\n", 3000),
		"scene breaks only":       strings.Repeat("* * *\n\n", 3000),
		"empty and spaces":        "   \n\n  \n\n",
		"one long line of code":   "```\n" + strings.Repeat("x", 50_000) + "\n```\n",
		"many code lines":         "```\n" + strings.Repeat("line of code here\n", 20_000) + "```\n",
		"a note referenced often": strings.Repeat("word[^a] ", 5000) + "\n\n[^a]: " + words(200) + "\n",
		"hard breaks":             strings.Repeat("line one\\\nline two\\\n", 4000),
		"raw html":                strings.Repeat("<script>alert(1)</script> <b onclick=x>b</b>\n\n", 500),
		"emphasis soup":           strings.Repeat("*a **b ~~c `d` c~~ b** a* ", 3000),
		"wiki and links":          strings.Repeat("[[Link|alias]] [text](https://example.com/"+strings.Repeat("a", 500)+") ", 500),
	}
	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			o := print6x9()
			o.DropCap, o.ChapterOrnament = True(), True()
			_, out, warns, took := hostileBook(t, name, md, o)
			assertHostileTime(t, name, took)
			if len(out) > 80<<20 {
				t.Errorf("%d bytes", len(out))
			}
			_ = warns
		})
	}
}

// A page full of every character the BMP has: nothing draws glyph 0, the
// missing ones are counted, and the file is the size of the text, not of the
// fonts.
func TestGlyphBombs(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var b strings.Builder
	for i := 0; i < 60_000; i++ {
		r := rune(rng.Intn(0xFFFF))
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		b.WriteRune(r)
		if i%7 == 6 {
			b.WriteByte(' ')
		}
	}
	// markdown-neutral: escape the characters that mean something
	md := strings.NewReplacer("\\", "", "*", "", "_", "", "`", "", "[", "", "]", "", "<", "", ">", "", "#", "", "|", "", "~", "", "!", "", "&", "").Replace(b.String())
	_, out, warns, took := hostileBook(t, "glyph bomb", md, print6x9())
	assertHostileTime(t, "glyph bomb", took)
	var missing bool
	for _, w := range warns {
		if w.Code == "missing_glyph" {
			missing = true
		}
	}
	if !missing {
		t.Error("no missing_glyph warning for random characters")
	}
	doc, _ := check.ParsePDF(out)
	if strings.Contains(doc.Text(), "\x00") {
		t.Error("a NUL reached the text")
	}
	// every embedded font is a subset of at most the glyphs the book uses
	if len(out) > 60<<20 {
		t.Errorf("%d bytes", len(out))
	}
}

// Every string a user writes that reaches the PDF is a hex string or a glyph.
func TestPDFStringsAreEscaped(t *testing.T) {
	evil := "a) /JS (b >> /OpenAction << /S /JavaScript >> (\\ \x00\x01 ‮ <FEFF> \xed\xa0\x80"
	b := bookFrom(evil, "Words that are fine.")
	b.Byline, b.Subtitle = evil, evil
	o := print6x9()
	o.Imprint = evil
	o.HeaderTitle = evil
	o.ChapterLabel = evil[:20]
	out, _, err := WritePDF(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.PDF(out); len(probs) > 0 {
		t.Fatal(probs)
	}
	for _, bad := range []string{"/OpenAction <<", "/JavaScript >>", "(b >>"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("the file contains %q", bad)
		}
	}
	doc, _ := check.ParsePDF(out)
	if !strings.Contains(doc.Title, "a) /JS (b >> /OpenAction") {
		t.Errorf("the title: %q", doc.Title)
	}
}

// Footnote text is bounded across the whole book (the structure's budget), so
// the layout of a book of amplified notes stays small.
func TestFootnoteAmplificationIsBounded(t *testing.T) {
	var chapters []string
	for i := 0; i < 5; i++ {
		var md strings.Builder
		for j := 0; j < 300; j++ {
			md.WriteString("ref[^a] ")
		}
		md.WriteString("\n\n[^a]: " + strings.Repeat("note text that is long ", 700) + "\n")
		chapters = append(chapters, md.String())
	}
	b := bookFrom("Amplified", chapters...)
	start := time.Now()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, _, err := WritePDF(b, print6x9())
	runtime.ReadMemStats(&after)
	// the notes are over the layout's limit: the export is refused, not written without them
	if !errors.Is(err, ErrTruncated) || out != nil {
		t.Fatalf("an amplified book was written (%v)", err)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 30*time.Second*timingFactor {
			t.Errorf("took %v", took)
		}
	})
	t.Logf("refused in %v, %d MB allocated", took, (after.TotalAlloc-before.TotalAlloc)>>20)
}

// The performance target: 150,000 words become a PDF in a few seconds.
func TestPDFOfALongBook(t *testing.T) {
	if testing.Short() {
		t.Skip("long book")
	}
	b := longBook(150_000)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	l, err := LayoutPDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	laid := time.Since(start)
	out, _, err := l.Render()
	if err != nil {
		t.Fatal(err)
	}
	total := time.Since(start)
	runtime.ReadMemStats(&after)
	alloc := (after.TotalAlloc - before.TotalAlloc) >> 20
	start = time.Now()
	doc, probs := check.ParsePDF(out)
	for i, p := range probs {
		if i < 3 {
			t.Error(p)
		}
	}
	t.Logf("150,000 words: %d pages, layout %v, layout and render %v, %d MB allocated, %d KB file, validator %v", len(doc.Pages), laid, total, alloc, len(out)>>10, time.Since(start))
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if total > 10*time.Second*timingFactor {
			t.Errorf("layout and render took %v", total)
		}
	})
	if alloc > 2000*uint64(timingFactor) {
		t.Errorf("%d MB allocated", alloc)
	}
}

// The cap of the export: 2,000,000 words lay out in bounded time and memory.
func TestPDFOfTheLargestBook(t *testing.T) {
	if testing.Short() || timingFactor > 1 {
		t.Skip("the largest book")
	}
	b := longBook(2_000_000)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	out, _, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	took := time.Since(start)
	t.Logf("2,000,000 words: %d KB file in %v, %d MB allocated, heap in use %d MB", len(out)>>10, took, (after.TotalAlloc-before.TotalAlloc)>>20, after.HeapInuse>>20)
	if after.HeapInuse>>20 > 3500 {
		t.Errorf("heap in use %d MB", after.HeapInuse>>20)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 120*time.Second {
			t.Errorf("took %v", took)
		}
	})
}

// longBook builds chapters of ordinary prose with some structure.
func longBook(words int) *Book {
	vocab := strings.Fields(loremWords)
	b := &Book{Title: "The Long Book", Byline: "A. Writer", Language: "en", Modified: bookOf(1).Modified, InputHash: "long"}
	notes := 0
	pos := 0
	perChapter := 2000
	for ch := 1; ch*perChapter <= words; ch++ {
		var md strings.Builder
		for p := 0; p < perChapter/80; p++ {
			for i := 0; i < 80; i++ {
				md.WriteString(vocab[pos%len(vocab)])
				pos++
				if i%11 == 10 {
					md.WriteString(",")
				}
				if i < 79 {
					md.WriteByte(' ')
				}
			}
			if p%9 == 4 {
				fmt.Fprintf(&md, " note[^n%d]", p)
			}
			md.WriteString(".\n\n")
			if p%9 == 4 {
				fmt.Fprintf(&md, "[^n%d]: A short footnote.\n\n", p)
			}
			if p%6 == 5 {
				md.WriteString("* * *\n\n")
			}
		}
		d := text.Structure(md.String(), text.StructOptions{NoteStart: notes})
		notes += len(d.Notes)
		b.Sections = append(b.Sections, Section{Kind: SecChapter, Title: fmt.Sprintf("Chapter title %d", ch), Number: ch, Level: 1, Nodes: d.Nodes, Notes: d.Notes})
	}
	b.Words = words
	return b
}
