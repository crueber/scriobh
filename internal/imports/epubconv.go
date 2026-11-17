package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/ops"
)

// EPUB import. The book is read (epubread.go), each content file scanned once
// (epubscan.go), the footnotes found and taken out of the flow (epubnotes.go),
// the text cut into sections, and each section written as Markdown through the
// importer's one emitter (the HTML walker of htmlmd.go and the sink of mdsink.go,
// so a book can never say anything that is not text). The sections become the
// tree of documents the converters of Scrivener, WordPress and Ghost make
// (convert.go), and from there the preview, the token, the edits, the caps, the
// pictures and the transaction are the folder importer's own.
//
// Where the cuts come from:
//
//	toc       the navigation: an entry that names a file starts a section there, an entry
//	          that names a fragment starts it at that element. Files the navigation does not
//	          name continue the section before them, except front matter before the first entry
//	          and back matter after the last (named by epub:type or by its title).
//	files     one section for each content file of the reading order.
//	headings  one section for each heading of the book's chapter level (the shallowest level
//	          used more than once), for a book that is one long file with no navigation.
//	auto      toc when the navigation has two or more usable entries, else files when there
//	          are several, else headings.

// Options of the EPUB import.
const (
	EpubSectionsAuto     = "auto"
	EpubSectionsTOC      = "toc"
	EpubSectionsFiles    = "files"
	EpubSectionsHeadings = "headings"
	EpubPagesSkip        = "skip"
	EpubPagesKeep        = "keep"
	// LayoutEpub is the layout name of an EPUB import.
	LayoutEpub = "epub"
)

// epubOpts are the options of the EPUB converter.
type epubOpts struct {
	sections string
	pages    string
}

// ecut is a place a section begins.
type ecut struct {
	doc, off int
	label    string
	depth    int
	listed   bool // from the navigation
	types    string
	frag     string
	entry    *tocEntry
}

// epiece is a stretch of one file.
type epiece struct{ doc, a, b int }

// esec is a section while the tree is built.
type esec struct {
	cut      ecut
	pieces   []epiece
	title    string
	titleSrc string // data-title, toc, heading, file
	kind     string
	body     string
	words    int
	source   string
	skipWhy  string
	kids     []*esec
	parent   *esec
	files    []int // the files the section's text comes from
	types    string
	chapter  int // index in the edition's chapter list
	doc      *cdoc
}

// epubChapter is one chapter of the book as read, for the edition record.
type epubChapter struct {
	cd        *cdoc  // the document made for it; its path is the plan's node key
	parentIdx int    // the chapter it sits in, -1 at the top
	key       string // the plan node key (the made file's path)
	parentKey string
	kind      string
	title     string
	source    string
	words     int
	baseline  string // the body as converted, with picture links by number
}

// epubData is what an EPUB conversion hands the importer besides documents.
// It is only ever read once made (the intake is shared by the cache).
type epubData struct {
	info     EpubInfo
	chapters []epubChapter
	sha      string
	size     int64
	label    string
	meta     map[string]any
	ident    string
	coverN   int // the picture number of the cover, 0 when none was kept
}

// MaxEpubChapters is how many chapters one book is read as: what an edition records, and so what the preview
// may propose. (The documents made are fewer still: MaxMarkdown.)
const MaxEpubChapters = editions.MaxChapters

func epubConvert(data []byte, name string, o convOpts, eo epubOpts) (res *cresult, ed *epubData, err error) {
	// One place for what both the import and ReadEpub must refuse: a book that cannot be kept, and a book that breaks the reader.
	defer func() {
		if r := recover(); r != nil {
			res, ed, err = nil, nil, errors.New("the book could not be read")
		}
	}()
	if len(data) > editions.MaxFileBytes {
		// The book is kept as received, and a book that cannot be kept is not previewed as if it could.
		return nil, nil, &ops.Error{Code: ops.CodeTooLarge, Message: fmt.Sprintf("A book of more than %d MB cannot be imported, because its file is kept with the project.", editions.MaxFileBytes>>20),
			Data: map[string]any{"max_file_bytes": editions.MaxFileBytes}}
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	book, err := readEpub(data, o.stop)
	if err != nil {
		return nil, nil, err
	}
	book.sha = sha
	res = &cresult{layout: LayoutEpub, srcHash: sha, meta: map[string]any{}, warnings: append([]string{}, book.warnings...)}
	ours := strings.HasPrefix(strings.ToLower(book.meta.generator), "scriobh")
	ed = &epubData{sha: sha, size: int64(len(data)), meta: map[string]any{}}

	// ---- the files of the flow -----------------------------------------------------------------------
	var docs []*epubDoc
	byPath := map[string]int{}
	budget := maxScanBook
	seen := map[string]bool{}
	landByPath := landmarkTypes(book.lands)
	fixedPages, notContent, tooBig := 0, 0, 0
	var skippedFiles []EpubFile
	for si, sp := range book.spine {
		if si%32 == 0 && o.stop() != nil {
			return nil, nil, o.stop()
		}
		it := sp.item
		if seen[it.path] {
			continue
		}
		seen[it.path] = true
		if (strings.Contains(sp.props, "rendition:layout-pre-paginated") || it.hasProp("rendition:layout-pre-paginated")) && book.meta.version != "" {
			fixedPages++
			skippedFiles = append(skippedFiles, EpubFile{Path: it.path, Skipped: "fixed layout page"})
			continue
		}
		switch it.mediaType {
		case "application/xhtml+xml", "text/html", "application/x-dtbook+xml", "text/x-oeb1-document", "":
		default:
			notContent++
			skippedFiles = append(skippedFiles, EpubFile{Path: it.path, Skipped: "not an XHTML page (" + cleanText(it.mediaType, 60) + ")"})
			continue
		}
		f := book.get(it.path)
		if f == nil || f.data == nil {
			tooBig++
			skippedFiles = append(skippedFiles, EpubFile{Path: it.path, Skipped: "larger than the " + strconv.Itoa(maxEpubXML>>20) + " MiB limit for one file, or not readable"})
			continue
		}
		d := &epubDoc{path: it.path, item: it, linear: sp.linear, src: prepareXHTML(f.data), stem: strings.TrimSuffix(path.Base(it.path), path.Ext(it.path))}
		d.sc = scanXHTML(d.src, &budget)
		if d.sc.truncated {
			res.warn("%s is too intricate to read in full (more than %d elements); part of it may be missing.", it.path, maxScanElems)
		}
		d.types = d.sc.bodyTypes
		n := 0
		for _, e := range d.sc.elems {
			if e.start >= d.sc.body && e.types != "" {
				d.types += " " + e.types
			}
			if n++; n > 8 {
				break
			}
		}
		d.types += landByPath[d.path]
		byPath[d.path] = len(docs)
		docs = append(docs, d)
	}
	if fixedPages > 0 && len(docs) == 0 {
		return nil, nil, errEpubFixed
	}
	if fixedPages > 0 {
		res.warn("%d fixed-layout page(s) were left out: they are pictures of pages, not text.", fixedPages)
	}
	if len(docs) == 0 {
		return nil, nil, errEpubNoSpine
	}

	// Pages that are not the book's text: the cover, a title page, the contents.
	pages := eo.pages
	if pages == "" {
		pages = EpubPagesSkip
	}
	var coverDoc string
	for _, l := range book.lands {
		if l.typ == "cover" && l.path != "" {
			coverDoc = l.path
		}
	}
	for i, d := range docs {
		switch {
		case book.tocPages[d.path] || typeAny(d.types, "toc") && d.sc.textBytes < 4000 && len(d.sc.links) > 3:
			d.skip = "the contents page (a book's contents are made when it is compiled)"
		case d.path == coverDoc || typeHas(d.types, "cover") || (i == 0 && d.sc.textBytes < 40 && d.sc.svgs > 0 && d.sc.images == 0):
			d.skip = "the cover page (the cover picture is kept as an attachment)"
		case pages == EpubPagesSkip && typeAny(d.types, "titlepage", "halftitlepage") && d.sc.textBytes < 1500:
			d.skip = "the title page (the title and author are the project's)"
		}
	}

	notes := findNotes(docs, byPath, o.stop)
	if err := o.stop(); err != nil {
		return nil, nil, err
	}
	if len(notes) > 0 {
		ed.meta["footnotes"] = len(notes)
	}
	for _, d := range docs {
		if d.skip == "" && d.notesConsumed > 0 && d.textIn(0, len(d.src)) == 0 && d.sc.images == 0 {
			d.skip = "notes only (the notes are footnotes of the chapters they belong to)"
		}
	}

	// ---- stylesheets and pictures ------------------------------------------------------------------------
	var sheets []string
	var itemIDs []string
	for id := range book.items {
		itemIDs = append(itemIDs, id)
	}
	sort.Strings(itemIDs)
	for _, id := range itemIDs {
		it := book.items[id]
		if it.mediaType == "text/css" && it.path != "" {
			if f := book.get(it.path); f != nil && f.data != nil {
				sheets = append(sheets, string(f.data))
			}
		}
	}
	classFmt := parseCSSFormats(sheets)
	pb := newPicBox(o)
	res.pics = pb

	// ---- the sections --------------------------------------------------------------------------------
	flow := make([]int, 0, len(docs)) // the files that are text
	for i, d := range docs {
		if d.skip == "" {
			flow = append(flow, i)
		}
	}
	strategy := eo.sections
	if strategy == "" {
		strategy = EpubSectionsAuto
	}
	cuts, unresolved, outOfOrder := tocCuts(book, docs, byPath)
	if unresolved > 0 {
		res.warn("%d entr%s of the table of contents name%s a place that is not in the book's text and %s left out.", unresolved, plural(unresolved, "y", "ies"), plural(unresolved, "s", ""), plural(unresolved, "was", "were"))
	}
	if outOfOrder > 0 {
		res.warn("%d entr%s of the table of contents %s out of reading order and %s left out.", outOfOrder, plural(outOfOrder, "y", "ies"), plural(outOfOrder, "is", "are"), plural(outOfOrder, "was", "were"))
	}
	if strategy == EpubSectionsAuto {
		switch {
		case len(cuts) >= 2:
			strategy = EpubSectionsTOC
		case len(flow) >= 2:
			strategy = EpubSectionsFiles
		default:
			strategy = EpubSectionsHeadings
		}
	}
	if strategy == EpubSectionsTOC && len(cuts) == 0 {
		res.warn("The book has no usable table of contents, so each file is a section.")
		strategy = EpubSectionsFiles
	}
	switch strategy {
	case EpubSectionsFiles:
		cuts = fileCuts(docs, flow)
	case EpubSectionsHeadings:
		cuts = headingCuts(docs, flow)
		if len(cuts) < 2 {
			cuts = fileCuts(docs, flow)
			strategy = EpubSectionsFiles
		}
	}
	if len(cuts) > MaxEpubChapters {
		res.warn("The book has %d places a chapter could begin; the first %d are read.", len(cuts), MaxEpubChapters)
		cuts = cuts[:MaxEpubChapters]
	}
	bookTitle := book.meta.title
	secs := buildSections(docs, flow, cuts, strategy, landByPath, bookTitle)
	if len(secs) > MaxEpubChapters {
		res.warn("The book has %d sections; the first %d are read.", len(secs), MaxEpubChapters)
		secs = secs[:MaxEpubChapters]
	}

	// ---- the text ------------------------------------------------------------------------------------------
	sk := newSink()
	var stats htmlStats
	for i, s := range secs {
		if i%64 == 0 && o.stop() != nil {
			return nil, nil, o.stop()
		}
		if err := convertSection(sk, &stats, pb, book, docs, notes, classFmt, s, ours, strategy); err != nil {
			return nil, nil, err
		}
	}

	// ---- kinds and the tree -------------------------------------------------------------------------------
	roots := classifyAndNest(secs, docs, byPath, ed, book)
	for _, d := range docs {
		if d.skip != "" {
			skippedFiles = append(skippedFiles, EpubFile{Path: d.path, Skipped: d.skip})
		}
	}

	// ---- the project and the report ---------------------------------------------------------------------------
	m := book.meta
	author := strings.Join(m.authors, ", ")
	res.project = cproject{title: cleanText(m.title, 200), subtitle: cleanText(m.subtitle, 200), author: cleanText(author, 200), language: m.language}
	if res.project.title == "" {
		res.project.title = cleanText(prettify(stem(folderName(name))), 200)
	}
	res.docs = roots
	for _, r := range secs {
		if r.skipWhy != "" {
			res.skipped = append(res.skipped, Skipped{Path: r.source, Reason: r.skipWhy})
		}
	}
	for _, f := range skippedFiles {
		res.skipped = append(res.skipped, Skipped{Path: f.Path, Reason: f.Skipped})
	}
	// the cover
	cover := &EpubCover{}
	if it := coverItem(book); it != nil {
		cover.Path = it.path
		if f := book.get(it.path); f != nil && f.data != nil {
			if p, why := pb.add(it.path, f.data, "cover"); p != nil {
				p.always = true
				ed.coverN = p.n
				cover.Kept, cover.Width, cover.Height = true, p.pic.Width, p.pic.Height
				if err := coverlayout.CheckPicture(p.pic.Data); err != nil {
					cover.UseReason = err.Error()
				} else {
					cover.CanUse = true
				}
			} else {
				cover.Reason = why
			}
		} else {
			cover.Reason = "not readable"
		}
		if it.mediaType == "image/svg+xml" && cover.Reason != "" {
			cover.Reason = "an SVG picture is not kept"
		}
	}
	ed.info = EpubInfo{Version: m.version, Title: m.title, Subtitle: m.subtitle, Authors: m.authors, Contributors: m.contributors, Language: m.language,
		Identifier: m.identifier, Publisher: m.publisher, Date: m.date, Rights: m.rights, Description: m.description, Subjects: m.subjects,
		Series: m.series, SeriesIndex: m.seriesIndex, Direction: map[bool]string{true: "rtl", false: "ltr"}[m.rtl],
		Strategy: strategy, Contents: map[string]string{"nav": "navigation document", "ncx": "NCX", "": "none"}[book.tocFrom],
		SHA256: sha, Size: int64(len(data)), Cover: cover, Generator: m.generator}
	ed.info.Files = fileMap(docs, secs, skippedFiles)
	ed.ident = m.identifier
	ed.label = cleanLine1(strings.TrimSpace(strings.Join(nonEmpty(m.title, yearOf(m.date)), " (")), 180)
	if yearOf(m.date) != "" && m.title != "" {
		ed.label += ")"
	}
	if ed.label == "" {
		ed.label = cleanLine1(prettify(stem(folderName(name))), 180)
	}
	ed.info.Label = ed.label
	em := ed.meta
	em["title"], em["subtitle"], em["authors"], em["language"] = m.title, m.subtitle, m.authors, m.language
	em["epub_version"], em["publisher"], em["date"], em["identifier"] = m.version, m.publisher, m.date, m.identifier
	if m.idScheme != "" {
		em["identifier_scheme"] = m.idScheme
	}
	if m.series != "" {
		em["series"], em["series_index"] = m.series, m.seriesIndex
	}
	if len(m.contributors) > 0 {
		em["contributors"] = m.contributors
	}
	if m.rights != "" {
		em["rights"] = cleanText(m.rights, 400)
	}
	if m.description != "" {
		em["description"] = cleanText(m.description, 1000)
	}
	if m.generator != "" {
		em["generator"] = m.generator
	}
	if m.modified != "" {
		em["modified"] = m.modified
	}
	em["direction"], em["sections"], em["contents"], em["files"] = ed.info.Direction, strategy, book.tocFrom, len(flow)
	em["bytes"], em["sha256"] = len(data), sha
	for k, v := range em {
		res.meta[k] = v
	}
	res.meta["epub"] = true
	if book.encryptedFonts > 0 {
		res.meta["obfuscated_fonts"] = book.encryptedFonts
	}
	// what the walk met and did not keep
	if stats.scripts+stats.iframes+stats.forms+stats.embeds+stats.media > 0 {
		res.meta["dropped_active_content"] = stats.scripts + stats.iframes + stats.forms + stats.embeds + stats.media
		res.warn("Scripts, forms, frames and embedded media (%d) in the book were removed; none was run.", stats.scripts+stats.iframes+stats.forms+stats.embeds+stats.media)
	}
	if stats.badLinks > 0 {
		res.meta["links_not_kept"] = stats.badLinks
	}
	if stats.images > 0 {
		res.meta["images_not_stored"] = stats.images
	}
	if n := stats.dropped["svg"]; n > 0 {
		res.meta["svg_graphics"] = n
	}
	if stats.notes > 0 {
		res.meta["footnotes_written"] = stats.notes
	}
	if stats.tables > 0 {
		res.meta["tables"] = stats.tables
	}
	if stats.tooBig > 0 {
		res.warn("Some of the book's text was too large or too intricate to read in full.")
	}
	res.meta["spine_files"] = len(book.spine)
	if fixedPages > 0 {
		res.meta["fixed_layout_pages"] = fixedPages
	}
	if book.fonts > 0 {
		res.meta["fonts"] = book.fonts
	}
	if book.skippedEntries > 0 {
		res.meta["zip_entries_ignored"] = book.skippedEntries
	}
	if len(notes) > 0 && stats.notes == 0 {
		res.warn("Footnotes were found but none could be written.")
	}
	ed.info.Footnotes = len(notes)
	if m.fixedLayout {
		return nil, nil, errEpubFixed
	}
	return res, ed, nil
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func cleanLine1(s string, max int) string { return cleanText(s, max) }

var reYear = regexp.MustCompile(`^\s*(\d{4})`)

func yearOf(date string) string {
	if m := reYear.FindStringSubmatch(date); m != nil {
		return m[1]
	}
	return ""
}

// coverItem is the manifest item of the cover picture.
func coverItem(b *epubBook) *epubItem {
	for _, id := range sortedKeys(b.items) {
		if it := b.items[id]; it.hasProp("cover-image") && it.path != "" {
			return it
		}
	}
	if b.coverID != "" {
		if it := b.items[b.coverID]; it != nil && it.path != "" {
			if strings.HasPrefix(it.mediaType, "image/") {
				return it
			}
		}
	}
	return nil
}

// ---- cuts -----------------------------------------------------------------------------------

// tocCuts turns the navigation into cuts: where each entry that names a place in
// the flow begins. Entries out of reading order, and entries naming something
// that is not in the flow, are left out and counted.
func tocCuts(b *epubBook, docs []*epubDoc, byPath map[string]int) (cuts []ecut, unresolved, outOfOrder int) {
	var walk func(es []*tocEntry, depth int)
	lastDoc, lastOff := -1, -1
	walk = func(es []*tocEntry, depth int) {
		for _, e := range es {
			target := e
			// An entry with no link of its own (a group heading) begins where its first child does.
			di, ok := byPath[e.path]
			if e.path == "" || !ok || docs[di].skip != "" {
				if e.path != "" || len(e.children) == 0 {
					unresolved++
					walk(e.children, depth)
					continue
				}
				// a group heading: it begins where its first resolvable child begins
				var first *tocEntry
				var find func(cs []*tocEntry)
				find = func(cs []*tocEntry) {
					for _, c := range cs {
						if first != nil {
							return
						}
						if c.path != "" {
							if _, ok := byPath[c.path]; ok {
								first = c
								return
							}
						}
						find(c.children)
					}
				}
				find(e.children)
				if first == nil {
					walk(e.children, depth+1)
					continue
				}
				target = first
				di = byPath[first.path]
			}
			d := docs[di]
			off := 0
			if target.frag != "" {
				if idx, ok := d.sc.byID[target.frag]; ok {
					off = d.sc.elems[d.sc.blockOf(idx)].start
				} else {
					unresolved++
				}
			}
			if di < lastDoc || (di == lastDoc && off < lastOff) {
				outOfOrder++
				walk(e.children, depth+1)
				continue
			}
			lastDoc, lastOff = di, off
			types := e.types
			if off == 0 {
				types += " " + d.types
			} else if idx, ok := d.sc.byID[target.frag]; ok {
				types += " " + d.sc.elems[idx].types
			}
			cuts = append(cuts, ecut{doc: di, off: off, label: e.label, depth: depth, listed: true, types: types, frag: target.frag, entry: e})
			walk(e.children, depth+1)
		}
	}
	walk(b.toc, 0)
	return cuts, unresolved, outOfOrder
}

// fileCuts is one cut at the start of each file of the flow.
func fileCuts(docs []*epubDoc, flow []int) []ecut {
	var cuts []ecut
	for _, di := range flow {
		d := docs[di]
		cuts = append(cuts, ecut{doc: di, off: 0, depth: 0, types: d.types})
	}
	return cuts
}

// headingCuts is a cut at each heading of the chapter level: the shallowest of
// h1 to h3 that the book uses at least twice.
func headingCuts(docs []*epubDoc, flow []int) []ecut {
	count := map[int]int{}
	for _, di := range flow {
		for _, h := range docs[di].sc.heads {
			if h.level <= 3 {
				count[h.level]++
			}
		}
	}
	level := 0
	for l := 1; l <= 3; l++ {
		if count[l] >= 2 {
			level = l
			break
		}
	}
	if level == 0 {
		return nil
	}
	var cuts []ecut
	for _, di := range flow {
		d := docs[di]
		for _, h := range d.sc.heads {
			if h.level == level {
				cuts = append(cuts, ecut{doc: di, off: d.sc.elems[h.elem].start, label: cleanText(h.text, 300), types: d.sc.elems[h.elem].types})
			}
		}
	}
	return cuts
}

// ---- sections ------------------------------------------------------------------------------

var backTypeWords = []string{"backmatter", "appendix", "afterword", "colophon", "glossary", "index", "bibliography", "endnotes", "acknowledgments", "acknowledgements", "credits", "keywords", "revision-history", "errata"}

// buildSections cuts the flow at the cuts. Files before the first cut are front
// matter sections of their own; files after a cut that no cut names continue its
// section, except one that declares itself another kind of matter (epub:type), and,
// after the last cut, one whose type or title says it is back matter.
func buildSections(docs []*epubDoc, flow []int, cuts []ecut, strategy string, landByPath map[string]string, bookTitle string) []*esec {
	var secs []*esec
	if len(flow) == 0 {
		return nil
	}
	last := func() (int, int) { d := docs[flow[len(flow)-1]]; return flow[len(flow)-1], len(d.src) }
	// piecesBetween lists the stretches of the flow from (d1, o1) to (d2, o2).
	piecesBetween := func(d1, o1, d2, o2 int) []epiece {
		var out []epiece
		for _, di := range flow {
			if di < d1 || di > d2 {
				continue
			}
			a, b := 0, len(docs[di].src)
			if di == d1 {
				a = o1
			}
			if di == d2 {
				b = o2
			}
			if a < b {
				out = append(out, epiece{di, a, b})
			}
		}
		return out
	}
	// before the first cut
	if len(cuts) > 0 {
		c0 := cuts[0]
		for _, di := range flow {
			if di > c0.doc {
				break
			}
			end := len(docs[di].src)
			if di == c0.doc {
				end = c0.off
			}
			if end <= 0 {
				continue
			}
			if docs[di].textIn(0, end) == 0 && docs[di].sc.images == 0 {
				continue
			}
			secs = append(secs, &esec{cut: ecut{doc: di, off: 0, types: docs[di].types}, pieces: []epiece{{di, 0, end}}, kind: "front_matter"})
		}
	}
	for k, c := range cuts {
		ld, lo := last()
		d2, o2 := ld, lo
		if k+1 < len(cuts) {
			d2, o2 = cuts[k+1].doc, cuts[k+1].off
		}
		ps := piecesBetween(c.doc, c.off, d2, o2)
		s := &esec{cut: c}
		secs = append(secs, s)
		cur := s
		lastCut := k+1 == len(cuts)
		backNow := false
		for _, p := range ps {
			if p.doc == c.doc || strategy == EpubSectionsFiles {
				s.pieces = append(s.pieces, p)
				continue
			}
			// A file no cut names continues the section, unless it declares itself another kind of matter,
			// or, after the last cut, looks like back matter.
			d := docs[p.doc]
			split := false
			if p.a == 0 {
				own := 3 // the level of the heading the section itself opens with
				if h, ok := docs[c.doc].sc.firstHeadingIn(c.off, len(docs[c.doc].src)); ok {
					own = h.level
				}
				switch {
				case typeAny(d.types, backTypeWords...) || typeAny(d.types, "frontmatter", "bodymatter", "chapter", "part", "preface", "foreword", "dedication", "epigraph"):
					split = true
				case lastCut && (backNow || looksBack(d.docTitle(bookTitle))):
					split, backNow = true, true
				case d.opensWithHeading(own):
					// A file that begins with a heading as high as the section's own is a section of its own
					// (the contents left it out); one that begins mid-text, as a book split by size does, goes on.
					split = true
				}
			}
			if split {
				ns := &esec{cut: ecut{doc: p.doc, off: p.a, types: d.types}, pieces: []epiece{p}}
				secs = append(secs, ns)
				cur = ns
				continue
			}
			cur.pieces = append(cur.pieces, p)
		}
	}
	for _, s := range secs {
		seenDoc := map[int]bool{}
		for _, p := range s.pieces {
			if !seenDoc[p.doc] {
				seenDoc[p.doc] = true
				s.files = append(s.files, p.doc)
			}
		}
	}
	return secs
}

// docTitle is a title for a file that no navigation names: its first heading, its
// <title> unless that is the book's, its name.
func (d *epubDoc) docTitle(bookTitle string) string {
	if h, ok := d.sc.firstHeadingIn(0, len(d.src)); ok && cleanText(h.text, 300) != "" {
		return cleanText(h.text, 300)
	}
	if t := d.sc.titleTag; t != "" && !strings.EqualFold(t, bookTitle) && !genericTitle(t) {
		return t
	}
	return prettify(strings.NewReplacer("_", " ", "-", " ").Replace(d.stem))
}

func genericTitle(t string) bool {
	l := strings.ToLower(strings.TrimSpace(t))
	if l == "" || l == "untitled" || l == "document" || l == "unknown" || l == "cover" {
		return true
	}
	return strings.HasSuffix(l, ".xhtml") || strings.HasSuffix(l, ".html")
}

var (
	reBackTitle  = regexp.MustCompile(`(?i)^(?:acknowledg(?:e)?ments?|about the (?:author|authors|publisher|translator)|about (?:the )?author|also by\b.*|other (?:books|works|titles) by\b.*|books by\b.*|praise for\b.*|newsletter\b.*|sign up\b.*|bibliography|works cited|sources|notes|endnotes|glossary|index|appendix\b.*|afterword|colophon|credits|permissions|further reading|reading group guide|discussion questions|a note on (?:the )?(?:text|sources)|author'?s note|copyright(?: page| notice)?|about this (?:edition|book)|preview\b.*|excerpt\b.*|coming soon.*|the end|(?:an? |the )?(?:full )?project gutenberg\b.*licen[cs]e)$`)
	reFrontTitle = regexp.MustCompile(`(?i)^(?:title page|half[- ]?title(?: page)?|copyright(?: page| notice)?|dedication|epigraph|foreword|preface|contents|table of contents|praise for\b.*|also by\b.*|books by\b.*|other (?:books|works|titles) by\b.*|publisher'?s note|about this (?:edition|book)|acknowledg(?:e)?ments?|prelude|introduction|note to the reader|a note from the author|author'?s note)$`)
	reNumLabel   = regexp.MustCompile(`(?i)^(?:chapter|part|book|volume|section|act|canto|letter)\s+(?:[0-9]+|[ivxlcdm]+|(?:twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)(?:[- ]\w+)?|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth)\b[.:]?$`)
	rePartTitle  = regexp.MustCompile(`(?i)^(?:part|book|volume)\s+(?:[0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten|first|second|third|fourth|fifth)\b`)
)

func looksBack(title string) bool { return reBackTitle.MatchString(strings.TrimSpace(title)) }

// convertEpub reads the book into the intake the plan is built from. A book that
// cannot be read is refused with the reason; nothing is guessed.
func (s source) convertEpub() (in *intake, err error) {
	defer func() {
		if r := recover(); r != nil {
			in, err = nil, ops.Invalid("epub", "The book could not be read.")
		}
	}()
	o := s.conv
	o.ctx = s.ctx
	res, ed, err := epubConvert(s.epub, s.name, o, epubOpts{sections: o.epubSections, pages: o.epubPages})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var oe *ops.Error
		if errors.As(err, &oe) {
			return nil, oe
		}
		return nil, ops.Invalid("epub", capSentence(err.Error()))
	}
	files := res.toFiles()
	in, err = intakeFiles(files, false)
	if err != nil {
		return nil, ops.Invalid("epub", err.Error())
	}
	finalizeEpub(ed)
	res.pics.report(res)
	res.srcKind = "epub"
	res.epub = ed
	in.apply(res.asPrepass(newExtras(res.research, res.pics)))
	return in, nil
}

// opensWithHeading says the file begins, before any text, with a heading of level
// at most max.
func (d *epubDoc) opensWithHeading(max int) bool {
	for _, h := range d.sc.heads {
		return h.textBefore <= 0 && h.level <= max
	}
	return false
}

// landmarkTypes is, for each file the landmarks and the guide name, the types they give it, in a
// string the section code reads with typeAny. A file keeps at most maxLandTypes distinct types, and the
// work is linear in the number of landmarks, which are capped when they are read.
func landmarkTypes(lands []landmark) map[string]string {
	seen := map[string]map[string]bool{}
	out := map[string]string{}
	for _, l := range lands {
		if l.path == "" || l.frag != "" {
			continue
		}
		t := l.typ
		switch t { // the EPUB 2 guide's names for what EPUB 3 calls ...
		case "title-page":
			t = "titlepage"
		case "text":
			t = "bodymatter"
		case "other.endnotes", "notes":
			t = "endnotes"
		}
		if t == "" || len(t) > 40 {
			continue
		}
		if seen[l.path] == nil {
			seen[l.path] = map[string]bool{}
		}
		if seen[l.path][t] || len(seen[l.path]) >= maxLandTypes {
			continue
		}
		seen[l.path][t] = true
		out[l.path] += " " + t
	}
	return out
}
