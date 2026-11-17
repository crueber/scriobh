package compile_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/testutil"
)

func compileIn(pid, preset string) compile.CompileIn {
	return compile.CompileIn{ProjectID: pid, Preset: preset}
}

// prose makes n words that do not repeat, so the compressed size and the time are those of a real book.
func prose(seed *uint32, n int) string {
	vocab := strings.Fields("the of and a to in was he she it that with as for his her on at by they had from but not were this which when there their said what one all out up some could into time then them would more over after before water salt harbour boat quay tide night light grey stone road door window hand voice morning evening silence nobody something nothing counted carried watched waited walked turned opened closed")
	var b strings.Builder
	for i := 0; i < n; i++ {
		*seed = *seed*1664525 + 1013904223
		w := vocab[int(*seed>>16)%len(vocab)]
		switch {
		case i%47 == 0:
			w = "*" + w + "*"
		case i%113 == 0:
			w = "**" + w + "**"
		}
		b.WriteString(w)
		if i%60 == 59 {
			b.WriteString(".\n\n")
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

const lorem = "The boat came in at *dawn*, and the **harbour** was grey with salt, [[Harbour]] and gulls and the smell of tar; nobody on the quay said a word, " +
	"because there was nothing left to say that the tide had not already said twice. She counted the nets, then the ropes, then the lamps.[^n]\n\n"

// A 120,000-word, 60-chapter book compiles to a DOCX and an EPUB in bounded
// time and memory. The bounds are generous (the target in ARCHITECTURE is under
// 10 s for 150,000 words); what they guard against is a slip to quadratic work.
func TestCompileOfALongBook(t *testing.T) {
	if testing.Short() {
		t.Skip("long book")
	}
	f := newFx(t, "The Long Book")
	f.setContact()
	var seed uint32 = 7
	words := 0
	for i := 1; i <= 60; i++ {
		body := prose(&seed, 1900) + "A note.[^n] [[Harbour]]\n\n* * *\n\n" + prose(&seed, 100) + "\n[^n]: A note about the nets.\n"
		d := f.doc(fmt.Sprintf("Chapter title %d", i), "chapter", "", body)
		words += d.WordCount
	}
	if words < 118_000 {
		t.Fatalf("only %d words", words)
	}
	for _, c := range []struct {
		preset string
		limit  time.Duration
		valid  func([]byte) []string
	}{
		{"shunn-us", 40 * time.Second, check.DOCX},
		{"epub3", 40 * time.Second, check.EPUB},
		{"plain-markdown", 20 * time.Second, nil},
	} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		out := f.compile(c.preset, publish.Options{})
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		alloc := (after.TotalAlloc - before.TotalAlloc) >> 20
		t.Logf("%s: %d words, %d bytes, %v, %d MB allocated, %d MB heap in use after", c.preset, out.Words, out.Bytes, took.Round(time.Millisecond), alloc, after.HeapAlloc>>20)
		c := c
		t.Run("time "+c.preset, func(t *testing.T) {
			testutil.Timing(t)
			if took > c.limit*timingFactor {
				t.Errorf("%s took %v", c.preset, took)
			}
		})
		if alloc > 3000 {
			t.Errorf("%s allocated %d MB", c.preset, alloc)
		}
		if out.Words < 115_000 {
			t.Errorf("%s compiled %d words", c.preset, out.Words)
		}
		if c.valid != nil {
			start = time.Now()
			if probs := c.valid(out.Body); len(probs) > 0 {
				t.Errorf("%s: %v", c.preset, probs[:min(5, len(probs))])
			}
			t.Logf("%s validated in %v", c.preset, time.Since(start).Round(time.Millisecond))
		}
	}
	start := time.Now()
	p := f.preview("shunn-us", publish.Options{})
	t.Logf("preview: %v, %d pages estimated", time.Since(start).Round(time.Millisecond), p.PageCount)
	if p.PageCount < 300 || p.PageCount > 700 {
		t.Errorf("an estimate of %d pages for 120,000 words at 250 words a page is off", p.PageCount)
	}
}
