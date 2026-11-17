package imports

import (
	"strings"

	"golang.org/x/net/html"
)

// parseNavDoc reads the navigation document of an EPUB 3: the nested list in
// <nav epub:type="toc"> and the landmarks. It reads tokens, tolerates what
// XHTML written by hand does (an unclosed item, a heading with no link), and
// keeps at most maxTocEntries entries nested at most maxTocDepth deep.
func parseNavDoc(src, navPath string, stop func() error) (toc []*tocEntry, lands []landmark) {
	z := html.NewTokenizer(strings.NewReader(src))
	type frame struct {
		e        *tocEntry
		inNested bool
	}
	var items []frame  // open <li>s
	var navKind string // "toc", "landmarks" or "" while inside a <nav>
	navDepth := 0      // <nav> nesting (only the outermost is read)
	olDepth := 0       // lists open inside the nav
	var land *landmark // a landmark link being read
	var linkEntry *tocEntry
	skip := ""
	count := 0
	extra := 0 // <li>s opened beyond what is kept (not in a contents, too deep, past the entry limit)
	tokens := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tokens++; tokens%4096 == 0 && stop != nil && stop() != nil {
			break // the caller has gone; readEpub says so
		}
		switch tt {
		case html.TextToken:
			if skip != "" {
				continue
			}
			t := string(z.Text())
			switch {
			case land != nil:
				if len(land.title) < 200 {
					land.title += t
				}
			case navKind == "toc" && len(items) > 0 && !items[len(items)-1].inNested:
				if e := items[len(items)-1].e; len(e.label) < 400 {
					e.label += t
				}
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := stripNS(string(name))
			attrs := map[string]string{}
			if hasAttr {
				for {
					k, v, more := z.TagAttr()
					if _, dup := attrs[string(k)]; !dup && len(attrs) < 16 {
						attrs[string(k)] = string(v)
					}
					if !more {
						break
					}
				}
			}
			if _, drop := dropTags[tag]; drop && tag != "title" && tag != "head" && tt == html.StartTagToken {
				skip = tag
				continue
			}
			switch tag {
			case "nav":
				if navDepth++; navDepth == 1 {
					ty := attrs["epub:type"]
					switch {
					case typeHas(ty, "toc") || attrs["role"] == "doc-toc":
						navKind = "toc"
					case typeHas(ty, "landmarks"):
						navKind = "landmarks"
					default:
						navKind = ""
					}
				}
			case "ol", "ul":
				if navKind == "" {
					continue
				}
				olDepth++
				if len(items) > 0 {
					items[len(items)-1].inNested = true
				}
			case "li":
				if navDepth == 0 {
					continue // outside every <nav>: nothing keeps it, and nothing counts it
				}
				if navKind != "toc" || count >= maxTocEntries || len(items) >= maxTocDepth {
					extra++ // counted, not kept: an unclosed <li> costs a number, not a frame
					continue
				}
				count++
				e := &tocEntry{}
				if len(items) == 0 {
					toc = append(toc, e)
				} else {
					p := items[len(items)-1].e
					p.children = append(p.children, e)
				}
				items = append(items, frame{e: e})
			case "a":
				if navKind == "landmarks" && len(lands) < maxLandmarks {
					l := landmark{typ: strings.TrimSpace(attrs["epub:type"])}
					if p, frag, ok := resolveRef(navPath, attrs["href"]); ok {
						l.path, l.frag = p, frag
					}
					lands = append(lands, l)
					land = &lands[len(lands)-1]
				}
				if navKind == "toc" && len(items) > 0 {
					e := items[len(items)-1].e
					if e.href == "" && attrs["href"] != "" {
						e.href = attrs["href"]
						e.types = attrs["epub:type"]
						if p, frag, ok := resolveRef(navPath, e.href); ok {
							e.path, e.frag = p, frag
						}
						linkEntry = e
					}
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := stripNS(string(name))
			if skip != "" {
				if tag == skip {
					skip = ""
				}
				continue
			}
			switch tag {
			case "nav":
				if navDepth--; navDepth == 0 {
					navKind = ""
					items, olDepth, extra = nil, 0, 0
				}
			case "ol", "ul":
				if navKind != "" && olDepth > 0 {
					olDepth--
					// back in the item that held the list
				}
			case "li":
				if navDepth == 0 {
					continue
				}
				if extra > 0 {
					extra--
				} else if len(items) > 0 {
					items = items[:len(items)-1]
				}
			case "a":
				if land != nil {
					land.title = cleanText(land.title, 200)
					land = nil
				}
				linkEntry = nil
			}
		}
	}
	_ = linkEntry
	var fin func(es []*tocEntry, depth int)
	fin = func(es []*tocEntry, depth int) {
		for _, e := range es {
			e.label = cleanText(e.label, 300)
			fin(e.children, depth+1)
		}
	}
	fin(toc, 0)
	return toc, lands
}
