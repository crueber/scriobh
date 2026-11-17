package site

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/text"
)

// Limits. A site is built in memory, so it is bounded.
const (
	MaxPosts     = 5_000
	MaxEntries   = 5_200
	MaxSiteBytes = 256 << 20
	maxTagsPost  = 10
	maxTagRunes  = 40
)

// Errors Build returns.
var (
	ErrNoPosts  = errors.New("site: there is nothing to publish: no post is selected")
	ErrTooMany  = errors.New("site: too many documents to publish")
	ErrTooLarge = errors.New("site: the site is too large")
)

type tag struct {
	name, key, slug string
	posts           []*post
}

type post struct {
	e          Entry
	idx        int
	b          body
	title      string
	slug       string
	slugFrom   string
	path       string
	date       time.Time
	dateFrom   string
	excerpt    string
	excerptSrc string
	minutes    int
	tags       []*tag
	cat        *tag
	images     int
}

// builder holds a build in progress.
type builder struct {
	in      Input
	o       Options
	title   string
	lang    string
	dir     string
	author  string
	base    *url.URL
	posts   []*post // newest first
	pages   []*post // reading order
	about   *post
	tags    []*tag // by name
	cats    []*tag
	imgs    *imageSet
	files   map[string][]byte
	bytes   int
	warns   []Warning
	hasFeed bool
}

func (b *builder) warn(code, msg string, p *post) {
	w := Warning{Code: code, Message: msg}
	if p != nil {
		w.DocumentID, w.Title = p.e.ID, p.title
	}
	b.warns = append(b.warns, w)
}

func (b *builder) put(path string, data []byte) error {
	if _, dup := b.files[path]; dup {
		return fmt.Errorf("site: two files at %s", path)
	}
	b.bytes += len(data)
	if b.bytes > MaxSiteBytes {
		return ErrTooLarge
	}
	b.files[path] = data
	return nil
}

func utc(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// Build assembles a site from its input. Same input, same files.
func Build(in Input) (*Result, error) {
	if len(in.Entries) > MaxEntries {
		return nil, ErrTooMany
	}
	o := in.Options
	if err := o.Validate(); err != nil {
		return nil, err
	}
	b := &builder{in: in, o: o, files: map[string][]byte{}}
	b.imgs = &imageSet{in: in.Images, done: map[string]*pubImage{}}
	b.title = o.Title
	if b.title == "" {
		b.title = publish.CleanLine(in.Project.Title)
	}
	if b.title == "" {
		b.title = "Untitled"
	}
	b.lang = o.Language
	if b.lang == "" {
		b.lang = in.Project.Language
	}
	if !validLang(b.lang) {
		b.lang = "en"
	}
	b.dir = o.Dir
	if b.dir == "" {
		b.dir = "ltr"
		if publish.IsRTL(b.lang) {
			b.dir = "rtl"
		}
	}
	b.author = publish.CleanLine(in.Project.Author)
	if o.BaseURL != "" {
		b.base, _ = ParseBase(o.BaseURL)
	}
	accentName := o.Accent
	if !AccentReadable(accentName) {
		b.warn("accent_replaced", "The accent "+accentName+" is not readable enough on the pages and was replaced by moss.", nil)
		accentName = "moss"
	}
	b.hasFeed = b.base != nil
	if b.base == nil {
		b.warn("no_base_url", "No base address is set, so no feed, sitemap or canonical link is written (a feed needs absolute links). Set base_url to where the site will be served.", nil)
	}

	if err := b.collect(); err != nil {
		return nil, err
	}
	if len(b.posts) == 0 {
		return nil, ErrNoPosts
	}
	if in.Resolve != nil {
		got := in.Resolve(b.imageDests())
		merged := make(map[string]Image, len(in.Images)+len(got))
		for k, v := range in.Images {
			merged[k] = v
		}
		for k, v := range got {
			merged[k] = v
		}
		b.imgs.in = merged
	}
	b.slugs()
	b.taxonomy()
	if o.AboutID != "" {
		for _, p := range b.pages {
			if p.e.ID == o.AboutID {
				b.about = p
			}
		}
		if b.about == nil {
			b.warn("about_missing", "The About page is not among the published pages, so no author link was made.", nil)
		}
	}
	if b.author == "" && o.ShowAuthor {
		b.warn("no_author", "The project has no author name, so posts show none and the feeds name the site instead.", nil)
	}

	css := stylesheet(accentName, o.ImageMaxWidth)
	if err := b.put("assets/site.css", css); err != nil {
		return nil, err
	}
	if o.ShowCover && len(in.CoverSVG) > 0 {
		if err := b.put("assets/cover.svg", in.CoverSVG); err != nil {
			return nil, err
		}
	}
	if err := b.writePages(); err != nil {
		return nil, err
	}
	if err := b.writeListings(); err != nil {
		return nil, err
	}
	if err := b.writeTaxonomy(); err != nil {
		return nil, err
	}
	if err := b.writePageIndex(); err != nil {
		return nil, err
	}
	if err := b.write404(); err != nil {
		return nil, err
	}
	if err := b.writeMeta(); err != nil {
		return nil, err
	}
	for _, dest := range b.imgs.order {
		p := b.imgs.done[dest]
		if p == nil {
			continue
		}
		if _, dup := b.files[p.path]; dup {
			continue // the same picture twice is one file
		}
		if err := b.put(p.path, p.data); err != nil {
			return nil, err
		}
	}
	return b.result(), nil
}

// collect parses the entries and splits them into posts and pages.
func (b *builder) collect() error {
	nPosts := 0
	for i, e := range b.in.Entries {
		p := &post{e: e, idx: i}
		p.title = publish.CleanLine(e.Title)
		p.b = parse(e)
		if len(p.b.nodes) == 0 {
			b.warn("empty_post", "A document with no text was left out.", p)
			continue
		}
		if p.title == "" {
			p.title = "Untitled"
			b.warn("untitled", "A document has no title, so it is called Untitled.", p)
		}
		if e.Role == RolePage {
			b.pages = append(b.pages, p)
			continue
		}
		nPosts++
		if nPosts > MaxPosts {
			b.warn("too_many_posts", fmt.Sprintf("Only the first %d posts are published.", MaxPosts), nil)
			nPosts--
			continue
		}
		p.date, p.dateFrom = utc(e.Published), "set"
		if e.Published.IsZero() {
			p.date, p.dateFrom = utc(e.Created), "created"
			if e.Created.IsZero() {
				p.date = utc(b.in.Project.Updated)
			}
			b.warn("no_date", "No publication date is set, so the date it was created is used.", p)
		}
		p.minutes = text.ReadingMinutes(p.b.words)
		if ex := publish.CleanLine(e.Excerpt); ex != "" {
			p.excerpt, p.excerptSrc = cut(ex, 300), "set"
		} else {
			p.excerpt, p.excerptSrc = excerptOf(p.b.nodes), "body"
			b.warn("no_excerpt", "No excerpt is set, so the first paragraph is used.", p)
		}
		b.posts = append(b.posts, p)
	}
	// Newest first; on the same moment the one later in the tree is newer.
	sort.SliceStable(b.posts, func(i, j int) bool {
		if !b.posts[i].date.Equal(b.posts[j].date) {
			return b.posts[i].date.After(b.posts[j].date)
		}
		return b.posts[i].idx > b.posts[j].idx
	})
	return nil
}

// slugs gives every entry an address. A slug the owner chose is honoured first;
// the rest are made from titles, oldest first, and a collision is settled by a
// suffix made from the document's id, so an older post keeps its address when a
// newer one is added.
func (b *builder) slugs() {
	for _, group := range []struct {
		list     []*post
		fallback string
		dir      string
	}{{b.posts, "post", "posts"}, {b.pages, "page", "pages"}} {
		u := newUniq()
		if group.dir == "pages" {
			u.used["index"] = true // pages/index.html lists the pages
		}
		for _, r := range b.in.Reserved {
			u.used[fold(r)] = true
		}
		rank := func(p *post) int { // explicit addresses first, then those already out, then new ones
			switch {
			case p.e.Slug != "":
				return 0
			case p.e.PinnedSlug != "":
				return 1
			}
			return 2
		}
		order := append([]*post(nil), group.list...)
		sort.SliceStable(order, func(i, j int) bool {
			if ri, rj := rank(order[i]), rank(order[j]); ri != rj {
				return ri < rj
			}
			if !order[i].date.Equal(order[j].date) {
				return order[i].date.Before(order[j].date)
			}
			return order[i].idx < order[j].idx
		})
		for _, p := range order {
			want, from := "", "title"
			if p.e.Slug != "" {
				want, from = Slugify(p.e.Slug), "set"
			}
			if want == "" && p.e.PinnedSlug != "" {
				want, from = Slugify(p.e.PinnedSlug), "pinned"
			}
			explicit := from == "set" && want != ""
			if want == "" {
				want, from = Slugify(p.title), "title"
			}
			slug, suffixed := u.take(want, p.e.ID, group.fallback+"-"+hash6(p.e.ID))
			p.slug, p.slugFrom = slug, from
			p.path = group.dir + "/" + slug + ".html"
			if suffixed {
				what := "the title"
				if explicit {
					what = "the address you chose"
				}
				b.warn("slug_suffixed", "Another document already has the address made from "+what+", so this one's ends in a code.", p)
			}
		}
	}
}

// taxonomy collects tags and categories.
func (b *builder) taxonomy() {
	tagBy := map[string]*tag{}
	catBy := map[string]*tag{}
	for _, p := range b.posts { // newest first: a tag's posts come out newest first
		seen := map[string]bool{}
		var tags []string
		dropped := false
		for _, raw := range p.e.Tags {
			t := publish.CleanLine(raw)
			if utf8.RuneCountInString(t) > maxTagRunes {
				t = string([]rune(t)[:maxTagRunes])
			}
			k := fold(t)
			if t == "" || seen[k] {
				continue
			}
			if len(tags) >= maxTagsPost {
				dropped = true
				continue
			}
			seen[k] = true
			tags = append(tags, t)
		}
		if dropped {
			b.warn("tags_trimmed", fmt.Sprintf("Only the first %d tags of a post are published.", maxTagsPost), p)
		}
		for _, t := range tags {
			k := fold(t)
			tg := tagBy[k]
			if tg == nil {
				tg = &tag{name: t, key: k}
				tagBy[k] = tg
			}
			tg.posts = append(tg.posts, p)
			p.tags = append(p.tags, tg)
		}
		if b.o.PartsAsCats && strings.TrimSpace(p.e.Category) != "" {
			c := publish.CleanLine(p.e.Category)
			k := fold(c)
			cg := catBy[k]
			if cg == nil {
				cg = &tag{name: c, key: k}
				catBy[k] = cg
			}
			cg.posts = append(cg.posts, p)
			p.cat = cg
		}
	}
	name := func(m map[string]*tag, fallback string) []*tag {
		u := newUniq()
		var out []*tag
		for _, k := range sortedKeys(m) {
			t := m[k]
			t.slug, _ = u.take(Slugify(t.name), t.key, fallback+"-"+hash6(t.key))
			out = append(out, t)
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].key < out[j].key })
		return out
	}
	b.tags = name(tagBy, "tag")
	b.cats = name(catBy, "category")
}

func (b *builder) result() *Result {
	paths := sortedKeys(b.files)
	r := &Result{Warnings: b.warns, Tags: len(b.tags), Cats: len(b.cats), Bytes: b.bytes, Modified: b.posts[0].date}
	r.InputHash, r.Docs = b.inputHash()
	h := sha256.New()
	for _, p := range paths {
		r.Files = append(r.Files, File{Path: p, Data: b.files[p]})
		sum := sha256.Sum256(b.files[p])
		fmt.Fprintf(h, "%s\x00%x\n", p, sum)
	}
	r.Hash = hex.EncodeToString(h.Sum(nil))
	for _, p := range b.posts {
		r.Posts = append(r.Posts, b.info(p))
	}
	for _, p := range b.pages {
		r.Posts = append(r.Posts, b.info(p))
	}
	return r
}

func (b *builder) info(p *post) PostInfo {
	pi := PostInfo{DocumentID: p.e.ID, Title: p.title, Role: p.e.Role, Slug: p.slug, SlugFrom: p.slugFrom, Path: p.path, Words: p.b.words, Images: p.images}
	if p.e.Role == RolePage {
		return pi
	}
	pi.Date, pi.DateFrom = p.date.Format("2006-01-02"), p.dateFrom
	pi.Minutes, pi.Excerpt, pi.ExcerptSrc, pi.Category = p.minutes, p.excerpt, p.excerptSrc, ""
	for _, t := range p.tags {
		pi.Tags = append(pi.Tags, t.name)
	}
	if p.cat != nil {
		pi.Category = p.cat.name
	}
	return pi
}

// imageDests lists the destination of every image the published entries embed.
func (b *builder) imageDests() []string {
	seen := map[string]bool{}
	var walkI func([]text.Inline)
	walkI = func(in []text.Inline) {
		for _, x := range in {
			if x.Kind == text.IImage {
				seen[x.Text] = true
			}
			walkI(x.Kids)
		}
	}
	var walk func([]text.Node)
	walk = func(ns []text.Node) {
		for _, n := range ns {
			walkI(n.Inl)
			walk(n.Kids)
			for _, it := range n.Items {
				walk(it)
			}
			for _, r := range n.Rows {
				for _, c := range r.Cells {
					walkI(c.Inl)
				}
			}
		}
	}
	for _, list := range [][]*post{b.posts, b.pages} {
		for _, p := range list {
			walk(p.b.nodes)
			for _, f := range p.b.notes {
				walk(f.Nodes)
			}
		}
	}
	return sortedKeys(seen)
}

// inputHash hashes what the site is made from, and each document's published
// text.
func (b *builder) inputHash() (string, []DocHash) {
	h := sha256.New()
	oj, _ := json.Marshal(b.o)
	p := b.in.Project
	fmt.Fprintf(h, "scriobh-site/1\x00%s\x00%s\x00%s\x00%s\x00%s\n", oj, p.Title, p.Subtitle, p.Author, p.Language)
	var docs []DocHash
	for _, list := range [][]*post{b.posts, b.pages} {
		for _, e := range list {
			th := sha256.New()
			th.Write([]byte(published(e.e.Content)))
			for _, s := range e.e.Scenes {
				th.Write([]byte{0})
				th.Write([]byte(published(s)))
			}
			dh := hex.EncodeToString(th.Sum(nil))
			docs = append(docs, DocHash{ID: e.e.ID, SHA256: dh})
			pub := ""
			if !e.e.Published.IsZero() {
				pub = utc(e.e.Published).Format(time.RFC3339)
			} else if e.e.Role == RolePost {
				pub = "created:" + utc(e.e.Created).Format(time.RFC3339)
			}
			fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\n", e.e.ID, e.e.Role, e.title, e.slug, pub, e.e.Excerpt, strings.Join(e.e.Tags, "\x01"), e.e.Category, dh)
		}
	}
	for _, k := range sortedKeys(b.imgs.in) {
		im := b.imgs.in[k]
		sum := sha256.Sum256(im.Data)
		fmt.Fprintf(h, "image\x00%s\x00%x\x00%s\x00%d\x00%d\n", k, sum, im.Ext, im.Width, im.Height)
	}
	if b.o.ShowCover {
		fmt.Fprintf(h, "cover\x00%x\n", sha256.Sum256(b.in.CoverSVG))
	}
	return hex.EncodeToString(h.Sum(nil)), docs
}
