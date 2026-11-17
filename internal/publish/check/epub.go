package check

import (
	"archive/zip"
	"bytes"
	"sort"
	"strings"
)

const (
	nsXHTML = "http://www.w3.org/1999/xhtml"
	nsOPF   = "http://www.idpf.org/2007/opf"
	nsOPS   = "http://www.idpf.org/2007/ops"
	nsCont  = "urn:oasis:names:tc:opendocument:xmlns:container"
)

var voidElements = map[string]bool{"br": true, "hr": true, "img": true, "meta": true, "link": true, "input": true, "col": true, "wbr": true}

// EPUB returns what is wrong with an EPUB written by internal/publish, or nil.
func EPUB(data []byte) []string {
	a, err := openZip(data)
	if err != nil {
		return []string{err.Error()}
	}
	// The mimetype entry: first, stored, exactly this text, no extra field.
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if len(zr.File) == 0 || zr.File[0].Name != "mimetype" {
		a.problem("mimetype must be the first entry")
	} else {
		f := zr.File[0]
		if f.Method != zip.Store {
			a.problem("mimetype must be stored, not compressed")
		}
		if len(f.Extra) != 0 {
			a.problem("mimetype must have no extra field")
		}
		if string(a.files["mimetype"]) != "application/epub+zip" {
			a.problem("mimetype must be exactly application/epub+zip")
		}
	}
	trees := map[string]*node{}
	for _, name := range a.order {
		l := strings.ToLower(name)
		if strings.HasSuffix(l, ".js") || strings.Contains(l, "/scripts/") {
			a.problem("script file %s", name)
		}
		if strings.HasSuffix(l, ".xml") || strings.HasSuffix(l, ".xhtml") || strings.HasSuffix(l, ".opf") || strings.HasSuffix(l, ".ncx") || strings.HasSuffix(l, ".svg") {
			t, err := parseXML(a.files[name])
			if err != nil {
				a.problem("%s is not well-formed XML: %v", name, err)
				continue
			}
			trees[name] = t
		}
	}
	// container.xml
	cont := trees["META-INF/container.xml"]
	if cont == nil || cont.name != "container" || cont.space != nsCont {
		a.problem("META-INF/container.xml is missing or is not a container")
		sort.Strings(a.probs)
		return a.probs
	}
	var opfPath string
	cont.walk(func(n *node) {
		if n.name == "rootfile" && opfPath == "" {
			opfPath = n.attr("full-path")
			if n.attr("media-type") != "application/oebps-package+xml" {
				a.problem("the rootfile's media type is not application/oebps-package+xml")
			}
		}
	})
	opf := trees[opfPath]
	if opf == nil {
		a.problem("the package document %q is not in the archive", opfPath)
		sort.Strings(a.probs)
		return a.probs
	}
	a.opf(opfPath, opf, trees)
	sort.Strings(a.probs)
	return a.probs
}

func (a *archive) opf(opfPath string, opf *node, trees map[string]*node) {
	if opf.name != "package" || opf.space != nsOPF || opf.attr("version") != "3.0" {
		a.problem("%s is not an EPUB 3 package", opfPath)
		return
	}
	// metadata
	md := opf.child("metadata")
	if md == nil {
		a.problem("no metadata")
		return
	}
	uid := opf.attr("unique-identifier")
	var haveTitle, haveLang, haveModified, haveUID bool
	for _, k := range md.kids {
		switch k.name {
		case "title":
			haveTitle = strings.TrimSpace(k.text) != ""
		case "language":
			haveLang = strings.TrimSpace(k.text) != ""
		case "identifier":
			if k.attr("id") == uid && strings.TrimSpace(k.text) != "" {
				haveUID = true
				if !strings.HasPrefix(k.text, "urn:uuid:") {
					a.problem("the identifier is not a urn:uuid")
				}
			}
		case "meta":
			if k.attr("property") == "dcterms:modified" {
				v := strings.TrimSpace(k.text)
				haveModified = len(v) == 20 && strings.HasSuffix(v, "Z") && v[10] == 'T'
			}
		}
	}
	if !haveTitle {
		a.problem("no dc:title")
	}
	if !haveLang {
		a.problem("no dc:language")
	}
	if !haveUID {
		a.problem("the unique identifier %q is not a dc:identifier", uid)
	}
	if !haveModified {
		a.problem("no dcterms:modified of the form CCYY-MM-DDThh:mm:ssZ")
	}
	// manifest
	man := opf.child("manifest")
	if man == nil {
		a.problem("no manifest")
		return
	}
	type item struct {
		id, href, mt, props string
		file                string
	}
	items := map[string]item{}
	byFile := map[string]string{}
	navCount := 0
	for _, k := range man.kids {
		if k.name != "item" {
			continue
		}
		it := item{id: k.attr("id"), href: k.attr("href"), mt: k.attr("media-type"), props: k.attr("properties")}
		if it.id == "" || it.href == "" || it.mt == "" {
			a.problem("a manifest item lacks id, href or media-type")
			continue
		}
		if _, dup := items[it.id]; dup {
			a.problem("duplicate manifest id %s", it.id)
		}
		if strings.Contains(it.href, "://") || strings.HasPrefix(it.href, "/") {
			a.problem("manifest item %s is not inside the package: %s", it.id, it.href)
		}
		it.file = resolve(opfPath, it.href)
		if _, ok := a.files[it.file]; !ok {
			a.problem("manifest item %s points at %s, which is not in the archive", it.id, it.file)
		}
		if other, dup := byFile[it.file]; dup {
			a.problem("%s is listed twice in the manifest (%s and %s)", it.file, other, it.id)
		}
		byFile[it.file] = it.id
		if strings.Contains(" "+it.props+" ", " nav ") {
			navCount++
			if it.mt != "application/xhtml+xml" {
				a.problem("the navigation document is not XHTML")
			}
		}
		if strings.Contains(" "+it.props+" ", " scripted ") || strings.Contains(" "+it.props+" ", " remote-resources ") {
			a.problem("manifest item %s declares scripted or remote resources", it.id)
		}
		items[it.id] = it
	}
	if navCount != 1 {
		a.problem("the manifest must have exactly one navigation document (has %d)", navCount)
	}
	for _, name := range a.order {
		if name == "mimetype" || strings.HasPrefix(name, "META-INF/") || name == opfPath {
			continue
		}
		if _, ok := byFile[name]; !ok {
			a.problem("%s is in the archive but not in the manifest", name)
		}
	}
	// spine
	spine := opf.child("spine")
	if spine == nil {
		a.problem("no spine")
		return
	}
	ncxID := spine.attr("toc")
	if ncxID != "" {
		if it, ok := items[ncxID]; !ok || it.mt != "application/x-dtbncx+xml" {
			a.problem("the spine's toc attribute does not name the NCX")
		}
	}
	seen := map[string]bool{}
	nSpine := 0
	for _, k := range spine.kids {
		if k.name != "itemref" {
			continue
		}
		id := k.attr("idref")
		it, ok := items[id]
		if !ok {
			a.problem("the spine refers to %s, which is not in the manifest", id)
			continue
		}
		if seen[id] {
			a.problem("the spine lists %s twice", id)
		}
		seen[id] = true
		nSpine++
		if it.mt != "application/xhtml+xml" && it.mt != "image/svg+xml" {
			a.problem("spine item %s is not a content document (%s)", id, it.mt)
		}
	}
	if nSpine == 0 {
		a.problem("the spine is empty")
	}
	// cover image
	for _, it := range items {
		if strings.Contains(" "+it.props+" ", " cover-image ") && !strings.HasPrefix(it.mt, "image/") {
			a.problem("the cover image %s is not an image", it.id)
		}
	}
	// content documents
	ids := map[string]map[string]bool{} // file -> ids
	for _, it := range items {
		if it.mt != "application/xhtml+xml" {
			continue
		}
		t := trees[it.file]
		if t == nil {
			continue
		}
		ids[it.file] = a.xhtml(it.file, t, byFile)
	}
	// every in-package link resolves, fragments included
	for _, it := range items {
		if it.mt != "application/xhtml+xml" {
			continue
		}
		t := trees[it.file]
		if t == nil {
			continue
		}
		t.walk(func(n *node) {
			if n.space != nsXHTML || n.name != "a" {
				return
			}
			h := n.attr("href")
			if h == "" || strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") || strings.HasPrefix(h, "mailto:") {
				return
			}
			if strings.Contains(h, "://") || strings.HasPrefix(h, "javascript:") {
				a.problem("%s: link with an unexpected scheme: %s", it.file, h)
				return
			}
			frag := ""
			if i := strings.IndexByte(h, '#'); i >= 0 {
				frag = h[i+1:]
			}
			target := it.file
			if p := strings.SplitN(h, "#", 2)[0]; p != "" {
				target = resolve(it.file, p)
			}
			if _, ok := a.files[target]; !ok {
				a.problem("%s: link to %s, which is not in the archive", it.file, h)
				return
			}
			if frag != "" {
				if tid, ok := ids[target]; ok && !tid[frag] {
					a.problem("%s: link to the missing anchor %s", it.file, h)
				}
			}
		})
	}
	// the navigation document has a toc nav with links to spine documents
	for _, it := range items {
		if !strings.Contains(" "+it.props+" ", " nav ") {
			continue
		}
		t := trees[it.file]
		if t == nil {
			continue
		}
		hasTOC := false
		t.walk(func(n *node) {
			if n.name == "nav" {
				if v, _ := n.attrNS(nsOPS, "type"); strings.Contains(" "+v+" ", " toc ") {
					hasTOC = true
					links := 0
					n.walk(func(m *node) {
						if m.name == "a" {
							links++
							f := resolve(it.file, m.attr("href"))
							if id, ok := byFile[f]; !ok || !seen[id] {
								a.problem("the table of contents links to %s, which is not in the spine", m.attr("href"))
							}
						}
					})
					if links == 0 {
						a.problem("the table of contents is empty")
					}
				}
			}
		})
		if !hasTOC {
			a.problem("the navigation document has no nav of type toc")
		}
	}
	// the NCX
	if ncxID != "" {
		if it, ok := items[ncxID]; ok {
			if t := trees[it.file]; t != nil {
				t.walk(func(n *node) {
					if n.name == "content" {
						f := resolve(it.file, n.attr("src"))
						if _, ok := a.files[f]; !ok {
							a.problem("the NCX points at %s, which is not in the archive", n.attr("src"))
						}
					}
				})
			}
		}
	}
}

// xhtml checks one content document and returns its ids.
func (a *archive) xhtml(file string, t *node, byFile map[string]string) map[string]bool {
	if t.name != "html" || t.space != nsXHTML {
		a.problem("%s is not an XHTML document", file)
		return nil
	}
	ids := map[string]bool{}
	hasTitle := false
	t.walk(func(n *node) {
		if n.space == nsXHTML {
			switch n.name {
			case "script", "iframe", "object", "embed", "form", "base", "applet":
				a.problem("%s: forbidden element <%s>", file, n.name)
			case "title":
				hasTitle = strings.TrimSpace(n.text) != ""
			case "link":
				if h := n.attr("href"); strings.Contains(h, "://") {
					a.problem("%s: <link> to %s", file, h)
				}
			}
		}
		for _, at := range n.attrs {
			l := at.Name.Local
			if at.Name.Space == "" && strings.HasPrefix(l, "on") {
				a.problem("%s: event handler attribute %s", file, l)
			}
			if at.Name.Space == "" && (l == "src" || l == "poster") && (strings.Contains(at.Value, "://") || strings.HasPrefix(at.Value, "data:") || strings.HasPrefix(at.Value, "//")) {
				a.problem("%s: <%s %s=%q> loads from outside the package", file, n.name, l, at.Value)
			}
			if at.Name.Space == "" && l == "style" && strings.Contains(strings.ToLower(at.Value), "url(") {
				a.problem("%s: a style attribute with url()", file)
			}
			if l == "id" && at.Name.Space == "" {
				if ids[at.Value] {
					a.problem("%s: duplicate id %s", file, at.Value)
				}
				ids[at.Value] = true
			}
			if (l == "src") && at.Name.Space == "" && !strings.Contains(at.Value, "://") {
				f := resolve(file, at.Value)
				if _, ok := a.files[f]; !ok {
					a.problem("%s: <%s src=%q> is not in the archive", file, n.name, at.Value)
				} else if _, ok := byFile[f]; !ok {
					a.problem("%s: %s is not in the manifest", file, f)
				}
			}
		}
	})
	if !hasTitle {
		a.problem("%s has no title", file)
	}
	// a stylesheet link resolves
	t.walk(func(n *node) {
		if n.name == "link" && n.attr("rel") == "stylesheet" {
			f := resolve(file, n.attr("href"))
			if _, ok := a.files[f]; !ok {
				a.problem("%s: stylesheet %s is not in the archive", file, n.attr("href"))
			}
		}
	})
	// void elements have no children
	t.walk(func(n *node) {
		if n.space == nsXHTML && voidElements[n.name] && (len(n.kids) > 0 || strings.TrimSpace(n.text) != "") {
			a.problem("%s: <%s> has content", file, n.name)
		}
	})
	return ids
}
