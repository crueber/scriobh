package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Scrivener import. A Scrivener project is a folder (a .scriv bundle) with an
// XML binder (the .scrivx) and one folder or file of text per binder item. This
// reads the formats of Scrivener 3 (Files/Data/<UUID>/content.rtf, synopsis.txt,
// notes.rtf) and of Scrivener 2 (Files/Docs/<id>.rtf, <id>_synopsis.txt,
// <id>_notes.rtf) as they are described and as the fixtures of the tests have
// them: the author of this code did not have a real Scrivener project to open
// it with, and no claim is made beyond those shapes.
//
// What becomes what:
//
//	Draft                   the manuscript. A folder that holds folders is a part, a folder of
//	                        documents a chapter, and a document in a chapter a scene; a document
//	                        in the Draft or in a part is a chapter. A folder named like front or
//	                        back matter (Front Matter, Acknowledgements ...) is that kind.
//	title, synopsis         the title; the synopsis is the document's synopsis
//	Include in Compile: No  "do not compile"
//	Status                  a document status (To Do is idea, In Progress and First Draft draft,
//	                        Revised Draft revise, Final Draft and Done done; others are draft and
//	                        reported); Labels and Keywords are reported, not kept
//	notes (notes.rtf)       %%private notes%% after the text (scrivener_notes: skip leaves them out)
//	annotations, comments   %%private notes%% in the text, closed at the end of each block
//	footnotes               Markdown footnotes
//	links to other documents  [[wiki links]], when the title is the only one of its name
//	Research                research notes (scrivener_research: skip leaves them out): folders
//	                        kept, keywords and labels as tags, a synopsis as a first line;
//	                        images, PDFs, web pages and other files are NOT imported: a note
//	                        names each. Notes in folders called Characters or Places are
//	                        marked as candidates for entities (nothing makes an entity)
//	Trash                   left out and counted
//	snapshots, collections, custom metadata, targets  counted or ignored, never imported

// Options of the Scrivener import.
const (
	ScrivResearchNotes = "notes"
	ScrivResearchSkip  = "skip"
	ScrivNotesPrivate  = "private"
	ScrivNotesSkip     = "skip"
)

const (
	maxBinderItems = 50_000
	maxBinderDepth = 200 // levels of folders; items deeper are counted and left out
	maxSourcePath  = 300
)

// convOpts are the options of the converters.
type convOpts struct {
	scrivResearch, scrivNotes string
	wpStatus, wpTypes         string
	ghostStatus, ghostTypes   string
	pictures                  string // import (default) or skip
	scrivSheets               string // notes (default) or entities: what the apply makes of character and place sheets
	epubSections, epubPages   string // the EPUB import's options
	pandocFmt                 string // the format of a document the Pandoc bridge reads
	docxSplit, docxStyle      string // how a Word document is cut (docxbreaks.go)
	docxWords                 int
	docxPattern, docxMatch    string // a pattern to cut a Word document at (docxpattern.go)
	docxTry, docxTryMatch     string // a pattern only tried: what it matches, and nothing is cut by it
	docxBreaks                []int
	// ctx is the caller's: a converter looks at it between items and stops when the
	// caller has gone. Nil is never cancelled.
	ctx context.Context
}

// stop is the error that says a conversion should end (the caller is gone), or nil.
func (o convOpts) stop() error {
	if o.ctx == nil {
		return nil
	}
	return o.ctx.Err()
}

type bitem struct {
	node          *xnode
	id, uuid, typ string
	title         string
	include       bool
	label, status string
	keywords      []string
	kids          []*bitem
}

type scrivConv struct {
	res   *cresult
	opt   convOpts
	idx   map[string]File // by lower-cased path under the project's root
	sk    *sink
	title map[string]string // binder id or UUID -> the title the document ends up with (when unique)

	labels, statuses, keywords map[string]string // id -> name
	labelUse                   *counter
	statusMiss                 *counter
	keywordUse                 *counter

	rtfBytes int64
	st       rtfStats
	items    int
	counts   struct {
		draft, research, trashed, outside, snapshots, unreadable, notFound, dupIDs int
		researchImages, researchPDF, researchWeb, researchOther                    int
		noteDocs, synopses, excluded, docLinks, docLinksDone                       int
	}
	seen map[string]bool
	// dataDirs are the folders under Files/Data (lower case): which items have files.
	dataDirs map[string]bool
	// notRead are the documents whose text file was there and was not read.
	notRead  []string
	deep     int // research folders nested deeper than allowed
	depthCut int // binder items nested deeper than maxBinderDepth, left out
	err      error
	// usedPics are the files read as pictures (not "files this reader does not
	// open"); inlinePics the pictures placed in the text; researchPics the research
	// items that became a picture in a note.
	usedPics     map[string]bool
	inlinePics   int
	researchPics int
}

// scrivenerConvert reads a Scrivener project from the files of its bundle.
func scrivenerConvert(files []File, name string, o convOpts) (res *cresult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, errors.New("the Scrivener project could not be read")
		}
	}()
	c := &scrivConv{res: &cresult{layout: LayoutScrivener, meta: map[string]any{}}, opt: o, sk: newSink(),
		title: map[string]string{}, labels: map[string]string{}, statuses: map[string]string{}, keywords: map[string]string{},
		labelUse: newCounter(), statusMiss: newCounter(), keywordUse: newCounter(), seen: map[string]bool{}, usedPics: map[string]bool{}}
	c.res.pics = newPicBox(o)
	if c.opt.scrivResearch == "" {
		c.opt.scrivResearch = ScrivResearchNotes
	}
	if c.opt.scrivNotes == "" {
		c.opt.scrivNotes = ScrivNotesPrivate
	}
	scrivx, other, err := c.index(files)
	if err != nil {
		return nil, err
	}
	doc, notes, err := parseBinderXML(scrivx)
	if err != nil {
		return nil, err
	}
	c.res.warnings = append(c.res.warnings, notes...)
	c.readSettings(doc)
	binder := doc.child("Binder")
	if binder == nil {
		return nil, errors.New("that is not a Scrivener project: the binder has no items")
	}
	var top []*bitem
	binder.each("BinderItem", func(n *xnode) { top = append(top, c.item(n, 0)) })
	if c.items > maxBinderItems {
		return nil, fmt.Errorf("the binder holds more than %d items", maxBinderItems)
	}
	// What a document will be called, before any body is read: a link to it needs the name.
	var drafts []*bitem
	for _, t := range top {
		if t.typ == "DraftFolder" {
			drafts = append(drafts, t)
		}
	}
	c.collectTitles(drafts)

	pt := cleanText(doc.child("ProjectProperties").childText("ProjectTitle"), 200)
	if pt == "" {
		pt = cleanText(doc.attr("Title"), 200)
	}
	if pt == "" {
		pt = strings.TrimSuffix(strings.TrimSuffix(path.Base(strings.ReplaceAll(name, "\\", "/")), ".zip"), ".scriv")
		pt = cleanText(strings.ReplaceAll(pt, "_", " "), 200)
	}
	author := cleanText(firstText(doc.child("ProjectProperties").childText("FullName"), doc.child("ProjectProperties").childText("Author"), doc.attr("Author")), 200)
	c.res.project = cproject{title: pt, author: author, kind: "blank"}

	for _, t := range top {
		switch t.typ {
		case "DraftFolder":
			c.res.docs = append(c.res.docs, c.draftKids(t, "Draft", "", 0)...)
		case "ResearchFolder":
			if c.opt.scrivResearch != ScrivResearchSkip {
				c.researchKids(t, "", 0, "")
			} else {
				c.counts.research += countItems(t) - 1
			}
		case "TrashFolder":
			c.counts.trashed += countItems(t) - 1
		default:
			c.counts.outside += countItems(t)
		}
		if c.err != nil {
			return nil, c.err
		}
	}
	c.report(other)
	h := sha256.New()
	for _, k := range sortedKeys(c.idx) {
		f := c.idx[k]
		if strings.HasSuffix(k, ".scrivx") {
			h.Write(f.bytesOf())
		}
	}
	c.res.srcHash = hex.EncodeToString(h.Sum(nil))
	return c.res, nil
}

func countItems(b *bitem) int {
	n := 1
	for _, k := range b.kids {
		n += countItems(k)
	}
	return n
}

// index finds the project in the files: its .scrivx, and every file by the path
// under the project's folder.
func (c *scrivConv) index(files []File) (scrivx []byte, other int, err error) {
	c.idx = map[string]File{}
	var paths []string
	for _, f := range files {
		p, why := cleanPath(f.Path)
		if why != "" || hidden(p) {
			other++
			continue
		}
		if f.Size < 0 {
			other++
			continue
		}
		paths = append(paths, p)
	}
	// The project's folder: the one that holds the .scrivx.
	best := ""
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".scrivx") && (best == "" || strings.Count(p, "/") < strings.Count(best, "/")) {
			best = p
		}
	}
	if best == "" {
		return nil, 0, errors.New("that is not a Scrivener project: there is no .scrivx file in it")
	}
	root := ""
	if i := strings.LastIndexByte(best, '/'); i >= 0 {
		root = best[:i+1]
	}
	for _, f := range files {
		p, why := cleanPath(f.Path)
		if why != "" || hidden(p) || f.Size < 0 || !strings.HasPrefix(p, root) {
			continue
		}
		rel := strings.ToLower(strings.TrimPrefix(p, root))
		if _, dup := c.idx[rel]; dup {
			other++ // a name used twice (paths are compared without regard to case): the first is read
			continue
		}
		c.idx[rel] = File{Path: rel, Data: f.Data, Size: f.Size, Content: f.Content}
		if rest, ok := strings.CutPrefix(rel, "files/data/"); ok {
			if dir, _, has := strings.Cut(rest, "/"); has {
				if c.dataDirs == nil {
					c.dataDirs = map[string]bool{}
				}
				c.dataDirs[dir] = true
			}
		}
	}
	main := c.idx[strings.ToLower(strings.TrimPrefix(best, root))]
	b := main.bytesOf()
	if len(b) == 0 {
		return nil, 0, errors.New("the .scrivx file was not sent, or is larger than the import limit")
	}
	if n := countScrivx(paths); n > 1 {
		c.res.warn("The folder holds %d .scrivx files; %s was read.", n, shown(path.Base(best)))
	}
	return b, other, nil
}

func countScrivx(paths []string) int {
	n := 0
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".scrivx") {
			n++
		}
	}
	return n
}

func parseBinderXML(b []byte) (*xnode, []string, error) {
	b, notes := decodeXML(b)
	doc, err := parseXML(b, xmlLimits{tokens: 8_000_000, text: 32 << 20, depth: 420})
	if err != nil {
		return nil, nil, fmt.Errorf("the binder (.scrivx) cannot be read: %v", err)
	}
	if doc.name != "ScrivenerProject" {
		return nil, nil, errors.New("that is not a Scrivener project: the .scrivx file is not a Scrivener binder")
	}
	return doc, notes, nil
}

// readSettings reads the lists of labels, statuses and keywords.
func (c *scrivConv) readSettings(doc *xnode) {
	doc.child("LabelSettings").child("Labels").each("Label", func(n *xnode) { c.labels[n.attr("ID")] = cleanText(n.text, 60) })
	doc.child("StatusSettings").child("StatusItems").each("Status", func(n *xnode) { c.statuses[n.attr("ID")] = cleanText(n.text, 60) })
	doc.child("Keywords").each("Keyword", func(n *xnode) {
		t := n.childText("Title")
		if t == "" {
			t = n.text
		}
		c.keywords[n.attr("ID")] = cleanText(t, 60)
	})
}

// item reads a binder item and its children.
func (c *scrivConv) item(n *xnode, depth int) *bitem {
	b := &bitem{node: n, id: n.attr("ID"), uuid: n.attr("UUID"), typ: n.attr("Type"), include: true}
	if c.items++; c.items > maxBinderItems {
		return b
	}
	b.title = cleanText(n.childText("Title"), 300)
	md := n.child("MetaData")
	if v := strings.ToLower(md.childText("IncludeInCompile")); v == "no" || v == "false" || v == "0" {
		b.include = false
	}
	if id := md.childText("LabelID"); id != "" && id != "-1" && id != "0" {
		b.label = c.labels[id]
	}
	if id := md.childText("StatusID"); id != "" && id != "-1" && id != "0" {
		b.status = c.statuses[id]
	}
	kw := n.child("Keywords")
	if kw == nil {
		kw = md.child("Keywords")
	}
	if kw != nil {
		kw.each("KeywordID", func(k *xnode) {
			if t := c.keywords[strings.TrimSpace(k.text)]; t != "" {
				b.keywords = append(b.keywords, t)
			}
		})
		kw.each("Keyword", func(k *xnode) {
			if t := cleanText(k.text, 60); t != "" {
				b.keywords = append(b.keywords, t)
			}
		})
		if len(kw.kids) == 0 {
			for _, t := range strings.FieldsFunc(kw.text, func(r rune) bool { return r == ',' || r == ';' }) {
				if t = cleanText(t, 60); t != "" {
					b.keywords = append(b.keywords, t)
				}
			}
		}
	}
	for _, key := range []string{b.uuid, b.id} {
		if key == "" {
			continue
		}
		if c.seen[key] {
			c.counts.dupIDs++
		}
		c.seen[key] = true
	}
	n.child("Children").each("BinderItem", func(k *xnode) {
		switch {
		case depth >= maxBinderDepth: // counted, not read
			c.depthCut++
		case c.items <= maxBinderItems:
			b.kids = append(b.kids, c.item(k, depth+1))
		}
	})
	return b
}

// key is the id a document's files are under.
func (b *bitem) key() string {
	if b.uuid != "" {
		return b.uuid
	}
	return b.id
}

// ---- the manuscript -----------------------------------------------------------------------------------

// kindOf says what a document of the Draft becomes.
func kindOf(b *bitem, parentKind string, depth int) string {
	if depth <= 1 {
		if k := classify(b.title); k != "" {
			return k
		}
	}
	if len(b.kids) > 0 { // a folder that holds folders is a part, one of documents a chapter
		for _, k := range b.kids {
			if len(k.kids) > 0 {
				return "part"
			}
		}
		return "chapter"
	}
	switch parentKind {
	case "chapter", "front_matter", "back_matter": // a document in a chapter is a scene, and so is one in front or back matter
		return "scene"
	}
	return "chapter"
}

// collectTitles notes the title each draft document will have, and which
// titles are used more than once (a link to those stays text).
func (c *scrivConv) collectTitles(items []*bitem) {
	count := map[string]int{}
	var walk func(its []*bitem)
	var all []*bitem
	walk = func(its []*bitem) {
		for _, b := range its {
			for _, k := range b.kids {
				all = append(all, k)
			}
			walk(b.kids)
		}
	}
	walk(items)
	for _, b := range all {
		t := b.title
		if t == "" {
			t = "Untitled"
		}
		count[strings.ToLower(t)]++
	}
	for _, b := range all {
		t := b.title
		if t == "" {
			t = "Untitled"
		}
		if count[strings.ToLower(t)] == 1 {
			for _, key := range []string{b.uuid, b.id} {
				if key != "" {
					c.title[key] = t
				}
			}
		}
	}
}

func (c *scrivConv) wikiOf(id, label string) string {
	c.counts.docLinks++
	t, ok := c.title[id]
	if !ok {
		return ""
	}
	w := wikiFor(t, label)
	if w != "" {
		c.counts.docLinksDone++
	}
	return w
}

// draftKids converts the children of a binder item.
func (c *scrivConv) draftKids(parent *bitem, srcPath, parentKind string, depth int) []*cdoc {
	var out []*cdoc
	for _, b := range parent.kids {
		if c.err == nil {
			c.err = c.opt.stop()
		}
		if c.err != nil {
			return out
		}
		if b.typ == "TrashFolder" || b.typ == "ResearchFolder" {
			continue
		}
		d := &cdoc{title: b.title, exclude: !b.include}
		if d.title == "" {
			d.title = "Untitled"
		}
		d.kind = kindOf(b, parentKind, depth+1)
		d.source = cleanText(srcPath+"/"+d.title, maxSourcePath)
		d.status = c.statusOf(b)
		if !b.include {
			c.counts.excluded++
		}
		if b.label != "" {
			c.labelUse.add(b.label)
		}
		for _, k := range b.keywords {
			c.keywordUse.add(k)
		}
		syn := c.text(b, "synopsis")
		if syn != "" {
			c.counts.synopses++
		}
		d.synopsis, d.synopsisLong = synopsisOf(syn)
		d.body = c.body(b, true, true)
		d.kids = c.draftKids(b, d.source, d.kind, depth+1)
		c.counts.draft++
		out = append(out, d)
	}
	return out
}

// statusOf maps a Scrivener status name to a document status.
func (c *scrivConv) statusOf(b *bitem) string {
	if b.status == "" {
		return "draft"
	}
	if s := mapStatus(b.status); s != "" {
		return s
	}
	c.statusMiss.add(b.status)
	return "draft"
}

func mapStatus(name string) string {
	switch strings.ToLower(strings.Join(strings.Fields(name), " ")) {
	case "to do", "todo", "idea", "ideas", "not started", "planned":
		return "idea"
	case "outline", "outlined", "plotted", "notes":
		return "outline"
	case "in progress", "first draft", "draft", "started", "writing", "1st draft", "rough draft", "wip":
		return "draft"
	case "revised draft", "second draft", "2nd draft", "revise", "revising", "revision", "editing", "edited", "needs work", "to revise", "polish":
		return "revise"
	case "final draft", "done", "final", "complete", "completed", "finished", "published", "third draft":
		return "done"
	}
	return ""
}

// ---- text ---------------------------------------------------------------------------------------------------

// file returns the bytes of a file of the project, by path under its folder.
func (c *scrivConv) file(rel string) ([]byte, bool) {
	f, ok := c.idx[strings.ToLower(rel)]
	if !ok {
		return nil, false
	}
	b := f.bytesOf()
	if len(b) == 0 && f.Size > 0 {
		return nil, false
	}
	return b, true
}

// text reads a plain text file of an item ("synopsis").
func (c *scrivConv) text(b *bitem, what string) string {
	var paths []string
	if b.uuid != "" {
		paths = append(paths, "files/data/"+b.uuid+"/"+what+".txt")
	}
	if b.id != "" {
		paths = append(paths, "files/docs/"+b.id+"_"+what+".txt")
	}
	for _, p := range paths {
		if data, ok := c.file(p); ok {
			s, _ := decodeText(data)
			return s
		}
	}
	return ""
}

// decodeText reads a small text file as UTF-8, UTF-16 with a mark, or Windows-1252.
func decodeText(b []byte) (string, bool) {
	out, _ := decodeXML(append([]byte("<?xml version=\"1.0\"?>"), b...))
	s := string(out[len("<?xml version=\"1.0\"?>"):])
	return strings.ToValidUTF8(s, "\uFFFD"), true
}

// rtfOf finds an item's RTF (content or notes).
func (c *scrivConv) rtfOf(b *bitem, what string) ([]byte, bool) {
	var paths []string
	if b.uuid != "" {
		paths = append(paths, "files/data/"+b.uuid+"/"+what+".rtf")
	}
	if b.id != "" {
		if what == "content" {
			paths = append(paths, "files/docs/"+b.id+".rtf")
		} else {
			paths = append(paths, "files/docs/"+b.id+"_"+what+".rtf")
		}
	}
	for _, p := range paths {
		if data, ok := c.file(p); ok {
			return data, true
		}
	}
	return nil, false
}

// body converts a document's text, and, for a draft document, its notes.
func (c *scrivConv) body(b *bitem, withNotes, links bool) string {
	if data, ok := c.rtfOf(b, "content"); ok {
		c.addRTF(b, data, false, links)
	} else if c.present(b, "content") {
		c.notRead = append(c.notRead, b.title)
	} else if c.hasData(b) {
		// the item has files, but not text (a picture or PDF): nothing to say here
	} else if b.typ == "Text" {
		c.counts.notFound++
	}
	if withNotes && c.opt.scrivNotes != ScrivNotesSkip {
		if data, ok := c.rtfOf(b, "notes"); ok {
			c.counts.noteDocs++
			c.addRTF(b, data, true, false)
		}
	}
	if c.sk.size() > 2*MaxTotalBytes {
		c.err = fmt.Errorf("the project's text is larger than the %d MB import limit", MaxTotalBytes>>20)
		c.sk.take()
		return ""
	}
	return c.sk.take()
}

func (c *scrivConv) hasData(b *bitem) bool {
	return b.uuid != "" && c.dataDirs[strings.ToLower(b.uuid)]
}

// present says an item's text file is in the project but could not be read (it
// is larger than the limit, or its bytes were not sent).
func (c *scrivConv) present(b *bitem, what string) bool {
	var paths []string
	if b.uuid != "" {
		paths = append(paths, "files/data/"+b.uuid+"/"+what+".rtf")
	}
	if b.id != "" {
		if what == "content" {
			paths = append(paths, "files/docs/"+b.id+".rtf")
		} else {
			paths = append(paths, "files/docs/"+b.id+"_"+what+".rtf")
		}
	}
	for _, p := range paths {
		if f, ok := c.idx[strings.ToLower(p)]; ok && len(f.bytesOf()) == 0 && f.Size > 0 {
			return true
		}
	}
	return false
}

func (c *scrivConv) addRTF(b *bitem, data []byte, asNote, links bool) {
	if c.rtfBytes += int64(len(data)); c.rtfBytes > maxScrivTotal {
		c.err = errors.New("the project's text files are larger than the import limit")
		return
	}
	d, err := parseRTF(data)
	if err != nil {
		c.counts.unreadable++
		if errors.Is(err, errRTFTooMuchText) {
			c.res.warn("%s: the text is too long to import and was left empty.", shown(b.title))
		}
		return
	}
	c.addStats(d.st)
	if asNote {
		d.asPrivate("Notes: ")
	}
	if links {
		d.emit(c.sk, rtfEmit{wiki: c.wikiOf, pic: c.textPicture(b)})
	} else {
		d.emit(c.sk, rtfEmit{pic: c.textPicture(b)}) // research notes have no wiki links
	}
}

func (c *scrivConv) addStats(s rtfStats) {
	t := &c.st
	t.images += s.images
	t.binBytes += s.binBytes
	t.underline += s.underline
	t.hidden += s.hidden
	t.unknown += s.unknown
	t.deepGroups += s.deepGroups
	t.annotations += s.annotations
	t.comments += s.comments
	t.footnotes += s.footnotes
	t.tables += s.tables
	t.links += s.links
	t.badLinks += s.badLinks
	t.docLinks += s.docLinks
	t.cmtLinks += s.cmtLinks
	t.badCP += s.badCP
	t.flattened += s.flattened
}

// asPrivate makes every paragraph of a document one private note: the writer's
// notes about a document are for the writer.
func (d *rtfDoc) asPrivate(prefix string) {
	for i := range d.paras {
		p := &d.paras[i]
		p.heading, p.quote, p.list = 0, false, false
		var segs []rseg
		for j, s := range p.segs {
			if s.image || s.note != 0 {
				continue
			}
			s.priv, s.link = true, ""
			if i == 0 && j == 0 {
				s.text = prefix + s.text
			}
			segs = append(segs, s)
		}
		p.segs = segs
	}
	d.notes = nil
}

// ---- research -----------------------------------------------------------------------------------------------

const maxResearchDepth = 10

// researchKids converts the children of a binder item to research notes.
func (c *scrivConv) researchKids(parent *bitem, parentKey string, depth int, candidate string) {
	for _, b := range parent.kids {
		if c.err == nil {
			c.err = c.opt.stop()
		}
		if c.err != nil {
			return
		}
		c.counts.research++
		if len(c.res.research) >= MaxResearch {
			continue
		}
		key := "research/" + strconv.Itoa(len(c.res.research)+1)
		title := b.title
		if title == "" {
			title = "Untitled"
		}
		var tags []string
		if b.label != "" {
			tags = append(tags, b.label)
		}
		tags = append(tags, b.keywords...)
		if b.typ == "Folder" || b.typ == "ResearchFolder" || len(b.kids) > 0 {
			c.res.research = append(c.res.research, &rnote{key: key, parentKey: parentKey, kind: "folder", title: title, tags: tags})
			cand := candidate
			if cand == "" {
				cand = candidateOf(title)
			}
			// A folder deeper than a research folder may be is not made; what is in it goes in its parent.
			inner := key
			if depth+1 >= maxResearchDepth {
				inner = parentKey
				c.deep++
			}
			if c.hasText(b) { // a folder with text of its own: the text is the first note in it
				c.researchNote(b, inner, key+"/text", title, tags, cand)
			}
			c.researchKids(b, inner, depth+1, cand)
			continue
		}
		c.researchNote(b, parentKey, key, title, tags, candidate)
	}
}

// researchNote converts one research item to a note.
func (c *scrivConv) researchNote(b *bitem, parentKey, key, title string, tags []string, cand string) {
	n := &rnote{key: key, parentKey: parentKey, kind: "note", title: title, tags: tags}
	switch {
	case b.typ == "Text" || b.typ == "Folder" || b.typ == "":
		if syn := c.text(b, "synopsis"); syn != "" {
			c.sk.para([]seg{{text: "Synopsis: ", i: true}, {text: strings.Join(strings.Fields(syn), " ")}})
		}
		n.body = c.body(b, true, false)
		n.candidate = cand
	default:
		if p := c.itemPicture(b, title); p != nil {
			c.researchPics++
			c.sk.para([]seg{p.seg(title)})
		} else {
			c.countResearchFile(b)
			c.sk.para([]seg{{text: "[attachment: " + title + " (" + b.typ + ") was not imported]", i: true}})
		}
		n.body = c.sk.take()
	}
	c.res.research = append(c.res.research, n)
}

func (c *scrivConv) hasText(b *bitem) bool {
	_, ok := c.rtfOf(b, "content")
	return ok
}

// itemPicture imports the picture a research item is (Files/Data/<UUID>/content.png,
// or Files/Docs/<id>.jpg in a Scrivener 2 project), or returns nil.
func (c *scrivConv) itemPicture(b *bitem, title string) *cpicture {
	var dirs []string
	if b.uuid != "" {
		dirs = append(dirs, "files/data/"+strings.ToLower(b.uuid)+"/content.")
	}
	if b.id != "" {
		dirs = append(dirs, "files/docs/"+strings.ToLower(b.id)+".")
	}
	for _, d := range dirs {
		for _, e := range []string{"png", "jpg", "jpeg", "gif", "webp", "tif", "tiff", "heic", "bmp", "svg"} {
			f, ok := c.idx[d+e]
			if !ok {
				continue
			}
			c.usedPics[d+e] = true
			p, _ := c.res.pics.add(d+e, f.bytesOf(), title+"."+e)
			return p
		}
	}
	return nil
}

// textPicture is the picture a NeXTGraphic in an item's text names: a file in the
// item's own folder. A name with a path in it is not looked up.
func (c *scrivConv) textPicture(b *bitem) func(name string) (seg, bool) {
	if b.uuid == "" {
		return nil
	}
	return func(name string) (seg, bool) {
		if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			return seg{}, false
		}
		key := "files/data/" + strings.ToLower(b.uuid) + "/" + strings.ToLower(name)
		f, ok := c.idx[key]
		if !ok {
			c.res.pics.missing++
			return seg{}, false
		}
		c.usedPics[key] = true
		p, _ := c.res.pics.add(key, f.bytesOf(), name)
		if p == nil {
			return seg{}, false
		}
		c.inlinePics++
		return p.seg(strings.TrimSuffix(name, path.Ext(name))), true
	}
}

// candidateOf says a research folder is a folder of character or place sheets.
func candidateOf(title string) string {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "characters", "character sheets", "cast", "people", "character":
		return "character"
	case "places", "locations", "settings", "setting", "place", "location":
		return "place"
	}
	return ""
}

func (c *scrivConv) countResearchFile(b *bitem) {
	switch strings.ToLower(b.typ) {
	case "image", "images":
		c.counts.researchImages++
	case "pdf":
		c.counts.researchPDF++
	case "webpage", "web page", "webarchive":
		c.counts.researchWeb++
	default:
		c.counts.researchOther++
	}
}

// ---- the report ---------------------------------------------------------------------------------------------

func topN(m map[string]int, n int) map[string]int {
	if len(m) <= n {
		return m
	}
	type kv struct {
		k string
		v int
	}
	var s []kv
	for k, v := range m {
		s = append(s, kv{k, v})
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].v != s[j].v {
			return s[i].v > s[j].v
		}
		return s[i].k < s[j].k
	})
	out := map[string]int{}
	for _, e := range s[:n] {
		out[e.k] = e.v
	}
	return out
}

func (c *scrivConv) report(other int) {
	r, k, st := c.res, c.counts, c.st
	rep := map[string]any{
		"items": c.items, "documents": k.draft, "research_items": k.research, "trashed": k.trashed, "outside_draft_research": k.outside,
		"synopses": k.synopses, "notes_documents": k.noteDocs, "excluded_from_compile": k.excluded,
		"labels": c.labelUse.m, "keywords": c.keywordUse.m, "statuses_not_mapped": c.statusMiss.m,
		"annotations": st.annotations, "comments": st.comments, "footnotes": st.footnotes, "images": st.images, "underline": st.underline,
		"table_rows": st.tables, "links": st.links, "document_links": k.docLinks, "document_links_made": k.docLinksDone,
		"unreadable": k.unreadable, "files_not_read": other, "binary_bytes_skipped": st.binBytes,
		"research_images": k.researchImages, "research_pdfs": k.researchPDF, "research_web_pages": k.researchWeb, "research_other": k.researchOther,
		"pictures_in_text": c.inlinePics, "research_pictures": c.researchPics,
	}
	r.meta["scrivener"] = rep
	r.warnN(c.depthCut, "%d binder item(s) were nested more than %d levels deep and were left out.", c.depthCut, maxBinderDepth)
	r.warnN(c.deep, "%d research folder(s) were nested deeper than %d levels; their contents were placed in the folder above.", c.deep, maxResearchDepth)
	r.warnN(other, "%d file(s) were not read: an unsafe path, a link, or a name used twice.", other)
	r.warnN(k.trashed, "%d item(s) in the Trash were left out.", k.trashed)
	r.warnN(k.outside, "%d binder item(s) outside Draft, Research and Trash were left out.", k.outside)
	r.warnN(st.annotations+st.comments, "%d annotation(s) and comment(s) found inside the text files became private notes (%%%%…%%%%): they are never exported, shared or sent to AI.", st.annotations+st.comments)
	r.warnN(st.underline, "%d underlined passage(s) became plain text: Markdown has no underline. In a manuscript underline often means italics; check them.", st.underline)
	r.warnN(st.images-c.inlinePics, "%d image(s) in the text were not imported; each is marked [image].", st.images-c.inlinePics)
	r.warnN(st.tables, "%d table row(s) were flattened into paragraphs.", st.tables)
	r.warnN(st.hidden, "%d run(s) of hidden text were left out.", st.hidden)
	r.warnN(st.unknown, "%d part(s) of the text files in a form this reader does not know were left out.", st.unknown)
	r.warnN(st.deepGroups, "%d group(s) nested deeper than %d were left out.", st.deepGroups, rtfMaxDepth)
	r.warnN(st.badCP, "%d character(s) in a code page this reader does not have were replaced.", st.badCP)
	r.warnN(st.cmtLinks, "%d link(s) to a Scrivener comment kept their text only.", st.cmtLinks)
	r.warnN(st.badLinks, "%d link(s) to something other than a web or e-mail address kept their text only.", st.badLinks)
	r.warnN(k.docLinks-k.docLinksDone, "%d link(s) to other documents could not be made links (the document is not in the manuscript, or shares its title) and kept their text.", k.docLinks-k.docLinksDone)
	r.warnN(k.unreadable, "%d text file(s) could not be read as RTF and were imported empty.", k.unreadable)
	r.warnN(k.notFound, "%d document(s) have no text file in the project.", k.notFound)
	r.warnN(k.dupIDs, "%d binder item(s) share an id with another; their text is the same.", k.dupIDs)
	r.warnN(k.researchImages+k.researchPDF+k.researchWeb+k.researchOther, "Research holds %d image(s), %d PDF(s), %d web page(s) and %d other file(s) that were not imported; a note names each.",
		k.researchImages, k.researchPDF, k.researchWeb, k.researchOther)
	if !c.statusMiss.empty() {
		r.warn("Status not known to Scríobh, imported as draft: %s.", topList(c.statusMiss.m))
	}
	if !c.labelUse.empty() {
		r.warn("Labels are not kept (a document has none): %s label(s) were in use. They are listed in the import's record.", c.labelUse.size())
	}
	if !c.keywordUse.empty() {
		r.warn("Keywords of manuscript documents are not kept: %s keyword(s) were in use. Keywords on research notes became tags.", c.keywordUse.size())
	}
	if len(c.notRead) > 0 {
		names := make([]string, 0, 5)
		for _, n := range c.notRead[:min(5, len(c.notRead))] {
			names = append(names, shown(n))
		}
		r.warn("%d document(s) have a text file that was not read (larger than the %d MB limit, or not sent), so they are empty: %s.", len(c.notRead), maxScrivFile>>20, strings.Join(names, ", "))
	}
	exts := newCounter()
	snaps, commentFiles := 0, 0
	for k := range c.idx {
		switch e := path.Ext(k); {
		case strings.Contains(k, "/snapshots/"):
			snaps++
		case e == ".rtf" || e == ".txt" || e == ".scrivx" || c.usedPics[k]:
		default:
			if e == "" {
				e = "(no extension)"
			}
			exts.add(e)
			if e == ".comments" {
				commentFiles++
			}
		}
	}
	rep["files_not_read_by_type"] = exts.m
	rep["snapshot_files"] = snaps
	r.warnN(snaps, "%d snapshot file(s) were not imported.", snaps)
	if !exts.empty() {
		r.warn("Files of the project this reader does not open: %s.", topList(exts.m))
		if commentFiles > 0 {
			r.warn("%d file(s) of comments kept apart from the text (.comments) were not read: those comments are not imported.", commentFiles)
		}
	}
	r.warn("Collections, custom metadata, targets and the project's settings are not imported.")
	cands := 0
	for _, n := range r.research {
		if n.candidate != "" {
			cands++
		}
	}
	if c.opt.scrivSheets == SheetsEntities {
		r.warnN(cands, "%d research note(s) look like character or place sheets. They will be made entities of the project's primary Compendium, where their names are free; the rest stay research notes.", cands)
	} else {
		r.warnN(cands, "%d research note(s) look like character or place sheets. They will be imported as research notes; choose to make them entities, or make entities from them in the Compendium later.", cands)
	}
}
