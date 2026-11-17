// Package check validates a generated site using nothing but the standard
// library: every page tokenizes as well-formed XML, ids are unique, every link
// and image resolves inside the site (fragments included), nothing loads from
// elsewhere, there is no script, inline style or event handler, the feeds parse
// strictly and carry what a reader needs, the sitemap lists absolute addresses,
// and every file name is safe on every operating system.
//
// It is a structural check of what internal/site means to write. It is not a
// feed reader, a search engine or a browser: it proves the files are what the
// generator intends, not that every reader accepts them.
package check

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Options say what the site was built with.
type Options struct {
	// Base is the site's base address (with its trailing slash), or empty when
	// none was set: then no feed or sitemap is expected and none may exist.
	Base string
	// NoIndex is set for a staging site.
	NoIndex bool
}

// Report is what the check found.
type Report struct {
	Problems []string
	Pages    int
	Links    int
	Images   int
	// External are the hosts the pages link to (as text links, never loaded).
	External []string
}

// OK reports whether the site passed.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

func (r *Report) addf(format string, a ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
}

// Site checks a site: files by path.
func Site(files map[string][]byte, o Options) *Report {
	r := &Report{}
	checkNames(files, r)
	ids := map[string]map[string]bool{}
	pages := map[string]*page{}
	var names []string
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if strings.HasSuffix(p, ".html") {
			pg := parsePage(p, files[p], r)
			pages[p] = pg
			ids[p] = pg.ids
			r.Pages++
		}
	}
	ext := map[string]bool{}
	for _, p := range names {
		pg := pages[p]
		if pg == nil {
			continue
		}
		for _, l := range pg.refs {
			r.Links++
			checkRef(p, l, files, ids, o, r, ext)
		}
		r.Images += pg.images
	}
	for h := range ext {
		r.External = append(r.External, h)
	}
	sort.Strings(r.External)
	for _, req := range []string{"index.html", "404.html", "robots.txt", "assets/site.css"} {
		if _, ok := files[req]; !ok {
			r.addf("%s is missing", req)
		}
	}
	feeds := []string{"feed.xml", "atom.xml", "sitemap.xml"}
	for _, f := range feeds {
		d, has := files[f]
		switch {
		case has && o.Base == "":
			r.addf("%s is written although there is no base address", f)
		case !has && o.Base != "" && !(o.NoIndex && f == "sitemap.xml"):
			r.addf("%s is missing although a base address is set", f)
		case has && o.NoIndex && f == "sitemap.xml":
			r.addf("a staging site (noindex) must not carry a sitemap")
		case has:
			switch f {
			case "feed.xml":
				checkRSS(d, o, r)
			case "atom.xml":
				checkAtom(d, o, r)
			default:
				checkSitemap(d, o, r)
			}
		}
	}
	if d, ok := files["robots.txt"]; ok {
		s := string(d)
		if o.NoIndex && !strings.Contains(s, "Disallow: /\n") {
			r.addf("robots.txt does not disallow a staging site")
		}
		if !o.NoIndex && strings.Contains(s, "Disallow: /") {
			r.addf("robots.txt disallows a site that is meant to be found")
		}
	}
	if d, ok := files["assets/site.css"]; ok {
		checkCSS(d, r)
	}
	for _, p := range names {
		if strings.HasSuffix(p, ".svg") {
			checkSVG(p, files[p], r)
		}
	}
	return r
}

// ---- names ------------------------------------------------------------------------------

// FoldKey is the key two file names are compared by on a disk that ignores case
// and normalization.
func FoldKey(p string) string { return foldKey(p) }

func foldKey(p string) string { return norm.NFC.String(cases.Fold().String(norm.NFC.String(p))) }

var winReserved = regexp.MustCompile(`^(?i)(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

func checkNames(files map[string][]byte, r *Report) {
	seen := map[string]string{}
	for p := range files {
		switch {
		case p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || len(p) > 220 || !utf8.ValidString(p):
			r.addf("unsafe path %q", p)
			continue
		}
		for _, seg := range strings.Split(p, "/") {
			base := seg
			if i := strings.IndexByte(base, '.'); i >= 0 {
				base = base[:i]
			}
			switch {
			case seg == "" || seg == "." || seg == "..":
				r.addf("unsafe path %q", p)
			case len(seg) > 120:
				r.addf("path segment too long in %q", p)
			case winReserved.MatchString(base):
				r.addf("%q is a name Windows reserves", p)
			case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") || strings.HasPrefix(seg, "."):
				r.addf("%q ends or begins with a dot or a space", p)
			}
			for _, c := range seg {
				if c < 0x20 || c == 0x7f || strings.ContainsRune(`<>:"|?*`, c) || unicode.Is(unicode.Cf, c) {
					r.addf("%q holds a character some systems refuse", p)
					break
				}
			}
		}
		// Two names a case-insensitive, normalization-insensitive disk (APFS, NTFS,
		// exFAT) treats as one file: full Unicode case folding of the composed and
		// of the decomposed form.
		for _, k := range []string{foldKey(norm.NFC.String(p)), foldKey(norm.NFD.String(p))} {
			if o, ok := seen[k]; ok && o != p {
				r.addf("%q and %q differ only by case or normalization", o, p)
			}
			seen[k] = p
		}
	}
}

// ---- pages ------------------------------------------------------------------------------

type ref struct {
	tag, attr, val string
	rel            string
}

type page struct {
	ids    map[string]bool
	refs   []ref
	images int
}

var voidEl = map[string]bool{"meta": true, "link": true, "img": true, "br": true, "hr": true, "input": true}

var bannedEl = map[string]bool{"script": true, "style": true, "iframe": true, "object": true, "embed": true, "form": true, "base": true,
	"frame": true, "frameset": true, "applet": true, "svg": true, "math": true, "template": true, "noscript": true, "video": true,
	"audio": true, "source": true, "track": true, "button": true, "select": true, "textarea": true, "dialog": true}

var urlAttr = map[string]bool{"href": true, "src": true, "action": true, "srcset": true, "poster": true, "data": true, "cite": true, "formaction": true, "background": true}

func parsePage(name string, data []byte, r *Report) *page {
	pg := &page{ids: map[string]bool{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil
	var stack []string
	var h1, mains, titles int
	var inTitle bool
	var title strings.Builder
	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			r.addf("%s: not well-formed: %v", name, err)
			return pg
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := strings.ToLower(t.Name.Local)
			if !sawRoot {
				sawRoot = true
				if el != "html" {
					r.addf("%s: the root element is %s, not html", name, el)
				}
				if attrOf(t, "lang") == "" {
					r.addf("%s: <html> has no lang", name)
				}
				if d := attrOf(t, "dir"); d != "ltr" && d != "rtl" {
					r.addf("%s: <html> has no valid dir", name)
				}
			}
			if t.Name.Space != "" && t.Name.Space != "http://www.w3.org/1999/xhtml" {
				r.addf("%s: element %s has a namespace", name, el)
			}
			if bannedEl[el] {
				r.addf("%s: <%s> is not allowed", name, el)
			}
			switch el {
			case "h1":
				h1++
			case "main":
				mains++
			case "title":
				titles++
				inTitle = true
			case "img":
				pg.images++
				if !hasAttr(t, "alt") {
					r.addf("%s: an <img> has no alt attribute", name)
				}
			case "a":
				if !hasAttr(t, "href") && !hasAttr(t, "id") {
					r.addf("%s: an <a> has neither href nor id", name)
				}
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				if a.Name.Space != "" {
					r.addf("%s: attribute %s:%s on <%s>", name, a.Name.Space, an, el)
					continue
				}
				switch {
				case an == "style":
					r.addf("%s: <%s> has an inline style", name, el)
				case strings.HasPrefix(an, "on"):
					r.addf("%s: <%s> has an event handler (%s)", name, el, an)
				case an == "id":
					if a.Value == "" {
						r.addf("%s: an empty id", name)
					} else if pg.ids[a.Value] {
						r.addf("%s: the id %q appears twice", name, a.Value)
					}
					pg.ids[a.Value] = true
				case urlAttr[an]:
					pg.refs = append(pg.refs, ref{tag: el, attr: an, val: a.Value, rel: attrOf(t, "rel")})
				}
			}
			if !voidEl[el] {
				stack = append(stack, el)
			}
		case xml.EndElement:
			el := strings.ToLower(t.Name.Local)
			if el == "title" {
				inTitle = false
			}
			if !voidEl[el] && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if inTitle {
				title.Write(t)
			}
		case xml.ProcInst:
			if t.Target != "xml" {
				r.addf("%s: a processing instruction", name)
			}
		case xml.Directive:
			if !strings.EqualFold(strings.TrimSpace(string(t)), "doctype html") {
				r.addf("%s: an unexpected directive <!%s>", name, t)
			}
		}
	}
	if len(stack) != 0 {
		r.addf("%s: unclosed elements %v", name, stack)
	}
	if !sawRoot {
		r.addf("%s: empty", name)
	}
	if h1 != 1 {
		r.addf("%s: has %d <h1> (want 1)", name, h1)
	}
	if mains != 1 {
		r.addf("%s: has %d <main> (want 1)", name, mains)
	}
	if titles != 1 || strings.TrimSpace(title.String()) == "" {
		r.addf("%s: needs exactly one non-empty <title>", name)
	}
	return pg
}

func attrOf(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func hasAttr(t xml.StartElement, name string) bool {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return true
		}
	}
	return false
}

// checkRef resolves one address of a page.
func checkRef(from string, l ref, files map[string][]byte, ids map[string]map[string]bool, o Options, r *Report, ext map[string]bool) {
	v := l.val
	if v == "" {
		r.addf("%s: an empty %s on <%s>", from, l.attr, l.tag)
		return
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			r.addf("%s: a control character in %s=%q", from, l.attr, v)
			return
		}
	}
	u, err := url.Parse(v)
	if err != nil {
		r.addf("%s: unparsable address %q", from, v)
		return
	}
	// Absolute and scheme-relative addresses.
	if u.Scheme != "" || strings.HasPrefix(v, "//") {
		switch {
		case strings.HasPrefix(v, "//"):
			r.addf("%s: scheme-relative address %q", from, v)
		case u.Scheme == "mailto":
			if l.tag != "a" {
				r.addf("%s: mailto on <%s>", from, l.tag)
			}
		case u.Scheme == "http" || u.Scheme == "https":
			if o.Base != "" && strings.HasPrefix(v, o.Base) {
				rest := strings.TrimPrefix(v, o.Base)
				if i := strings.IndexAny(rest, "#"); i >= 0 {
					rest = rest[:i] + rest[i:]
				}
				checkInternal(from, "", rest, files, ids, r)
				return
			}
			if l.tag != "a" {
				r.addf("%s: <%s %s=%q> would load something from outside the site", from, l.tag, l.attr, v)
				return
			}
			if l.attr != "href" || !strings.Contains(l.rel, "noopener") {
				r.addf("%s: an outside link %q without rel=noopener", from, v)
			}
			ext[strings.ToLower(u.Hostname())] = true
		default:
			r.addf("%s: the address scheme %q is not allowed (%q)", from, u.Scheme, v)
		}
		return
	}
	if strings.HasPrefix(v, "/") {
		if from != "404.html" {
			r.addf("%s: a root-relative address %q (only the 404 page may use one)", from, v)
			return
		}
		checkInternal(from, "", strings.TrimPrefix(v, "/"), files, ids, r)
		return
	}
	checkInternal(from, path.Dir(from), v, files, ids, r)
}

func checkInternal(from, dir, v string, files map[string][]byte, ids map[string]map[string]bool, r *Report) {
	frag := ""
	if i := strings.IndexByte(v, '#'); i >= 0 {
		v, frag = v[:i], v[i+1:]
	}
	if i := strings.IndexByte(v, '?'); i >= 0 {
		r.addf("%s: a query string in an internal address", from)
		v = v[:i]
	}
	target := from
	if v != "" {
		dec, err := url.PathUnescape(v)
		if err != nil {
			r.addf("%s: bad escape in %q", from, v)
			return
		}
		target = path.Clean(path.Join(dir, dec))
		if dir == "" {
			target = path.Clean(dec)
		}
		if strings.HasPrefix(target, "..") || strings.HasPrefix(target, "/") {
			r.addf("%s: %q leaves the site", from, v)
			return
		}
		if _, ok := files[target]; !ok {
			r.addf("%s: %q does not resolve (looked for %s)", from, v, target)
			return
		}
	}
	if frag != "" {
		dec, _ := url.PathUnescape(frag)
		m, ok := ids[target]
		if !ok || !m[dec] {
			r.addf("%s: the fragment #%s is not in %s", from, frag, target)
		}
	}
}

// ---- feeds ------------------------------------------------------------------------------

type xnode struct {
	name  string
	attrs map[string]string
	text  strings.Builder
	kids  []*xnode
}

func (n *xnode) child(name string) *xnode {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *xnode) children(name string) []*xnode {
	var out []*xnode
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

func (n *xnode) val() string { return strings.TrimSpace(n.text.String()) }

func parseTree(data []byte) (*xnode, error) {
	if !bytes.HasPrefix(data, []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>")) {
		return nil, fmt.Errorf("no XML declaration")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil
	var root *xnode
	var stack []*xnode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("two root elements")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.Directive:
			return nil, fmt.Errorf("a DTD or directive in a feed")
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty")
	}
	return root, nil
}

func absOK(s, base string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && strings.HasPrefix(s, base)
}

// checkBody checks the HTML a feed carries: well-formed, no script or style,
// and every address absolute.
func checkBody(where, html, base string, r *Report) {
	dec := xml.NewDecoder(strings.NewReader("<div>" + html + "</div>"))
	dec.Strict = true
	dec.Entity = nil
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			r.addf("%s: the HTML body is not well-formed: %v", where, err)
			return
		}
		if t, ok := tok.(xml.StartElement); ok {
			el := strings.ToLower(t.Name.Local)
			if bannedEl[el] {
				r.addf("%s: <%s> in a feed body", where, el)
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				if an == "style" || strings.HasPrefix(an, "on") {
					r.addf("%s: %s in a feed body", where, an)
				}
				if (an == "href" || an == "src") && !(absOK(a.Value, base) || (el == "a" && (strings.HasPrefix(a.Value, "https://") || strings.HasPrefix(a.Value, "http://") || strings.HasPrefix(a.Value, "mailto:")))) {
					r.addf("%s: the address %q in a feed body is not absolute", where, a.Value)
				}
			}
		}
	}
}

func checkRSS(data []byte, o Options, r *Report) {
	root, err := parseTree(data)
	if err != nil {
		r.addf("feed.xml: %v", err)
		return
	}
	if root.name != "rss" || root.attrs["version"] != "2.0" {
		r.addf("feed.xml: not an RSS 2.0 document")
		return
	}
	ch := root.child("channel")
	if ch == nil {
		r.addf("feed.xml: no channel")
		return
	}
	for _, f := range []string{"title", "link", "description"} {
		if c := ch.child(f); c == nil || c.val() == "" {
			r.addf("feed.xml: the channel has no %s", f)
		}
	}
	if l := ch.child("link"); l != nil && !absOK(l.val(), o.Base) {
		r.addf("feed.xml: the channel link is not absolute")
	}
	if d := ch.child("lastBuildDate"); d == nil {
		r.addf("feed.xml: no lastBuildDate")
	} else if _, err := time.Parse(time.RFC1123Z, d.val()); err != nil {
		r.addf("feed.xml: lastBuildDate %q is not RFC 822", d.val())
	}
	seen := map[string]bool{}
	for i, it := range ch.children("item") {
		w := fmt.Sprintf("feed.xml item %d", i+1)
		if it.child("title") == nil || it.child("title").val() == "" {
			r.addf("%s: no title", w)
		}
		if l := it.child("link"); l == nil || !absOK(l.val(), o.Base) {
			r.addf("%s: the link is missing or not absolute", w)
		}
		g := it.child("guid")
		if g == nil || g.val() == "" {
			r.addf("%s: no guid", w)
		} else {
			if seen[g.val()] {
				r.addf("%s: the guid %q repeats", w, g.val())
			}
			seen[g.val()] = true
			if g.attrs["isPermaLink"] != "false" {
				r.addf("%s: the guid must say isPermaLink=false", w)
			}
		}
		if d := it.child("pubDate"); d == nil {
			r.addf("%s: no pubDate", w)
		} else if _, err := time.Parse(time.RFC1123Z, d.val()); err != nil {
			r.addf("%s: pubDate %q is not RFC 822", w, d.val())
		}
		if it.child("author") != nil {
			r.addf("%s: <author> must be an email address; use dc:creator", w)
		}
		if c := it.child("encoded"); c != nil {
			checkBody(w, c.text.String(), o.Base, r)
		}
		if bytes.Contains(data, []byte("<![CDATA[")) {
			r.addf("feed.xml: CDATA is not used")
			break
		}
	}
}

func checkAtom(data []byte, o Options, r *Report) {
	root, err := parseTree(data)
	if err != nil {
		r.addf("atom.xml: %v", err)
		return
	}
	if root.name != "feed" {
		r.addf("atom.xml: not an Atom feed")
		return
	}
	for _, f := range []string{"id", "title", "updated"} {
		if c := root.child(f); c == nil || c.val() == "" {
			r.addf("atom.xml: no %s", f)
		}
	}
	if u := root.child("updated"); u != nil {
		if _, err := time.Parse(time.RFC3339, u.val()); err != nil {
			r.addf("atom.xml: updated %q is not RFC 3339", u.val())
		}
	}
	if root.child("author") == nil {
		r.addf("atom.xml: no author")
	}
	seen := map[string]bool{}
	for i, e := range root.children("entry") {
		w := fmt.Sprintf("atom.xml entry %d", i+1)
		id := e.child("id")
		if id == nil || id.val() == "" {
			r.addf("%s: no id", w)
		} else {
			if seen[id.val()] {
				r.addf("%s: the id repeats", w)
			}
			seen[id.val()] = true
		}
		if t := e.child("title"); t == nil || t.val() == "" {
			r.addf("%s: no title", w)
		}
		for _, f := range []string{"published", "updated"} {
			if c := e.child(f); c == nil {
				r.addf("%s: no %s", w, f)
			} else if _, err := time.Parse(time.RFC3339, c.val()); err != nil {
				r.addf("%s: %s %q is not RFC 3339", w, f, c.val())
			}
		}
		ok := false
		for _, l := range e.children("link") {
			if absOK(l.attrs["href"], o.Base) {
				ok = true
			}
		}
		if !ok {
			r.addf("%s: no absolute link", w)
		}
		if c := e.child("content"); c != nil {
			if c.attrs["type"] != "html" {
				r.addf("%s: content type must be html", w)
			}
			checkBody(w, c.text.String(), o.Base, r)
		}
	}
}

func checkSitemap(data []byte, o Options, r *Report) {
	root, err := parseTree(data)
	if err != nil {
		r.addf("sitemap.xml: %v", err)
		return
	}
	if root.name != "urlset" {
		r.addf("sitemap.xml: not a urlset")
		return
	}
	seen := map[string]bool{}
	for _, u := range root.children("url") {
		l := u.child("loc")
		if l == nil || !absOK(l.val(), o.Base) {
			r.addf("sitemap.xml: a url has no absolute loc")
			continue
		}
		if seen[l.val()] {
			r.addf("sitemap.xml: %s appears twice", l.val())
		}
		seen[l.val()] = true
		if m := u.child("lastmod"); m != nil {
			if _, err := time.Parse("2006-01-02", m.val()); err != nil {
				r.addf("sitemap.xml: lastmod %q is not a date", m.val())
			}
		}
	}
	if len(seen) > 50_000 {
		r.addf("sitemap.xml: more than 50,000 urls")
	}
}

// ---- stylesheet and SVG ----------------------------------------------------------------------

func checkCSS(d []byte, r *Report) {
	s := strings.ToLower(string(d))
	for _, bad := range []string{"@import", "gradient", "expression(", "javascript:", "behavior:", "-moz-binding", "box-shadow", "text-shadow", "http://", "https://", "@font-face", "url("} {
		if strings.Contains(s, bad) {
			r.addf("assets/site.css contains %q", bad)
		}
	}
}

func checkSVG(name string, d []byte, r *Report) {
	dec := xml.NewDecoder(bytes.NewReader(d))
	dec.Strict = true
	dec.Entity = nil
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			r.addf("%s: not well-formed: %v", name, err)
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "svg", "rect", "text", "tspan", "g", "title", "desc", "line", "path", "circle":
			default:
				r.addf("%s: element <%s> is not allowed", name, t.Name.Local)
			}
			for _, a := range t.Attr {
				an := strings.ToLower(a.Name.Local)
				if strings.HasPrefix(an, "on") || an == "href" || an == "style" || strings.Contains(strings.ToLower(a.Value), "url(") || strings.Contains(strings.ToLower(a.Value), "javascript") {
					r.addf("%s: attribute %s is not allowed", name, an)
				}
			}
		case xml.Directive, xml.ProcInst:
			if pi, ok := tok.(xml.ProcInst); !ok || pi.Target != "xml" {
				r.addf("%s: a directive or processing instruction", name)
			}
		}
	}
}
