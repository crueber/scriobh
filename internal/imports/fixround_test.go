package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// wxr wraps items in the channel of a WordPress export.
func wxr(body string) []File {
	head := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>T</title>`
	return []File{{Path: "x.xml", Data: []byte(head + body + `</channel></rss>`)}}
}

// peakWithin runs f and fails if it took longer than d (scaled for the race
// detector) or held more than mb megabytes of heap at any sample.
func peakWithin(t *testing.T, what string, d time.Duration, mb int, f func()) {
	t.Helper()
	took, peak := measurePeak(f)
	t.Logf("%s: %v, peak heap %d MB", what, took, peak)
	if peak > uint64(mb*(1+(int(hostileFactor)-1)/5)) {
		t.Errorf("%s took %v and %d MB (budget %v, %d MB)", what, took, peak, d*hostileFactor, mb)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > d*hostileFactor {
			t.Errorf("%s took %v and %d MB (budget %v, %d MB)", what, took, peak, d*hostileFactor, mb)
		}
	})
}

// ---- 1. a link inside a private note -------------------------------------------------------------------

func TestWikiLinkInsideAPrivateNoteStaysPrivate(t *testing.T) {
	wiki := func(id, label string) string { return wikiFor("Target", label) }
	link := func(label string) string {
		return `{§field{§*§fldinst HYPERLINK "scrivlnk://ABC"}{§fldrslt ` + label + `}}`
	}
	cases := map[string]string{
		"an annotation":         `Visible {§Scrv_annot secret ` + link("hidden label") + ` more secret} end.§par`,
		"a comment":             `Visible {§*§Scrv_cmt secret ` + link("hidden label") + `} end.§par`,
		"a word annotation":     `Visible {§*§annotation secret ` + link("hidden label") + `} end.§par`,
		"nested":                `Visible {§Scrv_annot a {{§b secret ` + link("hidden label") + `}} b} end.§par`,
		"in a footnote":         `Visible{§footnote note {§Scrv_annot secret ` + link("hidden label") + `}} end.§par`,
		"in a list item":        `§ls1§ilvl0 {§listtext •§tab}Visible {§Scrv_annot secret ` + link("hidden label") + `} end.§par`,
		"inside a link label":   `Visible {§field{§*§fldinst HYPERLINK "https://e.com/"}{§fldrslt shown {§Scrv_annot secret ` + link("hidden label") + `}}} end.§par`,
		"across two paragraphs": `Visible {§Scrv_annot secret§par ` + link("hidden label") + ` secret} end.§par`,
	}
	for name, src := range cases {
		d, err := parseRTF(rtfSrc("{§rtf1§ansi " + src + "}"))
		if err != nil {
			t.Fatal(err)
		}
		s := newSink()
		d.emit(s, rtfEmit{wiki: wiki})
		md := s.take()
		doc := text.Structure(md, text.StructOptions{})
		plain := text.PlainText(doc.Nodes)
		if strings.Contains(plain, "secret") || strings.Contains(plain, "hidden") || strings.Contains(text.SearchBody(md), "hidden") {
			t.Errorf("%s: a private word is visible: %q\n%q", name, plain, md)
		}
		if links := text.WikiLinks(md); len(links) != 0 {
			t.Errorf("%s: a wiki link was made inside a note: %+v\n%q", name, links, md)
		}
	}
	// the same link in plain text is still a link
	md := rtfMD(t, "{§rtf1§ansi See "+link("Target")+".§par}")
	d, _ := parseRTF(rtfSrc("{§rtf1§ansi See " + link("Target") + ".§par}"))
	s := newSink()
	d.emit(s, rtfEmit{wiki: wiki})
	if got := s.take(); !strings.Contains(got, "[[Target]]") {
		t.Errorf("a visible link was lost: %q (%q)", got, md)
	}
}

// ---- 2. a cut that must not split a private note -----------------------------------------------------------

func TestResearchCutNeverLeavesAnOpenPrivateNote(t *testing.T) {
	// The note straddles the cut at every offset around it.
	for back := 0; back < 30; back++ {
		pad := strings.Repeat("a", research.MaxBodyBytes-back)
		body := pad + " %%SECRETWORD hidden annotation text%% and after"
		im := research.Imported{Kind: "note", Title: "x", Body: body}
		if cut := im.Clean(); cut == "" {
			t.Fatal("the body was not cut")
		}
		if strings.Count(im.Body, "%%")%2 != 0 || strings.Contains(text.SearchBody(im.Body), "SECRET") || strings.Contains(text.PlainText(text.Structure(im.Body, text.StructOptions{}).Nodes), "SECRET") {
			t.Fatalf("back %d: the cut left a note open: tail %q", back, im.Body[max(0, len(im.Body)-30):])
		}
	}
	// A cut line of many notes and escaped percent signs keeps what is before it.
	line := strings.Repeat("one \\%\\% two %%note%% ", 100000)
	im := research.Imported{Kind: "note", Title: "x", Body: line + "\n" + line}
	im.Clean()
	if strings.Count(im.Body, "%%")%2 != 0 || len(im.Body) > research.MaxBodyBytes {
		t.Errorf("a long line: %d bytes, %d delimiters", len(im.Body), strings.Count(im.Body, "%%"))
	}
}

// The same through the importer: a research note of about a megabyte that ends in an annotation.
func TestImportedResearchOverTheLimitKeepsItsAnnotationsPrivate(t *testing.T) {
	scrivx := `<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="D" Type="DraftFolder"><Title>Draft</Title><Children><BinderItem UUID="A" Type="Text"><Title>One</Title></BinderItem></Children></BinderItem>` +
		`<BinderItem UUID="R" Type="ResearchFolder"><Title>Research</Title><Children><BinderItem UUID="N" Type="Text"><Title>Big note</Title></BinderItem></Children></BinderItem></Binder></ScrivenerProject>`
	for _, extra := range []int{-8, -3, 0, 3, 40} {
		pad := strings.Repeat("word ", (research.MaxBodyBytes-60)/5+extra)
		body := "{§rtf1§ansi " + pad + "{§Scrv_annot SECRETWORD private remark that goes on and on}§par}"
		res, err := scrivenerConvert([]File{
			{Path: "p.scrivx", Data: []byte(scrivx)},
			{Path: "Files/Data/A/content.rtf", Data: rtfSrc("{§rtf1 x§par}")},
			{Path: "Files/Data/N/content.rtf", Data: rtfSrc(body)},
		}, "p", convOpts{})
		if err != nil {
			t.Fatal(err)
		}
		in := &intake{extras: newExtras(res.research, nil)}
		p := &plan{}
		p.addResearch(in.extras)
		for _, rp := range p.rByKey {
			if rp.title != "Big note" {
				continue
			}
			plain := text.PlainText(text.Structure(rp.body, text.StructOptions{}).Nodes)
			if strings.Contains(plain, "SECRETWORD") || strings.Contains(plain, "private remark") || strings.Contains(text.SearchBody(rp.body), "SECRETWORD") {
				t.Errorf("extra %d: a private note is visible in a note cut at %d bytes", extra, len(rp.body))
			}
			if rp.words != text.WordCount(rp.body) {
				t.Errorf("the word count is not the visible words")
			}
		}
	}
}

// ---- 3. a zip never allocates by what it says --------------------------------------------------------------

func lyingZip(t *testing.T, n int, ext string, declared uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	w.Create("P.scriv/P.scrivx")
	for i := 0; i < n; i++ {
		h := &zip.FileHeader{Name: fmt.Sprintf("P.scriv/Files/Data/%d%s", i, ext), Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte("x")), CompressedSize64: 1, UncompressedSize64: declared}
		fw, err := w.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte("x"))
	}
	w.Close()
	return buf.Bytes()
}

// allocatedBy runs f runs times and reports the fewest bytes any run allocated
// (runtime.MemStats.TotalAlloc, which only ever grows). Unlike the heap in use it does
// not depend on when the collector runs, on GOGC, or on GOMAXPROCS; anything else
// allocating in the process only adds to a run, so the fewest of several is the cost of
// f itself.
func allocatedBy(f func()) uint64 {
	best := ^uint64(0)
	for i := 0; i < 3; i++ {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		best = min(best, after.TotalAlloc-before.TotalAlloc)
	}
	return best
}

// An entry that says it holds 32 MB and holds one byte must cost what it holds, not what
// it says. Allocating by the declared sizes would take about 1.2 TB here, and a buffer
// started at a capped hint of the declared size (it was 32 KB) takes 1.2 GB: 39,000
// entries at 33 KB each, all of it garbage the moment the entry is rejected, which is
// what put the heap in use at 269 MB under GOGC=800 and 530 MB under GOMAXPROCS=1 while
// the code did nothing different. So this asserts what the call allocates in total,
// which no collector setting changes, and not the heap at a sampled instant.
//
// Measured: 22 MB (about 600 bytes an entry: the archive's own directory, the list
// of files, a few small readers). The bound is 2 KB an entry, 78 MB: more than three
// times what is measured, and 16 times under the 33 KB an entry the old code took.
func TestZipEntriesAreNotAllocatedByTheirDeclaredSize(t *testing.T) {
	const n = 39_000
	const perEntry = 2 << 10
	z := lyingZip(t, n, ".rtf", 32<<20) // 39,000 entries, each saying it holds 32 MB
	var fs []File
	var err error
	got := allocatedBy(func() { fs, err = readZipFor(z, maxScrivEntries, maxScrivTotal, scrivWant) })
	if err != nil || len(fs) != n+1 {
		t.Errorf("%d files, %v", len(fs), err)
	}
	t.Logf("%d entries declaring 32 MB: %d MB allocated, %d bytes an entry (bound %d)", n, got>>20, got/n, perEntry)
	if got > n*perEntry {
		t.Errorf("%d entries declaring 32 MB allocated %d MB, %d bytes an entry; the bound is %d", n, got>>20, got/n, perEntry)
	}
	// The cost may not move with the declared size either: the same entries declaring
	// 1 KB and declaring 32 MB (neither is true) cost about the same, whatever the
	// code does with the first number it is told.
	small := lyingZip(t, n, ".rtf", 1<<10)
	gotSmall := allocatedBy(func() { readZipFor(small, maxScrivEntries, maxScrivTotal, scrivWant) })
	t.Logf("the same declaring 1 KB: %d MB allocated", gotSmall>>20)
	if got > gotSmall+gotSmall/4+(4<<20) {
		t.Errorf("declaring 32 MB allocates %d MB and declaring 1 KB %d MB: the cost follows what the entries say", got>>20, gotSmall>>20)
	}
	// the generic folder zip: 9,000 entries each declaring a whole document
	md := lyingZip(t, 9_000, ".md", MaxFileBytes)
	gotMD := allocatedBy(func() {
		if _, err := ReadZip(md); err != nil {
			t.Error(err)
		}
	})
	// (ReadAll starts at 512 bytes and the directory entries are longer: 1.4 KB an entry
	// measured, so the bound here is 4 KB, three times that and still nowhere near 2 MiB.)
	const perMD = 4 << 10
	t.Logf("9,000 Markdown entries declaring 2 MiB: %d MB allocated, %d bytes an entry (bound %d)", gotMD>>20, gotMD/9_000, perMD)
	if gotMD > 9_000*perMD {
		t.Errorf("9,000 Markdown entries declaring 2 MiB allocated %d MB, %d bytes an entry; the bound is %d", gotMD>>20, gotMD/9_000, perMD)
	}
}

// What is kept of an entry is its bytes and no more: not the room a buffer had when
// it was started from a size the archive gave.
func TestZipEntriesKeepOnlyTheirBytes(t *testing.T) {
	var ents []ent
	for i, size := range []int{0, 1, 100, 5000, 40_000, 300_000} {
		ents = append(ents, ent{fmt.Sprintf("p.scriv/Files/Data/U%d/content.rtf", i), bytes.Repeat([]byte("a"), size)})
	}
	fs, err := readZipFor(zipBytes(t, ents), maxScrivEntries, maxScrivTotal, scrivWant)
	if err != nil || len(fs) != len(ents) {
		t.Fatalf("%d files, %v", len(fs), err)
	}
	for i, f := range fs {
		if len(f.Data) != len(ents[i].data) || cap(f.Data) != len(f.Data) {
			t.Errorf("%s: %d bytes read, %d kept, room for %d (want %d)", f.Path, f.Size, len(f.Data), cap(f.Data), len(ents[i].data))
		}
		if !bytes.Equal(f.Data, ents[i].data) {
			t.Errorf("%s: the bytes changed", f.Path)
		}
	}
}

// What the entries say about their size is never the allowance: honest entries are
// held to a total, and entries that say less than they hold are unreadable (the
// archive reader itself refuses a stream longer than its header says, and what is
// read is counted as work whatever happens to it).
func TestZipTotalIsBoundedWhateverTheEntriesSay(t *testing.T) {
	var honest bytes.Buffer
	w := zip.NewWriter(&honest)
	data := bytes.Repeat([]byte("a"), 60<<10)
	for i := 0; i < 100; i++ {
		fw, _ := w.Create(fmt.Sprintf("p/Files/Data/%d/content.rtf", i))
		fw.Write(data)
	}
	w.Close()
	want := func(string) (int64, bool) { return 64 << 10, true }
	if _, err := readZipFor(honest.Bytes(), 1000, 1<<20, want); err == nil || !strings.Contains(err.Error(), "more than the import limit") {
		t.Errorf("honest entries past the total: %v", err)
	}
	var liars bytes.Buffer
	lw := zip.NewWriter(&liars)
	for i := 0; i < 400; i++ {
		h := &zip.FileHeader{Name: fmt.Sprintf("p/Files/Data/%d/content.rtf", i), Method: zip.Store, CRC32: crc32.ChecksumIEEE(data), CompressedSize64: uint64(len(data)), UncompressedSize64: 1000}
		fw, err := lw.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
	}
	lw.Close()
	var fs []File
	peakWithin(t, "400 entries that hold more than they say", 5*time.Second, 100, func() {
		var err error
		fs, err = readZipFor(liars.Bytes(), 1000, 1<<20, want)
		if err != nil {
			t.Error(err)
		}
	})
	for _, f := range fs {
		if len(f.Data) > 1000 {
			t.Errorf("%s: %d bytes were kept of an entry that says 1,000", f.Path, len(f.Data))
		}
	}
}

// ---- 4. Ghost: counted as it is read -------------------------------------------------------------------

func TestGhostExportIsRefusedWithoutBeingBuilt(t *testing.T) {
	s := `{"posts":[` + strings.TrimSuffix(strings.Repeat("{},", (36<<20)/3), ",") + `]}`
	f := []File{{Path: "g.json", Data: []byte(s)}}
	peakWithin(t, "12 million empty posts", 3*time.Second, 150, func() {
		if _, err := ghostConvert(f, "g", convOpts{}); err == nil || !strings.Contains(err.Error(), "more than") {
			t.Errorf("%v", err)
		}
	})
	tags := `{"posts":[{"title":"a","html":"<p>x</p>","status":"published"}],"tags":[` + strings.TrimSuffix(strings.Repeat(`{"id":"a","name":"n"},`, maxGhostAux+10), ",") + `]}`
	if _, err := ghostConvert([]File{{Path: "g.json", Data: []byte(tags)}}, "g", convOpts{}); err == nil || !strings.Contains(err.Error(), "tags") {
		t.Errorf("tags: %v", err)
	}
}

func TestGhostTolerantOfOneWrongField(t *testing.T) {
	good := `{"title":"Good","status":"published","html":"<p>fine</p>","published_at":"2023-12-31T23:30:00.000-05:00"}`
	for name, bad := range map[string]string{
		"a number for a title":    `{"title":5,"status":"published","html":"<p>x</p>"}`,
		"a list for a status":     `{"title":"T","status":["x"],"html":"<p>x</p>"}`,
		"an object for html":      `{"title":"T","status":"published","html":{"a":1}}`,
		"a post that is a list":   `[1,2]`,
		"a post that is a number": `7`,
	} {
		r, err := ghostConvert([]File{{Path: "g.json", Data: []byte(`{"posts":[` + bad + `,` + good + `]}`)}}, "g", convOpts{})
		if err != nil {
			t.Errorf("%s: the whole export was refused: %v", name, err)
			continue
		}
		found := false
		for _, d := range r.docs {
			found = found || d.title == "Good"
		}
		if !found || !strings.Contains(strings.Join(r.warnings, "|"), "not the kind of value") {
			t.Errorf("%s: good post kept %v, warnings %v", name, found, r.warnings)
		}
	}
	// The date is the day it was where the post says it was (the offset it carries).
	r, _ := ghostConvert([]File{{Path: "g.json", Data: []byte(`{"posts":[` + good + `]}`)}}, "g", convOpts{})
	if r.docs[0].storyDate != "2023-12-31" {
		t.Errorf("date %q", r.docs[0].storyDate)
	}
	// A bare list of posts still has its tags, links and authors.
	r, err := ghostConvert([]File{{Path: "g.json", Data: []byte(`{"posts":[{"id":1,"title":"a","status":"published","html":"<p>x</p>"}],"posts_tags":[{"post_id":1,"tag_id":"t"}],"tags":[{"id":"t","name":"Nine"}],"users":[{"id":"u","name":"Ada"}],"posts_authors":[{"post_id":1,"author_id":"u"}]}`)}}, "g", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rep := r.meta["ghost"].(map[string]any); rep["tags"].(map[string]int)["Nine"] != 1 || r.project.author != "Ada" {
		t.Errorf("bare root: %v author %q", rep["tags"], r.project.author)
	}
	// A setting that is not text does not stop the title.
	r, err = ghostConvert([]File{{Path: "g.json", Data: []byte(`{"db":[{"data":{"posts":[{"title":"a","status":"published","html":"<p>x</p>"}],"settings":[{"key":"title","value":{"x":1}},{"key":"title","value":"Site"}]}}]}`)}}, "g", convOpts{})
	if err != nil || r.project.title != "Site" {
		t.Errorf("settings: %v %q", err, r.project.title)
	}
}

// ---- 5. XML: what is built, and what an element may hold -----------------------------------------------

func TestXMLManyChildrenAndAttributesAreNotBuilt(t *testing.T) {
	n := (36 << 20) / 4
	for name, body := range map[string]string{
		"9 million empty children in an item":     `<item><wp:post_type>post</wp:post_type><wp:status>publish</wp:status>` + strings.Repeat("<a/>", n) + `</item>`,
		"9 million empty children in the channel": strings.Repeat("<a/>", n) + `<item><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><title>x</title></item>`,
	} {
		f := wxr(body)
		peakWithin(t, name, 15*time.Second, 360, func() {
			if _, err := wordpressConvert(f, "x", convOpts{}); err != nil && !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "too intricate") {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
	// Millions of attributes on one element: refused before the decoder builds them.
	var b bytes.Buffer
	b.WriteString(`<item><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><x `)
	for i := 0; b.Len() < 36<<20; i++ {
		fmt.Fprintf(&b, `a%d="" `, i)
	}
	b.WriteString(`/></item>`)
	f := wxr(b.String())
	peakWithin(t, "one element with millions of attributes", 5*time.Second, 200, func() {
		if _, err := wordpressConvert(f, "x", convOpts{}); err == nil || !strings.Contains(err.Error(), "attributes") {
			t.Errorf("%v", err)
		}
	})
	// A few thousand attributes are counted and not kept; the data URI in a post's HTML is not a long tag.
	var c bytes.Buffer
	c.WriteString(`<item><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><title>t</title><x `)
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&c, `a%d="v" `, i)
	}
	c.WriteString(`/><content:encoded><![CDATA[<p><img src="data:image/png;base64,` + strings.Repeat("A", 2<<20) + `" alt="pic"></p>]]></content:encoded></item>`)
	r, err := wordpressConvert(wxr(c.String()), "x", convOpts{})
	if err != nil || len(r.docs) != 1 {
		t.Errorf("a post with attributes and a data address: %v", err)
	}
	// Elements kept: a node never holds more than maxXMLKids and maxXMLAttrs.
	var k strings.Builder
	k.WriteString(`<r a0="" a1="" a2="" a3="" a4="" a5="" a6="" a7="" a8="" a9="" a10="" a11="" a12="" a13="" a14="" a15="" a16="" a17="" a18="" a19="" a20="" a21="" a22="" a23="" a24="" a25="" a26="" a27="" a28="" a29="" a30="" a31="" a32="" a33="" a34="" a35="" a36="" a37="" a38="" a39="" a40="" a41="" a42="" a43="" a44="" a45="" a46="" a47="" a48="" a49="" a50="" a51="" a52="" a53="" a54="" a55="" a56="" a57="" a58="" a59="" a60="" a61="" a62="" a63="" a64="" a65="">`)
	k.WriteString(strings.Repeat("<c/>", maxXMLKids+5000) + `</r>`)
	root, err := parseXML([]byte(k.String()), xmlLimits{tokens: 1_000_000, text: 1 << 20})
	if err != nil || len(root.kids) != maxXMLKids || len(root.attrs) != maxXMLAttrs {
		t.Errorf("kept %d children and %d attributes: %v", len(root.kids), len(root.attrs), err)
	}
}

// ---- 6. footnotes are written once -------------------------------------------------------------------------

func TestFootnotesAreWrittenOnceHoweverOftenTheyAreCited(t *testing.T) {
	note := strings.Repeat("word ", 20000) // 100 KB
	fn, _ := json.Marshal([]map[string]string{{"id": "a", "content": note}})
	content := "<!-- wp:paragraph --><p>" + strings.Repeat(`<sup data-fn="a"></sup>`, 20000) + "</p><!-- /wp:paragraph -->"
	item := `<item><title>x</title><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><content:encoded><![CDATA[` + content + `]]></content:encoded><wp:postmeta><wp:meta_key>footnotes</wp:meta_key><wp:meta_value><![CDATA[` + string(fn) + `]]></wp:meta_value></wp:postmeta></item>`
	var res *cresult
	var err error
	peakWithin(t, "20,000 citations of a 100 KB note", 5*time.Second, 120, func() { res, err = wordpressConvert(wxr(item), "x", convOpts{}) })
	if err != nil {
		t.Fatal(err)
	}
	body := res.docs[0].body
	if n := strings.Count(body, "[^1]: "); n != 1 || len(body) > 400<<10 {
		t.Errorf("%d definitions, %d bytes", n, len(body))
	}
	// Markers forged with character references cite the same note, not a copy each.
	forged := "<p>a [footnote]" + strings.Repeat("note ", 1500) + "[/footnote]" + strings.Repeat("&#xE000;0&#xE001;", 20000) + "</p>"
	item = `<item><title>x</title><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><content:encoded><![CDATA[` + forged + `]]></content:encoded></item>`
	peakWithin(t, "20,000 forged markers", 5*time.Second, 120, func() { res, err = wordpressConvert(wxr(item), "x", convOpts{}) })
	if body := res.docs[0].body; err != nil || strings.Count(body, "[^1]: ") != 1 || len(body) > 200<<10 {
		t.Errorf("forged markers: %v, %d bytes", err, len(body))
	}
	// And a document's notes stop being read when they are as many as they may be.
	var many strings.Builder
	many.WriteString("<p>")
	for i := 0; i < maxNotes+500; i++ {
		fmt.Fprintf(&many, "[footnote]n%d[/footnote]", i)
	}
	many.WriteString("</p>")
	item = `<item><title>x</title><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><content:encoded><![CDATA[` + many.String() + `]]></content:encoded></item>`
	res, err = wordpressConvert(wxr(item), "x", convOpts{})
	if err != nil || strings.Count(res.docs[0].body, "]: ") > maxNotes {
		t.Errorf("notes past the limit: %v", err)
	}
}

// ---- 7. runs ----------------------------------------------------------------------------------------------

func TestRTFRunLimitBoundsRunsAndNeverMergesAPrivateNote(t *testing.T) {
	unit := "{§b a}{§i a}"
	src := rtfSrc("{§rtf1 " + strings.Repeat(unit, (32<<20)/len(unit)) + "}")
	var d *rtfDoc
	var err error
	peakWithin(t, "4.8 million alternating runs", 5*time.Second, 500, func() { d, err = parseRTF(src) })
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range d.paras {
		n += len(p.segs)
	}
	if n > rtfMaxRuns+rtfMaxRuns/2 || d.st.flattened == 0 {
		t.Errorf("%d segments (limit %d), flattened %d", n, rtfMaxRuns, d.st.flattened)
	}
	// A private note after the limit is still a note.
	var b strings.Builder
	b.WriteString(`{§rtf1§ansi `)
	for i := 0; i < rtfMaxRuns/2+10; i++ {
		b.WriteString(`{§b a}{§i a}`)
	}
	b.WriteString(`§par Visible start {§Scrv_annot SECRETWORD} after.§par}`)
	md := rtfMD(t, b.String())
	if strings.Contains(text.SearchBody(md), "SECRETWORD") || !strings.Contains(md, "%%SECRETWORD%%") {
		t.Errorf("a note past the run limit is not private: %q", md[max(0, len(md)-120):])
	}
	// Toggling private and plain past the limit is refused, not accepted without bound.
	var c strings.Builder
	c.WriteString(`{§rtf1§ansi `)
	for i := 0; i < 2*rtfMaxRuns; i++ {
		c.WriteString(`a{§Scrv_annot b}`)
	}
	c.WriteString(`}`)
	if _, err := parseRTF(rtfSrc(c.String())); err == nil {
		t.Errorf("a million switches between plain text and notes were accepted")
	}
}

// ---- 8. Ghost Markdown: one budget for the bad shapes -------------------------------------------------------

func TestGhostMarkdownCostIsOneBudget(t *testing.T) {
	card := strings.Repeat("[a](b", (MaxFileBytes-100)/5)
	var b strings.Builder
	b.WriteString(`{"posts":[`)
	for i := 0; i < 14; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"title":"t%d","status":"published","markdown":"%s"}`, i, card)
	}
	b.WriteString(`]}`)
	f := []File{{Path: "g.json", Data: []byte(b.String())}}
	var r *cresult
	var err error
	peakWithin(t, "14 posts of 2 MiB of unfinished links", 12*time.Second, 700, func() { r, err = ghostConvert(f, "g", convOpts{}) })
	if err != nil {
		t.Fatal(err)
	}
	if skipped := r.meta["ghost"].(map[string]any)["markdown_not_read"].(int); skipped < 10 || !strings.Contains(strings.Join(r.warnings, "|"), "left empty") {
		t.Errorf("not read: %d, warnings %v", skipped, r.warnings)
	}
	// And a time budget stops the rest of an export that is slow for some other reason.
	c := &ghostConv{md: nil, sk: newSink(), hs: &htmlStats{}, mdBudget: time.Nanosecond}
	c.mdTime = time.Second
	c.markdown("a\n")
	if c.mdSkipped != 1 {
		t.Errorf("the time budget was not applied")
	}
}

// ---- 9. report keys are bounded ----------------------------------------------------------------------------

func TestReportKeysAreBounded(t *testing.T) {
	// WordPress: types, statuses, categories, tags, custom fields
	var b strings.Builder
	for i := 0; i < 50_000; i++ {
		fmt.Fprintf(&b, `<item><wp:post_type>t%d%s</wp:post_type><wp:status>s%d</wp:status></item>`, i, strings.Repeat("y", 200), i)
	}
	b.WriteString(`<item><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><title>x</title>`)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, `<category domain="category">c%d</category><category domain="post_tag">g%d</category><wp:postmeta><wp:meta_key>k%d</wp:meta_key><wp:meta_value>v</wp:meta_value></wp:postmeta>`, i, i, i)
	}
	b.WriteString(`</item>`)
	r, err := wordpressConvert(wxr(b.String()), "x", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rep := r.meta["wordpress"].(map[string]any)
	for _, k := range []string{"by_type", "not_imported", "categories", "tags", "custom_field_keys", "statuses"} {
		if n := len(rep[k].(map[string]int)); n > maxReportKeys+1 {
			t.Errorf("wordpress %s has %d keys", k, n)
		}
	}
	for k, v := range rep["by_type"].(map[string]int) {
		if len(k) > 60 {
			t.Errorf("a key of %d bytes: %q", len(k), k)
		}
		_ = v
	}
	if rep["by_type"].(map[string]int)["(other)"] == 0 {
		t.Errorf("the other types were not counted")
	}
	// Ghost: visibility, statuses, types, tags
	var g strings.Builder
	g.WriteString(`{"posts":[`)
	for i := 0; i < 50_000; i++ {
		if i > 0 {
			g.WriteString(",")
		}
		fmt.Fprintf(&g, `{"title":"t","status":"published","visibility":"<img src=x onerror=alert(1)>%s%d","type":"x%d","html":"<p>x</p>"}`, strings.Repeat("v", 300), i, i)
	}
	g.WriteString(`,{"title":"ok","status":"published","html":"<p>x</p>"}]}`)
	gr, err := ghostConvert([]File{{Path: "g.json", Data: []byte(g.String())}}, "g", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	grep := gr.meta["ghost"].(map[string]any)
	for _, k := range []string{"visibility", "not_imported", "statuses"} {
		if n := len(grep[k].(map[string]int)); n > maxReportKeys+1 {
			t.Errorf("ghost %s has %d keys", k, n)
		}
	}
	for k := range grep["not_imported"].(map[string]int) {
		if strings.ContainsAny(k, "<>") || len(k) > 60 {
			t.Errorf("an unclean key %q", k)
		}
	}
	// the counter itself
	c := newCounter()
	for i := 0; i < 100_000; i++ {
		c.add(fmt.Sprint("key", i))
	}
	if len(c.m) != maxReportKeys+1 || c.m["(other)"] != 100_000-maxReportKeys || !c.overflow {
		t.Errorf("counter: %d keys", len(c.m))
	}
}

// ---- 10. pictures --------------------------------------------------------------------------------------------

func TestPictureWithItsStandInIsOneImage(t *testing.T) {
	for name, c := range map[string]struct {
		src    string
		images int
	}{
		"a picture and its stand-in": {`a {§*§shppict{§pict§pngblip 89504e47}}{§nonshppict{§pict§wmetafile8 0100}} b§par`, 1},
		"a lone picture":             {`a {§pict§pngblip 89504e47} b§par`, 1},
		"only a stand-in":            {`a {§nonshppict{§pict§wmetafile8 0100}} b§par`, 0},
		"two pictures":               {`a {§*§shppict{§pict 89}}{§nonshppict{§pict 01}} b {§*§shppict{§pict 89}}{§nonshppict{§pict 01}}§par`, 2},
		"an attachment":              {`a {{§NeXTGraphic x.png §width1 §height1}} b§par`, 1},
	} {
		d, err := parseRTF(rtfSrc("{§rtf1 " + c.src + "}"))
		if err != nil {
			t.Fatal(err)
		}
		s := newSink()
		d.emit(s, rtfEmit{})
		md := s.take()
		if d.st.images != c.images || strings.Count(md, `\[image\]`) != c.images {
			t.Errorf("%s: %d images counted, %d marked: %q", name, d.st.images, strings.Count(md, `\[image\]`), md)
		}
	}
}

// ---- the smaller ones --------------------------------------------------------------------------------------

func TestRTFSkipCountEndsWithItsGroupAndASurrogateIsNeverLost(t *testing.T) {
	for src, want := range map[string]string{
		`{§rtf1§ansi§uc1 {§u8212}abc§par}`:          "—abc\n",
		`{§rtf1§ansi§uc2 x{§u8212 }abc§par}`:        "x—abc\n",
		`{§rtf1§ansi§uc1 end§u-10179 §par}`:         "end�\n",
		`{§rtf1§ansi§uc1 end§u-10179}`:              "end�\n",
		`{§rtf1§ansi§uc1 a{b§u-10179}c§par}`:        "ab�c\n",
		`{§rtf1§ansi§uc1 x§u-10179?§u-8704? y§par}`: "x😀 y\n",
	} {
		if got := rtfMD(t, src); got != want {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
}

func TestHTMLBlocksComeOutInTheirOrder(t *testing.T) {
	cases := map[string]string{
		`<ul><li>one<pre>code in li</pre></li></ul>`:                    "- one\n\n```\ncode in li\n```\n",
		`<ul><li>one<table><tr><td>a</td></tr></table>two</li></ul>`:    "- one\n\n| a |\n| --- |\n\ntwo\n",
		`<blockquote>q<pre>code</pre>r</blockquote>`:                    "> q\n\n```\ncode\n```\n\n> r\n",
		`<blockquote><pre>code</pre></blockquote>`:                      "```\ncode\n```\n",
		`<table><tr><td>a<pre>b  c</pre></td></tr></table><p>after</p>`: "| a b c |\n| --- |\n\nafter\n",
		`<p>before</p><ul><li>x<pre>c</pre>y</li></ul><p>after</p>`:     "before\n\n- x\n\n```\nc\n```\n\ny\n\nafter\n",
	}
	for in, want := range cases {
		if got, _ := htmlMD(t, in); got != want {
			t.Errorf("%s\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestShownCutsBetweenCharacters(t *testing.T) {
	s := shown(strings.Repeat("日本語", 100))
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) != 123 {
		t.Errorf("%d runes, valid %v", utf8.RuneCountInString(s), utf8.ValidString(s))
	}
	if got := jsonErrShort(fmt.Errorf("%s", strings.Repeat("é", 300))); !utf8.ValidString(got) {
		t.Errorf("a cut error: %q", got)
	}
}

func TestBinderItemsPastTheDepthLimitAreCounted(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="D" Type="DraftFolder"><Title>Draft</Title><Children>`)
	for i := 0; i < maxBinderDepth+5; i++ {
		fmt.Fprintf(&b, `<BinderItem UUID="F%d" Type="Folder"><Title>L%d</Title><Children>`, i, i)
	}
	b.WriteString(`<BinderItem UUID="X" Type="Text"><Title>Deep</Title></BinderItem><BinderItem UUID="Y" Type="Text"><Title>Deeper</Title></BinderItem>`)
	for i := 0; i < maxBinderDepth+5; i++ {
		b.WriteString(`</Children></BinderItem>`)
	}
	b.WriteString(`</Children></BinderItem></Binder></ScrivenerProject>`)
	r, err := scrivenerConvert([]File{{Path: "p.scrivx", Data: []byte(b.String())}}, "p", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(r.warnings, "|"); !strings.Contains(w, "nested more than") {
		t.Errorf("items left out for depth were not reported: %v", r.warnings)
	}
}

// Words that mark Scríobh's own constructs cannot be written in code.
func TestCodeFromForeignSourcesCannotCarryMarkers(t *testing.T) {
	marker := `&lt;!-- scriobh:alts {"v":1} --&gt;`
	ghost := `&lt;scriobh-ghost&gt;G&lt;/scriobh-ghost&gt;`
	for _, h := range []string{
		"<p>x</p><pre>" + marker + "</pre>",
		"<pre>a\n" + marker + "\n" + ghost + "\n</pre>",
		"<p><code>" + ghost + "</code></p>",
		"<table><tr><td><code>" + marker + "</code></td></tr></table>",
	} {
		md, _ := htmlMD(t, h)
		if strings.Contains(md, "<scriobh-ghost") || strings.Contains(md, "</scriobh-ghost") || strings.Contains(md, "<!-- scriobh:") {
			t.Errorf("%q still holds a marker: %q", h, md)
		}
	}
	// the Markdown channels of Ghost: a fenced block and an indented one
	for _, m := range []string{"x\n\n```\n<!-- scriobh:alts {} -->\n```", "x\n\n    <scriobh-ghost>G</scriobh-ghost>\n"} {
		c := &ghostConv{md: ghostMarkdown(), sk: newSink(), hs: &htmlStats{}, mdBudget: time.Minute}
		c.markdown(m)
		if got := c.sk.take(); strings.Contains(got, "<scriobh-ghost") || strings.Contains(got, "<!-- scriobh:") {
			t.Errorf("%q: %q", m, got)
		}
	}
	// and in a fence the text is still what it said, apart from a space nobody sees
	md, _ := htmlMD(t, "<pre>&lt;scriobh-ghost&gt;</pre>")
	if strings.ReplaceAll(md, "​", "") != "```\n<scriobh-ghost>\n```\n" {
		t.Errorf("%q", md)
	}
}

// ---- cancellation ------------------------------------------------------------------------------------------

func TestConvertersStopWhenTheCallerGoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := convOpts{ctx: ctx}
	wp, _ := os.ReadFile("testdata/wordpress-1.2.xml")
	g5, _ := os.ReadFile("testdata/ghost-5.json")
	if _, err := scrivenerConvert(readTree(t, "testdata/Salt Road.scriv"), "x", o); err != context.Canceled {
		t.Errorf("scrivener: %v", err)
	}
	if _, err := wordpressConvert([]File{{Path: "e.xml", Data: wp}}, "x", o); err != context.Canceled {
		t.Errorf("wordpress: %v", err)
	}
	if _, err := ghostConvert([]File{{Path: "g.json", Data: g5}}, "x", o); err != context.Canceled {
		t.Errorf("ghost: %v", err)
	}
}

// ---- the fixtures the tests read are in the repository -------------------------------------------------------

// A fixture that a rule of .gitignore swallows works here and is missing from a
// clone (the Files/Data folders of a Scrivener project did, once). This fails if a
// file under testdata is ignored, or not yet added.
func TestFixtureFilesAreTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = "."
		out, err := cmd.Output()
		return string(out), err
	}
	if _, err := run("rev-parse", "--git-dir"); err != nil {
		t.Skip("not a git checkout")
	}
	ignored, err := run("ls-files", "--others", "--ignored", "--exclude-standard", "--", "testdata", "../editor/testdata/corpus", "../../scripts/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(ignored) != "" {
		t.Errorf("fixtures that git ignores (a clone will not have them):\n%s", ignored)
	}
	untracked, err := run("ls-files", "--others", "--exclude-standard", "--", "testdata", "../editor/testdata/corpus", "../../scripts/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(untracked) != "" {
		t.Errorf("fixtures that are not added to git:\n%s", untracked)
	}
	// and every one of the fixture files the converters read is listed
	tracked, _ := run("ls-files", "--", "testdata")
	for _, must := range []string{"testdata/wordpress-1.2.xml", "testdata/ghost-4.json", "testdata/ghost-5.json",
		"testdata/Salt Road.scriv/Salt Road.scrivx", "testdata/Old Book.scriv/Old Book.scrivx"} {
		if !strings.Contains(tracked, must) {
			t.Errorf("%s is not tracked", must)
		}
	}
	data := 0
	filepath.WalkDir("testdata/Salt Road.scriv/Files/Data", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data++
			if !strings.Contains(tracked, filepath.ToSlash(p)) {
				t.Errorf("%s is not tracked", p)
			}
		}
		return nil
	})
	if data < 10 {
		t.Errorf("only %d files under Files/Data", data)
	}
}
