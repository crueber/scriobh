package site

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(40 + 120*x/w), uint8(110 + 60*y/h), 120, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// richSample is a small blog with every element the pages style.
func richSample() Input {
	in := sample()
	in.Options.Title = "The Salt Road"
	in.Options.Tagline = "Notes on walking the coast, slowly"
	in.Options.PostsPerPage = 3
	in.Options.AboutID = "doc_about"
	in.Options.FooterText = "Written by hand. No tracking, no scripts."
	in.Options.Nav = []NavLink{{Label: "Source", URL: "https://example.net/source"}}
	in.Options.ShowCover = true
	in.CoverSVG = []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 600 800\" width=\"600\" height=\"800\" role=\"img\" aria-label=\"The Salt Road\"><rect width=\"600\" height=\"800\" fill=\"#2F5D50\"/><text x=\"40\" y=\"400\" font-size=\"56\" fill=\"#FBFAF7\">The Salt Road</text></svg>\n")
	long := "The path leaves the harbour wall and climbs through gorse, then drops again to a beach nobody has named. " +
		"Between the two headlands the tide leaves a line of weed, and the weed leaves a line of flies, and by noon the line is a road of its own. " +
		"I walked it twice, once with the sea on my left and once, the next morning, with the sea on my right, and it was a different road."
	in.Images = map[string]Image{"/files/att_boat": {Data: samplePNG(1200, 700), Mime: "image/png", Ext: "png", Width: 1200, Height: 700}}
	in.Entries = []Entry{
		{ID: "doc_about", Title: "About", Role: RolePage, Content: "I write about places I walk. This site is plain files: no database, no scripts, no analytics.\n\n[Get in touch](mailto:hello@example.org) or read the [feed](https://example.org/blog/feed.xml).\n"},
		{ID: "p1", Title: "Harbour wall to Carrig strand", Role: RolePost, Published: day(0), Tags: []string{"walking", "coast"}, Category: "Spring",
			Content: long + "\n\n![A boat on the shingle, seen from the path](/files/att_boat)\n\n## The second morning\n\n" + long + "[^1]\n\n> The sea does not keep time; it keeps appointments.\n\n[^1]: The tide tables for that week are in the archive.\n"},
		{ID: "p2", Title: "A very long title for a short post that has to wrap on a narrow screen without breaking the layout", Role: RolePost, Published: day(9), Tags: []string{"notes"}, Excerpt: "Short.", Content: "One line.\n"},
		{ID: "p3", Title: "What I carry", Role: RolePost, Published: day(20), Tags: []string{"gear", "walking"}, Category: "Spring",
			Content: "A list, in order of weight:\n\n1. Water\n2. A map I do not trust\n3. Bread\n\n- notes\n- a pencil\n\n| Item | Grams |\n|:-----|------:|\n| Water | 1500 |\n| Bread | 400 |\n\n```\nrsync -a ~/walks/ backup:/walks/\n```\n\nInline `code` too.\n"},
		{ID: "p4", Title: "Back in August", Role: RolePost, Published: day(200), Tags: []string{"coast"}, Category: "Summer", Content: long + "\n\n" + long},
		{ID: "p5", Title: "Rain", Role: RolePost, Published: day(230), Content: "It rained. " + long, Scenes: []string{"Later it stopped."}},
		{ID: "p6", Title: "مرحبا", Role: RolePost, Published: day(260), Tags: []string{"عربي"}, Content: "نص قصير بالعربية.\n"},
	}
	return in
}

// TestWriteSampleSite writes the rich sample to $SCRIOBH_SITE_SAMPLE_DIR, for
// looking at in a browser. It does nothing when the variable is not set.
func TestWriteSampleSite(t *testing.T) {
	dir := os.Getenv("SCRIOBH_SITE_SAMPLE_DIR")
	if dir == "" {
		t.Skip("set SCRIOBH_SITE_SAMPLE_DIR to write the sample site")
	}
	in := richSample()
	if lang := os.Getenv("SCRIOBH_SITE_SAMPLE_LANG"); lang != "" {
		in.Options.Language = lang
	}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	for _, f := range r.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println(len(r.Files), "files;", strings.Join(r.Paths()[:3], ", "))
}

func TestRichSamplePassesItsCheck(t *testing.T) {
	in := richSample()
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	var big bool
	for _, f := range r.Files {
		big = big || strings.HasPrefix(f.Path, "assets/img/")
	}
	if !big {
		t.Error("the picture was not published")
	}
}
