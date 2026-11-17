// Package site generates a static website from the posts of a project: HTML
// pages, an index with pagination, tag and category pages, RSS and Atom feeds,
// a sitemap, a stylesheet and the images the posts use, as a list of files in
// memory. It is pure: no database, no clock, no network and no file system, so
// the same input is the same bytes. The domain package (internal/domain/site)
// chooses what goes in, and Dir writes what comes out to a folder.
//
// What the site holds is only what the owner chose: the text each post shows a
// reader (private notes, ghosted text, HTML comments, front matter and the
// alternatives annotation are gone before a word is rendered), the images those
// posts embed, and the options below. Nothing else is read. The output uses no
// script, no inline style, no external address and no font from elsewhere, so
// it can be served under `default-src 'self'`.
package site

import (
	"time"
)

// Role says how an entry is published.
type Role string

// The roles.
const (
	RolePost Role = "post" // listed, dated, in the feeds
	RolePage Role = "page" // a page of its own (an about page), in the navigation
)

// Entry is one document to publish: a post or a page.
type Entry struct {
	// ID is the document id. It is hashed to make feed identifiers and the last
	// part of a colliding slug; it is never written to a page.
	ID    string
	Title string
	// Content is the stored text of the document. Scenes are the stored text of
	// the documents inside it, which are folded in after it, with a scene break
	// between.
	Content string
	Scenes  []string
	Role    Role
	// Slug is the address the owner chose; empty asks for one made from the title.
	Slug string
	// PinnedSlug is the address this document was given the first time the site
	// was built or published. It is kept when the title changes and when other
	// documents arrive, so an address that has been out does not move; it comes
	// after an address the owner chose and before every new one.
	PinnedSlug string
	// Published is the publication date. Zero falls back to Created, with a
	// warning.
	Published time.Time
	Created   time.Time
	// Excerpt is the owner's summary; empty asks for the first paragraph.
	Excerpt string
	Tags    []string
	// Category is the title of the part the post sits in; empty for none.
	Category string
}

// Project is the project facts a site shows.
type Project struct {
	ID       string
	Title    string
	Subtitle string
	Author   string
	Language string
	// Updated is when the project last changed: the date a feed carries when it
	// has no post to take one from.
	Updated time.Time
}

// Image is a picture a post embeds that the caller has resolved and vouched
// for: bytes sniffed as PNG, JPEG, GIF or WebP, never SVG.
type Image struct {
	Data          []byte
	Mime          string
	Ext           string // png, jpg, gif, webp
	Width, Height int    // pixels; 0 when unknown
	// Rotated says the caller already turned the pixels upright (its EXIF
	// orientation was applied), so the picture can be reported as such.
	Rotated bool
}

// Input is everything a site is built from.
type Input struct {
	Project Project
	Options Options
	// Entries are the posts and pages in reading order (the tree's order).
	Entries []Entry
	// Images are keyed by the destination exactly as the text writes it.
	Images map[string]Image
	// Resolve, when set, is called once with the destinations of every image the
	// published text embeds (sorted, no duplicates) and returns the ones that may
	// be published. Destinations it leaves out are shown as a bracketed note.
	Resolve func(dests []string) map[string]Image
	// CoverSVG is the generated cover, used when Options.ShowCover is on.
	CoverSVG []byte
	// Reserved are addresses pinned to documents that are not in this build (a
	// post that went back to draft): no new document takes one, so the address
	// is there when the document returns.
	Reserved []string
}

// Warning is something the owner should know before the site goes out.
type Warning struct {
	Code       string `json:"code" desc:"A stable token: no_base_url, no_date, no_excerpt, missing_alt, image_missing, image_remote, image_skipped, image_rotated, slug_suffixed, untitled, empty_post, no_author, about_missing, accent_replaced, too_many_posts, tags_trimmed, no_posts, site_too_large."`
	Message    string `json:"message"`
	DocumentID string `json:"document_id,omitempty"`
	Title      string `json:"title,omitempty"`
}

// File is one file of the site: a slash-separated path with no leading slash,
// and its bytes.
type File struct {
	Path string
	Data []byte
}

// PostInfo says what became of one entry.
type PostInfo struct {
	DocumentID string   `json:"document_id"`
	Title      string   `json:"title"`
	Role       Role     `json:"role" enum:"post,page"`
	Slug       string   `json:"slug"`
	SlugFrom   string   `json:"slug_from,omitempty" enum:"set,pinned,title" desc:"Where the address came from: chosen by the owner, kept from an earlier build, or made from the title."`
	Path       string   `json:"path" desc:"Where the page is, relative to the site's root."`
	Date       string   `json:"date,omitempty" desc:"The publication date (YYYY-MM-DD); empty for a page."`
	DateFrom   string   `json:"date_from,omitempty" enum:"set,created"`
	Words      int      `json:"words"`
	Minutes    int      `json:"reading_minutes,omitempty"`
	Excerpt    string   `json:"excerpt,omitempty"`
	ExcerptSrc string   `json:"excerpt_from,omitempty" enum:"set,body"`
	Tags       []string `json:"tags,omitempty"`
	Category   string   `json:"category,omitempty"`
	Images     int      `json:"images,omitempty"`
}

// Result is a built site.
type Result struct {
	// Files are sorted by path.
	Files    []File
	Posts    []PostInfo // posts newest first, then pages in reading order
	Warnings []Warning
	Tags     int
	Cats     int
	// Hash is the SHA-256 of every file's path and bytes: the same site is the
	// same hash.
	Hash string
	// Bytes is the total size of the files.
	Bytes int
	// InputHash covers everything the files are made from: the options, the
	// project's title and author, each entry's text, title, address, date,
	// excerpt, tags and category, and the images and cover. The same input hash
	// is the same site.
	InputHash string
	// Docs lists the documents published, each with the SHA-256 of the text that
	// was published from it (hidden text already removed).
	Docs []DocHash
	// Modified is the newest post's date: the time the zip's entries carry.
	Modified time.Time
}

// DocHash is a published document and the hash of what was published of it.
type DocHash struct {
	ID     string
	SHA256 string
}
