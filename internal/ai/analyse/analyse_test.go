package analyse

import (
	"math/rand"
	"strings"
	"testing"
)

func TestSentences(t *testing.T) {
	s := "Mr. Nolan met Dr. Who at 3.5 miles. She said \"No.\" He left! Was it J. R. Smith? Yes… Of course."
	var got []string
	for _, sp := range Sentences(s) {
		got = append(got, s[sp[0]:sp[1]])
	}
	want := []string{"Mr. Nolan met Dr. Who at 3.5 miles.", "She said \"No.\"", "He left!", "Was it J. R. Smith?", "Yes…", "Of course."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestLongSentencesAndHedges(t *testing.T) {
	long := "Word " + strings.Repeat("word ", 44) + "end."
	body := "Short one. " + long + " Perhaps she was quite tired, I think.\n\n## Heading, perhaps\n\n```\nperhaps in code  \n```\n"
	marks, ok := Run(body, Options{Language: "en"})
	if !ok {
		t.Fatal("not analysed")
	}
	var longs, hedges []string
	for _, m := range marks {
		switch m.Kind {
		case KindLong:
			longs = append(longs, m.Reason)
			if body[m.Start:m.End] != strings.TrimSpace(long) {
				t.Fatalf("long sentence range %q", body[m.Start:m.End])
			}
		case KindHedge:
			hedges = append(hedges, strings.ToLower(body[m.Start:m.End]))
		}
		if m.Fix != nil && m.Kind != KindPunct {
			t.Fatalf("a fix on %s", m.Kind)
		}
	}
	if len(longs) != 1 || longs[0] != "46 words in one sentence" {
		t.Fatalf("%v", longs)
	}
	if strings.Join(hedges, ",") != "perhaps,quite,i think" {
		t.Fatalf("hedges %v: the heading and the code fence are not looked at", hedges)
	}
	// a threshold, and another language has no English hedges
	if m, _ := Run(body, Options{Kinds: []string{KindLong}, LongWords: 100}); len(m) != 0 {
		t.Fatal("threshold ignored")
	}
	if m, _ := Run(body, Options{Kinds: []string{KindHedge}, Language: "de"}); len(m) != 0 {
		t.Fatal("English hedges in German")
	}
}

func TestPunctuationFixes(t *testing.T) {
	body := "Two  spaces here. Commas,, and colons:: repeat. Space before , and ! ; done. Dots.. and ellipsis... fine.  \nSecond line.\n"
	marks, _ := Run(body, Options{Kinds: []string{KindPunct}, Language: "en"})
	var fixes []string
	for _, m := range marks {
		if m.Fix == nil {
			t.Fatal("a punctuation mark without a fix")
		}
		fixes = append(fixes, body[m.Start:m.End]+"->"+*m.Fix)
		// applying one fix alone yields text that is the original but for exactly that range
		if body[m.Start:m.End] == *m.Fix {
			t.Fatalf("a fix that changes nothing: %+v", m)
		}
	}
	got := strings.Join(fixes, "|")
	for _, want := range []string{"  -> ", ",,->,", "::->:", " ->"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "...->") || strings.Contains(got, "\n") {
		t.Fatalf("an ellipsis or a hard break was touched: %q", got)
	}
	// French sets a space before ; : ! and ?
	fr, _ := Run("Quoi ! Vraiment ?\n", Options{Kinds: []string{KindPunct}, Language: "fr"})
	if len(fr) != 0 {
		t.Fatalf("%+v", fr)
	}
	if en, _ := Run("Really ! Truly ?\n", Options{Kinds: []string{KindPunct}, Language: "en"}); len(en) != 2 {
		t.Fatalf("%+v", en)
	}
}

// A private note or a ghost that holds many other skipped spans (code, addresses,
// wiki links) hides everything inside it, however many there are.
func TestHiddenRangesHideEverythingInsideThemWhateverTheyHold(t *testing.T) {
	inner := strings.Repeat("`c` https://x.example/a [[W]] ", 12)
	for name, body := range map[string]string{
		"note":  "Start of it. %% " + inner + "perhaps  quite, really,, %% end of it.\n",
		"ghost": "Start of it. <scriobh-ghost>" + inner + "perhaps  quite, really,,</scriobh-ghost> end of it.\n",
	} {
		marks, ok := Run(body, Options{Language: "en"})
		if !ok {
			t.Fatal(name)
		}
		lo := strings.Index(body, "perhaps")
		hi := strings.Index(body, "end of it")
		for _, m := range marks {
			if m.Start >= lo-len(inner) && m.End <= hi {
				t.Errorf("%s: a mark inside the hidden range: %+v %q", name, m, body[m.Start:m.End])
			}
		}
	}
	// and the words outside are still found
	marks, _ := Run("Perhaps so. %% "+inner+" %% Perhaps not.\n", Options{Kinds: []string{KindHedge}})
	if len(marks) != 2 {
		t.Fatalf("%+v", marks)
	}
}

func TestAnalysersNeverPanicAndStayBounded(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	alphabet := []string{"a", "perhaps", " ", "  ", ".", "!", "\n", "\n\n", "%%", "`", "\"", "“", "”", "<scriobh-ghost>", "</scriobh-ghost>", "é", "\U0001F600", ",,", "https://x.y", "[[w]]", "# h\n", "```\n"}
	for i := 0; i < 1500; i++ {
		var b strings.Builder
		for n := r.Intn(120); n > 0; n-- {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		body := b.String()
		marks, ok := Run(body, Options{Language: "en"})
		if !ok {
			continue
		}
		if len(marks) > MaxMarks {
			t.Fatalf("%d marks", len(marks))
		}
		for _, m := range marks {
			if m.Start < 0 || m.End > len(body) || m.Start >= m.End {
				t.Fatalf("a range outside the body: %+v in %q", m, body)
			}
			if strings.Contains(body[m.Start:m.End], "%%") && m.Kind != KindLong {
				t.Fatalf("a private note was marked: %q", body[m.Start:m.End])
			}
		}
	}
	// a large document is analysed in bounded time with a bounded result
	big := strings.Repeat("Perhaps  she was quite tired,, and so it goes. ", 50000)
	marks, _ := Run(big, Options{})
	if len(marks) > MaxMarks {
		t.Fatalf("%d", len(marks))
	}
}

func FuzzRun(f *testing.F) {
	for _, s := range []string{"Perhaps  a.\n\n\"b\" “c”", "%% half a note", "<scriobh-ghost>x", "```\ncode", "a,,b..c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		marks, ok := Run(body, Options{})
		if !ok {
			return
		}
		for _, m := range marks {
			if m.Start < 0 || m.End > len(body) || m.Start >= m.End {
				t.Fatalf("%+v", m)
			}
		}
	})
}
