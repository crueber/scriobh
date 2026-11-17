package site

import (
	"fmt"
	"strings"
	"time"
)

// guid is a feed identifier that does not change when a post's title or slug
// does: a UUID-shaped hash of the project and document ids. It says nothing
// about either.
func guid(projectID, docID string) string {
	h := hashHex("scriobh:site:" + projectID + ":" + docID)
	b := []byte(h[:32])
	b[12] = '5'
	switch b[16] { // variant bits: 8, 9, a or b
	case '0', '4', '8', 'c':
		b[16] = '8'
	case '1', '5', '9', 'd':
		b[16] = '9'
	case '2', '6', 'a', 'e':
		b[16] = 'a'
	default:
		b[16] = 'b'
	}
	s := string(b)
	return "urn:uuid:" + s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

func (b *builder) feedPosts() []*post {
	n := min(b.o.FeedItems, len(b.posts))
	return b.posts[:n]
}

// feedUpdated is the date a feed says it was built: the newest post's. It is
// not the clock, so the same site is the same bytes.
func (b *builder) feedUpdated() time.Time {
	if len(b.posts) > 0 {
		return b.posts[0].date
	}
	return utc(b.in.Project.Updated)
}

func (b *builder) feedAuthor() string {
	if b.author != "" {
		return b.author
	}
	return b.title
}

func (b *builder) feedDesc() string {
	switch {
	case b.o.Tagline != "":
		return b.o.Tagline
	case strings.TrimSpace(b.in.Project.Subtitle) != "":
		return strings.TrimSpace(b.in.Project.Subtitle)
	}
	return b.title
}

func (b *builder) writeRSS() error {
	self := absURL(b.base, "feed.xml")
	var s strings.Builder
	s.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	s.WriteString("<rss version=\"2.0\" xmlns:atom=\"http://www.w3.org/2005/Atom\" xmlns:dc=\"http://purl.org/dc/elements/1.1/\" xmlns:content=\"http://purl.org/rss/1.0/modules/content/\">\n<channel>\n")
	fmt.Fprintf(&s, "<title>%s</title>\n<link>%s</link>\n<description>%s</description>\n<language>%s</language>\n", esc(b.title), esc(b.base.String()), esc(b.feedDesc()), esc(b.lang))
	fmt.Fprintf(&s, "<lastBuildDate>%s</lastBuildDate>\n<atom:link href=\"%s\" rel=\"self\" type=\"application/rss+xml\"/>\n", b.feedUpdated().Format(time.RFC1123Z), esc(self))
	for _, p := range b.feedPosts() {
		link := absURL(b.base, p.path)
		fmt.Fprintf(&s, "<item>\n<title>%s</title>\n<link>%s</link>\n<guid isPermaLink=\"false\">%s</guid>\n<pubDate>%s</pubDate>\n", esc(p.title), esc(link), guid(b.in.Project.ID, p.e.ID), p.date.Format(time.RFC1123Z))
		fmt.Fprintf(&s, "<dc:creator>%s</dc:creator>\n", esc(b.feedAuthor()))
		if p.cat != nil {
			fmt.Fprintf(&s, "<category>%s</category>\n", esc(p.cat.name))
		}
		for _, t := range p.tags {
			fmt.Fprintf(&s, "<category>%s</category>\n", esc(t.name))
		}
		fmt.Fprintf(&s, "<description>%s</description>\n", esc(p.excerpt))
		if b.o.FeedContent == "full" {
			html, _ := b.renderBody(p, "", true)
			fmt.Fprintf(&s, "<content:encoded>%s</content:encoded>\n", esc(html))
		}
		s.WriteString("</item>\n")
	}
	s.WriteString("</channel>\n</rss>\n")
	return b.put("feed.xml", []byte(s.String()))
}

func (b *builder) writeAtom() error {
	self := absURL(b.base, "atom.xml")
	var s strings.Builder
	s.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&s, "<feed xmlns=\"http://www.w3.org/2005/Atom\" xml:lang=\"%s\">\n", esc(b.lang))
	fmt.Fprintf(&s, "<id>%s</id>\n<title>%s</title>\n<subtitle>%s</subtitle>\n<updated>%s</updated>\n", guid(b.in.Project.ID, "site"), esc(b.title), esc(b.feedDesc()), b.feedUpdated().Format(time.RFC3339))
	fmt.Fprintf(&s, "<link rel=\"self\" type=\"application/atom+xml\" href=\"%s\"/>\n<link rel=\"alternate\" type=\"text/html\" href=\"%s\"/>\n", esc(self), esc(b.base.String()))
	fmt.Fprintf(&s, "<author><name>%s</name></author>\n", esc(b.feedAuthor()))
	for _, p := range b.feedPosts() {
		link := absURL(b.base, p.path)
		fmt.Fprintf(&s, "<entry>\n<id>%s</id>\n<title>%s</title>\n<link rel=\"alternate\" type=\"text/html\" href=\"%s\"/>\n<published>%s</published>\n<updated>%s</updated>\n",
			guid(b.in.Project.ID, p.e.ID), esc(p.title), esc(link), p.date.Format(time.RFC3339), p.date.Format(time.RFC3339))
		if p.cat != nil {
			fmt.Fprintf(&s, "<category term=\"%s\"/>\n", esc(p.cat.name))
		}
		for _, t := range p.tags {
			fmt.Fprintf(&s, "<category term=\"%s\"/>\n", esc(t.name))
		}
		fmt.Fprintf(&s, "<summary>%s</summary>\n", esc(p.excerpt))
		if b.o.FeedContent == "full" {
			html, _ := b.renderBody(p, "", true)
			fmt.Fprintf(&s, "<content type=\"html\">%s</content>\n", esc(html))
		}
		s.WriteString("</entry>\n")
	}
	s.WriteString("</feed>\n")
	return b.put("atom.xml", []byte(s.String()))
}

func (b *builder) writeSitemap() error {
	var s strings.Builder
	s.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	url := func(path, lastmod string) {
		loc := absURL(b.base, path)
		if path == "index.html" {
			loc = b.base.String()
		}
		fmt.Fprintf(&s, "<url><loc>%s</loc>", esc(loc))
		if lastmod != "" {
			fmt.Fprintf(&s, "<lastmod>%s</lastmod>", lastmod)
		}
		s.WriteString("</url>\n")
	}
	last := ""
	if len(b.posts) > 0 {
		last = b.posts[0].date.Format("2006-01-02")
	}
	url("index.html", last)
	for _, p := range b.posts {
		url(p.path, p.date.Format("2006-01-02"))
	}
	for _, p := range b.pages {
		url(p.path, "")
	}
	if len(b.navPages()) > maxNavPages {
		url("pages/index.html", "")
	}
	if len(b.tags) > 0 {
		url("tags/index.html", "")
		for _, t := range b.tags {
			url("tags/"+t.slug+"/index.html", "")
		}
	}
	if len(b.cats) > 0 {
		url("categories/index.html", "")
		for _, t := range b.cats {
			url("categories/"+t.slug+"/index.html", "")
		}
	}
	s.WriteString("</urlset>\n")
	return b.put("sitemap.xml", []byte(s.String()))
}

// writeMeta writes the files that are not pages: the feeds and the sitemap
// (when there is a base address to make them absolute with) and robots.txt.
func (b *builder) writeMeta() error {
	var robots strings.Builder
	robots.WriteString("User-agent: *\n")
	if b.o.NoIndex {
		robots.WriteString("Disallow: /\n")
	} else {
		robots.WriteString("Allow: /\n")
	}
	if b.base != nil {
		if err := b.writeRSS(); err != nil {
			return err
		}
		if err := b.writeAtom(); err != nil {
			return err
		}
		if !b.o.NoIndex {
			if err := b.writeSitemap(); err != nil {
				return err
			}
			fmt.Fprintf(&robots, "\nSitemap: %s\n", absURL(b.base, "sitemap.xml"))
		}
	}
	return b.put("robots.txt", []byte(robots.String()))
}
