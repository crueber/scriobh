package site

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/publish"
)

// NavLink is a fixed link in the site's navigation: an address outside the
// site. Pages of the site are listed automatically.
type NavLink struct {
	Label string `json:"label" desc:"1 to 40 characters."`
	URL   string `json:"url" desc:"An http or https address, up to 300 characters."`
}

// Options are a project's site options. Every field has a default, so the zero
// Options is not valid: use Defaults and Merge.
type Options struct {
	Title         string    `json:"title" desc:"The site's name; empty: the project's title."`
	Tagline       string    `json:"tagline" desc:"A line under the name, up to 200 characters."`
	BaseURL       string    `json:"base_url" desc:"Where the site will be served, for example https://example.org/blog/. Feeds, the sitemap and canonical links need it; without one they are not written."`
	Language      string    `json:"language" desc:"A language tag such as en or ga-IE; empty: the project's."`
	Dir           string    `json:"dir" enum:",ltr,rtl" desc:"Text direction; empty: from the language."`
	Posts         string    `json:"posts" enum:"chapters,top_level" desc:"chapters: every chapter is a post (its scenes are folded in); top_level: every top-level document is."`
	IncludeDrafts bool      `json:"include_drafts" desc:"Also publish documents whose status is draft or revise (default: only done)."`
	PartsAsCats   bool      `json:"parts_as_categories" desc:"A post in a part is in a category named after the part."`
	PostsPerPage  int       `json:"posts_per_page" desc:"1 to 100."`
	FeedContent   string    `json:"feed_content" enum:"full,excerpt"`
	FeedItems     int       `json:"feed_items" desc:"1 to 100."`
	ShowDates     bool      `json:"show_dates"`
	ShowAuthor    bool      `json:"show_author"`
	ShowReading   bool      `json:"show_reading_time"`
	FooterText    string    `json:"footer_text" desc:"Plain text in the footer, up to 300 characters."`
	AboutID       string    `json:"about_id" desc:"The id of a page document that is the About page: the author's name links to it and it comes first in the navigation."`
	Nav           []NavLink `json:"nav" desc:"Up to 8 fixed links to addresses outside the site."`
	Accent        string    `json:"accent" enum:"moss,slate,sepia,meadow,contrast" desc:"The link colour. Each is checked for readable contrast in the light and dark theme."`
	ImageMaxWidth int       `json:"image_max_width" desc:"The widest an image is shown, in pixels (200 to 2400); smaller screens always shrink it."`
	ShowCover     bool      `json:"show_cover" desc:"Show the project's generated cover on the front page."`
	NoIndex       bool      `json:"noindex" desc:"Ask search engines to stay away (a staging site): robots.txt disallows everything and every page says noindex."`
}

// Defaults are the options of a project that has set none.
func Defaults() Options {
	return Options{Nav: []NavLink{}, Posts: "chapters", PartsAsCats: true, PostsPerPage: 10, FeedContent: "full", FeedItems: 20,
		ShowDates: true, ShowAuthor: true, ShowReading: true, Accent: "moss", ImageMaxWidth: 760}
}

// Patch changes some options. A nil field is left alone; Clear names fields to
// return to their default.
type Patch struct {
	Title         *string    `json:"title,omitempty"`
	Tagline       *string    `json:"tagline,omitempty"`
	BaseURL       *string    `json:"base_url,omitempty"`
	Language      *string    `json:"language,omitempty"`
	Dir           *string    `json:"dir,omitempty"`
	Posts         *string    `json:"posts,omitempty"`
	IncludeDrafts *bool      `json:"include_drafts,omitempty"`
	PartsAsCats   *bool      `json:"parts_as_categories,omitempty"`
	PostsPerPage  *int       `json:"posts_per_page,omitempty"`
	FeedContent   *string    `json:"feed_content,omitempty"`
	FeedItems     *int       `json:"feed_items,omitempty"`
	ShowDates     *bool      `json:"show_dates,omitempty"`
	ShowAuthor    *bool      `json:"show_author,omitempty"`
	ShowReading   *bool      `json:"show_reading_time,omitempty"`
	FooterText    *string    `json:"footer_text,omitempty"`
	AboutID       *string    `json:"about_id,omitempty"`
	Nav           *[]NavLink `json:"nav,omitempty" desc:"Replaces the whole list; send [] to remove every link."`
	Accent        *string    `json:"accent,omitempty"`
	ImageMaxWidth *int       `json:"image_max_width,omitempty"`
	ShowCover     *bool      `json:"show_cover,omitempty"`
	NoIndex       *bool      `json:"noindex,omitempty"`
	Clear         []string   `json:"clear,omitempty" desc:"Option names to return to their default."`
}

// ClearableFields are the names Patch.Clear accepts.
var ClearableFields = []string{"title", "tagline", "base_url", "language", "dir", "posts", "include_drafts", "parts_as_categories",
	"posts_per_page", "feed_content", "feed_items", "show_dates", "show_author", "show_reading_time", "footer_text", "about_id", "nav",
	"accent", "image_max_width", "show_cover", "noindex"}

// Apply returns o with p applied: fields are set where sent, then those named
// in Clear go back to their default.
func (o Options) Apply(p Patch) (Options, error) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setB := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	set(&o.Title, p.Title)
	set(&o.Tagline, p.Tagline)
	set(&o.BaseURL, p.BaseURL)
	set(&o.Language, p.Language)
	set(&o.Dir, p.Dir)
	set(&o.Posts, p.Posts)
	setB(&o.IncludeDrafts, p.IncludeDrafts)
	setB(&o.PartsAsCats, p.PartsAsCats)
	setI(&o.PostsPerPage, p.PostsPerPage)
	set(&o.FeedContent, p.FeedContent)
	setI(&o.FeedItems, p.FeedItems)
	setB(&o.ShowDates, p.ShowDates)
	setB(&o.ShowAuthor, p.ShowAuthor)
	setB(&o.ShowReading, p.ShowReading)
	set(&o.FooterText, p.FooterText)
	set(&o.AboutID, p.AboutID)
	if p.Nav != nil {
		o.Nav = append([]NavLink(nil), (*p.Nav)...)
	}
	set(&o.Accent, p.Accent)
	setI(&o.ImageMaxWidth, p.ImageMaxWidth)
	setB(&o.ShowCover, p.ShowCover)
	setB(&o.NoIndex, p.NoIndex)
	d := Defaults()
	for _, name := range p.Clear {
		switch name {
		case "title":
			o.Title = d.Title
		case "tagline":
			o.Tagline = d.Tagline
		case "base_url":
			o.BaseURL = d.BaseURL
		case "language":
			o.Language = d.Language
		case "dir":
			o.Dir = d.Dir
		case "posts":
			o.Posts = d.Posts
		case "include_drafts":
			o.IncludeDrafts = d.IncludeDrafts
		case "parts_as_categories":
			o.PartsAsCats = d.PartsAsCats
		case "posts_per_page":
			o.PostsPerPage = d.PostsPerPage
		case "feed_content":
			o.FeedContent = d.FeedContent
		case "feed_items":
			o.FeedItems = d.FeedItems
		case "show_dates":
			o.ShowDates = d.ShowDates
		case "show_author":
			o.ShowAuthor = d.ShowAuthor
		case "show_reading_time":
			o.ShowReading = d.ShowReading
		case "footer_text":
			o.FooterText = d.FooterText
		case "about_id":
			o.AboutID = d.AboutID
		case "nav":
			o.Nav = nil
		case "accent":
			o.Accent = d.Accent
		case "image_max_width":
			o.ImageMaxWidth = d.ImageMaxWidth
		case "show_cover":
			o.ShowCover = d.ShowCover
		case "noindex":
			o.NoIndex = d.NoIndex
		default:
			return o, &FieldError{"clear", fmt.Sprintf("%q is not an option name.", name)}
		}
	}
	return o.normalize(), nil
}

// FieldError names the option that is wrong.
type FieldError struct{ Field, Message string }

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func bad(f, m string) error { return &FieldError{f, m} }

// normalize cleans the free text of the options: one line, nothing XML cannot
// hold, no direction overrides.
func (o Options) normalize() Options {
	o.Title = publish.CleanLine(o.Title)
	o.Tagline = publish.CleanLine(o.Tagline)
	o.FooterText = publish.CleanLine(o.FooterText)
	o.BaseURL = strings.TrimSpace(o.BaseURL)
	o.Language = strings.TrimSpace(o.Language)
	o.AboutID = strings.TrimSpace(o.AboutID)
	nav := make([]NavLink, len(o.Nav))
	for i, n := range o.Nav {
		nav[i] = NavLink{Label: publish.CleanLine(n.Label), URL: strings.TrimSpace(n.URL)}
	}
	o.Nav = nav
	return o
}

// Validate checks every option and returns the first problem.
func (o *Options) Validate() error {
	*o = o.normalize()
	if utf8.RuneCountInString(o.Title) > 120 {
		return bad("title", "The title is limited to 120 characters.")
	}
	if utf8.RuneCountInString(o.Tagline) > 200 {
		return bad("tagline", "The tagline is limited to 200 characters.")
	}
	if utf8.RuneCountInString(o.FooterText) > 300 {
		return bad("footer_text", "The footer text is limited to 300 characters.")
	}
	if o.BaseURL != "" {
		if _, err := ParseBase(o.BaseURL); err != nil {
			return bad("base_url", err.Error())
		}
	}
	if o.Language != "" && !validLang(o.Language) {
		return bad("language", "Use a language tag such as en, en-GB or ga.")
	}
	switch o.Dir {
	case "", "ltr", "rtl":
	default:
		return bad("dir", "Use ltr or rtl, or leave it empty.")
	}
	switch o.Posts {
	case "chapters", "top_level":
	default:
		return bad("posts", "Use chapters or top_level.")
	}
	if o.PostsPerPage < 1 || o.PostsPerPage > 100 {
		return bad("posts_per_page", "Posts per page is between 1 and 100.")
	}
	switch o.FeedContent {
	case "full", "excerpt":
	default:
		return bad("feed_content", "Use full or excerpt.")
	}
	if o.FeedItems < 1 || o.FeedItems > 100 {
		return bad("feed_items", "Feed items is between 1 and 100.")
	}
	if len(o.AboutID) > 64 {
		return bad("about_id", "That is not a document id.")
	}
	if len(o.Nav) > 8 {
		return bad("nav", "At most 8 navigation links.")
	}
	for i, n := range o.Nav {
		if n.Label == "" || utf8.RuneCountInString(n.Label) > 40 {
			return bad("nav", fmt.Sprintf("Link %d needs a label of 1 to 40 characters.", i+1))
		}
		if len(n.URL) > 300 || !ExternalURL(n.URL) {
			return bad("nav", fmt.Sprintf("Link %d must be an http or https address.", i+1))
		}
	}
	if _, ok := accents[o.Accent]; !ok {
		return bad("accent", "Use moss, slate, sepia, meadow or contrast.")
	}
	if o.ImageMaxWidth < 200 || o.ImageMaxWidth > 2400 {
		return bad("image_max_width", "The widest image is between 200 and 2400 pixels.")
	}
	return nil
}

func validLang(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ParseBase checks a base URL and returns it as the site will use it: http or
// https, a host, no credentials, query or fragment, and a path that ends in a
// slash.
func ParseBase(s string) (*url.URL, error) {
	if len(s) > 300 {
		return nil, fmt.Errorf("The base address is limited to 300 characters.")
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r > 0x7e {
			return nil, fmt.Errorf("The base address may not hold spaces, control or non-ASCII characters; write a domain name in its ASCII form.")
		}
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("The base address must be an http or https address, such as https://example.org/.")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(s, "#") {
		return nil, fmt.Errorf("The base address may not hold credentials, a query or a fragment.")
	}
	p := u.EscapedPath()
	if strings.Contains(p, "//") || strings.Contains(p, "/../") || strings.Contains(p, "/./") || strings.HasSuffix(p, "/..") || strings.HasSuffix(p, "/.") {
		return nil, fmt.Errorf("The base address has an odd path.")
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.RawPath = p
	if dec, err := url.PathUnescape(p); err == nil {
		u.Path = dec
	}
	return u, nil
}

// ExternalURL reports whether s is an http or https address with a host and
// nothing a browser would read differently from a person (no spaces, control
// characters or credentials).
func ExternalURL(s string) bool {
	if s == "" || len(s) > 2000 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '\\' || r == '<' || r == '>' || r == '"' || r == '`' || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	return true
}
