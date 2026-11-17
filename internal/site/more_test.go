package site

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestManyPagesAreListedOnAPageOfTheirOwn(t *testing.T) {
	in := sample()
	in.Entries = in.Entries[:2]
	for i := 0; i < 10; i++ {
		in.Entries = append(in.Entries, Entry{ID: fmt.Sprintf("pg%d", i), Title: fmt.Sprintf("Page %d", i), Role: RolePage, Content: "A page."})
	}
	// A page whose title is "index" must not take the page list's file name.
	in.Entries = append(in.Entries, Entry{ID: "pgx", Title: "Index", Role: RolePage, Content: "Called index."})
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	m := files(r)
	idx := string(m["pages/index.html"])
	list := idx[strings.Index(idx, `<ul class="taglist">`):]
	if strings.Count(list, "<li><a") != 11 {
		t.Errorf("the page list holds %d links, want 11", strings.Count(list, "<li><a"))
	}
	home := string(m["index.html"])
	nav := home[strings.Index(home, `<nav class="main"`):strings.Index(home, "</nav>")]
	if n := strings.Count(nav, "<li>"); n > maxNavPages+3 { // Home, the pages, Tags, Categories at most
		t.Errorf("the navigation holds %d items", n)
	}
	if !strings.Contains(nav, "All pages") {
		t.Error("no link to the list of all pages")
	}
	if !strings.Contains(string(m["sitemap.xml"]), "pages/index.html") {
		t.Error("the page list is not in the sitemap")
	}
	for _, p := range r.Posts {
		if p.Role == RolePage && p.Title == "Index" && p.Slug == "index" {
			t.Error("a page took the name of the page list")
		}
	}
}

func TestDataPicturesAreNeverPublished(t *testing.T) {
	in := sample()
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	in.Images = map[string]Image{png: {Data: tinyPNG(""), Mime: "image/png", Ext: "png", Width: 1, Height: 1}}
	in.Entries = []Entry{{ID: "d1", Title: "Inline", Role: RolePost, Published: day(0), Content: "![inline](" + png + ")\n"}}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	for _, f := range r.Files {
		if strings.HasPrefix(f.Path, "assets/img/") {
			t.Fatalf("an inline picture was published as %s", f.Path)
		}
	}
}

// The stylesheet uses logical properties only, so a right-to-left site mirrors.
func TestStylesheetIsDirectionNeutral(t *testing.T) {
	css := string(stylesheet("moss", 760))
	for _, bad := range []string{"margin-left", "margin-right", "padding-left", "padding-right", "border-left", "border-right", "text-align: right", "text-align: left", "left:", "right:", "float:"} {
		if strings.Contains(css, bad) {
			t.Errorf("the stylesheet uses %q", bad)
		}
	}
	if !regexp.MustCompile(`inset-inline-start`).MatchString(css) {
		t.Error("no logical property in the stylesheet")
	}
}
