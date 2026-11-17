package editiondiff

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

// A generated book: chapter c has paragraphs of words drawn from a vocabulary seeded by the chapter, so two
// chapters share some common words and almost no runs of three.
var vocab = func() []string {
	r := rand.New(rand.NewSource(7))
	const letters = "abcdefghijklmnopqrstuvwxyz"
	w := make([]string, 4000)
	for i := range w {
		n := 3 + r.Intn(7)
		b := make([]byte, n)
		for j := range b {
			b[j] = letters[r.Intn(len(letters))]
		}
		w[i] = string(b)
	}
	return w
}()

func para(r *rand.Rand, words int) string {
	var b strings.Builder
	for i := 0; i < words; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(vocab[r.Intn(len(vocab))])
	}
	return b.String() + "."
}

type chapter struct {
	title string
	paras []string
}

func (c chapter) text() string { return strings.Join(c.paras, "\n\n") + "\n" }

func genBook(seed int64, chapters, paras, words int) []chapter {
	r := rand.New(rand.NewSource(seed))
	out := make([]chapter, chapters)
	for i := range out {
		out[i].title = fmt.Sprintf("The %s of %s", vocab[r.Intn(len(vocab))], vocab[r.Intn(len(vocab))])
		for p := 0; p < paras; p++ {
			out[i].paras = append(out[i].paras, para(r, words))
		}
	}
	return out
}

func clone(bk []chapter) []chapter {
	out := make([]chapter, len(bk))
	for i, c := range bk {
		out[i] = chapter{title: c.title, paras: append([]string(nil), c.paras...)}
	}
	return out
}

func units(bk []chapter, ed string) []Unit {
	us := make([]Unit, len(bk))
	for i, c := range bk {
		us[i] = Unit{Key: fmt.Sprintf("%s%d", ed, i+1), Ord: i + 1, Kind: "chapter", Title: c.title, Path: fmt.Sprintf("OEBPS/%s%02d.xhtml", ed, i+1), Parent: -1, Text: c.text()}
	}
	return us
}

func compare(t testing.TB, a, b []chapter) *Result {
	t.Helper()
	// two editions: the same paths would pair by position, which is what real files do
	res, err := Compare(context.Background(), Input{Base: units(a, "e"), Head: units(b, "e"), Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func statuses(r *Result) []string {
	var out []string
	for _, s := range r.Steps {
		t := s.Status
		if len(s.Base) > 0 {
			t += ":" + s.Base[0].Title
		}
		out = append(out, t)
	}
	return out
}

func find(t testing.TB, r *Result, title string) Step {
	t.Helper()
	for _, s := range r.Steps {
		for _, h := range s.Head {
			if h.Title == title {
				return s
			}
		}
		for _, b := range s.Base {
			if b.Title == title {
				return s
			}
		}
	}
	t.Fatalf("no step for %q in %v", title, statuses(r))
	return Step{}
}

func TestIdenticalBooksAreAllUnchanged(t *testing.T) {
	bk := genBook(1, 12, 6, 40)
	r := compare(t, bk, clone(bk))
	for _, s := range r.Steps {
		if s.Status != StatusUnchanged || s.Words.Added != 0 || s.Words.Removed != 0 || s.Compared != ComparedEqual {
			t.Errorf("%+v", s)
		}
	}
	if r.Summary.WordsAdded != 0 || r.Summary.Changes != 0 || r.TooDifferent || r.Partial {
		t.Errorf("%+v", r.Summary)
	}
}

func TestEditedChaptersReportExactlyTheEdits(t *testing.T) {
	a := genBook(2, 10, 8, 50)
	b := clone(a)
	// chapter 3: one paragraph reworded (three words changed)
	words := strings.Fields(b[2].paras[4])
	words[5], words[6], words[7] = "qqqone", "qqqtwo", "qqqthree"
	b[2].paras[4] = strings.Join(words, " ")
	// chapter 5: a paragraph added (50 words) and one removed (50 words)
	add := para(rand.New(rand.NewSource(99)), 50)
	b[4].paras = append(b[4].paras[:2], append([]string{add}, b[4].paras[2:]...)...)
	b[4].paras = append(b[4].paras[:6], b[4].paras[7:]...)
	r := compare(t, a, b)
	three := find(t, r, a[2].title)
	if three.Status != StatusEdited || three.Words.Added != 3 || three.Words.Removed != 3 || three.Changes != 1 {
		t.Errorf("chapter 3: %+v", three)
	}
	five := find(t, r, a[4].title)
	if five.Status != StatusEdited || five.Words.Added != 50 || five.Words.Removed != 50 || five.Changes != 2 {
		t.Errorf("chapter 5: %+v", five)
	}
	for i, s := range r.Steps {
		if i != 2 && i != 4 && s.Status != StatusUnchanged {
			t.Errorf("step %d %s should be unchanged", i+1, s.Status)
		}
	}
	if r.Summary.WordsAdded != 53 || r.Summary.WordsRemoved != 53 || r.Summary.Edited != 2 || r.Summary.Changes != 3 {
		t.Errorf("summary %+v", r.Summary)
	}
}

func TestHunksOfAChapter(t *testing.T) {
	a := genBook(3, 6, 6, 30)
	b := clone(a)
	words := strings.Fields(b[1].paras[3])
	words[2] = "zzzedit"
	b[1].paras[3] = strings.Join(words, " ")
	b[1].paras = append(b[1].paras[:5], b[1].paras[6:]...) // paragraph 6 removed
	res, err := Compare(context.Background(), Input{Base: units(a, "e"), Head: units(b, "e"), Limits: Defaults(), Detail: "b2:he2"})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Steps[1]
	if len(st.Hunks) != 2 {
		t.Fatalf("hunks: %+v", st)
	}
	h := st.Hunks[0]
	if h.Kind != HunkReplace || h.Words.Added != 1 || h.Words.Removed != 1 || !strings.Contains(h.Head, "zzzedit") || !strings.Contains(h.Base, words[3]) {
		t.Errorf("hunk 1: %+v", h)
	}
	var sawDel, sawIns bool
	for _, sg := range h.Segments {
		if sg.Op == "delete" && sg.Text != "" {
			sawDel = true
		}
		if sg.Op == "insert" && sg.Text == "zzzedit" {
			sawIns = true
		}
	}
	if !sawDel || !sawIns {
		t.Errorf("segments %+v", h.Segments)
	}
	// the head offsets locate the second text's paragraphs in it
	head := trimTail(b[1].text())
	if head[h.HeadStart:h.HeadEnd] != h.Head {
		t.Errorf("offsets: %q vs %q", head[h.HeadStart:h.HeadEnd], h.Head)
	}
	r := st.Hunks[1]
	if r.Kind != HunkRemoved || r.HeadStart != r.HeadEnd || r.Words.Removed != 30 {
		t.Errorf("hunk 2: %+v", r)
	}
	if st.Hunks[0].ID == st.Hunks[1].ID || st.Hunks[0].ID == "" {
		t.Error("ids")
	}
	// the hunks are not returned for any other step
	if len(res.Steps[0].Hunks) != 0 || len(res.Steps[2].Hunks) != 0 {
		t.Error("hunks for another step")
	}
}

func TestMatchingCases(t *testing.T) {
	a := genBook(4, 12, 6, 60)
	b := clone(a)
	// renamed: chapter 2 keeps its text, new title (paired by path, then flagged renamed)
	b[1].title = "A new name"
	// moved: chapter 9 moves to the front; its path changes with the position, as files do
	moved := b[8]
	b = append([]chapter{moved}, append(b[:8], b[9:]...)...)
	// removed: chapter 4 (originally 5th) dropped
	removed := a[4].title
	for i := range b {
		if b[i].title == removed {
			b = append(b[:i], b[i+1:]...)
			break
		}
	}
	// added: a new chapter at the end
	b = append(b, chapter{title: "A brand new chapter", paras: genBook(50, 1, 5, 60)[0].paras})
	// split: chapter 7 into two
	var ch7 chapter
	for i := range b {
		if b[i].title == a[6].title {
			ch7 = b[i]
			half := len(ch7.paras) / 2
			first := chapter{title: ch7.title, paras: ch7.paras[:half]}
			second := chapter{title: "The second half", paras: ch7.paras[half:]}
			b = append(b[:i], append([]chapter{first, second}, b[i+1:]...)...)
			break
		}
	}
	// merged: chapters 11 and 12 into one
	for i := range b {
		if b[i].title == a[10].title {
			j := i + 1
			if b[j].title != a[11].title {
				t.Fatalf("unexpected order at %d: %q", j, b[j].title)
			}
			b[i].paras = append(append([]string(nil), b[i].paras...), b[j].paras...)
			b = append(b[:j], b[j+1:]...)
			break
		}
	}
	r := compare(t, a, b)

	if s := find(t, r, a[8].title); s.Status != StatusMoved && !s.Moved {
		t.Errorf("moved: %+v", s)
	} else if s.Match == nil || s.Match.Method == "" {
		t.Errorf("moved has no match: %+v", s)
	}
	if s := find(t, r, "A new name"); s.Status != StatusEdited || !s.Renamed || s.Words.Added != 0 || s.Words.Removed != 0 {
		t.Errorf("renamed: %+v", s)
	} else if s.Match.Method != MethodSimilarity && s.Match.Method != MethodPath {
		t.Errorf("renamed matched by %s", s.Match.Method)
	}
	if s := find(t, r, removed); s.Status != StatusRemoved || s.Match != nil || s.Words.Removed != 360 {
		t.Errorf("removed: %+v", s)
	}
	if s := find(t, r, "A brand new chapter"); s.Status != StatusAdded || s.Words.Added != 300 {
		t.Errorf("added: %+v", s)
	}
	sp := find(t, r, a[6].title)
	if sp.Status != StatusSplit || len(sp.Head) != 2 || len(sp.Base) != 1 {
		t.Errorf("split: %+v", sp)
	}
	if sp.Words.Added != 0 || sp.Words.Removed != 0 {
		t.Errorf("a split loses no words: %+v", sp.Words)
	}
	mg := find(t, r, a[10].title)
	if mg.Status != StatusMerged || len(mg.Base) != 2 || len(mg.Head) != 1 {
		t.Errorf("merged: %+v", mg)
	}
	if mg.Words.Added != 0 || mg.Words.Removed != 0 {
		t.Errorf("a merge loses no words: %+v", mg.Words)
	}
	// every chapter of both books is in exactly one step
	nb, nh := 0, 0
	for _, s := range r.Steps {
		nb += len(s.Base)
		nh += len(s.Head)
	}
	if nb != len(a) {
		t.Errorf("%d base chapters in the steps, want %d", nb, len(a))
	}
	if want := len(b) + 0; nh != want {
		t.Errorf("%d head chapters in the steps, want %d", nh, want)
	}
	// indexes run from 1 and keys are unique
	keys := map[string]bool{}
	for i, s := range r.Steps {
		if s.Index != i+1 || keys[s.Key] {
			t.Errorf("step %d: index %d key %q", i+1, s.Index, s.Key)
		}
		keys[s.Key] = true
	}
}

func TestTitleAndSimilarityAreGuesses(t *testing.T) {
	a := genBook(5, 6, 6, 60)
	b := clone(a)
	// paths differ completely (a different tool made the second edition), so only title and text can pair them
	ub, uh := units(a, "e"), units(b, "e")
	for i := range uh {
		uh[i].Path = fmt.Sprintf("text/section-%03d.html", 100-i)
	}
	// chapter 2: same title, text reworded in every paragraph (shares little)
	for i := range b[1].paras {
		w := strings.Fields(b[1].paras[i])
		for j := 1; j < len(w); j += 3 {
			w[j] = "x" + w[j]
		}
		b[1].paras[i] = strings.Join(w, " ")
	}
	uh[1].Text = b[1].text()
	// chapter 4: title changed, text the same
	uh[3].Title = "Another title altogether"
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	s := find(t, res, a[3].title)
	if s.Match == nil || s.Match.Method != MethodSimilarity || !s.Match.Guess || s.Match.Score < 0.9 || !s.Renamed {
		t.Errorf("similarity: %+v", s.Match)
	}
	s = find(t, res, a[0].title)
	if s.Match == nil || s.Match.Method != MethodTitle || !s.Match.Guess {
		t.Errorf("title: %+v", s.Match)
	}
	// a chapter reworded throughout under the same title stays one chapter, edited
	s = find(t, res, a[1].title)
	if s.Status != StatusEdited || s.Match == nil || s.Match.Method != MethodTitle {
		t.Errorf("reworded: %+v", s)
	}
}

func TestPositionalTitlesAreNotPairedOnTheirOwn(t *testing.T) {
	a := genBook(6, 4, 5, 60)
	b := genBook(7, 4, 5, 60) // an unrelated book
	for i := range a {
		a[i].title = fmt.Sprintf("Chapter %d", i+1)
		b[i].title = fmt.Sprintf("Chapter %d", i+1)
	}
	ub, uh := units(a, "e"), units(b, "e")
	for i := range uh {
		uh[i].Path = "other" + uh[i].Path
	}
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Steps {
		if s.Status != StatusAdded && s.Status != StatusRemoved {
			t.Errorf("unrelated chapters were paired: %+v", s)
		}
	}
}

func TestProjectModePairsByRecordedMappingThenPath(t *testing.T) {
	a := genBook(8, 5, 5, 50)
	ub := units(a, "e")
	var uh []Unit
	for i, c := range a {
		uh = append(uh, Unit{Key: fmt.Sprintf("doc%d", i), ID: fmt.Sprintf("doc%d", i), Title: c.title, Kind: "chapter", Parent: -1, Text: c.text()})
	}
	ub[0].Mapped = "doc0"
	ub[1].Mapped = "doc1"
	// chapter 3's mapping is gone (its document was replaced) but its document still names its source file
	uh[2].Path = ub[2].Path
	// chapter 2's document was rewritten entirely, and renamed: the recorded mapping still holds
	uh[1].Text = genBook(9, 1, 5, 50)[0].text()
	uh[1].Title = "Something else"
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, ProjectMode: true, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Steps[1]
	if s.Match == nil || s.Match.Method != MethodRecorded || s.Match.Score != 1 || s.Match.Guess || s.Status != StatusEdited {
		t.Errorf("recorded: %+v", s)
	}
	s = res.Steps[2]
	if s.Match == nil || s.Match.Method != MethodPath || s.Match.Guess || s.Status != StatusUnchanged {
		t.Errorf("path: %+v", s)
	}
}

func TestMergedByTheImportIsMergedAndUnchanged(t *testing.T) {
	a := genBook(10, 3, 4, 40)
	ub := units(a, "e")
	ub[0].Mapped, ub[1].Mapped = "doc0", "doc0"
	ub[2].Mapped = "doc1"
	lim := Defaults()
	joined := joinPlain(ub[0].Text, ub[1].Text)
	uh := []Unit{
		{Key: "doc0", ID: "doc0", Title: a[0].title, Parent: -1, Text: joined},
		{Key: "doc1", ID: "doc1", Title: a[2].title, Parent: -1, Text: ub[2].Text},
	}
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, ProjectMode: true, Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 2 || res.Steps[0].Status != StatusMerged || len(res.Steps[0].Base) != 2 || res.Steps[0].Compared != ComparedEqual || res.Steps[0].Match.Method != MethodRecorded {
		t.Errorf("%+v", res.Steps)
	}
}

func TestSpacingOnlyDifferencesAreNotChanges(t *testing.T) {
	a := genBook(11, 3, 4, 30)
	b := clone(a)
	ub, uh := units(a, "e"), units(b, "e")
	uh[0].Text = strings.ReplaceAll(uh[0].Text, "\n\n", "\n\n\n") + "\n\n"
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps[0].Status != StatusUnchanged || res.Steps[0].Changes != 0 || res.Summary.Changes != 0 {
		t.Errorf("%+v", res.Steps[0])
	}
}

func TestEmptyBooksAndEmptyChapters(t *testing.T) {
	res, err := Compare(context.Background(), Input{Limits: Defaults()})
	if err != nil || len(res.Steps) != 0 || res.TooDifferent {
		t.Fatalf("%v %+v", err, res)
	}
	us := []Unit{{Key: "e1", Ord: 1, Title: "Part One", Kind: "part", Parent: -1}, {Key: "e2", Ord: 2, Title: "Empty", Parent: 0, Text: ""}}
	res, err = Compare(context.Background(), Input{Base: us, Head: us, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Steps {
		if s.Status != StatusUnchanged {
			t.Errorf("%+v", s)
		}
	}
}

func TestACancelledContextStopsTheComparison(t *testing.T) {
	a := genBook(12, 5, 5, 50)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compare(ctx, Input{Base: units(a, "e"), Head: units(a, "e"), Limits: Defaults()}); err == nil {
		t.Error("a cancelled comparison ran")
	}
}

// ---- budgets and hostile input ------------------------------------------------------------------------------------

func bigBook(chapters, wordsPerChapter int) []chapter {
	return genBook(100, chapters, wordsPerChapter/60, 60)
}

func TestBudgetTheBookAgainstItsOwnEdition(t *testing.T) {
	// 90,000 words in 40 chapters, a few edits in every chapter: under two seconds
	a := bigBook(40, 2250)
	b := clone(a)
	r := rand.New(rand.NewSource(5))
	for i := range b {
		for k := 0; k < 3; k++ {
			p := r.Intn(len(b[i].paras))
			w := strings.Fields(b[i].paras[p])
			w[r.Intn(len(w))] = fmt.Sprintf("edit%d", k)
			b[i].paras[p] = strings.Join(w, " ")
		}
	}
	start := time.Now()
	res := compare(t, a, b)
	el := time.Since(start)
	t.Logf("90k words, 40 chapters, 120 edits: %v", el)
	if res.Summary.Edited != 40 || res.Summary.Changes < 100 {
		t.Errorf("%+v", res.Summary)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 2 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
}

func TestBudgetTwoEditionsOfThreeHundredPages(t *testing.T) {
	// two editions that share most of their text and differ in paths and titles: under five seconds
	a := bigBook(40, 2250)
	b := clone(a)
	r := rand.New(rand.NewSource(6))
	for i := range b {
		b[i].title = a[i].title + " (revised)"
		for k := 0; k < 8; k++ {
			p := r.Intn(len(b[i].paras))
			b[i].paras[p] = para(r, 60)
		}
	}
	// drop two chapters, add one
	b = append(b[:10], b[12:]...)
	b = append(b, genBook(77, 1, 30, 60)[0])
	ub, uh := units(a, "a"), units(b, "b")
	start := time.Now()
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("compare 2 x 90k words: %v, %+v", el, res.Summary)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 5 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
	if res.Summary.Removed != 2 || res.Summary.Added != 1 || res.Summary.Edited != 38 {
		t.Errorf("%+v", res.Summary)
	}
}

func TestTwoHugeUnrelatedBooksAreTooDifferent(t *testing.T) {
	a := genBook(200, 500, 30, 60) // 900,000 words
	b := genBook(201, 500, 30, 60)
	// every chapter has a distinct title so nothing can pair on a name
	for i := range a {
		a[i].title = fmt.Sprintf("Alpha %d", i)
		b[i].title = fmt.Sprintf("Beta %d", i)
	}
	ub, uh := units(a, "a"), units(b, "b")
	for i := range uh {
		uh[i].Path = fmt.Sprintf("z/%d.html", i)
	}
	start := time.Now()
	res, err := Compare(context.Background(), Input{Base: ub, Head: uh, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("two unrelated 900k-word books: %v", el)
	if !res.TooDifferent || res.Summary.Added != 500 || res.Summary.Removed != 500 || len(res.Notices) == 0 {
		t.Errorf("%+v %v", res.Summary, res.Notices)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 10 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
}

func TestAChapterLargerThanADiffTakesIsComparedByEquality(t *testing.T) {
	big := strings.Repeat("alpha beta gamma delta epsilon\n", 16<<20/31) // 16 MB
	big2 := big + "one more line\n"
	us := []Unit{{Key: "e1", Ord: 1, Title: "Huge", Parent: -1, Text: big}}
	vs := []Unit{{Key: "e1", Ord: 1, Title: "Huge", Parent: -1, Text: big2}}
	lim := Defaults()
	lim.MaxTotalBytes = 64 << 20
	start := time.Now()
	var res *Result
	var err error
	measure(t, "16 MB chapter", func() { res, err = Compare(context.Background(), Input{Base: us, Head: vs, Limits: lim}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("16 MB chapter: %v", time.Since(start))
	s := res.Steps[0]
	if s.Status != StatusEdited || s.Compared != ComparedNot || s.Note == "" || res.Summary.Uncounted != 1 {
		t.Errorf("%+v", s)
	}
	// identical ones are still known to be identical
	res, err = Compare(context.Background(), Input{Base: us, Head: us, Limits: lim})
	if err != nil || res.Steps[0].Status != StatusUnchanged {
		t.Errorf("%v %+v", err, res.Steps)
	}
}

func TestTooMuchTextIsRefused(t *testing.T) {
	us := []Unit{{Key: "e1", Title: "x", Text: strings.Repeat("a b c\n", 100)}}
	lim := Defaults()
	lim.MaxTotalBytes = 100
	if _, err := Compare(context.Background(), Input{Base: us, Head: us, Limits: lim}); err == nil {
		t.Error("too much text was compared")
	}
	lim = Defaults()
	lim.MaxUnits = 1
	if _, err := Compare(context.Background(), Input{Base: append(us, us...), Head: us, Limits: lim}); err == nil {
		t.Error("too many chapters were compared")
	}
}

func TestAMillionTinyParagraphsStayWithinTheBudget(t *testing.T) {
	// 2 MB of one-letter paragraphs on each side, differing throughout: the line pass gives up, the budget holds
	var a, b strings.Builder
	for i := 0; i < 1_000_000; i++ {
		a.WriteString("a\n")
		if i%2 == 0 {
			b.WriteString("b\n")
		} else {
			b.WriteString("a\n")
		}
	}
	us := []Unit{{Key: "e1", Ord: 1, Title: "Dust", Parent: -1, Text: a.String()}}
	vs := []Unit{{Key: "e1", Ord: 1, Title: "Dust", Parent: -1, Text: b.String()}}
	lim := Defaults()
	start := time.Now()
	res, err := Compare(context.Background(), Input{Base: us, Head: vs, Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("a million tiny paragraphs: %v, %s %s", el, res.Steps[0].Compared, res.Steps[0].Note)
	s := res.Steps[0]
	if s.Status != StatusEdited || s.Compared != ComparedNot || s.Note == "" {
		t.Errorf("%+v", s)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 8 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
}

func TestRepeatedTextDoesNotMakeTheMatchingQuadratic(t *testing.T) {
	// 3,000 chapters that are all the same refrain, on both sides, with different endings: the index must skip
	// the refrain, and the comparison must end quickly with every chapter accounted for
	refrain := strings.Repeat("it was the best of times it was the worst of times ", 200)
	var us, vs []Unit
	for i := 0; i < 3000; i++ {
		us = append(us, Unit{Key: fmt.Sprintf("a%d", i), Ord: i + 1, Title: "Untitled", Parent: -1, Text: refrain + fmt.Sprintf(" ending number %d of the old", i)})
		vs = append(vs, Unit{Key: fmt.Sprintf("b%d", i), Ord: i + 1, Title: "Untitled", Parent: -1, Text: refrain + fmt.Sprintf(" ending number %d of the new", i)})
	}
	start := time.Now()
	res, err := Compare(context.Background(), Input{Base: us, Head: vs, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("3000 identical refrains: %v, %+v", el, res.Summary)
	nb, nh := 0, 0
	for _, s := range res.Steps {
		nb += len(s.Base)
		nh += len(s.Head)
	}
	if nb != 3000 || nh != 3000 {
		t.Errorf("%d and %d chapters accounted for", nb, nh)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 10 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
}

func TestARefrainInEveryChapterDoesNotHideTheChaptersThatDiffer(t *testing.T) {
	refrain := strings.Repeat("it was the best of times it was the worst of times ", 50)
	var us, vs []Unit
	for i := 0; i < 300; i++ {
		us = append(us, Unit{Key: fmt.Sprintf("a%d", i), Ord: i + 1, Title: "Untitled", Parent: -1, Text: refrain + fmt.Sprintf(" ending number %d of the old", i)})
		vs = append(vs, Unit{Key: fmt.Sprintf("b%d", i), Ord: i + 1, Title: "Untitled", Parent: -1, Text: refrain + fmt.Sprintf(" ending number %d of the new", i)})
	}
	res, err := Compare(context.Background(), Input{Base: us, Head: vs, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Edited != 300 || res.Summary.Added != 0 {
		t.Errorf("%+v", res.Summary)
	}
	for i, s := range res.Steps {
		if s.Base[0].Ord != s.Head[0].Ord || s.Match.Method != MethodSimilarity || !s.Match.Guess {
			t.Fatalf("step %d paired wrongly: %+v", i, s)
		}
	}
}

func TestPathologicalRepeatedLinesDoNotHang(t *testing.T) {
	// a text of the same line repeated, and the same with one in ten changed: the classic worst case for a naive diff
	var a, b strings.Builder
	for i := 0; i < 120_000; i++ {
		a.WriteString("same line again\n")
		if i%10 == 0 {
			b.WriteString("another line\n")
		} else {
			b.WriteString("same line again\n")
		}
	}
	us := []Unit{{Key: "e1", Ord: 1, Title: "Loop", Parent: -1, Text: a.String()}}
	vs := []Unit{{Key: "e1", Ord: 1, Title: "Loop", Parent: -1, Text: b.String()}}
	start := time.Now()
	res, err := Compare(context.Background(), Input{Base: us, Head: vs, Limits: Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("120,000 repeated lines, one in ten changed: %v, %s", el, res.Steps[0].Compared)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 8 * time.Second
		if raceEnabled {
			limit *= 8
		}
		if el > limit {
			t.Errorf("took %v", el)
		}
	})
}

func TestTheBudgetOfTheWholeComparisonIsShared(t *testing.T) {
	// many chapters that each need real work: when the budget is gone the rest are listed as not compared, never hung
	a := genBook(300, 40, 5, 60)
	b := clone(a)
	for i := range b {
		for p := range b[i].paras {
			if p%2 == 0 {
				b[i].paras[p] = para(rand.New(rand.NewSource(int64(i*100+p))), 60)
			}
		}
	}
	lim := Defaults()
	lim.DiffBudget = 20_000
	res, err := Compare(context.Background(), Input{Base: units(a, "e"), Head: units(b, "e"), Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Partial || res.Summary.Uncounted == 0 {
		t.Errorf("the budget was not spent: %+v", res.Summary)
	}
	for _, s := range res.Steps {
		if s.Compared == ComparedNot && (s.Note == "" || s.Status != StatusEdited) {
			t.Errorf("%+v", s)
		}
	}
	// the same texts and limits give the same answer
	res2, err := Compare(context.Background(), Input{Base: units(a, "e"), Head: units(b, "e"), Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Summary != res.Summary {
		t.Errorf("not deterministic: %+v vs %+v", res.Summary, res2.Summary)
	}
}

// measure runs f and logs what it allocated in all and what the live heap held at its end; the hostile cases must
// not need a gigabyte.
func measure(t *testing.T, what string, f func()) {
	t.Helper()
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	total := (b.TotalAlloc - a.TotalAlloc) >> 20
	t.Logf("%s: %d MiB allocated in all", what, total)
	if total > 1500 {
		t.Errorf("%s allocated %d MiB", what, total)
	}
}

func TestHostileCasesAreBoundedInMemory(t *testing.T) {
	big := strings.Repeat("alpha beta gamma delta epsilon\n", 2<<20/31)
	cases := map[string]Input{}
	cases["two 2 MiB chapters of five repeated words"] = Input{Base: []Unit{{Key: "e1", Title: "x", Parent: -1, Text: big}}, Head: []Unit{{Key: "e1", Title: "x", Parent: -1, Text: big + "x\n"}}, Limits: Defaults()}
	a := genBook(300, 300, 30, 60)
	b := genBook(301, 300, 30, 60)
	for i := range a {
		a[i].title, b[i].title = fmt.Sprintf("A%d", i), fmt.Sprintf("B%d", i)
	}
	cases["two unrelated 540,000-word books"] = Input{Base: units(a, "a"), Head: units(b, "b"), Limits: Defaults()}
	var l1, l2 strings.Builder
	for i := 0; i < 120_000; i++ {
		l1.WriteString("same line again\n")
		if i%10 == 0 {
			l2.WriteString("another line\n")
		} else {
			l2.WriteString("same line again\n")
		}
	}
	cases["120,000 repeated lines"] = Input{Base: []Unit{{Key: "e1", Title: "x", Parent: -1, Text: l1.String()}}, Head: []Unit{{Key: "e1", Title: "x", Parent: -1, Text: l2.String()}}, Limits: Defaults()}
	for name, in := range cases {
		measure(t, name, func() {
			if _, err := Compare(context.Background(), in); err != nil {
				t.Error(err)
			}
		})
	}
}
