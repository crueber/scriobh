// Package bench is the performance gate behind `scriobh bench`: it builds
// synthetic projects of a stated size through the real op registry, times the
// operations the budgets in docs/ARCHITECTURE.md name, and compares the result
// with a committed baseline and with the absolute budgets.
//
// The data is deterministic (a fixed seed), so two runs build the same book and
// differences between runs are the machine's, not the generator's.
package bench

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

// Seed is the generator's seed. Changing it (or the generator) changes every
// number, so it is recorded in the baseline.
const Seed = 20260401

// GeneratorVersion is bumped whenever the generated text or structure changes.
const GeneratorVersion = 1

// Size is one project size to build.
type Size struct {
	Name  string
	Words int
}

// ParseSizes reads a comma-separated list such as "150k,500k,1m". A bare number
// is a word count; the suffixes k and m mean thousands and millions.
func ParseSizes(list string) ([]Size, error) {
	var out []Size
	for _, f := range strings.Split(list, ",") {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		mult, num := 1, f
		switch {
		case strings.HasSuffix(f, "k"):
			mult, num = 1000, strings.TrimSuffix(f, "k")
		case strings.HasSuffix(f, "m"):
			mult, num = 1_000_000, strings.TrimSuffix(f, "m")
		}
		n, err := strconv.ParseFloat(num, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q is not a size (use 150k, 500k, 1m or a word count)", f)
		}
		out = append(out, Size{Name: f, Words: int(n * float64(mult))})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no sizes given")
	}
	return out, nil
}

// Fixture is the generated project and the ids the measurements use.
type Fixture struct {
	ProjectID, Slug string
	Words, Docs     int
	// Chapters are the standalone ~5,000-word chapters that open, save, diff
	// and revision measurements run on.
	Chapters []string
	// Scenes are scene ids, for sampling "open a scene".
	Scenes []string
	// Folders are chapter ids that hold scenes.
	Folders []string
	// Titles of the chapters that hold scenes, for wiki links.
	CommonQuery, RareQuery, PrefixQuery string
	// Revisions, per measured chapter: the revision ids, oldest first.
	ChapterRevs map[string][]string
}

// Counts of the generated book, for the report.
type shape struct {
	bigChapters, bigWords, scenes, chapters, parts int
}

func shapeFor(words int) shape {
	var s shape
	s.bigChapters = words / 150_000
	if s.bigChapters < 1 {
		s.bigChapters = 1
	}
	if s.bigChapters > 4 {
		s.bigChapters = 4
	}
	s.bigWords = 5000
	if words < 40_000 {
		s.bigWords = words / 8
		if s.bigWords < 400 {
			s.bigWords = 400
		}
	}
	rest := words - s.bigChapters*s.bigWords
	if rest < 0 {
		rest = 0
	}
	const sceneAvg, introWords = 560, 80
	s.scenes = rest / sceneAvg
	if s.scenes < 4 {
		s.scenes = 4
	}
	s.chapters = (s.scenes + 7) / 8
	s.parts = (s.chapters + 9) / 10
	return s
}

// ---- prose --------------------------------------------------------------------------

var common = strings.Fields(`the of and to a in he she it was that his her with as for had on at they but not by
 from him be said were you this have which one all up out there their when would who so been into then down more
 could them like over only now other did than back about after through before again where what too little long
 thought hand eyes door light water night stone road morning window voice room street river field wind house
 hours years name side door hands face head word`)

var syll = []string{"ba", "bel", "cor", "dan", "el", "fen", "gar", "hal", "is", "jor", "kel", "lin", "mar", "nor",
	"or", "pen", "quin", "ros", "sel", "tor", "ul", "vern", "wyn", "yar", "zel", "ar", "en", "ith", "ow", "an", "ey"}

func vocabulary() []string {
	v := make([]string, 0, 6000)
	v = append(v, common...)
	seen := map[string]bool{}
	for _, w := range v {
		seen[w] = true
	}
	// Pronounceable made-up words fill the long tail, in a fixed order.
	for a := 0; a < len(syll) && len(v) < 6000; a++ {
		for b := 0; b < len(syll) && len(v) < 6000; b++ {
			for c := 0; c < len(syll) && len(v) < 6000; c += 3 {
				w := syll[a] + syll[b] + syll[c]
				if !seen[w] {
					seen[w] = true
					v = append(v, w)
				}
			}
		}
	}
	// Two words the queries use sit at fixed ranks: one common, one in the middle.
	v[60], v[400] = "harbour", "lantern"
	return v
}

type writer struct {
	r     *rand.Rand
	z     *rand.Zipf
	vocab []string
}

func newWriter(seed int64) *writer {
	r := rand.New(rand.NewSource(seed))
	v := vocabulary()
	return &writer{r: r, z: rand.NewZipf(r, 1.08, 6, uint64(len(v)-1)), vocab: v}
}

func capital(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func (w *writer) word() string { return w.vocab[w.z.Uint64()] }

// sentence is 6 to 22 words with punctuation, occasional emphasis and speech.
func (w *writer) sentence(first bool) (string, int) {
	n := 6 + w.r.Intn(17)
	var b strings.Builder
	for i := 0; i < n; i++ {
		word := w.word()
		if i == 0 {
			word = strings.ToUpper(word[:1]) + word[1:]
		}
		switch {
		case i > 0 && i < n-1 && w.r.Intn(60) == 0:
			word = "*" + word + "*"
		case i > 0 && i < n-1 && w.r.Intn(11) == 0:
			word += ","
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(word)
	}
	end := "."
	switch w.r.Intn(14) {
	case 0:
		end = "?"
	case 1:
		end = "!"
	}
	b.WriteString(end)
	s := b.String()
	if !first && w.r.Intn(9) == 0 {
		s = "“" + s + "”"
	}
	return s, n
}

// prose writes about `words` words as paragraphs, with an occasional scene
// break and a heading. Wiki links, when given, are placed in the first paragraph.
func (w *writer) prose(words int, links []string) string {
	var b strings.Builder
	got := 0
	para := 0
	for got < words {
		k := 3 + w.r.Intn(5)
		var line strings.Builder
		for i := 0; i < k && got < words; i++ {
			s, n := w.sentence(i == 0)
			got += n
			if i > 0 {
				line.WriteByte(' ')
			}
			line.WriteString(s)
		}
		if para == 0 && len(links) > 0 {
			line.WriteString(" She thought of [[" + strings.Join(links, "]] and [[") + "]].")
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(line.String())
		para++
		if para%9 == 0 && got < words {
			b.WriteString("\n\n* * *")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// ---- building ------------------------------------------------------------------------

var povs = []string{"Maren", "Tobias", "Ilsa", "Corin", "Wen", "Odalys", "Pell", "Rook", "Saoirse", "Aldous", "Neve", "Emrys"}
var places = []string{"the harbour", "the lighthouse", "the long road", "Callow Street", "the north field", "the mill", "the library",
	"the ferry", "the orchard", "the chapel", "the counting house", "the cellar", "the ridge", "the market", "the quarry"}

// Build generates one project through the registry. progress, when set, is called
// with the number of documents made so far.
func Build(ctx context.Context, reg *ops.Registry, who ops.Principal, size Size, progress func(done, total int)) (*Fixture, error) {
	sh := shapeFor(size.Words)
	total := sh.parts + sh.chapters + sh.scenes + sh.bigChapters
	w := newWriter(Seed + int64(size.Words))
	call := func(name string, in any) (any, error) { return reg.Call(ctx, who, name, in) }

	out, err := call("project.create", projects.CreateIn{Title: "Bench " + size.Name, Kind: "novel", AuthorName: "Bench Author", Subtitle: "A synthetic manuscript"})
	if err != nil {
		return nil, err
	}
	fx := &Fixture{ProjectID: out.(projects.Item).Item.ID, Slug: out.(projects.Item).Item.Slug, ChapterRevs: map[string][]string{},
		CommonQuery: "harbour lantern", RareQuery: w.vocab[3000], PrefixQuery: "lant"}
	done := 0
	tick := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	create := func(in docs.CreateIn) (string, error) {
		in.ProjectID = fx.ProjectID
		o, err := call("doc.create", in)
		if err != nil {
			return "", fmt.Errorf("doc.create %q: %w", in.Title, err)
		}
		tick()
		fx.Docs++
		return o.(docs.Item).Item.ID, nil
	}

	var chapterTitles []string
	sceneNo, chapterNo, partNo := 0, 0, 0
	scenesLeft := sh.scenes
	chaptersLeft := sh.chapters
	words := 0
	for p := 0; p < sh.parts && chaptersLeft > 0; p++ {
		partNo++
		partID, err := create(docs.CreateIn{Title: fmt.Sprintf("Part %d", partNo), Kind: "part"})
		if err != nil {
			return nil, err
		}
		for c := 0; c < 10 && chaptersLeft > 0; c++ {
			chaptersLeft--
			chapterNo++
			title := fmt.Sprintf("Chapter %d: %s %s", chapterNo, capital(w.word()), capital(w.word()))
			intro := w.prose(80, nil)
			chID, err := create(docs.CreateIn{Title: title, Kind: "chapter", ParentID: partID, Content: intro})
			if err != nil {
				return nil, err
			}
			words += 80
			fx.Folders = append(fx.Folders, chID)
			for s := 0; s < 8 && scenesLeft > 0; s++ {
				scenesLeft--
				sceneNo++
				n := 300 + w.r.Intn(520)
				var links []string
				if sceneNo%20 == 3 && len(chapterTitles) > 0 {
					links = []string{chapterTitles[w.r.Intn(len(chapterTitles))]}
				}
				id, err := create(docs.CreateIn{Title: fmt.Sprintf("Scene %d", sceneNo), Kind: "scene", ParentID: chID, Content: w.prose(n, links)})
				if err != nil {
					return nil, err
				}
				words += n
				fx.Scenes = append(fx.Scenes, id)
			}
			chapterTitles = append(chapterTitles, title)
		}
	}
	for i := 0; i < sh.bigChapters; i++ {
		id, err := create(docs.CreateIn{Title: fmt.Sprintf("Long chapter %d", i+1), Kind: "chapter", Content: w.prose(sh.bigWords, nil)})
		if err != nil {
			return nil, err
		}
		words += sh.bigWords
		fx.Chapters = append(fx.Chapters, id)
	}
	fx.Words = words

	// Scene metadata on every scene (point of view, place, story date), one save each.
	for i, id := range fx.Scenes {
		pov, loc, date := povs[i%len(povs)], places[(i*7)%len(places)], fmt.Sprintf("1912-%02d-%02d", 1+(i/28)%12, 1+i%28)
		status := []string{"draft", "revise", "done"}[i%3]
		if _, err := call("doc.update", docs.UpdateIn{ID: id, BaseRevision: 1, Pov: &pov, Location: &loc, StoryDate: &date, Status: &status}); err != nil {
			return nil, fmt.Errorf("scene metadata: %w", err)
		}
	}

	// History: a few saves and a named snapshot on the long chapters and a sample
	// of scenes, so the revision list and diff have something to read.
	hist := append([]string{}, fx.Chapters...)
	for i := 0; i < len(fx.Scenes); i += 25 {
		hist = append(hist, fx.Scenes[i])
	}
	for _, id := range hist {
		o, err := call("doc.get", docs.DocIn{ID: id})
		if err != nil {
			return nil, err
		}
		d := o.(docs.Item).Item
		rev, content := d.Revision, d.Content
		for k := 0; k < 3; k++ {
			content += fmt.Sprintf("\nA line added in pass %d.\n", k+1)
			up, err := call("doc.update", docs.UpdateIn{ID: id, BaseRevision: rev, Content: &content})
			if err != nil {
				return nil, err
			}
			rev = up.(docs.Item).Item.Revision
			if _, err := call("revision.snapshot", revisions.SnapshotIn{ID: id, Label: fmt.Sprintf("pass %d", k+1)}); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range fx.Chapters {
		o, err := call("revision.list", revisions.ListIn{ID: id, Limit: 200})
		if err != nil {
			return nil, err
		}
		items := o.(revisions.ListOut).Items
		for i := len(items) - 1; i >= 0; i-- { // newest first from the op; keep oldest first
			fx.ChapterRevs[id] = append(fx.ChapterRevs[id], items[i].ID)
		}
	}
	return fx, nil
}
