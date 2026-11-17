package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/crueber/scriobh/internal/domain/storydate"
)

// WordPress import: a WXR export (the file Tools > Export writes), schema 1.0 to
// 1.2 as it is documented and as the fixture of the tests has it; no real
// export was available to open it with.
//
//	posts (and pages with wp_types)  chapters of a blog project, oldest first; pages in a part
//	                                 called Pages after them
//	title, slug, date, excerpt       the title; the slug and the type are the document's source
//	                                 path; the date is the document's story date (YYYY-MM-DD);
//	                                 the excerpt is its synopsis
//	status                           publish is done; future outline; pending revise; draft draft;
//	                                 private done. Anything but publish is imported only with
//	                                 wp_status=all, and is marked do not compile
//	content                          HTML or Gutenberg blocks, read by the walker of htmlmd.go
//	categories, tags, custom fields  counted and reported, not kept
//	comments, attachments, menus     counted and reported; attachments by address, never fetched
//
// Nothing in the export is ever opened as an address: an image is a note, a link
// is a link to where it points and nothing is requested. Author e-mail addresses
// and comment authors are never read.

const (
	maxWPItems     = 60_000
	wpXMLTokens    = 40_000_000
	wpPostMetaSize = 1 << 20
)

type wpItem struct {
	id, typ, status        string
	title, slug, date      string
	author                 string
	excerpt, content       string
	parent, order          int
	cats, tags             []string
	metaKeys               []string
	footnotes              string // the post's footnotes metadata, JSON
	attachmentURL          string
	sourceOrder            int
	published, unpublished bool
}

// wordpressConvert reads a WordPress export.
func wordpressConvert(files []File, name string, o convOpts) (res *cresult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, errors.New("the WordPress export could not be read")
		}
	}()
	data, fname, err := singleFile(files, ".xml")
	if err != nil {
		return nil, err
	}
	res = &cresult{layout: LayoutWordPress, meta: map[string]any{}}
	b, notes := decodeXML(data)
	res.warnings = append(res.warnings, notes...)
	sum := sha256.Sum256(data)
	res.srcHash = hex.EncodeToString(sum[:])
	if !strings.Contains(string(b[:min(len(b), 200_000)]), "<rss") {
		return nil, errors.New("that is not a WordPress export: it is not an RSS file")
	}
	if o.wpStatus == "" {
		o.wpStatus = "published"
	}
	if o.wpTypes == "" {
		o.wpTypes = "posts"
	}
	// The channel: site title, language, version, authors. Read first and cheaply.
	site := wpChannel{authors: map[string]string{}}
	if err := site.read(b); err != nil {
		return nil, err
	}
	var items []*wpItem
	seenTypes := newCounter()
	skipped, err := streamXML(b, "item", xmlLimits{tokens: wpXMLTokens, text: 256 << 20}, []string{"wp:comment"}, wpItemKids, func(n *xnode) error {
		if len(items) >= maxWPItems {
			return errors.New("the export holds more than " + strconv.Itoa(maxWPItems) + " items")
		}
		if len(items)%256 == 0 {
			if err := o.stop(); err != nil {
				return err
			}
		}
		it := wpRead(n)
		it.sourceOrder = len(items)
		seenTypes.add(it.typ)
		items = append(items, it)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("the export has no posts or pages")
	}

	// Which items.
	var posts, pages []*wpItem
	notTaken := newCounter()
	attach := 0
	var attachURLs []string
	for _, it := range items {
		switch it.typ {
		case "attachment":
			attach++
			if it.attachmentURL != "" && len(attachURLs) < 20 {
				attachURLs = append(attachURLs, cleanText(it.attachmentURL, 300))
			}
			continue
		case "post":
		case "page":
		default:
			notTaken.add("type " + it.typ)
			continue
		}
		pub := it.status == "publish"
		switch {
		case pub:
		case o.wpStatus == "all" && (it.status == "draft" || it.status == "pending" || it.status == "future" || it.status == "private"):
		default:
			notTaken.add("status " + it.status)
			continue
		}
		if it.typ == "post" && o.wpTypes != "pages" {
			posts = append(posts, it)
		} else if it.typ == "page" && o.wpTypes != "posts" {
			pages = append(pages, it)
		} else {
			notTaken.add("type " + it.typ + " (not chosen)")
		}
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if posts[i].date != posts[j].date {
			return posts[i].date < posts[j].date
		}
		return posts[i].sourceOrder < posts[j].sourceOrder
	})
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].order != pages[j].order {
			return pages[i].order < pages[j].order
		}
		if pages[i].date != pages[j].date {
			return pages[i].date < pages[j].date
		}
		return pages[i].sourceOrder < pages[j].sourceOrder
	})

	c := &wpConv{res: res, sk: newSink(), hs: &htmlStats{}, cats: newCounter(), tags: newCounter(), authors: map[string]bool{}, statuses: newCounter(), metaKeys: newCounter()}
	res.pics = newPicBox(o)
	if ep := newExportPics(files, res.pics); ep != nil {
		c.hs.pic = ep.resolve
	}
	for _, it := range posts {
		if err := o.stop(); err != nil {
			return nil, err
		}
		if d := c.doc(it, site); d != nil {
			res.docs = append(res.docs, d)
		}
		if c.err != nil {
			return nil, c.err
		}
	}
	if len(pages) > 0 {
		part := &cdoc{kind: "part", title: "Pages", status: "draft", source: "wordpress/pages"}
		for _, it := range pages {
			if err := o.stop(); err != nil {
				return nil, err
			}
			if d := c.doc(it, site); d != nil {
				part.kids = append(part.kids, d)
			}
			if c.err != nil {
				return nil, c.err
			}
		}
		res.docs = append(res.docs, part)
	}

	// The project.
	title := cleanText(html.UnescapeString(site.title), 200)
	if title == "" {
		title = cleanText(strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".xml"), 200)
	}
	author := ""
	for _, it := range items {
		for _, k := range it.metaKeys {
			if !strings.HasPrefix(k, "_") { // WordPress's own bookkeeping keys are not the writer's fields
				c.metaKeys.add(k)
			}
		}
	}
	if len(c.authors) == 1 {
		for a := range c.authors {
			author = cleanText(site.authors[a], 200)
			if author == "" {
				author = cleanText(a, 200)
			}
		}
	}
	lang := strings.TrimSpace(site.language)
	if len(lang) > 16 || strings.ContainsAny(lang, " <>\"'") {
		lang = ""
	}
	res.project = cproject{title: title, subtitle: cleanText(site.description, 200), kind: "blog", author: author, language: lang}

	// The report.
	rep := map[string]any{
		"file": shown(fname), "wxr_version": site.version, "items": len(items), "posts": len(posts), "pages": len(pages), "by_type": seenTypes.m,
		"not_imported": notTaken.m, "comments": skipped["wp:comment"], "attachments": attach, "attachment_urls": attachURLs,
		"categories": c.cats.m, "tags": c.tags.m, "authors": len(c.authors), "statuses": c.statuses.m,
		"custom_field_keys": c.metaKeys.m, "images": c.hs.images, "image_addresses": c.hs.imageSrc, "pictures_from_zip": c.hs.placed,
		"scripts_dropped": c.hs.scripts, "iframes_dropped": c.hs.iframes, "forms_dropped": c.hs.forms, "embeds_dropped": c.hs.embeds,
		"media_dropped": c.hs.media, "links": c.hs.links, "links_not_kept": c.hs.badLinks, "relative_links": c.hs.internalLinks,
		"tables": c.hs.tables, "footnotes": c.hs.notes, "shortcodes_removed": c.hs.shortcodes, "classic_editor_posts": c.autop,
	}
	res.meta["wordpress"] = rep
	res.warnN(c.statuses.total()-c.statuses.m["publish"], "Drafts, pending, scheduled and private posts were imported and marked do not compile.")
	res.warnN(len(notTaken.m), "Not imported: %s.", topList(notTaken.m))
	res.warnN(skipped["wp:comment"], "%d comment(s) were not imported: comments are reported, never kept. Commenters' names and addresses were not read.", skipped["wp:comment"])
	res.warnN(attach, "%d attachment(s) (media files) are listed in the export by address; none was fetched and none was imported. The first addresses are in the report.", attach)
	res.warnN(c.hs.images, "%d image(s) in the posts were not imported; each is marked in the text with its description.", c.hs.images)
	res.warnN(c.hs.scripts+c.hs.iframes+c.hs.forms+c.hs.embeds+c.hs.media, "Left out of the posts: %d script(s), %d iframe(s), %d form(s), %d embed(s), %d audio or video element(s).", c.hs.scripts, c.hs.iframes, c.hs.forms, c.hs.embeds, c.hs.media)
	res.warnN(c.hs.badLinks, "%d link(s) to something other than a web or e-mail address kept their text only.", c.hs.badLinks)
	res.warnN(c.hs.internalLinks, "%d link(s) to other pages of the site (relative or anchors) kept their text only.", c.hs.internalLinks)
	res.warnN(len(c.cats.m)+len(c.tags.m), "Categories (%s) and tags (%s) are not kept; they are listed in the import's record.", c.cats.size(), c.tags.size())
	res.warnN(len(c.metaKeys.m), "Custom fields (%s key(s)) are not kept; they are listed in the import's record.", c.metaKeys.size())
	res.warnN(skipped["(children)"]+skipped["(attributes)"], "%d element(s) or attribute(s) of the export that this reader does not use, or past what it keeps of one element, were counted and left out.", skipped["(children)"]+skipped["(attributes)"])
	res.warnN(c.hs.tooBig, "%d post(s) were too large to read in full.", c.hs.tooBig)
	return res, nil
}

// singleFile finds the one file with the extension in a listing.
func singleFile(files []File, ext string) ([]byte, string, error) {
	var found *File
	n := 0
	for i := range files {
		p, why := cleanPath(files[i].Path)
		if why != "" || hidden(p) || files[i].Size < 0 {
			continue
		}
		if strings.HasSuffix(strings.ToLower(p), ext) {
			if found == nil || len(files[i].bytesOf()) > len(found.bytesOf()) {
				found = &files[i]
			}
			n++
		}
	}
	if found == nil {
		return nil, "", errors.New("there is no " + ext + " file to read")
	}
	b := found.bytesOf()
	if len(b) == 0 {
		if found.Size > 0 {
			return nil, "", errors.New("the file was not sent, or is larger than the import limit")
		}
		return nil, "", errors.New("the file is empty")
	}
	return b, found.Path, nil
}

// The children of an item and of the channel that are read; the others are counted
// and thrown away unbuilt.
var (
	wpItemKids = map[string]bool{"title": true, "pubDate": true, "dc:creator": true, "content:encoded": true, "excerpt:encoded": true,
		"wp:post_id": true, "wp:post_date": true, "wp:post_name": true, "wp:status": true, "wp:post_parent": true, "wp:menu_order": true,
		"wp:post_type": true, "wp:attachment_url": true, "category": true, "wp:postmeta": true}
	wpChannelKids = map[string]bool{"title": true, "description": true, "language": true, "wp:wxr_version": true, "wp:author": true}
)

type wpChannel struct {
	title, description, language, version string
	authors                               map[string]string // login -> display name
}

// read takes the channel's own facts from the start of the file, and the author list.
func (ch *wpChannel) read(b []byte) error {
	// The channel facts come before the first item; read the document up to it with
	// the items thrown away.
	_, err := streamXML(b, "channel", xmlLimits{tokens: wpXMLTokens, text: 256 << 20}, []string{"item"}, wpChannelKids, func(n *xnode) error {
		ch.title = cleanText(n.childText("title"), 300)
		ch.description = cleanText(n.childText("description"), 300)
		ch.language = n.childText("language")
		ch.version = cleanText(n.childText("wp:wxr_version"), 10)
		n.each("wp:author", func(a *xnode) {
			login := cleanText(a.childText("wp:author_login"), 100)
			if login != "" {
				ch.authors[login] = cleanText(a.childText("wp:author_display_name"), 200)
			}
		})
		return nil
	})
	return err
}

func wpRead(n *xnode) *wpItem {
	it := &wpItem{
		id: n.childText("wp:post_id"), typ: n.childText("wp:post_type"), status: n.childText("wp:status"),
		title: n.childText("title"), slug: n.childText("wp:post_name"), author: n.childText("dc:creator"),
		content: n.rawText("content:encoded"), excerpt: n.rawText("excerpt:encoded"),
	}
	it.order, _ = strconv.Atoi(n.childText("wp:menu_order"))
	it.parent, _ = strconv.Atoi(n.childText("wp:post_parent"))
	date := n.childText("wp:post_date")
	if t, err := time.Parse("2006-01-02 15:04:05", date); err == nil {
		it.date = t.Format("2006-01-02")
	} else if t, err := time.Parse(time.RFC1123Z, n.childText("pubDate")); err == nil {
		it.date = t.UTC().Format("2006-01-02")
	}
	if c, ok := storydate.Canonical(it.date); ok {
		it.date = c
	} else {
		it.date = ""
	}
	it.attachmentURL = n.childText("wp:attachment_url")
	for _, k := range n.kids {
		switch k.name {
		case "category":
			t := cleanText(k.text, 100)
			switch k.attr("domain") {
			case "category":
				if len(it.cats) < 500 {
					it.cats = append(it.cats, t)
				}
			case "post_tag":
				if len(it.tags) < 500 {
					it.tags = append(it.tags, t)
				}
			}
		case "wp:postmeta":
			key := cleanText(k.childText("wp:meta_key"), 100)
			if key == "" {
				continue
			}
			if key == "footnotes" {
				if v := k.child("wp:meta_value"); v != nil && len(v.text) <= wpPostMetaSize {
					it.footnotes = v.text
				}
				continue
			}
			if len(it.metaKeys) < 500 {
				it.metaKeys = append(it.metaKeys, key)
			}
		}
	}
	return it
}

type wpConv struct {
	res      *cresult
	sk       *sink
	hs       *htmlStats
	cats     *counter
	tags     *counter
	authors  map[string]bool
	statuses *counter
	metaKeys *counter
	autop    int
	err      error
}

// doc converts one post or page.
func (c *wpConv) doc(it *wpItem, site wpChannel) *cdoc {
	title := cleanText(html.UnescapeString(it.title), 300)
	if title == "" {
		title = cleanText(html.UnescapeString(strings.ReplaceAll(it.slug, "-", " ")), 300)
	}
	if title == "" {
		title = "Untitled"
	}
	d := &cdoc{kind: "chapter", title: title, storyDate: it.date, exclude: it.status != "publish"}
	switch it.status {
	case "publish", "private":
		d.status = "done"
	case "pending":
		d.status = "revise"
	case "future":
		d.status = "outline"
	default:
		d.status = "draft"
	}
	c.statuses.add(it.status)
	slug := cleanText(it.slug, 100)
	d.source = cleanText("wordpress/"+it.typ+"/"+slug, maxSourcePath)
	if it.excerpt != "" {
		pic := c.hs.pic // a synopsis is one line of text: no picture is taken for it
		c.hs.pic = nil
		segs := htmlInline(c.sk, it.excerpt, htmlOpts{}, c.hs)
		c.hs.pic = pic
		d.synopsis, d.synopsisLong = synopsisOf(plainOf(segs))
	}
	for _, t := range it.cats {
		c.cats.add(t)
	}
	for _, t := range it.tags {
		c.tags.add(t)
	}
	if it.author != "" && len(c.authors) < 100 {
		c.authors[it.author] = true
	}
	d.body = c.body(it)
	return d
}

// body converts a post's content.
func (c *wpConv) body(it *wpItem) string {
	o := htmlOpts{}
	if it.footnotes != "" {
		var fn []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(it.footnotes), &fn) == nil {
			o.notes = map[string]string{}
			for i, f := range fn {
				if i >= 2000 {
					break
				}
				o.notes[f.ID] = f.Content
			}
		}
	}
	src := it.content
	if len(src) > 16<<20 {
		c.hs.tooBig++
		return ""
	}
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	src = takeNoteShortcodes(src, &o)
	if !strings.Contains(src, "<!-- wp:") {
		o.classic = true // the classic editor: blank lines are paragraphs
		c.autop++
	}
	if err := htmlToBlocks(c.sk, src, o, c.hs); err != nil {
		c.sk.take()
		if strings.Contains(err.Error(), "limit") {
			c.err = fmt.Errorf("the posts are larger than the %d MB import limit", MaxTotalBytes>>20)
		} else {
			c.res.warn("%s: the content could not be read and was left empty.", shown(it.title))
		}
		return ""
	}
	return c.sk.take()
}

func splitBlank(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '\n' {
			out = append(out, s[start:i])
			for j < len(s) && (s[j] == '\n' || s[j] == ' ' || s[j] == '\t') {
				j++
			}
			start, i = j, j-1
		}
	}
	return append(out, s[start:])
}
