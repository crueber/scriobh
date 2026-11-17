package imports

import (
	"archive/zip"
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// measure runs f and reports the time and the megabytes it allocated.
func measure(f func()) (time.Duration, uint64) {
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&m1)
	return d, (m1.TotalAlloc - m0.TotalAlloc) >> 20
}

// allocBudget scales an allocation bound for the race detector, which keeps more.
func allocBudget(mb int) uint64 { return uint64(mb * (1 + (int(hostileFactor)-1)/5)) }

func within(t *testing.T, what string, d time.Duration, alloc uint64, maxTime time.Duration, maxMB int) {
	t.Helper()
	t.Logf("%s: %v, %d MB", what, d, alloc)
	if alloc > allocBudget(maxMB) {
		t.Errorf("%s took %v and %d MB (budget %v, %d MB)", what, d, alloc, maxTime*hostileFactor, allocBudget(maxMB))
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > maxTime*hostileFactor {
			t.Errorf("%s took %v and %d MB (budget %v, %d MB)", what, d, alloc, maxTime*hostileFactor, allocBudget(maxMB))
		}
	})
}

// ---- RTF --------------------------------------------------------------------------------------------------------

func TestRTFControlWordBomb(t *testing.T) {
	// two million control words, a million toggles of bold between letters, a million groups
	data := rtfSrc(`{§rtf1§ansi ` + strings.Repeat(`§b0§b `, 1_000_000) + `text` + strings.Repeat(`§i§i0§plain§pard`, 500_000) + `}`)
	var doc *rtfDoc
	var err error
	d, a := measure(func() { doc, err = parseRTF(data) })
	if err != nil || rtfText(doc) != "text" {
		t.Errorf("err %v text %q", err, rtfText(doc))
	}
	within(t, "control words", d, a, 5*time.Second, 80)
}

func TestRTFNestingBomb(t *testing.T) {
	data := rtfSrc("{§rtf1 a" + strings.Repeat("{", 5_000_000) + "deep" + strings.Repeat("}", 5_000_000) + " b}")
	var doc *rtfDoc
	var err error
	d, a := measure(func() { doc, err = parseRTF(data) })
	if err != nil {
		t.Fatal(err)
	}
	if got := rtfText(doc); !strings.HasPrefix(got, "a") || strings.Contains(got, "deep") && doc.st.deepGroups == 0 {
		t.Errorf("got %q, deep groups %d", got, doc.st.deepGroups)
	}
	if doc.st.deepGroups == 0 {
		t.Errorf("deep groups were not counted")
	}
	within(t, "nested groups", d, a, 5*time.Second, 80)
}

func TestRTFBinaryAndPictures(t *testing.T) {
	// \bin with a size far past the data, a picture of 20 MB of hex, an object
	huge := strings.Repeat("0123456789abcdef", 1_300_000)
	data := rtfSrc("{§rtf1 before {§pict§pngblip " + huge + "} middle §bin6 {}}}§par{{ §par}" + "{§object{§*§objdata " + huge + "}}after}")
	var doc *rtfDoc
	var err error
	d, a := measure(func() { doc, err = parseRTF(data) })
	if err != nil {
		t.Fatal(err)
	}
	if got := rtfText(doc); !strings.Contains(got, "before") || strings.Contains(got, "0123") || !strings.Contains(got, "after") || strings.Contains(got, "par") {
		t.Errorf("text %q", got[:min(len(got), 80)])
	}
	if doc.st.images < 2 {
		t.Errorf("images counted: %d", doc.st.images)
	}
	within(t, "pictures and \\bin", d, a, 5*time.Second, 80)
	// \bin that holds braces and control words is skipped, not read
	src := "{§rtf1 a§bin12 }}}§par§par{{b}}c"
	doc, err = parseRTF(rtfSrc(src))
	if err != nil || strings.Contains(rtfText(doc), "par") || !strings.HasPrefix(rtfText(doc), "a") {
		t.Errorf("binary data was read as RTF: %q %v", rtfText(doc), err)
	}
}

func TestRTFUnicodeAndCodePages(t *testing.T) {
	cases := []struct{ src, want string }{
		{`§u55357?§u56832?`, "😀"},      // a surrogate pair
		{`§u55357?x`, "\uFFFDx"},       // a high half with no low half
		{`§u56832?x`, "\uFFFDx"},       // a low half alone
		{`§u-1?§u65?`, "\uFFFDA"},      // a signed value: 0xFFFF, then A
		{`§uc2§u8364ab c`, "€ c"},      // two fallback characters skipped
		{`§uc0§u8364 c`, "€c"},         // none skipped
		{`§uc99999999999§u65ab`, "A"},  // the count is clamped and then the whole run is skipped
		{`§u99999999999?x`, "\uFFFDx"}, // out of range
		{`§'e9§'80§'ff`, "é€ÿ"},        // windows-1252
		{`§'zz§'9`, "zz9"},             // not hex: the text is kept
		{`§u8217§'92`, "’"},            // \u then its \' fallback is skipped
		{`a§'`, "a"},                   // truncated at the end
		{"a\xe9b", "aéb"},              // a raw byte in the code page
		{`§~§_§-x`, " -x"},             // no-break space and hyphens
		{`§emdash§endash§bullet§lquote x`, "—–•‘x"},
	}
	for _, c := range cases {
		doc, err := parseRTF(rtfSrc("{§rtf1§ansi§ansicpg1252 " + c.src + "}"))
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		got := rtfText(doc)
		// the leading space after \ansicpg1252 is the delimiter
		if got != c.want && strings.TrimSpace(got) != strings.TrimSpace(c.want) {
			t.Errorf("%q: got %q want %q", c.src, got, c.want)
		}
	}
	// A code page the reader does not have is counted, not guessed.
	doc, _ := parseRTF(rtfSrc("{§rtf1§ansi§ansicpg932 §'82§'a0}"))
	if doc.st.badCP == 0 {
		t.Errorf("an unknown code page was not counted")
	}
	// Mac Roman
	doc, _ = parseRTF(rtfSrc("{§rtf1§mac §'8e}"))
	if rtfText(doc) != "é" {
		t.Errorf("mac: %q", rtfText(doc))
	}
}

func TestRTFNotRTFAndTruncated(t *testing.T) {
	for _, src := range []string{"", "plain text", "{\\rtf", "<html>", "\x00\x01\x02"} {
		if _, err := parseRTF([]byte(src)); err == nil {
			t.Errorf("%q was read as RTF", src)
		}
	}
	for _, src := range []string{`{§rtf1 open`, `{§rtf1 §`, `{§rtf1 §b`, `{§rtf1 {§footnote never closed`, `{§rtf1 }}}} x`, `{§rtf1 {§field{§*§fldinst HYPERLINK "https://e.com"}}`, "\xef\xbb\xbf{§rtf1 bom}"} {
		if _, err := parseRTF(rtfSrc(src)); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}

func TestRTFTextAndRunLimits(t *testing.T) {
	// More text than a file may hold is refused.
	big := rtfSrc("{§rtf1 " + strings.Repeat("abcdefgh ", (rtfMaxText/9)+10) + "}")
	d, a := measure(func() {
		if _, err := parseRTF(big); err == nil {
			t.Errorf("%d bytes of text were accepted", len(big))
		}
	})
	within(t, "too much text", d, a, 5*time.Second, 120)
	// Alternating bold with one letter each: the runs are bounded and the text kept.
	alt := rtfSrc("{§rtf1 " + strings.Repeat("§b a§b0  b", 700_000) + "}")
	var doc *rtfDoc
	var err error
	d, a = measure(func() { doc, err = parseRTF(alt) })
	if err != nil || len(rtfText(doc)) < 1_400_000 {
		t.Errorf("runs: %v, %d bytes", err, len(rtfText(doc)))
	}
	if doc.st.flattened == 0 {
		t.Errorf("formatting was not flattened past the run limit")
	}
	within(t, "a million runs", d, a, 8*time.Second, 400)
	// Footnotes: many are capped, nested ones are dropped.
	var fb strings.Builder
	fb.WriteString("{§rtf1 ")
	for i := 0; i < rtfMaxNotes+500; i++ {
		fb.WriteString("x{§footnote n}")
	}
	fb.WriteString("{§footnote outer {§footnote inner}}}")
	doc, err = parseRTF(rtfSrc(fb.String()))
	if err != nil || len(doc.notes) > rtfMaxNotes {
		t.Errorf("notes: %v %d", err, len(doc.notes))
	}
}

// ---- private notes ------------------------------------------------------------------------------------------------

func rtfMD(t testing.TB, src string) string {
	t.Helper()
	doc, err := parseRTF(rtfSrc(src))
	if err != nil {
		t.Fatal(err)
	}
	s := newSink()
	doc.emit(s, rtfEmit{})
	return s.take()
}

// Whatever block an annotation sits in, none of its words are visible: not in the
// structure, the search text or the word count.
func TestRTFAnnotationsNeverLeak(t *testing.T) {
	cases := []struct{ name, src, visible string }{
		{"in a paragraph", `Before {§*§Scrv_annot SECRET one} after§par`, "Before after"},
		{"across paragraphs", `A {§*§Scrv_annot SECRET one§par SECRET two§par SECRET three} B§par`, "A B"},
		{"a whole paragraph", `{§*§Scrv_annot SECRET only}§par Visible§par`, "Visible"},
		{"with percent signs", `100% {§*§Scrv_annot SECRET %% and % and %%%} 50%§par`, "100% 50%"},
		{"percent next to the note", `a%{§*§Scrv_annot SECRET}%b§par`, "a%%b"},
		{"markdown inside", `x {§*§Scrv_annot SECRET *star* # head - list [[Wiki]] <b> ` + "`code`" + `} y§par`, "x y"},
		{"in a heading", `§outlinelevel0 Title {§*§Scrv_annot SECRET} end§par`, "Title end"},
		{"in a list item", `§ls1§ilvl0 {§listtext •§tab}item {§*§Scrv_annot SECRET} here§par`, "item here"},
		{"in a bold run", `§b bold {§*§Scrv_annot SECRET} still§b0 §par`, "bold still"},
		{"in a link", `{§field{§*§fldinst HYPERLINK "https://e.com/"}{§fldrslt text {§*§Scrv_annot SECRET} more}}§par`, "text more"},
		{"in a footnote", `x{§footnote note {§*§Scrv_annot SECRET} end}§par`, "x"},
		{"a comment", `a {§*§Scrv_cmt SECRET} b§par`, "a b"},
		{"a word annotation", `a {§*§annotation SECRET} b§par`, "a b"},
		{"empty", `a {§*§Scrv_annot } b§par`, "a b"},
		{"two in a row", `a {§*§Scrv_annot SECRET}{§*§Scrv_annot SECRET2} b§par`, "a b"},
		{"after a line break", `a§line b {§*§Scrv_annot SECRET}§line c§par`, "a b c"},
	}
	for _, c := range cases {
		md := rtfMD(t, "{§rtf1§ansi "+c.src+"}")
		d := text.Structure(md, text.StructOptions{})
		got := strings.Join(strings.Fields(text.PlainText(d.Nodes)), " ")
		if strings.Contains(strings.ToLower(got), "secret") {
			t.Errorf("%s: a note is visible: %q\nmarkdown: %q", c.name, got, md)
		}
		if got != c.visible {
			t.Errorf("%s: visible %q, want %q\nmarkdown: %q", c.name, got, c.visible, md)
		}
		if body := text.SearchBody(md); strings.Contains(strings.ToLower(body), "secret") {
			t.Errorf("%s: the search text holds a note: %q", c.name, body)
		}
		if n := text.WordCount(md); n != len(strings.Fields(c.visible)) && c.name != "in a footnote" && !strings.Contains(c.name, "percent") {
			t.Errorf("%s: %d words in %q (visible %q)", c.name, n, md, c.visible)
		}
		if links := text.WikiLinks(md); len(links) != 0 {
			t.Errorf("%s: a wiki link: %+v in %q", c.name, links, md)
		}
	}
}

// What a text file says about Markdown is only text.
func TestRTFTextCannotBeMarkdown(t *testing.T) {
	src := "{§rtf1§ansi # not a heading§par - not a list§par 1. nor this§par :::directive§par [[not a link]]§par [^1] and [^1]: def§par " +
		"<scriobh-ghost>g</scriobh-ghost> <!-- c -->§par %% not a note %%§par ---§par ===§par ```§par > quote§par | a | b |§par\n" +
		"x§line # inside§line    code?§par}"
	md := rtfMD(t, src)
	d := text.Structure(md, text.StructOptions{})
	for i, n := range d.Nodes {
		if n.Kind != text.NPara {
			t.Errorf("block %d is kind %v in %q", i, n.Kind, md)
		}
	}
	plain := text.PlainText(d.Nodes)
	for _, want := range []string{"# not a heading", "- not a list", "1. nor this", ":::directive", "[[not a link]]", "[^1] and [^1]: def", "<scriobh-ghost>", "%% not a note %%", "---", "===", "```", "> quote", "| a | b |", "# inside"} {
		if !strings.Contains(plain, want) {
			t.Errorf("%q is not plain text in %q\nmarkdown %q", want, plain, md)
		}
	}
	if len(text.WikiLinks(md)) != 0 {
		t.Errorf("wiki links in %q", md)
	}
}

// A link to another document cannot make a link to something else.
func TestScrivenerLinkTargetsCannotInject(t *testing.T) {
	for _, title := range []string{"Evil ]] %% [[x", "A|B", "Heading#1", "a^b", "<scriobh-ghost>", "x\\y", "ok title", " lead", "with  two"} {
		w := wikiFor(title, "label")
		if w == "" {
			continue
		}
		if title != "ok title" {
			t.Errorf("title %q became %q", title, w)
		}
		if w != "[[ok title|label]]" {
			t.Errorf("link %q", w)
		}
	}
	if w := wikiFor("Dawn", "100% [[x]] label"); w != "" {
		t.Errorf("an unsafe label made a link that is not the writer's words: %q", w)
	}
	if w := wikiFor("Dawn", "dawn"); w != "[[Dawn]]" {
		t.Errorf("the same label: %q", w)
	}
}

// ---- XML --------------------------------------------------------------------------------------------------------

func TestXMLHostile(t *testing.T) {
	cases := []struct {
		name, src string
		want      error
	}{
		{"a DOCTYPE with entities", `<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;">]><r>&b;</r>`, errXMLDoctyp},
		{"a DOCTYPE with an external entity", `<!DOCTYPE r [<!ENTITY x SYSTEM "file:///etc/passwd">]><r>&x;</r>`, errXMLDoctyp},
		{"deep nesting", strings.Repeat("<a>", 100_000) + strings.Repeat("</a>", 100_000), errXMLDeep},
		{"too many tokens", "<r>" + strings.Repeat("<a/>", 600_000) + "</r>", errXMLTokens},
		{"too much text", "<r>" + strings.Repeat("x", 3<<20) + "</r>", errXMLText},
	}
	for _, c := range cases {
		var err error
		d, a := measure(func() { _, err = parseXML([]byte(c.src), xmlLimits{tokens: 500_000, text: 2 << 20}) })
		if err != c.want {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		within(t, c.name, d, a, 4*time.Second, 200)
	}
	// An undefined entity is not expanded and is an error, not text.
	if _, err := parseXML([]byte(`<r>&nbsp;</r>`), xmlLimits{tokens: 100, text: 100}); err == nil {
		t.Errorf("an undefined entity was accepted")
	}
	// A skipped subtree (comments) is not built, but still pays and still has a depth limit.
	skip := "<c><item>" + strings.Repeat("<wp:comment xmlns:wp=\"http://wordpress.org/export/1.2/\"><x>y</x></wp:comment>", 200_000) + "</item></c>"
	var n int
	var skipped map[string]int
	var err error
	d, a := measure(func() {
		skipped, err = streamXML([]byte(skip), "item", xmlLimits{tokens: 5_000_000, text: 4 << 20}, []string{"wp:comment"}, nil, func(x *xnode) error { n++; return nil })
	})
	if err != nil || n != 1 || skipped["wp:comment"] != 200_000 {
		t.Errorf("stream: %v %d %v", err, n, skipped)
	}
	within(t, "200,000 comments", d, a, 6*time.Second, 300)
	deepSkip := "<item>" + `<wp:comment xmlns:wp="http://wordpress.org/export/1.2/">` + strings.Repeat("<a>", 100_000) + "</item>"
	if _, err := streamXML([]byte(deepSkip), "item", xmlLimits{tokens: 5_000_000, text: 4 << 20}, []string{"wp:comment"}, nil, func(*xnode) error { return nil }); err == nil {
		t.Errorf("a deep skipped subtree was accepted")
	}
}

func TestXMLEncodings(t *testing.T) {
	// UTF-16 with a byte-order mark, both orders
	for _, le := range []bool{true, false} {
		src := `<?xml version="1.0" encoding="UTF-16"?><r>é — 日本</r>`
		var b []byte
		if le {
			b = []byte{0xFF, 0xFE}
		} else {
			b = []byte{0xFE, 0xFF}
		}
		for _, r := range src {
			if r > 0xFFFF {
				continue
			}
			if le {
				b = append(b, byte(r), byte(r>>8))
			} else {
				b = append(b, byte(r>>8), byte(r))
			}
		}
		out, notes := decodeXML(b)
		n, err := parseXML(out, xmlLimits{tokens: 100, text: 1000})
		if err != nil || n.text != "é — 日本" || len(notes) == 0 {
			t.Errorf("utf-16 le=%v: %v %q %v", le, err, n.text, notes)
		}
	}
	// A UTF-8 byte-order mark, Latin-1 with a declaration, no declaration at all, control characters
	out, _ := decodeXML(append([]byte{0xEF, 0xBB, 0xBF}, `<r>ok</r>`...))
	if n, err := parseXML(out, xmlLimits{tokens: 10, text: 10}); err != nil || n.text != "ok" {
		t.Errorf("bom: %v", err)
	}
	out, notes := decodeXML([]byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><r>caf\xe9</r>"))
	if n, err := parseXML(out, xmlLimits{tokens: 10, text: 10}); err != nil || n.text != "café" || len(notes) == 0 {
		t.Errorf("latin-1: %v %q %v", err, n.text, notes)
	}
	out, _ = decodeXML([]byte("<r>caf\xe9 \x93x\x94</r>"))
	if n, err := parseXML(out, xmlLimits{tokens: 10, text: 20}); err != nil || n.text != "café “x”" {
		t.Errorf("no declaration: %v %q", err, n.text)
	}
	out, notes = decodeXML([]byte("<r>a\x01b\x0bc\x1fd</r>"))
	if n, err := parseXML(out, xmlLimits{tokens: 10, text: 20}); err != nil || n.text != "a b c d" || len(notes) == 0 {
		t.Errorf("control characters: %v %q %v", err, n.text, notes)
	}
}

// ---- zips and listings --------------------------------------------------------------------------------------------

func zipBytes(t testing.TB, entries []struct {
	name string
	data []byte
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	zw.Close()
	return buf.Bytes()
}

type ent = struct {
	name string
	data []byte
}

func TestScrivenerZipHostile(t *testing.T) {
	// too many entries
	var many []ent
	for i := 0; i <= maxScrivEntries; i++ {
		many = append(many, ent{fmt.Sprintf("a/%d.txt", i), nil})
	}
	if _, err := readZipFor(zipBytes(t, many), maxScrivEntries, maxScrivTotal, scrivWant); err == nil {
		t.Errorf("a zip of %d entries was accepted", len(many))
	}
	// a file past the per-file limit is listed by size, not read; together they pass the total
	big := bytes.Repeat([]byte("a"), maxScrivFile)
	var total []ent
	for i := 0; i < maxScrivTotal/maxScrivFile+3; i++ {
		total = append(total, ent{fmt.Sprintf("p.scriv/Files/Data/U%d/content.rtf", i), big})
	}
	z := zipBytes(t, total)
	t.Logf("a zip of %d MB holding %d MB", len(z)>>20, len(total)*maxScrivFile>>20)
	var err error
	d, peak := measurePeak(func() { _, err = readZipFor(z, maxScrivEntries, maxScrivTotal, scrivWant) })
	if err == nil {
		t.Errorf("more than the total limit was read")
	}
	t.Logf("inflating past the total: %v, peak heap %d MB", d, peak)
	if peak > uint64(640*(1+(int(hostileFactor)-1)/5)) {
		t.Errorf("inflating past the total took %v and %d MB", d, peak)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 6*time.Second*hostileFactor {
			t.Errorf("inflating past the total took %v and %d MB", d, peak)
		}
	})
	over := zipBytes(t, []ent{{"p.scriv/p.scrivx", []byte("<x/>")}, {"p.scriv/Files/Data/U/content.rtf", append(big, 'x')}})
	fs, err := readZipFor(over, maxScrivEntries, maxScrivTotal, scrivWant)
	if err != nil || len(fs) != 2 || len(fs[1].Data) != 0 || fs[1].Size == 0 {
		t.Errorf("an oversize file: %v %+v", err, fs)
	}
	// unsafe names are not read as part of the project
	res, err := scrivenerConvert([]File{
		{Path: "p.scriv/p.scrivx", Data: []byte(`<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="A" Type="DraftFolder"><Title>Draft</Title><Children><BinderItem UUID="B" Type="Text"><Title>One</Title></BinderItem></Children></BinderItem></Binder></ScrivenerProject>`)},
		{Path: "p.scriv/Files/Data/B/content.rtf", Data: []byte("{\\rtf1 inside}")},
		{Path: "../evil/Files/Data/B/content.rtf", Data: []byte("{\\rtf1 outside}")},
		{Path: "/abs/Files/Data/B/content.rtf", Data: []byte("{\\rtf1 absolute}")},
		{Path: "p.scriv\\..\\Files/Data/B/content.rtf", Data: []byte("{\\rtf1 dots}")},
		{Path: "p.scriv/Files/Data/B/link.rtf", Size: -1},
	}, "p.scriv", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.docs[0].body; !strings.Contains(got, "inside") || strings.Contains(got, "outside") || strings.Contains(got, "absolute") || strings.Contains(got, "dots") {
		t.Errorf("the body came from %q", got)
	}
}

func TestScrivenerBinderHostile(t *testing.T) {
	scrivx := func(inner string) []File {
		return []File{{Path: "p.scrivx", Data: []byte(`<ScrivenerProject Version="2.0"><Binder>` + inner + `</Binder></ScrivenerProject>`)}}
	}
	// duplicate UUIDs: both documents exist, with the same text, and it is said
	dup := `<BinderItem UUID="A" Type="DraftFolder"><Title>Draft</Title><Children>` +
		`<BinderItem UUID="X" Type="Text"><Title>One</Title></BinderItem><BinderItem UUID="X" Type="Text"><Title>Two</Title></BinderItem></Children></BinderItem>`
	fs := append(scrivx(dup), File{Path: "Files/Data/X/content.rtf", Data: []byte("{\\rtf1 same}")})
	res, err := scrivenerConvert(fs, "p", convOpts{})
	if err != nil || len(res.docs) != 2 || res.docs[0].body != res.docs[1].body || !strings.Contains(strings.Join(res.warnings, "|"), "share an id") {
		t.Errorf("duplicate ids: %v %+v", err, res)
	}
	// a Draft inside a Draft, a Trash inside the Draft: not read twice, not looped
	nest := `<BinderItem UUID="A" Type="DraftFolder"><Title>Draft</Title><Children><BinderItem UUID="D" Type="DraftFolder"><Title>Inner</Title><Children>` +
		`<BinderItem UUID="T" Type="TrashFolder"><Title>Trash</Title><Children><BinderItem UUID="Z" Type="Text"><Title>Gone</Title></BinderItem></Children></BinderItem>` +
		`<BinderItem UUID="K" Type="Text"><Title>Kept</Title></BinderItem></Children></BinderItem></Children></BinderItem>`
	res, err = scrivenerConvert(scrivx(nest), "p", convOpts{})
	if err != nil || len(res.docs) != 1 || len(res.docs[0].kids) != 1 || res.docs[0].kids[0].title != "Kept" {
		t.Errorf("nesting: %v %+v", err, res)
	}
	// two hundred nested folders: the importer's path limit is not a loss of documents
	var b strings.Builder
	b.WriteString(`<BinderItem UUID="A" Type="DraftFolder"><Title>Draft</Title><Children>`)
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, `<BinderItem UUID="F%d" Type="Folder"><Title>Level %d</Title><Children>`, i, i)
	}
	b.WriteString(`<BinderItem UUID="L" Type="Text"><Title>Leaf</Title></BinderItem>`)
	for i := 0; i < 60; i++ {
		b.WriteString(`</Children></BinderItem>`)
	}
	b.WriteString(`</Children></BinderItem>`)
	res, err = scrivenerConvert(scrivx(b.String()), "p", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	files := res.toFiles()
	in, err := intakeFiles(files, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.md) != 61 {
		t.Errorf("%d of 61 documents survived the path limit (skipped: %v)", len(in.md), in.skipped)
	}
	if !strings.Contains(strings.Join(res.warnings, "|"), "deeper than a path may be") {
		t.Errorf("flattening was not said: %v", res.warnings)
	}
	// a hundred thousand binder items
	var c strings.Builder
	c.WriteString(`<BinderItem UUID="A" Type="DraftFolder"><Title>Draft</Title><Children>`)
	for i := 0; i < maxBinderItems+10; i++ {
		c.WriteString(`<BinderItem Type="Text"><Title>x</Title></BinderItem>`)
	}
	c.WriteString(`</Children></BinderItem>`)
	var got *cresult
	d, a := measure(func() { got, err = scrivenerConvert(scrivx(c.String()), "p", convOpts{}) })
	if err == nil {
		t.Errorf("a binder of %d items was accepted (%d docs)", maxBinderItems+10, len(got.docs))
	}
	within(t, "a huge binder", d, a, 6*time.Second, 300)
	// bad project files
	for name, src := range map[string]string{
		"not xml":        "hello",
		"wrong root":     `<html><body/></html>`,
		"a DOCTYPE":      `<!DOCTYPE r [<!ENTITY a "b">]><ScrivenerProject/>`,
		"truncated":      `<ScrivenerProject><Binder><BinderItem`,
		"no binder":      `<ScrivenerProject Version="2.0"/>`,
		"deep nesting":   `<ScrivenerProject><Binder>` + strings.Repeat("<BinderItem><Children>", 5000) + `</Binder></ScrivenerProject>`,
		"huge title":     `<ScrivenerProject><Binder><BinderItem Type="DraftFolder"><Title>` + strings.Repeat("x", 100_000) + `</Title></BinderItem></Binder></ScrivenerProject>`,
		"control chars":  "<ScrivenerProject><Binder><BinderItem Type=\"DraftFolder\"><Title>a\x01b</Title></BinderItem></Binder></ScrivenerProject>",
		"title with RTL": "<ScrivenerProject><Binder><BinderItem Type=\"DraftFolder\"><Children><BinderItem Type=\"Text\"><Title>a\u202Eb</Title></BinderItem></Children></BinderItem></Binder></ScrivenerProject>",
	} {
		r, err := scrivenerConvert([]File{{Path: "p.scrivx", Data: []byte(src)}}, "p", convOpts{})
		switch name {
		case "huge title", "control chars":
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case "title with RTL":
			if err != nil || len(r.docs) != 1 || strings.ContainsRune(r.docs[0].title, '\u202E') {
				t.Errorf("%s: %v %+v", name, err, r)
			}
		default:
			if err == nil {
				t.Errorf("%s was accepted", name)
			}
		}
	}
}
