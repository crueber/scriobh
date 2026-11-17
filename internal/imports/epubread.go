package imports

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode"
)

// EPUB reading: the zip, the container, the package document (metadata, manifest,
// spine), the navigation (an EPUB 3 nav document, an EPUB 2 NCX, or the OPF
// guide) and the checks that refuse a book (DRM, a fixed layout). Nothing here
// converts text; epubconv.go does.
//
// Every limit of the folder importer's zip reading applies (the directory is
// walked before the archive is opened, an entry's declared size is never
// trusted, an entry is read through a limit and counted against a budget of
// work), and every XML file goes through the strict, entity-free reader of
// xdom.go. A DOCTYPE that only names a public identifier (every EPUB 2 NCX and
// XHTML 1.1 file has one) is tolerated by being removed; one with an internal
// subset, or any entity definition, is refused. A reference in the package
// document is resolved against the package's own folder and never leaves the
// zip.

const (
	maxEpubEntries = maxExportZipEntries // entries in the zip
	maxEpubXML     = 8 << 20             // one XHTML, package, navigation or XML file
	maxEpubCSS     = 1 << 20             // one stylesheet
	maxEpubText    = 96 << 20            // all XML and CSS, inflated
	maxSpine       = 20_000              // spine items read
	maxManifest    = 40_000              // manifest items read
	maxTocEntries  = 50_000              // navigation entries read
	maxTocDepth    = 32
	maxMetaItems   = 200
	maxLandmarks   = 2000 // landmarks and guide references kept
	maxLandTypes   = 16   // distinct types kept for one file
	maxPeople      = 50   // authors, and contributors, kept
	maxMetaText    = 4000
)

// Errors that refuse a book, in words a writer can act on.
var (
	errEpubNotZip   = errors.New("that is not an EPUB file (it is not a zip)")
	errEpubNoPkg    = errors.New("that is not a readable EPUB: it has no META-INF/container.xml naming a package document")
	errEpubDRM      = errors.New("this book is protected by DRM (its text is encrypted): Scríobh does not remove copy protection, so it cannot be imported. A copy without DRM can")
	errEpubFixed    = errors.New("this is a fixed-layout EPUB (its pages are laid out as pictures of pages, as comics and picture books are): it has no text flow to turn into chapters, so it cannot be imported")
	errEpubNoSpine  = errors.New("the book's reading order (its spine) is empty or names nothing that is in the file")
	errEpubTooMuch  = errors.New("the book is larger than the import limit")
	errEpubBadOPF   = errors.New("the package document is not valid")
	errEpubDoctype  = errors.New("the book has a DOCTYPE with an internal subset or an entity definition, which is not allowed")
	errEpubNotEpub3 = "the file's mimetype entry is not application/epub+zip; it was read as an EPUB anyway."
)

// epubFile is an entry of the zip.
type epubFile struct {
	name string // as stored, cleaned of backslashes
	data []byte // nil when the entry was not read (too big, not wanted, or the archive lied)
	size int64
}

// epubItem is a manifest item.
type epubItem struct {
	id, href, mediaType, props string
	path                       string // the zip path, "" when the reference leaves the book
}

func (it *epubItem) hasProp(p string) bool {
	for _, f := range strings.Fields(it.props) {
		if f == p {
			return true
		}
	}
	return false
}

// epubSpine is an itemref.
type epubSpine struct {
	item   *epubItem
	linear bool
	props  string
}

// tocEntry is a navigation entry.
type tocEntry struct {
	label    string
	href     string // as written
	path     string // the zip path of its file, "" when it names nothing in the book
	frag     string
	types    string // epub:type, for a nav entry
	children []*tocEntry
}

// landmark is a landmarks entry (EPUB 3) or a guide reference (EPUB 2): what a
// part of the book is.
type landmark struct {
	typ, title, path, frag string
}

// epubMeta is what the package document says about the book.
type epubMeta struct {
	title, subtitle                       string
	authors, contributors                 []string
	language, identifier, idScheme        string
	publisher, date, description, rights  string
	subjects                              []string
	series, seriesIndex                   string
	modified, generator, version, dirAttr string
	rtl                                   bool
	fixedLayout                           bool
}

// epubBook is a book as read from its container.
type epubBook struct {
	files    map[string]*epubFile
	lower    map[string]string
	order    []string
	opfPath  string
	opfDir   string
	meta     epubMeta
	items    map[string]*epubItem
	byPath   map[string]*epubItem
	spine    []epubSpine
	toc      []*tocEntry
	tocFrom  string // "nav", "ncx" or ""
	tocPages map[string]bool
	lands    []landmark
	coverID  string
	warnings []string
	// counts of what was read and not kept
	skippedEntries int
	unsafeEntries  int
	fonts          int
	encryptedFonts int
	fixedItems     int
	css            []string
	stop           func() error // the caller has gone when it returns an error
	sha            string
	size           int64
}

// addLand keeps a landmark, up to maxLandmarks: a navigation document can list as many as it has bytes.
func (b *epubBook) addLand(l landmark) {
	if len(b.lands) < maxLandmarks {
		b.lands = append(b.lands, l)
	}
}

func (b *epubBook) warn(format string, args ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, args...))
}

// epubWant is what an EPUB is read for, and up to what size.
func epubWant(lower string) (int64, bool) {
	switch {
	case strings.HasSuffix(lower, ".xhtml"), strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"), strings.HasSuffix(lower, ".xml"),
		strings.HasSuffix(lower, ".opf"), strings.HasSuffix(lower, ".ncx"), strings.HasSuffix(lower, ".dtbook"):
		return maxEpubXML, true
	case strings.HasSuffix(lower, ".css"):
		return maxEpubCSS, true
	case lower == "mimetype":
		return 256, true
	}
	return pictureWant(lower)
}

// cleanZipName makes a zip entry name into a path inside the book, or "" when
// it is not one (absolute, climbing, empty, or holding a NUL).
func cleanZipName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || len(name) > MaxPathBytes {
		return ""
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return ""
		}
	}
	c := path.Clean(name)
	if c == "." || strings.HasPrefix(c, "../") {
		return ""
	}
	return c
}

// readEpub reads the container of a book: the files, the package document, the
// navigation. It refuses DRM and a fixed layout. stop returns an error when the
// caller has gone.
func readEpub(data []byte, stop func() error) (b *epubBook, err error) {
	defer func() {
		if r := recover(); r != nil {
			b, err = nil, errors.New("the EPUB could not be read")
		}
	}()
	if len(data) == 0 {
		return nil, errEpubNotZip
	}
	list, err := readZipFor(data, maxEpubEntries, maxEpubText, epubWant)
	if err != nil {
		if strings.Contains(err.Error(), "not a readable zip") || strings.Contains(err.Error(), "zip64") {
			return nil, errEpubNotZip
		}
		return nil, err
	}
	b = &epubBook{files: map[string]*epubFile{}, lower: map[string]string{}, items: map[string]*epubItem{}, byPath: map[string]*epubItem{}, tocPages: map[string]bool{}}
	b.size = int64(len(data))
	b.stop = stop
	for _, f := range list {
		switch path.Ext(strings.ToLower(f.Path)) {
		case ".ttf", ".otf", ".woff", ".woff2", ".eot":
			b.fonts++
		}
		if f.Size < 0 { // a link stored in the archive
			b.skippedEntries++
			continue
		}
		name := cleanZipName(f.Path)
		if name == "" {
			b.unsafeEntries++
			continue
		}
		if _, dup := b.files[name]; dup {
			b.skippedEntries++
			continue
		}
		ef := &epubFile{name: name, data: f.Data, size: f.Size}
		b.files[name] = ef
		if _, ok := b.lower[strings.ToLower(name)]; !ok {
			b.lower[strings.ToLower(name)] = name
		}
		b.order = append(b.order, name)
	}
	if stop != nil {
		if err := stop(); err != nil {
			return nil, err
		}
	}
	if b.unsafeEntries > 0 {
		b.warn("%d entr%s in the zip had a path that is absolute or climbs out of the book and %s ignored.", b.unsafeEntries, plural(b.unsafeEntries, "y", "ies"), plural(b.unsafeEntries, "was", "were"))
	}
	if mt := b.get("mimetype"); mt != nil && strings.TrimSpace(string(mt.data)) != "application/epub+zip" {
		b.warn("%s", errEpubNotEpub3)
	}
	if err := b.checkEncryption(); err != nil {
		return nil, err
	}
	if err := b.readContainer(); err != nil {
		return nil, err
	}
	if err := b.readPackage(); err != nil {
		return nil, err
	}
	b.readNavigation()
	if stop != nil {
		if err := stop(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// get finds a file by its path, exactly or, failing that, ignoring case.
func (b *epubBook) get(p string) *epubFile {
	if f, ok := b.files[p]; ok {
		return f
	}
	if n, ok := b.lower[strings.ToLower(p)]; ok {
		return b.files[n]
	}
	return nil
}

// text returns the decoded text of an XML or HTML file, or nil.
func (b *epubBook) text(p string) []byte {
	f := b.get(p)
	if f == nil || f.data == nil {
		return nil
	}
	out, _ := decodeXML(append([]byte(nil), f.data...))
	return out
}

// resolve turns a reference written in the file at from into a path inside the
// book and a fragment. ok is false for an address outside the book (a scheme, a
// host, a path that climbs out of the zip).
func resolveRef(from, ref string) (p, frag string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 2000 {
		return "", "", false
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(ref, "//") {
		return "", "", false
	}
	frag = u.Fragment
	rp := u.Path
	if rp == "" {
		return from, frag, true // a fragment of the same file
	}
	if strings.ContainsRune(rp, 0) {
		return "", "", false
	}
	if strings.HasPrefix(rp, "/") {
		rp = path.Clean(rp)[1:]
	} else {
		rp = path.Join(path.Dir(from), rp)
	}
	if rp == "." || rp == "" || rp == ".." || strings.HasPrefix(rp, "../") || strings.HasPrefix(rp, "/") {
		return "", "", false
	}
	return rp, frag, true
}

// ---- encryption ---------------------------------------------------------------------------

// checkEncryption refuses a book whose text is encrypted. META-INF/encryption.xml
// also lists obfuscated fonts, which are not copy protection: a book that
// encrypts nothing but fonts is read as usual.
func (b *epubBook) checkEncryption() error {
	if b.get("META-INF/rights.xml") != nil || b.get("META-INF/sinf.xml") != nil {
		return errEpubDRM
	}
	f := b.get("META-INF/encryption.xml")
	if f == nil {
		return nil
	}
	if f.data == nil {
		return errEpubDRM // too large to look at: not a font list
	}
	root, err := parseEpubXML(b.text("META-INF/encryption.xml"))
	if err != nil {
		return errEpubDRM // unreadable: do not guess that it is harmless
	}
	for _, ed := range root.kidsNamed("EncryptedData") {
		alg := ""
		if m := ed.child("EncryptionMethod"); m != nil {
			alg = m.attr("Algorithm")
		}
		target := ""
		if cd := ed.child("CipherData"); cd != nil {
			if cr := cd.child("CipherReference"); cr != nil {
				target = cr.attr("URI")
			}
		}
		fontAlg := alg == "http://www.idpf.org/2008/embedding" || alg == "http://ns.adobe.com/pdf/enc#RC"
		if fontAlg {
			b.encryptedFonts++
			continue
		}
		_ = target
		return errEpubDRM
	}
	return nil
}

// kidsNamed lists the descendants with the name (two levels are enough for
// encryption.xml: EncryptedData sits directly under encryption).
func (n *xnode) kidsNamed(name string) []*xnode {
	var out []*xnode
	var walk func(x *xnode, depth int)
	walk = func(x *xnode, depth int) {
		for _, k := range x.kids {
			if k.name == name {
				out = append(out, k)
			} else if depth < 4 {
				walk(k, depth+1)
			}
		}
	}
	if n != nil {
		walk(n, 0)
	}
	return out
}

// ---- XML ----------------------------------------------------------------------------------

// parseEpubXML reads an XML file of the package under the importer's limits,
// after removing a DOCTYPE that is only a name and an identifier.
func parseEpubXML(b []byte) (*xnode, error) {
	b, err := stripSafeDoctype(b)
	if err != nil {
		return nil, err
	}
	return parseXML(b, xmlLimits{tokens: 4_000_000, text: 32 << 20})
}

// stripSafeDoctype removes a <!DOCTYPE name PUBLIC "..." "..."> from the start of
// a document, replacing it with spaces so offsets hold. A DOCTYPE with an internal
// subset ([ ... ]), and an entity declaration anywhere in the prolog, is refused.
func stripSafeDoctype(b []byte) ([]byte, error) {
	i := 0
	for i < len(b) {
		for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
			i++
		}
		switch {
		case hasPrefixAt(b, i, "<?"):
			j := indexFrom(b, i, "?>")
			if j < 0 {
				return b, nil
			}
			i = j + 2
		case hasPrefixAt(b, i, "<!--"):
			j := indexFrom(b, i, "-->")
			if j < 0 {
				return b, nil
			}
			i = j + 3
		case hasPrefixAt(b, i, "<!DOCTYPE"), hasPrefixAt(b, i, "<!doctype"):
			end, quote := -1, byte(0)
			for k := i + 9; k < len(b) && k < i+4096; k++ {
				c := b[k]
				switch {
				case quote != 0:
					if c == quote {
						quote = 0
					}
				case c == '"' || c == '\'':
					quote = c
				case c == '[':
					return nil, errEpubDoctype
				case c == '>':
					end = k
				}
				if end >= 0 {
					break
				}
			}
			if end < 0 {
				return nil, errEpubDoctype
			}
			out := append([]byte(nil), b...)
			for k := i; k <= end; k++ {
				out[k] = ' '
			}
			return out, nil
		default:
			return b, nil
		}
	}
	return b, nil
}

func hasPrefixAt(b []byte, i int, p string) bool {
	return i+len(p) <= len(b) && string(b[i:i+len(p)]) == p
}

func indexFrom(b []byte, i int, p string) int {
	if i >= len(b) {
		return -1
	}
	j := strings.Index(string(b[i:]), p)
	if j < 0 {
		return -1
	}
	return i + j
}

// ---- container and package ---------------------------------------------------------------------------

func (b *epubBook) readContainer() error {
	raw := b.text("META-INF/container.xml")
	if raw == nil {
		return errEpubNoPkg
	}
	root, err := parseEpubXML(raw)
	if err != nil {
		if errors.Is(err, errEpubDoctype) || errors.Is(err, errXMLDoctyp) {
			return errEpubDoctype
		}
		return errEpubNoPkg
	}
	var roots []*xnode
	for _, rf := range root.kidsNamed("rootfile") {
		roots = append(roots, rf)
	}
	for _, rf := range roots {
		if mt := rf.attr("media-type"); mt != "" && mt != "application/oebps-package+xml" {
			continue
		}
		p := cleanZipName(rf.attr("full-path"))
		if p == "" || b.get(p) == nil {
			continue
		}
		b.opfPath = b.get(p).name
		b.opfDir = path.Dir(b.opfPath)
		if len(roots) > 1 {
			b.warn("The book has %d renditions; the first was read.", len(roots))
		}
		return nil
	}
	return errEpubNoPkg
}

func (b *epubBook) readPackage() error {
	raw := b.text(b.opfPath)
	if raw == nil {
		return errEpubBadOPF
	}
	root, err := parseEpubXML(raw)
	if err != nil {
		if errors.Is(err, errEpubDoctype) || errors.Is(err, errXMLDoctyp) {
			return errEpubDoctype
		}
		return fmt.Errorf("%w (%v)", errEpubBadOPF, err)
	}
	if root.name != "package" {
		return errEpubBadOPF
	}
	b.meta.version = cleanText(root.attr("version"), 20)
	if b.meta.version == "" {
		b.meta.version = "2.0"
	}
	b.meta.dirAttr = root.attr("dir")
	b.readMetadata(root)
	// manifest
	if man := root.child("manifest"); man != nil {
		n := 0
		for _, it := range man.kids {
			if it.name != "item" {
				continue
			}
			if n++; n > maxManifest {
				b.warn("The manifest lists more than %d items; the rest were ignored.", maxManifest)
				break
			}
			id := strings.TrimSpace(it.attr("id"))
			if id == "" || b.items[id] != nil {
				continue
			}
			e := &epubItem{id: id, href: it.attr("href"), mediaType: strings.ToLower(strings.TrimSpace(it.attr("media-type"))), props: it.attr("properties")}
			if p, _, ok := resolveRef(b.opfPath, e.href); ok {
				if f := b.get(p); f != nil {
					e.path = f.name
				}
			}
			b.items[id] = e
			if e.path != "" {
				if _, dup := b.byPath[e.path]; !dup {
					b.byPath[e.path] = e
				}
			}
		}
	}
	sp := root.child("spine")
	if sp == nil {
		return errEpubNoSpine
	}
	if sp.attr("page-progression-direction") == "rtl" {
		b.meta.rtl = true
	}
	n, missing := 0, 0
	for _, ir := range sp.kids {
		if ir.name != "itemref" {
			continue
		}
		if n++; n > maxSpine {
			b.warn("The spine lists more than %d items; the rest were ignored.", maxSpine)
			break
		}
		it := b.items[ir.attr("idref")]
		if it == nil || it.path == "" {
			missing++
			continue
		}
		b.spine = append(b.spine, epubSpine{item: it, linear: !strings.EqualFold(ir.attr("linear"), "no"), props: ir.attr("properties")})
	}
	if missing > 0 {
		b.warn("%d item(s) of the reading order name something that is not in the file and were left out.", missing)
	}
	if len(b.spine) == 0 {
		return errEpubNoSpine
	}
	// A fixed layout has no flow to import.
	if b.meta.fixedLayout {
		return errEpubFixed
	}
	// the NCX named by the spine, kept for readNavigation
	b.tocPages["ncx:"+sp.attr("toc")] = true
	// guide
	if g := root.child("guide"); g != nil {
		for _, r := range g.kids {
			if r.name != "reference" {
				continue
			}
			p, frag, ok := resolveRef(b.opfPath, r.attr("href"))
			if !ok {
				continue
			}
			t := strings.ToLower(strings.TrimSpace(r.attr("type")))
			if t == "text" {
				t = "bodymatter"
			}
			b.addLand(landmark{typ: t, title: cleanText(r.attr("title"), 200), path: p, frag: frag})
		}
	}
	return nil
}

// readMetadata reads dc: elements and meta refinements.
func (b *epubBook) readMetadata(root *xnode) {
	md := root.child("metadata")
	if md == nil {
		return
	}
	type refined map[string]string
	refine := map[string]refined{}
	for _, k := range md.kids {
		if k.name == "meta" {
			if r := strings.TrimPrefix(strings.TrimSpace(k.attr("refines")), "#"); r != "" && k.attr("property") != "" {
				if refine[r] == nil {
					refine[r] = refined{}
				}
				refine[r][k.attr("property")] = strings.TrimSpace(k.text)
			}
		}
	}
	one := func(s string, max int) string { return cleanText(s, max) }
	var titles []struct {
		text, typ string
		order     int
	}
	count := 0
	for _, k := range md.kids {
		if count++; count > maxMetaItems*4 {
			break
		}
		txt := strings.TrimSpace(k.text)
		id := k.attr("id")
		switch k.name {
		case "dc:title":
			typ := refine[id]["title-type"]
			order := 0
			fmt.Sscanf(refine[id]["display-seq"], "%d", &order)
			titles = append(titles, struct {
				text, typ string
				order     int
			}{one(txt, 300), typ, order})
		case "dc:creator", "dc:contributor":
			role := strings.ToLower(firstOf(refine[id]["role"], k.attr("role")))
			name := one(txt, 200)
			if name == "" {
				continue
			}
			if k.name == "dc:creator" && (role == "" || role == "aut") && len(b.meta.authors) < maxPeople {
				b.meta.authors = append(b.meta.authors, name)
			} else if len(b.meta.contributors) < maxPeople {
				b.meta.contributors = append(b.meta.contributors, name)
			}
		case "dc:language":
			if b.meta.language == "" {
				b.meta.language = langOf(txt)
			}
		case "dc:identifier":
			if b.meta.identifier == "" || id == root.attr("unique-identifier") {
				b.meta.identifier = one(txt, 300)
				b.meta.idScheme = one(firstOf(k.attr("scheme"), refine[id]["identifier-type"]), 60)
			}
		case "dc:publisher":
			if b.meta.publisher == "" {
				b.meta.publisher = one(txt, 200)
			}
		case "dc:date":
			if b.meta.date == "" {
				b.meta.date = one(txt, 40)
			}
		case "dc:description":
			if b.meta.description == "" {
				b.meta.description = cleanText(txt, maxMetaText)
			}
		case "dc:rights":
			if b.meta.rights == "" {
				b.meta.rights = one(txt, 400)
			}
		case "dc:subject":
			if s := one(txt, 100); s != "" && len(b.meta.subjects) < 50 {
				b.meta.subjects = append(b.meta.subjects, s)
			}
		case "meta":
			name, content := k.attr("name"), k.attr("content")
			prop := k.attr("property")
			switch {
			case name == "cover":
				b.coverID = strings.TrimSpace(content)
			case name == "calibre:series":
				b.meta.series = one(content, 200)
			case name == "calibre:series_index":
				b.meta.seriesIndex = one(content, 20)
			case name == "generator" || name == "calibre:generator":
				b.meta.generator = one(content, 100)
			case name == "fixed-layout" && strings.EqualFold(content, "true"):
				b.meta.fixedLayout = true
			case name == "original-resolution" && content != "":
				b.meta.fixedLayout = true
			case prop == "rendition:layout" && strings.EqualFold(txt, "pre-paginated") && k.attr("refines") == "":
				b.meta.fixedLayout = true
			case prop == "dcterms:modified":
				b.meta.modified = one(txt, 40)
			case prop == "belongs-to-collection" && b.meta.series == "":
				id2 := k.attr("id")
				if refine[id2]["collection-type"] == "series" || refine[id2]["collection-type"] == "" {
					b.meta.series = one(txt, 200)
					b.meta.seriesIndex = one(refine[id2]["group-position"], 20)
				}
			}
		}
	}
	// the main title is the one refined as such, else the first; a subtitle only when the book says it is one
	sort.SliceStable(titles, func(i, j int) bool {
		return titles[i].order < titles[j].order && titles[i].order != 0 && titles[j].order != 0
	})
	for _, t := range titles {
		switch {
		case t.typ == "subtitle" && b.meta.subtitle == "":
			b.meta.subtitle = t.text
		case (t.typ == "main" || t.typ == "") && b.meta.title == "":
			b.meta.title = t.text
		}
	}
	if b.meta.title == "" && len(titles) > 0 {
		b.meta.title = titles[0].text
	}
	b.meta.rtl = b.meta.rtl || b.meta.dirAttr == "rtl" || epubRTL(b.meta.language)
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// langOf is a language tag as the project holds it, or "".
func langOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 16 || strings.ContainsAny(s, " <>\"'") {
		return ""
	}
	return s
}

// epubRTL says a language is written right to left.
func epubRTL(lang string) bool {
	l := strings.ToLower(lang)
	for _, p := range []string{"ar", "he", "iw", "fa", "ur", "yi", "ps", "sd", "ug", "dv", "ckb"} {
		if l == p || strings.HasPrefix(l, p+"-") || strings.HasPrefix(l, p+"_") {
			return true
		}
	}
	return false
}

// ---- navigation ---------------------------------------------------------------------------

// readNavigation reads the table of contents: an EPUB 3 navigation document if
// the book has one, else the NCX, else nothing (the sections are then the files).
func (b *epubBook) readNavigation() {
	var navItem, ncxItem *epubItem
	for _, it := range b.items {
		if it.path == "" {
			continue
		}
		if it.hasProp("nav") && navItem == nil {
			navItem = it
		}
		if it.mediaType == "application/x-dtbncx+xml" && ncxItem == nil {
			ncxItem = it
		}
	}
	if navItem != nil {
		if f := b.get(navItem.path); f != nil && f.data != nil {
			toc, lands := parseNavDoc(prepareXHTML(f.data), navItem.path, b.stop)
			for _, l := range lands {
				b.addLand(l)
			}
			if len(toc) > 0 {
				b.toc, b.tocFrom = toc, "nav"
				b.tocPages[navItem.path] = true
			}
		}
	}
	if b.tocFrom == "" && ncxItem != nil {
		if raw := b.text(ncxItem.path); raw != nil {
			if root, err := parseEpubXML(raw); err == nil {
				var n int
				toc := parseNCX(root, ncxItem.path, 0, &n)
				if len(toc) > 0 {
					b.toc, b.tocFrom = toc, "ncx"
				}
			} else {
				b.warn("The table of contents (NCX) could not be read (%v); the files are used as chapters.", err)
			}
		}
	}
}

func parseNCX(n *xnode, ncxPath string, depth int, count *int) []*tocEntry {
	var out []*tocEntry
	holder := n
	if depth == 0 {
		holder = n.child("navMap")
		if holder == nil {
			return nil
		}
	}
	for _, np := range holder.kids {
		if np.name != "navPoint" {
			continue
		}
		if *count++; *count > maxTocEntries {
			break
		}
		e := &tocEntry{}
		if nl := np.child("navLabel"); nl != nil {
			e.label = cleanText(nl.childText("text"), 300)
		}
		if c := np.child("content"); c != nil {
			e.href = c.attr("src")
			if p, frag, ok := resolveRef(ncxPath, e.href); ok {
				e.path, e.frag = p, frag
			}
		}
		if depth < maxTocDepth {
			e.children = parseNCX(np, ncxPath, depth+1, count)
		}
		out = append(out, e)
	}
	return out
}

// labelLike says text is the label of a footnote marker or a back link: a number, a symbol, a
// word or two such as "back".
func labelLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if len([]rune(s)) > 8 {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters == 0 {
		return true
	}
	l := strings.ToLower(strings.Trim(s, "[]()↩↑^↵ ."))
	switch l {
	case "back", "return", "ret", "top", "up", "fn", "note":
		return true
	}
	return false
}
