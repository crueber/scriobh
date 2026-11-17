package site

import (
	"fmt"
	"strings"
)

// pg is a page to lay out.
type pg struct {
	path    string // the site path of this page
	href    func(sitePath string) string
	title   string // the page's own title; the site's name is added
	desc    string
	current string // the site path the navigation marks as the current page
	main    string // the inside of <main>, already escaped
	cover   bool
}

// arrow is the arrow of a "back" (previous) or "forward" (next) link: it points
// the way the page reads, so on a right-to-left site "back" points right.
func (b *builder) arrow(back bool) string {
	left := back
	if b.dir == "rtl" {
		left = !left
	}
	if left {
		return `<span aria-hidden="true">←</span>`
	}
	return `<span aria-hidden="true">→</span>`
}

func (b *builder) hrefFrom(path string) func(string) string {
	return func(to string) string { return rel(path, to) }
}

// rootHref is for the one page that is served at addresses of every depth: the
// 404. Its links cannot be relative.
func (b *builder) rootHref() func(string) string {
	return func(to string) string {
		if b.base != nil {
			return absURL(b.base, to)
		}
		return "/" + escPath(to)
	}
}

func (b *builder) navigation(href func(string) string, current string) string {
	var s strings.Builder
	item := func(to, label string) {
		cur := ""
		if to == current {
			cur = ` aria-current="page"`
		}
		fmt.Fprintf(&s, "<li><a href=\"%s\"%s>%s</a></li>\n", esc(href(to)), cur, esc(label))
	}
	s.WriteString("<ul>\n")
	item("index.html", "Home")
	nav := b.navPages()
	if len(nav) > maxNavPages {
		for _, p := range nav[:maxNavPages-1] {
			item(p.path, p.title)
		}
		item("pages/index.html", "All pages")
	} else {
		for _, p := range nav {
			item(p.path, p.title)
		}
	}
	if len(b.tags) > 0 {
		item("tags/index.html", "Tags")
	}
	if len(b.cats) > 0 {
		item("categories/index.html", "Categories")
	}
	for _, n := range b.o.Nav {
		fmt.Fprintf(&s, "<li><a href=\"%s\" rel=\"noopener noreferrer\">%s</a></li>\n", esc(n.URL), esc(n.Label))
	}
	s.WriteString("</ul>")
	return s.String()
}

// layout wraps a page's main part in the site's frame.
func (b *builder) layout(p pg) []byte {
	if p.href == nil {
		p.href = b.hrefFrom(p.path)
	}
	title := b.title
	if p.title != "" {
		title = p.title + " – " + b.title
	}
	var s strings.Builder
	fmt.Fprintf(&s, "<!doctype html>\n<html lang=\"%s\" dir=\"%s\">\n<head>\n<meta charset=\"utf-8\"/>\n", esc(b.lang), esc(b.dir))
	s.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"/>\n<meta name=\"color-scheme\" content=\"light dark\"/>\n<meta name=\"referrer\" content=\"no-referrer\"/>\n")
	if b.o.NoIndex {
		s.WriteString("<meta name=\"robots\" content=\"noindex, nofollow\"/>\n")
	}
	fmt.Fprintf(&s, "<title>%s</title>\n", esc(title))
	desc := p.desc
	if desc == "" {
		desc = b.o.Tagline
	}
	if desc != "" {
		fmt.Fprintf(&s, "<meta name=\"description\" content=\"%s\"/>\n", esc(desc))
	}
	if b.base != nil && !b.o.NoIndex && p.path != "404.html" {
		canon := absURL(b.base, p.path)
		if p.path == "index.html" {
			canon = b.base.String()
		}
		fmt.Fprintf(&s, "<link rel=\"canonical\" href=\"%s\"/>\n", esc(canon))
	}
	fmt.Fprintf(&s, "<link rel=\"stylesheet\" href=\"%s\"/>\n", esc(p.href("assets/site.css")))
	if b.hasFeed {
		fmt.Fprintf(&s, "<link rel=\"alternate\" type=\"application/rss+xml\" title=\"%s\" href=\"%s\"/>\n", esc(b.title+" (RSS)"), esc(p.href("feed.xml")))
		fmt.Fprintf(&s, "<link rel=\"alternate\" type=\"application/atom+xml\" title=\"%s\" href=\"%s\"/>\n", esc(b.title+" (Atom)"), esc(p.href("atom.xml")))
	}
	s.WriteString("</head>\n<body>\n<a class=\"skip\" href=\"#main\">Skip to the content</a>\n<header class=\"site\">\n")
	fmt.Fprintf(&s, "<p class=\"brand\" dir=\"auto\"><a href=\"%s\">%s</a></p>\n", esc(p.href("index.html")), esc(b.title))
	if b.o.Tagline != "" {
		fmt.Fprintf(&s, "<p class=\"tagline\" dir=\"auto\">%s</p>\n", esc(b.o.Tagline))
	}
	fmt.Fprintf(&s, "<nav class=\"main\" aria-label=\"Main\">\n%s\n</nav>\n</header>\n<main id=\"main\">\n", b.navigation(p.href, p.current))
	if p.cover && b.o.ShowCover && len(b.in.CoverSVG) > 0 {
		fmt.Fprintf(&s, "<img class=\"cover\" src=\"%s\" alt=\"\" width=\"600\" height=\"800\"/>\n", esc(p.href("assets/cover.svg")))
	}
	s.WriteString(p.main)
	s.WriteString("</main>\n<footer class=\"site\">\n")
	if b.o.FooterText != "" {
		fmt.Fprintf(&s, "<p dir=\"auto\">%s</p>\n", esc(b.o.FooterText))
	}
	if b.hasFeed {
		fmt.Fprintf(&s, "<p><a href=\"%s\">RSS</a> · <a href=\"%s\">Atom</a></p>\n", esc(p.href("feed.xml")), esc(p.href("atom.xml")))
	}
	s.WriteString("</footer>\n</body>\n</html>\n")
	return []byte(s.String())
}

func (b *builder) metaLine(from string, p *post) string {
	var parts []string
	if b.o.ShowDates {
		d := p.date.Format("2006-01-02")
		parts = append(parts, fmt.Sprintf("<time datetime=\"%s\">%s</time>", d, d))
	}
	if b.o.ShowAuthor && b.author != "" {
		if b.about != nil {
			parts = append(parts, fmt.Sprintf("<a href=\"%s\">%s</a>", esc(rel(from, b.about.path)), esc(b.author)))
		} else {
			parts = append(parts, esc(b.author))
		}
	}
	if b.o.ShowReading && p.minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d min read", p.minutes))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<p class=\"meta\">" + strings.Join(parts, " · ") + "</p>\n"
}

// renderBody writes a post's text, and its notes, for a page at `from`, or for
// a feed (abs set). The counts of what was left out come back with it.
func (b *builder) renderBody(p *post, from string, feed bool) (string, *writer) {
	w := &writer{from: from, imgs: b.imgs}
	if feed {
		w.from, w.abs, w.frag = "", b.base, absURL(b.base, p.path)
	}
	w.blocks(p.b.nodes, 1)
	used := map[int]bool{}
	noteRefs(p.b.nodes, used)
	w.notes(p.b.notes, used)
	return w.out.String(), w
}

func (b *builder) writePages() error {
	for i, p := range b.posts {
		html, w := b.renderBody(p, p.path, false)
		p.images = w.imagesUsed
		b.imageWarnings(p, w)
		var m strings.Builder
		m.WriteString("<article class=\"post\">\n<header>\n")
		fmt.Fprintf(&m, "<h1 dir=\"auto\">%s</h1>\n%s</header>\n", esc(p.title), b.metaLine(p.path, p))
		m.WriteString(html)
		if len(p.tags) > 0 || p.cat != nil {
			m.WriteString("<ul class=\"tags\">\n")
			if p.cat != nil {
				fmt.Fprintf(&m, "<li><a href=\"%s\" rel=\"category tag\">%s</a></li>\n", esc(rel(p.path, "categories/"+p.cat.slug+"/index.html")), esc(p.cat.name))
			}
			for _, t := range p.tags {
				fmt.Fprintf(&m, "<li><a href=\"%s\" rel=\"tag\">%s</a></li>\n", esc(rel(p.path, "tags/"+t.slug+"/index.html")), esc(t.name))
			}
			m.WriteString("</ul>\n")
		}
		m.WriteString("</article>\n")
		if i > 0 || i < len(b.posts)-1 {
			m.WriteString("<nav class=\"post-nav\" aria-label=\"More posts\">\n")
			if i < len(b.posts)-1 { // older
				o := b.posts[i+1]
				fmt.Fprintf(&m, "<a class=\"prev\" rel=\"prev\" href=\"%s\">%s <span dir=\"auto\">%s</span></a>\n", esc(rel(p.path, o.path)), b.arrow(true), esc(cut(o.title, 60)))
			}
			if i > 0 { // newer
				n := b.posts[i-1]
				fmt.Fprintf(&m, "<a class=\"next\" rel=\"next\" href=\"%s\"><span dir=\"auto\">%s</span> %s</a>\n", esc(rel(p.path, n.path)), esc(cut(n.title, 60)), b.arrow(false))
			}
			m.WriteString("</nav>\n")
		}
		desc := p.excerpt
		if err := b.put(p.path, b.layout(pg{path: p.path, title: p.title, desc: desc, current: "", main: m.String()})); err != nil {
			return err
		}
	}
	for _, p := range b.pages {
		html, w := b.renderBody(p, p.path, false)
		p.images = w.imagesUsed
		b.imageWarnings(p, w)
		main := fmt.Sprintf("<article class=\"page\">\n<h1 dir=\"auto\">%s</h1>\n%s</article>\n", esc(p.title), html)
		if err := b.put(p.path, b.layout(pg{path: p.path, title: p.title, current: p.path, main: main})); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) imageWarnings(p *post, w *writer) {
	if w.missingAlt > 0 {
		b.warn("missing_alt", fmt.Sprintf("%d image(s) have no description (alt text), so a screen reader says nothing for them.", w.missingAlt), p)
	}
	if w.remote > 0 {
		b.warn("image_remote", fmt.Sprintf("%d image(s) link to a web address. A site never loads one, so a bracketed note stands in.", w.remote), p)
	}
	if w.missing > 0 {
		b.warn("image_missing", fmt.Sprintf("%d image(s) could not be published (not found among the project's attachments, too large, or not a PNG, JPEG, GIF or WebP) and are left out.", w.missing), p)
	}
	if w.rotated > 0 {
		b.warn("image_rotated", fmt.Sprintf("%d photo(s) said they were rotated (EXIF orientation); they were turned upright and saved again as JPEG, since the orientation note is removed from published pictures.", w.rotated), p)
	}
	if w.damaged > 0 {
		b.warn("image_skipped", fmt.Sprintf("%d image(s) could not be read cleanly and are left out.", w.damaged), p)
	}
}

// entryHTML is one post in a list.
func (b *builder) entryHTML(from string, p *post) string {
	var s strings.Builder
	s.WriteString("<article class=\"entry\">\n")
	fmt.Fprintf(&s, "<h2 dir=\"auto\"><a href=\"%s\">%s</a></h2>\n%s", esc(rel(from, p.path)), esc(p.title), b.metaLine(from, p))
	if p.excerpt != "" {
		fmt.Fprintf(&s, "<p class=\"excerpt\" dir=\"auto\">%s</p>\n", esc(p.excerpt))
	}
	s.WriteString("</article>\n")
	return s.String()
}

// paged writes a list of posts as pages of PostsPerPage, the first at
// pathOf(1). heading and intro (already escaped) sit above the first page; cur
// is the navigation item to mark.
func (b *builder) paged(posts []*post, pathOf func(n int) string, title, heading, intro, cur string, cover bool) error {
	per := b.o.PostsPerPage
	n := (len(posts) + per - 1) / per
	if n == 0 {
		n = 1
	}
	for i := 1; i <= n; i++ {
		path := pathOf(i)
		var m strings.Builder
		if heading != "" {
			fmt.Fprintf(&m, "<h1>%s</h1>\n", heading)
			if i == 1 {
				m.WriteString(intro)
			}
		} else {
			m.WriteString("<h1 class=\"sr\">Posts</h1>\n")
		}
		lo, hi := (i-1)*per, min(i*per, len(posts))
		for _, p := range posts[lo:hi] {
			m.WriteString(b.entryHTML(path, p))
		}
		if n > 1 {
			m.WriteString("<nav class=\"pager\" aria-label=\"Pages\">\n")
			if i > 1 {
				fmt.Fprintf(&m, "<a class=\"prev\" rel=\"prev\" href=\"%s\">%s <span dir=\"auto\">Newer posts</span></a>\n", esc(rel(path, pathOf(i-1))), b.arrow(true))
			}
			fmt.Fprintf(&m, "<span>Page %d of %d</span>\n", i, n)
			if i < n {
				fmt.Fprintf(&m, "<a class=\"next\" rel=\"next\" href=\"%s\"><span dir=\"auto\">Older posts</span> %s</a>\n", esc(rel(path, pathOf(i+1))), b.arrow(false))
			}
			m.WriteString("</nav>\n")
		}
		t := title
		if i > 1 {
			t = fmt.Sprintf("%s (page %d)", title, i)
		}
		if err := b.put(path, b.layout(pg{path: path, title: t, current: cur, main: m.String(), cover: cover && i == 1})); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) writeListings() error {
	return b.paged(b.posts, func(n int) string {
		if n == 1 {
			return "index.html"
		}
		return fmt.Sprintf("page/%d.html", n)
	}, "", "", "", "index.html", true)
}

func (b *builder) writeTaxonomy() error {
	for _, kind := range []struct {
		list   []*tag
		dir    string
		title  string
		single string
	}{{b.tags, "tags", "Tags", "Tag"}, {b.cats, "categories", "Categories", "Category"}} {
		if len(kind.list) == 0 {
			continue
		}
		idx := kind.dir + "/index.html"
		var m strings.Builder
		fmt.Fprintf(&m, "<h1>%s</h1>\n<ul class=\"taglist\">\n", kind.title)
		for _, t := range kind.list {
			fmt.Fprintf(&m, "<li><a href=\"%s\">%s</a> <span class=\"count\">(%d)</span></li>\n", esc(rel(idx, kind.dir+"/"+t.slug+"/index.html")), esc(t.name), len(t.posts))
		}
		m.WriteString("</ul>\n")
		if err := b.put(idx, b.layout(pg{path: idx, title: kind.title, current: idx, main: m.String()})); err != nil {
			return err
		}
		for _, t := range kind.list {
			dir := kind.dir + "/" + t.slug
			if err := b.paged(t.posts, func(n int) string {
				if n == 1 {
					return dir + "/index.html"
				}
				return fmt.Sprintf("%s/%d.html", dir, n)
			}, kind.single+": "+t.name, esc(kind.single+": "+t.name), "", idx, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxNavPages is how many pages the navigation lists before it says "All pages":
// every page is in the navigation of every page, so the output would grow as
// pages times files.
const maxNavPages = 6

// navPages are the pages in navigation order: the About page first.
func (b *builder) navPages() []*post {
	var out []*post
	if b.about != nil {
		out = append(out, b.about)
	}
	for _, p := range b.pages {
		if p != b.about {
			out = append(out, p)
		}
	}
	return out
}

// writePageIndex writes pages/index.html when the navigation cannot list every page.
func (b *builder) writePageIndex() error {
	nav := b.navPages()
	if len(nav) <= maxNavPages {
		return nil
	}
	idx := "pages/index.html"
	var m strings.Builder
	m.WriteString("<h1>All pages</h1>\n<ul class=\"taglist\">\n")
	for _, p := range nav {
		fmt.Fprintf(&m, "<li><a href=\"%s\" dir=\"auto\">%s</a></li>\n", esc(rel(idx, p.path)), esc(p.title))
	}
	m.WriteString("</ul>\n")
	return b.put(idx, b.layout(pg{path: idx, title: "All pages", current: idx, main: m.String()}))
}

func (b *builder) write404() error {
	main := "<h1>Not found</h1>\n<p>That page is not here. <a href=\"" + esc(b.rootHref()("index.html")) + "\">Back to the front page.</a></p>\n"
	return b.put("404.html", b.layout(pg{path: "404.html", href: b.rootHref(), title: "Not found", main: main}))
}
