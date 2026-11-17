package imports

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/crueber/scriobh/internal/domain/storydate"
)

// Ghost import: the JSON a Ghost site exports (Settings > Labs > Export, db.data;
// the shapes of the versions that wrote mobiledoc (1.x to 4.x), markdown (0.x),
// and lexical (5.x) are read, as they are documented and as the fixtures of the
// tests have them; no real export was available to open it with).
//
//	posts, pages (ghost_types)   chapters of a blog project, oldest first; pages in a part
//	                             called Pages after them
//	title, slug, published date  the title; slug and type as the source path; the date as
//	                             the story date; custom_excerpt as the synopsis
//	status                       published and sent are done; scheduled outline; draft draft.
//	                             Anything but published is imported only with ghost_status=all
//	                             and is marked do not compile
//	content                      lexical (5.x) or mobiledoc (a markdown card, or sections), read
//	                             natively; the html field is the fallback. A Markdown card is
//	                             parsed as Markdown (raw HTML in it is dropped) and read by the
//	                             same walker as HTML, so what it says is text, never a
//	                             Scríobh construct
//	tags, authors, visibility    counted and reported, not kept; user e-mail addresses are
//	                             never read
//
// Nothing is fetched: an image, a bookmark or an embed is a note with its address.

const (
	maxGhostPosts = 60_000
	maxLexNodes   = 2_000_000
	// maxGhostMarkdownCost is what the Markdown of one export may cost the parser, in
	// all: its bytes, plus mdPlaceCost for each place that had to be broken up.
	maxGhostMarkdownCost = 48 << 20
	mdPlaceCost          = 64 << 10
	// maxGhostMarkdownTime is the time the Markdown of one export may take.
	maxGhostMarkdownTime = 15 * time.Second
	maxLexDepth          = 64
)

type gflexID string

func (f *gflexID) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = gflexID(s)
		return nil
	}
	var n json.Number
	if json.Unmarshal(b, &n) == nil {
		*f = gflexID(n.String())
	}
	return nil
}

// gflexTime reads a date written as text or as milliseconds since 1970.
type gflexTime string

func (f *gflexTime) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = gflexTime(s)
		return nil
	}
	var n float64
	if json.Unmarshal(b, &n) == nil && n > 0 && n < 1e14 {
		*f = gflexTime(time.UnixMilli(int64(n)).UTC().Format(time.RFC3339))
	}
	return nil
}

func (f gflexTime) day() string {
	s := strings.TrimSpace(string(f))
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			// The date as the post writes it: with its own offset, the day it was there
			// (WordPress's post_date is local too); a time with no offset is UTC.
			if c, ok := storydate.Canonical(t.Format("2006-01-02")); ok {
				return c
			}
		}
	}
	return ""
}

type gflexBool bool

func (f *gflexBool) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		*f = gflexBool(v)
		return nil
	}
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*f = gflexBool(n != 0)
	}
	return nil
}

type ghostPost struct {
	ID            gflexID         `json:"id"`
	Title         string          `json:"title"`
	Slug          string          `json:"slug"`
	Type          string          `json:"type"`
	Page          gflexBool       `json:"page"`
	Status        string          `json:"status"`
	Visibility    string          `json:"visibility"`
	Markdown      string          `json:"markdown"`
	Mobiledoc     json.RawMessage `json:"mobiledoc"`
	Lexical       json.RawMessage `json:"lexical"`
	HTML          string          `json:"html"`
	CustomExcerpt string          `json:"custom_excerpt"`
	PublishedAt   gflexTime       `json:"published_at"`
	CreatedAt     gflexTime       `json:"created_at"`
}

type ghostTag struct {
	ID   gflexID `json:"id"`
	Name string  `json:"name"`
}

type ghostPostTag struct {
	PostID gflexID `json:"post_id"`
	TagID  gflexID `json:"tag_id"`
}

type ghostUser struct {
	ID   gflexID `json:"id"`
	Name string  `json:"name"`
}

type ghostPostAuthor struct {
	PostID   gflexID `json:"post_id"`
	AuthorID gflexID `json:"author_id"`
}

type ghostSetting struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type ghostData struct {
	Posts        []ghostPost
	Tags         []ghostTag
	PostsTags    []ghostPostTag
	Users        []ghostUser
	PostsAuthors []ghostPostAuthor
	Settings     []ghostSetting
}

// maxGhostAux is how many tags, post-tag pairs, users, post-author pairs and
// settings an export may hold.
const maxGhostAux = 200_000

// ghostLoader reads the export as a stream of JSON tokens, one list item at a time,
// counting as it goes: an export with a list of twelve million empty posts is refused
// at the sixty-thousandth, with nothing of the rest read into memory.
type ghostLoader struct {
	dec      *json.Decoder
	opt      convOpts
	d        ghostData
	version  string
	found    bool // a list of posts was seen
	typeErrs int  // fields of the wrong kind, read as empty
	skipped  int  // list items past a limit that was not an error
}

var errGhostShape = errors.New("that is not a Ghost export: the JSON is not shaped like one")

func loadGhost(data []byte, o convOpts) (*ghostLoader, error) {
	l := &ghostLoader{dec: json.NewDecoder(bytes.NewReader(data)), opt: o}
	if err := l.object(func(key string) error {
		switch key {
		case "db":
			return l.list(func() error {
				return l.object(func(k string) error {
					switch k {
					case "meta":
						return l.meta()
					case "data":
						return l.data()
					}
					return l.skip()
				})
			})
		case "data":
			return l.data()
		case "meta":
			return l.meta()
		}
		if ok, err := l.dataKey(key); ok || err != nil {
			return err
		}
		return l.skip()
	}); err != nil {
		return nil, err
	}
	return l, nil
}

// object reads an object, calling fn for each key; fn must read the value.
func (l *ghostLoader) object(fn func(key string) error) error {
	t, err := l.dec.Token()
	if err != nil {
		return fmt.Errorf("that is not a Ghost export: the file is not readable JSON (%s)", jsonErrShort(err))
	}
	d, ok := t.(json.Delim)
	if !ok || d != '{' {
		if ok { // an array: read past it
			if err := l.skipRest(); err != nil {
				return err
			}
		}
		return errGhostShape
	}
	for l.dec.More() {
		kt, err := l.dec.Token()
		if err != nil {
			return fmt.Errorf("that is not a Ghost export: the file is not readable JSON (%s)", jsonErrShort(err))
		}
		key, _ := kt.(string)
		if err := fn(key); err != nil {
			return err
		}
	}
	_, err = l.dec.Token()
	return jsonErr(err)
}

func jsonErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("that is not a Ghost export: the file is not readable JSON (%s)", jsonErrShort(err))
}

// list reads a list, calling fn at each item (which must read it). A value that
// is null, or not a list, is an empty list.
func (l *ghostLoader) list(fn func() error) error {
	t, err := l.dec.Token()
	if err != nil {
		return jsonErr(err)
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if d != '[' {
		return l.skipRest() // an object where a list was expected
	}
	for l.dec.More() {
		if err := fn(); err != nil {
			return err
		}
	}
	_, err = l.dec.Token()
	return jsonErr(err)
}

// skip reads one value and keeps nothing.
func (l *ghostLoader) skip() error {
	t, err := l.dec.Token()
	if err != nil {
		return jsonErr(err)
	}
	if _, ok := t.(json.Delim); ok {
		return l.skipRest()
	}
	return nil
}

// skipRest reads to the end of the array or object just opened.
func (l *ghostLoader) skipRest() error {
	for depth := 1; depth > 0; {
		t, err := l.dec.Token()
		if err != nil {
			return jsonErr(err)
		}
		if d, ok := t.(json.Delim); ok {
			if d == '[' || d == '{' {
				if depth++; depth > 100 {
					return errors.New("that is not a Ghost export: the JSON is nested too deeply")
				}
			} else {
				depth--
			}
		}
	}
	return nil
}

// decode reads one item into v; a field of the wrong kind is counted and left empty.
func (l *ghostLoader) decode(v any) error {
	err := l.dec.Decode(v)
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		l.typeErrs++
		return nil
	}
	return jsonErr(err)
}

func (l *ghostLoader) meta() error {
	var m struct {
		Version string `json:"version"`
	}
	if err := l.decode(&m); err != nil {
		return err
	}
	if l.version == "" {
		l.version = m.Version
	}
	return nil
}

func (l *ghostLoader) data() error {
	return l.object(func(k string) error {
		if ok, err := l.dataKey(k); ok || err != nil {
			return err
		}
		return l.skip()
	})
}

// dataKey reads one of the lists of an export if key names it.
func (l *ghostLoader) dataKey(key string) (bool, error) {
	over := func(what string) error {
		return fmt.Errorf("the export holds more than %d %s", maxGhostAux, what)
	}
	switch key {
	case "posts":
		l.found = true
		return true, l.list(func() error {
			if len(l.d.Posts) >= maxGhostPosts {
				return fmt.Errorf("the export holds more than %d posts", maxGhostPosts)
			}
			if len(l.d.Posts)%256 == 0 {
				if err := l.opt.stop(); err != nil {
					return err
				}
			}
			var p ghostPost
			if err := l.decode(&p); err != nil {
				return err
			}
			l.d.Posts = append(l.d.Posts, p)
			return nil
		})
	case "tags":
		return true, l.list(func() error {
			if len(l.d.Tags) >= maxGhostAux {
				return over("tags")
			}
			var v ghostTag
			err := l.decode(&v)
			l.d.Tags = append(l.d.Tags, v)
			return err
		})
	case "posts_tags":
		return true, l.list(func() error {
			if len(l.d.PostsTags) >= maxGhostAux {
				return over("post and tag pairs")
			}
			var v ghostPostTag
			err := l.decode(&v)
			l.d.PostsTags = append(l.d.PostsTags, v)
			return err
		})
	case "users":
		return true, l.list(func() error {
			if len(l.d.Users) >= maxGhostAux {
				return over("users")
			}
			var v ghostUser
			err := l.decode(&v)
			l.d.Users = append(l.d.Users, v)
			return err
		})
	case "posts_authors":
		return true, l.list(func() error {
			if len(l.d.PostsAuthors) >= maxGhostAux {
				return over("post and author pairs")
			}
			var v ghostPostAuthor
			err := l.decode(&v)
			l.d.PostsAuthors = append(l.d.PostsAuthors, v)
			return err
		})
	case "settings":
		return true, l.list(func() error {
			if len(l.d.Settings) >= maxGhostAux {
				l.skipped++
				return l.skip()
			}
			var v ghostSetting
			err := l.decode(&v)
			l.d.Settings = append(l.d.Settings, v)
			return err
		})
	}
	return false, nil
}

// ghostConvert reads a Ghost export.
func ghostConvert(files []File, name string, o convOpts) (res *cresult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, errors.New("the Ghost export could not be read")
		}
	}()
	data, fname, err := singleFile(files, ".json")
	if err != nil {
		return nil, err
	}
	res = &cresult{layout: LayoutGhost, meta: map[string]any{}}
	sum := sha256.Sum256(data)
	res.srcHash = hex.EncodeToString(sum[:])
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(data) >= 2 && (data[0] == 0xFF && data[1] == 0xFE || data[0] == 0xFE && data[1] == 0xFF) {
		d, _ := decodeXML(data) // a UTF-16 file with a mark
		data = d
	}
	if !utfValidJSON(data) {
		data = bytes.ToValidUTF8(data, []byte("\uFFFD"))
		res.warn("The file is not valid UTF-8; the bad bytes were replaced.")
	}
	ld, err := loadGhost(data, o)
	if err != nil {
		return nil, err
	}
	d, version := &ld.d, ld.version
	if !ld.found {
		return nil, errors.New("that is not a Ghost export: it has no db, data or posts")
	}
	if len(d.Posts) == 0 {
		return nil, errors.New("the export has no posts or pages")
	}
	res.warnN(ld.typeErrs, "%d field(s) in the file were not the kind of value Ghost writes (a number where there is a title, say) and were read as empty.", ld.typeErrs)
	res.warnN(ld.skipped, "%d setting(s) past the first %d were not read.", ld.skipped, maxGhostAux)
	if o.ghostStatus == "" {
		o.ghostStatus = "published"
	}
	if o.ghostTypes == "" {
		o.ghostTypes = "posts"
	}
	tagName := map[gflexID]string{}
	for _, t := range d.Tags {
		tagName[t.ID] = cleanText(t.Name, 100)
	}
	userName := map[gflexID]string{}
	for _, u := range d.Users {
		userName[u.ID] = cleanText(u.Name, 200)
	}
	postTags := map[gflexID][]string{}
	for _, pt := range d.PostsTags {
		if n := tagName[pt.TagID]; n != "" {
			postTags[pt.PostID] = append(postTags[pt.PostID], n)
		}
	}
	postAuthors := map[gflexID][]string{}
	for _, pa := range d.PostsAuthors {
		if n := userName[pa.AuthorID]; n != "" {
			postAuthors[pa.PostID] = append(postAuthors[pa.PostID], n)
		}
	}

	type sel struct {
		p    *ghostPost
		page bool
		date string
		ord  int
	}
	var posts, pages []sel
	notTaken, byStatus, byVis := newCounter(), newCounter(), newCounter()
	for i := range d.Posts {
		p := &d.Posts[i]
		page := p.Type == "page" || bool(p.Page) && p.Type == ""
		switch {
		case p.Type != "" && p.Type != "post" && p.Type != "page":
			notTaken.add("type " + p.Type)
			continue
		}
		status := p.Status
		if status == "" {
			status = "draft"
		}
		if status != "published" && !(o.ghostStatus == "all" && (status == "draft" || status == "scheduled" || status == "sent")) {
			notTaken.add("status " + status)
			continue
		}
		date := p.PublishedAt.day()
		if date == "" {
			date = p.CreatedAt.day()
		}
		s := sel{p: p, page: page, date: date, ord: i}
		switch {
		case page && o.ghostTypes != "posts":
			pages = append(pages, s)
		case !page && o.ghostTypes != "pages":
			posts = append(posts, s)
		default:
			notTaken.add("type " + map[bool]string{true: "page", false: "post"}[page] + " (not chosen)")
			continue
		}
		byStatus.add(status)
		if p.Visibility != "" {
			byVis.add(p.Visibility)
		}
	}
	less := func(l []sel) func(i, j int) bool {
		return func(i, j int) bool {
			if l[i].date != l[j].date {
				return l[i].date < l[j].date
			}
			return l[i].ord < l[j].ord
		}
	}
	sort.SliceStable(posts, less(posts))
	sort.SliceStable(pages, less(pages))

	c := &ghostConv{res: res, mdBudget: maxGhostMarkdownTime, sk: newSink(), hs: &htmlStats{}, tags: newCounter(), authors: map[string]bool{}, unknown: newCounter(), md: ghostMarkdown()}
	res.pics = newPicBox(o)
	if ep := newExportPics(files, res.pics); ep != nil {
		c.hs.pic = ep.resolve
	}
	mk := func(s sel) *cdoc {
		p := s.p
		title := cleanText(p.Title, 300)
		if title == "" {
			title = cleanText(strings.ReplaceAll(p.Slug, "-", " "), 300)
		}
		if title == "" {
			title = "Untitled"
		}
		doc := &cdoc{kind: "chapter", title: title, storyDate: s.date, exclude: p.Status != "published" && p.Status != ""}
		switch p.Status {
		case "published", "sent":
			doc.status = "done"
		case "scheduled":
			doc.status = "outline"
		default:
			doc.status = "draft"
		}
		doc.source = cleanText("ghost/"+map[bool]string{true: "page", false: "post"}[s.page]+"/"+cleanText(p.Slug, 100), maxSourcePath)
		doc.synopsis, doc.synopsisLong = synopsisOf(p.CustomExcerpt)
		for _, t := range postTags[p.ID] {
			c.tags.add(t)
		}
		for _, a := range postAuthors[p.ID] {
			if len(c.authors) < 100 {
				c.authors[a] = true
			}
		}
		doc.body = c.body(p)
		return doc
	}
	for _, s := range posts {
		if err := o.stop(); err != nil {
			return nil, err
		}
		res.docs = append(res.docs, mk(s))
		if c.err != nil {
			return nil, c.err
		}
	}
	if len(pages) > 0 {
		part := &cdoc{kind: "part", title: "Pages", status: "draft", source: "ghost/pages"}
		for _, s := range pages {
			if err := o.stop(); err != nil {
				return nil, err
			}
			part.kids = append(part.kids, mk(s))
			if c.err != nil {
				return nil, c.err
			}
		}
		res.docs = append(res.docs, part)
	}

	site := ""
	for _, s := range d.Settings {
		if s.Key == "title" {
			var v string
			if json.Unmarshal(s.Value, &v) == nil {
				site = cleanText(v, 200)
			}
		}
	}
	if site == "" {
		site = cleanText(strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".json"), 200)
	}
	author := ""
	if len(c.authors) == 1 {
		for a := range c.authors {
			author = cleanText(a, 200)
		}
	}
	res.project = cproject{title: site, kind: "blog", author: author}
	rep := map[string]any{
		"file": shown(fname), "ghost_version": cleanText(version, 20), "posts_in_file": len(d.Posts), "imported_posts": len(posts), "imported_pages": len(pages),
		"not_imported": notTaken.m, "statuses": byStatus.m, "visibility": byVis.m, "tags": c.tags.m, "authors": len(c.authors),
		"sources": c.sources, "unknown_nodes": c.unknown.m, "images": c.hs.images + c.images, "image_addresses": c.hs.imageSrc, "pictures_from_zip": c.hs.placed,
		"embeds_noted": c.embeds, "scripts_dropped": c.hs.scripts, "iframes_dropped": c.hs.iframes, "forms_dropped": c.hs.forms,
		"links": c.hs.links, "links_not_kept": c.hs.badLinks, "markdown_raw_html_dropped": c.rawHTML, "markdown_broken_up": c.bounded, "markdown_not_read": c.mdSkipped, "footnotes": c.hs.notes,
	}
	res.meta["ghost"] = rep
	res.warnN(len(notTaken.m), "Not imported: %s.", topList(notTaken.m))
	res.warnN(byStatus.m["draft"]+byStatus.m["scheduled"]+byStatus.m["sent"], "Drafts, scheduled and sent posts were imported and marked do not compile.")
	res.warnN(c.hs.images+c.images, "%d image(s) were not imported; each is marked in the text with its description.", c.hs.images+c.images)
	res.warnN(c.embeds, "%d embed(s), bookmark(s) or video(s) became a note with their address; nothing was fetched.", c.embeds)
	res.warnN(len(c.unknown.m), "Cards this reader does not know were left out: %s.", topList(c.unknown.m))
	res.warnN(c.hs.scripts+c.hs.iframes+c.hs.forms+c.hs.embeds+c.hs.media, "Left out: %d script(s), %d iframe(s), %d form(s), %d embed(s), %d audio or video element(s).", c.hs.scripts, c.hs.iframes, c.hs.forms, c.hs.embeds, c.hs.media)
	res.warnN(c.hs.badLinks, "%d link(s) to something other than a web or e-mail address kept their text only.", c.hs.badLinks)
	res.warnN(c.rawHTML, "%d piece(s) of raw HTML in Markdown cards were dropped.", c.rawHTML)
	res.warnN(c.bounded, "%d place(s) in Markdown were broken up: a paragraph of more than 8 KB, more than 16 quote or list markers on a line, or more than 64 columns of indent.", c.bounded)
	res.warnN(len(c.tags.m), "Tags (%s) and authors (%d) are not kept; they are listed in the import's record. E-mail addresses were not read.", c.tags.size(), len(c.authors))
	res.warnN(c.mdSkipped, "%d post(s) of Markdown were left empty: the Markdown of this export is more than it may cost to read (very long lines, deep quotes or lists), so the rest was not read.", c.mdSkipped)
	res.warnN(c.empty, "%d post(s) had no readable text.", c.empty)
	return res, nil
}

func topList(m map[string]int) string {
	var s []string
	for _, k := range sortedKeys(m) {
		s = append(s, fmt.Sprintf("%s (%d)", shown(k), m[k]))
	}
	return strings.Join(s[:min(len(s), 8)], ", ")
}

func utfValidJSON(b []byte) bool { return bytes.IndexByte(b, 0) < 0 && utf8.Valid(b) }

func jsonErrShort(err error) string { return cleanText(err.Error(), 120) }

type ghostConv struct {
	res       *cresult
	sk        *sink
	hs        *htmlStats
	md        goldmark.Markdown
	tags      *counter
	authors   map[string]bool
	unknown   *counter
	sources   map[string]int
	embeds    int
	images    int
	rawHTML   int
	mdCost    int           // what the Markdown given to the parser has cost so far
	mdSkipped int           // posts whose Markdown was not read: over the budget
	mdTime    time.Duration // time spent on it
	mdBudget  time.Duration
	bounded   int // places where pathological Markdown was broken up
	empty     int
	err       error
	nodes     int
}

func (c *ghostConv) unk(kind string) { c.unknown.add(kind) }

func (c *ghostConv) source(s string) {
	if c.sources == nil {
		c.sources = map[string]int{}
	}
	c.sources[s]++
}

// body converts a post: lexical, else mobiledoc, else markdown, else html.
func (c *ghostConv) body(p *ghostPost) string {
	done := false
	if raw := jsonString(p.Lexical); raw != "" {
		var doc map[string]any
		if json.Unmarshal([]byte(raw), &doc) == nil {
			if root, ok := doc["root"].(map[string]any); ok {
				c.nodes = 0
				c.lexBlocks(root, 0)
				done = !c.sk.empty()
				if done {
					c.source("lexical")
				}
			}
		}
	}
	if !done {
		if raw := jsonString(p.Mobiledoc); raw != "" {
			var m struct {
				Cards    [][]any `json:"cards"`
				Sections [][]any `json:"sections"`
				Markups  [][]any `json:"markups"`
			}
			if json.Unmarshal([]byte(raw), &m) == nil {
				if md, only := mobiledocMarkdown(m.Cards, m.Sections); only && md != "" {
					c.markdown(md)
					done = !c.sk.empty()
					if done {
						c.source("mobiledoc markdown card")
					}
				}
			}
		}
	}
	if !done && p.Markdown != "" {
		c.markdown(p.Markdown)
		done = !c.sk.empty()
		if done {
			c.source("markdown")
		}
	}
	if !done && p.HTML != "" {
		if len(p.HTML) > 16<<20 {
			c.hs.tooBig++
		} else if err := htmlToBlocks(c.sk, p.HTML, htmlOpts{}, c.hs); err != nil {
			c.sk.take()
			if strings.Contains(err.Error(), "limit") {
				c.err = fmt.Errorf("the posts are larger than the %d MB import limit", MaxTotalBytes>>20)
			}
		} else {
			c.source("html")
		}
	}
	text := c.sk.take()
	if strings.TrimSpace(text) == "" {
		c.empty++
	}
	return text
}

// jsonString reads a field that is a JSON document written as a string (Ghost's
// way) or as an object.
func jsonString(r json.RawMessage) string {
	r = bytes.TrimSpace(r)
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	if r[0] == '"' {
		var s string
		if json.Unmarshal(r, &s) == nil {
			return s
		}
		return ""
	}
	return string(r)
}

// markdown reads Markdown from a foreign source: it is parsed, rendered to HTML
// without raw HTML, and read by the same walker as any HTML, so nothing in it
// can be a Scríobh construct it was not written as.
func (c *ghostConv) markdown(md string) {
	if len(md) > MaxFileBytes { // a document this large is not imported anyway
		c.hs.tooBig++
		return
	}
	// What the parser is given, in all, is bounded by a cost, not only by size: the
	// places mdbound had to break up cost what a long unfinished link costs, so a
	// post of those is dear and fourteen of them are refused after the first, with a
	// clear count. A time budget for the Markdown of the whole export backs it up.
	md, n := boundMarkdown(md)
	if cost := len(md) + n*mdPlaceCost; c.mdCost+cost > maxGhostMarkdownCost || c.mdTime > c.mdBudget {
		c.mdSkipped++
		return
	} else {
		c.mdCost += cost
	}
	c.bounded += n
	start := time.Now()
	defer func() { c.mdTime += time.Since(start) }()
	var buf bytes.Buffer
	if err := c.md.Convert([]byte(md), &buf); err != nil {
		return
	}
	out := buf.String()
	c.rawHTML += strings.Count(out, "<!-- raw HTML omitted -->")
	if err := htmlToBlocks(c.sk, out, htmlOpts{}, c.hs); err != nil && strings.Contains(err.Error(), "limit") {
		c.err = fmt.Errorf("the posts are larger than the %d MB import limit", MaxTotalBytes>>20)
	}
}

// mobiledocMarkdown returns the Markdown of a mobiledoc that holds only Markdown
// cards (what Ghost wrote before it had its own editor), in order.
func mobiledocMarkdown(cards [][]any, sections [][]any) (string, bool) {
	var parts []string
	for _, s := range sections {
		if len(s) < 2 {
			return "", false
		}
		kind, _ := s[0].(float64)
		if kind != 10 {
			// a paragraph with no text is nothing; anything else is not a Markdown-only post
			if kind == 1 && len(s) >= 3 {
				if ms, ok := s[2].([]any); ok && len(ms) == 0 {
					continue
				}
			}
			return "", false
		}
		idx, _ := s[1].(float64)
		if int(idx) < 0 || int(idx) >= len(cards) || len(cards[int(idx)]) < 2 {
			return "", false
		}
		name, _ := cards[int(idx)][0].(string)
		payload, _ := cards[int(idx)][1].(map[string]any)
		if name != "markdown" {
			return "", false
		}
		md, _ := payload["markdown"].(string)
		parts = append(parts, md)
	}
	return strings.Join(parts, "\n\n"), len(parts) > 0
}

// ---- lexical ----------------------------------------------------------------------------------------------------

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func kids(m map[string]any) []any {
	k, _ := m["children"].([]any)
	return k
}

// lexBlocks writes the block children of a lexical node.
func (c *ghostConv) lexBlocks(n map[string]any, depth int) {
	for _, ch := range kids(n) {
		m, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		if c.nodes++; c.nodes > maxLexNodes || depth > maxLexDepth {
			c.hs.deep++
			return
		}
		if c.err != nil || c.sk.overflow() || c.sk.size() > 2*MaxTotalBytes {
			c.err = fmt.Errorf("the posts are larger than the %d MB import limit", MaxTotalBytes>>20)
			return
		}
		switch t := str(m, "type"); t {
		case "paragraph":
			c.sk.para(c.lexInline(m, depth+1))
		case "heading", "extended-heading":
			level := 2
			if tag := str(m, "tag"); len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
				level = int(tag[1] - '0')
			}
			c.sk.heading(level, plainOf(c.lexInline(m, depth+1)))
		case "quote", "extended-quote", "aside":
			c.sk.quote(c.lexInline(m, depth+1))
		case "list":
			c.lexList(m, 0, depth+1, "g"+strconv.Itoa(c.nodes))
		case "horizontalrule":
			c.sk.scene()
		case "codeblock", "code":
			if code := str(m, "code"); strings.TrimSpace(code) != "" {
				c.sk.codeLines(code)
			}
		case "image":
			c.picture(m)
			if cap := str(m, "caption"); cap != "" {
				s := newSinkSegs(c, cap)
				c.sk.para(s)
			}
		case "html":
			if err := htmlToBlocks(c.sk, str(m, "html"), htmlOpts{}, c.hs); err != nil {
				c.err = err
			}
		case "markdown":
			c.markdown(str(m, "markdown"))
		case "callout":
			c.sk.para(htmlInline(c.sk, str(m, "calloutText"), htmlOpts{}, c.hs))
		case "toggle":
			c.sk.para([]seg{{text: cleanText(str(m, "heading"), 300), b: true}})
			if h := str(m, "content"); h != "" {
				if err := htmlToBlocks(c.sk, h, htmlOpts{}, c.hs); err != nil {
					c.err = err
				}
			}
		case "bookmark", "embed", "video", "audio", "file", "gallery", "button", "product", "header", "signup", "paywall", "email", "email-cta", "call-to-action", "transistor", "tk":
			c.card(t, m)
		case "linebreak", "tab":
		default:
			if _, isText := m["text"]; isText {
				c.sk.para(c.lexInline(map[string]any{"children": []any{m}}, depth+1))
			} else if len(kids(m)) > 0 {
				c.lexBlocks(m, depth+1)
			} else {
				c.unk(t)
			}
		}
	}
}

// card notes a card that is not text: its address is a note, never fetched.
func (c *ghostConv) card(kind string, m map[string]any) {
	switch kind {
	case "bookmark":
		c.embeds++
		u := str(m, "url")
		title := cleanText(str(m, "title"), 200)
		if t, ok := safeTarget(u); ok {
			if title == "" {
				title = t
			}
			c.hs.links++
			c.sk.para([]seg{{text: title, link: t}})
		} else if title != "" {
			c.sk.para([]seg{{text: title}})
		}
	case "embed", "video", "audio", "file":
		c.embeds++
		c.noteAddr(m, kind, "", firstText(str(m, "url"), str(m, "src")))
	case "gallery":
		imgs, _ := m["images"].([]any)
		for _, im := range imgs {
			if mm, ok := im.(map[string]any); ok {
				c.picture(mm)
			}
		}
	default:
		c.unk(kind)
	}
}

// picture is an image card: the picture, when its bytes came with the export, else a note of it.
func (c *ghostConv) picture(m map[string]any) {
	if c.hs.pic != nil {
		if sg, ok := c.hs.pic(str(m, "src"), cleanText(str(m, "alt"), 120)); ok {
			c.hs.placed++
			c.sk.para([]seg{sg})
			return
		}
	}
	c.images++
	c.noteAddr(m, "image", str(m, "alt"), str(m, "src"))
}

func (c *ghostConv) noteAddr(m map[string]any, kind, alt, addr string) {
	if addr != "" && len(c.hs.imageSrc) < 20 {
		c.hs.imageSrc = append(c.hs.imageSrc, cleanText(addr, 200))
	}
	label := "[" + kind + "]"
	if a := cleanText(alt, 120); a != "" {
		label = "[" + kind + ": " + a + "]"
	}
	c.sk.para([]seg{{text: label, i: true}})
}

func newSinkSegs(c *ghostConv, caption string) []seg {
	return htmlInline(c.sk, caption, htmlOpts{}, c.hs)
}

// lexList writes a list, its items and the lists inside them.
func (c *ghostConv) lexList(m map[string]any, level, depth int, id string) {
	if depth > maxLexDepth {
		return
	}
	ordered := str(m, "listType") == "number" || str(m, "tag") == "ol"
	n := 0
	if v, ok := m["start"].(float64); ok && v >= 1 && v < 1e6 {
		n = int(v) - 1
	}
	for _, it := range kids(m) {
		li, ok := it.(map[string]any)
		if !ok {
			continue
		}
		n++
		var inline []any
		var nested []map[string]any
		for _, ch := range kids(li) {
			if cm, ok := ch.(map[string]any); ok && str(cm, "type") == "list" {
				nested = append(nested, cm)
			} else {
				inline = append(inline, ch)
			}
		}
		c.sk.item(c.lexInline(map[string]any{"children": inline}, depth+1), level, ordered, n, id)
		for _, nl := range nested {
			c.lexList(nl, min(level+1, 8), depth+1, id)
		}
	}
}

// lexInline reads the inline children of a node as segments.
func (c *ghostConv) lexInline(n map[string]any, depth int) []seg {
	var out []seg
	var walk func(children []any, link string, d int)
	walk = func(children []any, link string, d int) {
		for _, ch := range children {
			m, ok := ch.(map[string]any)
			if !ok {
				continue
			}
			if c.nodes++; c.nodes > maxLexNodes || d > maxLexDepth {
				c.hs.deep++
				return
			}
			switch str(m, "type") {
			case "text", "extended-text", "code-highlight":
				f, _ := m["format"].(float64)
				fl := int(f)
				out = append(out, seg{text: str(m, "text"), b: fl&1 != 0, i: fl&2 != 0, s: fl&4 != 0, code: fl&16 != 0, link: link})
			case "linebreak":
				out = append(out, seg{brk: true})
			case "tab":
				out = append(out, seg{text: " "})
			case "link", "autolink":
				l := ""
				if t, ok := safeTarget(strings.TrimSpace(str(m, "url"))); ok {
					l = t
					c.hs.links++
				} else {
					c.hs.badLinks++
				}
				walk(kids(m), l, d+1)
			default:
				walk(kids(m), link, d+1)
			}
		}
	}
	walk(kids(n), "", depth)
	return out
}

// ghostMarkdown is the parser for the Markdown of an export: CommonMark with
// strikethrough and tables, and no raw HTML (goldmark leaves it out unless asked).
func ghostMarkdown() goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.Table))
}
