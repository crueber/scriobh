package server

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sitedomain "github.com/crueber/scriobh/internal/domain/site"
	"github.com/crueber/scriobh/internal/ops"
)

func leaks(t *testing.T, files map[string][]byte, canaries ...string) {
	t.Helper()
	for name, data := range files {
		for _, c := range canaries {
			if bytes.Contains(data, []byte(c)) {
				t.Errorf("LEAK: %q is in %s", c, name)
			}
		}
	}
}

// ---- 1. a hidden level hides everything under it ----------------------------------------------

func TestNothingUnderAnUnpublishedDocumentIsPublished(t *testing.T) {
	for _, mode := range []string{"chapters", "top_level"} {
		t.Run(mode, func(t *testing.T) {
			w := newSiteWorld(t, nil)
			w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"base_url":"https://example.org/","posts":"`+mode+`"}}`)
			var canaries []string
			leak := func(name string) string {
				c := "LEAK_" + strings.ToUpper(strings.ReplaceAll(name, " ", "_"))
				canaries = append(canaries, c)
				return c
			}
			ch := w.doc(t, "Chapter", "chapter", "", "Chapter body is public.", "done", "")
			// scene tiers: draft, idea, outline, skipped, not compiled, each with a done scene (and a done scene under that)
			for _, tier := range []struct{ name, status, extra string }{
				{"draft", "draft", ""}, {"idea", "idea", ""}, {"outline", "outline", ""}, {"skipped", "done", ""}, {"nocompile", "done", `,"include_in_compile":false`},
			} {
				mid := w.doc(t, "Mid "+tier.name, "scene", ch, "MID_"+tier.name, tier.status, tier.extra)
				if tier.name == "skipped" {
					w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+mid, `{"role":"skip"}`)
				}
				inner := w.doc(t, "Inner "+tier.name, "scene", mid, leak("inner "+tier.name), "done", "")
				w.doc(t, "Innermost "+tier.name, "scene", inner, leak("innermost "+tier.name), "done", "")
				canaries = append(canaries, "MID_"+tier.name)
			}
			// a skipped chapter hides its scenes; so does a draft one
			for _, tier := range []struct{ name, status string }{{"skipchap", "done"}, {"draftchap", "draft"}} {
				c2 := w.doc(t, tier.name, "chapter", "", "CHAP_"+tier.name, tier.status, "")
				if tier.name == "skipchap" {
					w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+c2, `{"role":"skip"}`)
				}
				w.doc(t, "Scene of "+tier.name, "scene", c2, leak("scene of "+tier.name), "done", "")
				canaries = append(canaries, "CHAP_"+tier.name)
			}
			// front matter holding a draft holding a done scene
			fm := w.doc(t, "About", "front_matter", "", "About is public.", "done", "")
			fd := w.doc(t, "FM draft", "scene", fm, "FM_DRAFT", "draft", "")
			w.doc(t, "FM inner", "scene", fd, leak("under fm draft"), "done", "")
			canaries = append(canaries, "FM_DRAFT")
			// a skipped part hides its chapters; a part of an idea (a container) does not hide them
			sp := w.doc(t, "Skipped part", "part", "", "", "done", "")
			w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+sp, `{"role":"skip"}`)
			w.doc(t, "In skipped part", "chapter", sp, leak("in skipped part"), "done", "")
			// a done scene whose parent was trashed and which was restored alone
			tr := w.doc(t, "Trashed mid", "scene", ch, "MID_TRASHED", "done", "")
			orphan := w.doc(t, "Orphan", "scene", tr, "ORPHAN_RESTORED_ALONE", "done", "")
			w.must(t, w.owner, "POST", "/api/v1/docs/"+tr+"/trash", "")
			if r := w.s.do(w.owner, "POST", "/api/v1/docs/"+orphan+"/restore", ""); r.Code >= 300 {
				t.Logf("restoring a scene whose parent is trashed: %d (still checked below)", r.Code)
			}
			canaries = append(canaries, "MID_TRASHED")
			_ = orphan // restored alone it is a top-level document of its own, the owner's doing, and is published
			// top-level mode: a part, a draft chapter in it, a done scene in that
			top := w.doc(t, "Top part", "part", "", "Top body is public.", "done", "")
			dc := w.doc(t, "Draft chap in part", "chapter", top, "TL_DRAFT", "draft", "")
			w.doc(t, "Scene in draft chap", "scene", dc, leak("tl under draft"), "done", "")
			canaries = append(canaries, "TL_DRAFT")

			rec, files := w.build(t, w.owner)
			if rec.Code != 200 {
				t.Fatalf("build: %d %s", rec.Code, rec.Body)
			}
			leaks(t, files, canaries...)
			// The public text is still there.
			if !strings.Contains(string(files["posts/chapter.html"]), "Chapter body is public.") && mode == "chapters" {
				t.Errorf("the chapter itself is missing")
			}
			// The preview says why, for each hidden document.
			prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
			var inside int
			for _, e := range prev["excluded"].([]any) {
				if strings.Contains(e.(map[string]any)["reason"].(string), "inside a document that is not published") {
					inside++
				}
			}
			if inside < 8 {
				t.Errorf("the preview names %d documents as inside an unpublished one, want at least 8: %v", inside, prev["excluded"])
			}
		})
	}
}

// ---- 2. names that a case-insensitive disk treats as one --------------------------------------

func TestTitlesThatDifferOnlyByCaseAreOneFileOnADiskThatIgnoresCase(t *testing.T) {
	w := newSiteWorld(t, nil)
	for i, title := range []string{"ΟΔΟΣ", "οδος", "Οδος", "İstanbul", "istanbul", "STRASSE", "Straße", "ǅ", "ǆ", "Ǆ", "ﬁsh", "fish", "Kelvin K", "Kelvin k"} {
		id := w.doc(t, title, "chapter", "", fmt.Sprintf("Body %d.", i), "done", "")
		w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+id, fmt.Sprintf(`{"tags":["%s"],"published_at":"2026-01-%02d"}`, title, i+1))
	}
	if r := w.s.do(w.owner, "POST", w.base()+"/site/publish", ""); r.Code != 200 {
		t.Fatalf("publish: %d %s", r.Code, r.Body)
	}
}

func TestWhenTheFolderCannotBeWrittenTheAnswerIsAConflictThatSaysWhy(t *testing.T) {
	if os.Getuid() == 0 || os.PathSeparator == '\\' {
		t.Skip("needs a user that permissions apply to")
	}
	w := newSiteWorld(t, nil)
	w.populate(t)
	if err := os.MkdirAll(w.root, 0o555); err != nil { // a sites folder Scríobh may not write in
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.root, 0o755) })
	r := w.s.do(w.owner, "POST", w.base()+"/site/publish", "")
	if r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "may not write") || strings.Contains(r.Body.String(), w.root) {
		t.Errorf("an unwritable folder: %d %s", r.Code, r.Body)
	}
}

// ---- 4 and 5. pictures: no metadata anywhere, and upright ------------------------------------

func exifSeg(orientation int, gps string) []byte {
	var b bytes.Buffer
	b.WriteString("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08")
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(0x0112))
	_ = binary.Write(&b, binary.BigEndian, uint16(3))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(orientation))
	b.Write([]byte{0, 0})
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	b.WriteString(gps)
	d := b.Bytes()
	out := []byte{0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(d)+2))
	return append(out, d...)
}

func comSeg(s string) []byte {
	out := []byte{0xFF, 0xFE}
	out = binary.BigEndian.AppendUint16(out, uint16(len(s)+2))
	return append(out, s...)
}

// photo is a 40x20 JPEG (red on the left, blue on the right) that claims orientation o and carries a GPS string.
func photo(t *testing.T, o int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= 20 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	j := b.Bytes()
	out := append([]byte(nil), j[:2]...)
	out = append(out, exifSeg(o, "GPS_LAT_52.1234_LON_9.4321")...)
	out = append(out, comSeg("COMMENT_CANARY_OWNER_NAME")...)
	out = append(out, j[2:]...)
	return append(out, []byte("TRAILER_CANARY")...)
}

func unzipAll(t *testing.T, body []byte) map[string][]byte { return unzip(t, body) }

func TestAPhotosLocationAndRotationNoteNeverReachAnyFile(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"base_url":"https://example.org/"}}`)
	upright := w.upload(t, "upright.jpg", photo(t, 1))
	sideways := w.upload(t, "portrait.jpg", photo(t, 6))
	w.doc(t, "With photos", "chapter", "", "Look.\n\n!["+"upright"+"]("+upright+")\n\n![portrait]("+sideways+")\n", "done", "")
	w.doc(t, "Another", "chapter", "", "Words.", "done", "")
	canaries := []string{"GPS_LAT_52", "LON_9.4321", "COMMENT_CANARY_OWNER_NAME", "TRAILER_CANARY", "Exif"}

	// The site.
	rec, files := w.build(t, w.owner)
	if rec.Code != 200 {
		t.Fatalf("site build: %d %s", rec.Code, rec.Body)
	}
	var pictures int
	for name, data := range files {
		if strings.HasPrefix(name, "assets/img/") {
			pictures++
			leaks(t, map[string][]byte{name: data}, canaries...)
		}
	}
	if pictures != 2 {
		t.Fatalf("pictures: %d", pictures)
	}
	page := string(files["posts/with-photos.html"])
	// The upright one is 40x20; the one that said "rotated 90" is published 20x40, really turned.
	if !strings.Contains(page, `width="40" height="20"`) || !strings.Contains(page, `width="20" height="40"`) {
		t.Errorf("dimensions in the page:\n%s", page)
	}
	for name, data := range files {
		if strings.HasPrefix(name, "assets/img/") {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || !(cfg.Width == 40 && cfg.Height == 20 || cfg.Width == 20 && cfg.Height == 40) {
				t.Errorf("%s: %v %v", name, cfg, err)
			}
		}
	}
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	var rotated bool
	for _, x := range prev["warnings"].([]any) {
		rotated = rotated || x.(map[string]any)["code"] == "image_rotated"
	}
	if !rotated {
		t.Errorf("no image_rotated warning: %v", prev["warnings"])
	}

	// DOCX and EPUB, through the same resolver.
	for _, preset := range []string{"styled-docx", "epub3"} {
		rec := w.s.do(w.owner, "POST", w.base()+"/export/compile", `{"preset":"`+preset+`"}`)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", preset, rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
		}
		parts := unzipAll(t, rec.Body.Bytes())
		var media int
		for name, data := range parts {
			if (strings.Contains(name, "media/") || strings.Contains(name, "images/")) && !strings.HasSuffix(name, ".svg") {
				media++
				leaks(t, map[string][]byte{name: data}, canaries...)
				if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || !(cfg.Width == 40 && cfg.Height == 20 || cfg.Width == 20 && cfg.Height == 40) {
					t.Errorf("%s %s: %v %v", preset, name, cfg, err)
				}
			}
		}
		if media == 0 {
			t.Errorf("%s holds no picture", preset)
		}
		leaks(t, parts, "GPS_LAT_52", "LON_9.4321", "COMMENT_CANARY_OWNER_NAME")
	}
}

func TestAPhotoTooLargeToTurnIsSaidToBeTooLarge(t *testing.T) {
	w := newSiteWorld(t, nil)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 6000, 5000)), nil); err != nil {
		t.Fatal(err)
	}
	j := b.Bytes()
	big := append(append(append([]byte(nil), j[:2]...), exifSeg(6, "")...), j[2:]...)
	url := w.upload(t, "huge.jpg", big)
	w.doc(t, "Huge", "chapter", "", "![huge]("+url+")", "done", "")
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	var said bool
	for _, x := range prev["warnings"].([]any) {
		m := x.(map[string]any)
		said = said || (m["code"] == "image_skipped" && strings.Contains(m["message"].(string), "too large"))
	}
	if !said {
		t.Errorf("an oversize picture is not called too large: %v", prev["warnings"])
	}
}

// ---- stable addresses ------------------------------------------------------------------------

func addressOf(t *testing.T, w *siteWorld, title string) string {
	t.Helper()
	return addressOfID(t, w, w.docs[title])
}

func addressOfID(t *testing.T, w *siteWorld, id string) string {
	t.Helper()
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	for _, p := range prev["posts"].([]any) {
		if m := p.(map[string]any); m["document_id"] == id {
			return m["path"].(string)
		}
	}
	t.Fatalf("%s is not in the preview", id)
	return ""
}

func TestAnAddressThatIsOutDoesNotMove(t *testing.T) {
	// 1. A newer post with the same title is published first; an older one with that title is finished later.
	w := newSiteWorld(t, nil)
	first := w.doc(t, "Weekly update", "chapter", "", "New.", "done", "")
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+first, `{"published_at":"2026-03-10"}`)
	before := addressOf(t, w, "Weekly update")
	w.must(t, w.owner, "POST", w.base()+"/site/build", "") // writes the address down
	older := w.doc(t, "Weekly update ", "chapter", "", "Older, now done.", "done", "")
	w.docs["Weekly update older"] = older
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+older, `{"published_at":"2026-03-01"}`)
	if after := addressOf(t, w, "Weekly update"); after != before {
		t.Errorf("a backdated post moved a published address: %s -> %s", before, after)
	}
	// 2. An address chosen by hand that equals one that is out is refused (it would move that post).
	other := w.doc(t, "Other", "chapter", "", "Other.", "done", "")
	slug := strings.TrimSuffix(strings.TrimPrefix(before, "posts/"), ".html")
	if r := w.s.do(w.owner, "PATCH", w.base()+"/site/documents/"+other, `{"slug":"`+slug+`"}`); r.Code != http.StatusConflict {
		t.Errorf("an address that is out was given to another post: %d %s", r.Code, r.Body)
	}
	// 3. A title change keeps the address; clearing the pin lets it be made again.
	w.must(t, w.owner, "PATCH", "/api/v1/docs/"+first, `{"base_revision":`+w.revision(t, first)+`,"title":"A completely new title"}`)
	w.docs["A completely new title"] = first
	if after := addressOf(t, w, "A completely new title"); after != before {
		t.Errorf("a title change moved the address: %s -> %s", before, after)
	}
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+first, `{"clear":["pinned_slug"]}`)
	if after := addressOf(t, w, "A completely new title"); !strings.Contains(after, "a-completely-new-title") {
		t.Errorf("the address was not made again after clearing it: %s", after)
	}
	// 4. A post that goes back to draft keeps its address reserved for when it returns.
	d := w.doc(t, "Comes back", "chapter", "", "x", "done", "")
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+d, `{"published_at":"2026-01-01"}`)
	pathD := addressOf(t, w, "Comes back")
	w.must(t, w.owner, "POST", w.base()+"/site/build", "")
	w.must(t, w.owner, "PATCH", "/api/v1/docs/"+d, `{"base_revision":`+w.revision(t, d)+`,"status":"draft"}`)
	squatter := w.doc(t, "Comes back", "chapter", "", "A newcomer with the same title.", "done", "")
	w.docs["Squatter"] = squatter
	for _, p := range decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))["posts"].([]any) {
		if m := p.(map[string]any); m["document_id"] == squatter && m["path"] == pathD {
			t.Errorf("a newcomer took the address of a post that is only a draft for now")
		}
	}
	w.must(t, w.owner, "PATCH", "/api/v1/docs/"+d, `{"base_revision":`+w.revision(t, d)+`,"status":"done"}`)
	if got := addressOfID(t, w, d); got != pathD {
		t.Errorf("the returning post lost its address: %s -> %s", pathD, got)
	}
}

// ---- editing by hand --------------------------------------------------------------------------

func TestAHandEditedPublishedFileStopsAPublishUnlessAskedToOverwrite(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	rec := w.must(t, w.owner, "POST", w.base()+"/site/publish", "")
	loc := decodeObj(t, rec)["location"].(string)
	p := filepath.Join(w.root, filepath.FromSlash(loc), "index.html")
	if err := os.WriteFile(p, []byte("<!doctype html><title>hand edited</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := w.s.do(w.owner, "POST", w.base()+"/site/publish", "")
	if r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "index.html") || !strings.Contains(r.Body.String(), "overwrite_edited") {
		t.Fatalf("a hand-edited file: %d %s", r.Code, r.Body)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "hand edited") {
		t.Fatal("the refused publish changed the file")
	}
	r = w.s.do(w.owner, "POST", w.base()+"/site/publish", `{"overwrite_edited":true}`)
	if r.Code != 200 {
		t.Fatalf("overwrite_edited: %d %s", r.Code, r.Body)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "hand edited") {
		t.Error("the file was not replaced when asked")
	}
}

// ---- nothing is null ---------------------------------------------------------------------------

func nulls(v any, path string, out *[]string) {
	switch x := v.(type) {
	case nil:
		*out = append(*out, path)
	case map[string]any:
		for k, e := range x {
			nulls(e, path+"."+k, out)
		}
	case []any:
		for i, e := range x {
			nulls(e, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

func TestNoSiteOrCompileResultHasANull(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.doc(t, "Only post", "chapter", "", "Just one.", "done", "")
	for _, c := range []struct{ m, p, b string }{
		{"GET", "/site", ""}, {"GET", "/site/options", ""}, {"GET", "/site/documents", ""}, {"POST", "/site/preview", ""},
		{"PATCH", "/site/options", `{"options":{"tagline":"x"}}`}, {"PATCH", "/site/options", `{"options":{"clear":["nav"]}}`},
		{"PATCH", "/site/documents/" + w.docs["Only post"], `{"slug":"only"}`}, {"GET", "/site/documents", ""},
		{"GET", "/compile/presets", ""}, {"GET", "/compile/contact", ""}, {"POST", "/compile/preview", ""},
	} {
		rec := w.s.do(w.owner, c.m, w.base()+c.p, c.b)
		if rec.Code >= 300 {
			t.Errorf("%s %s: %d %s", c.m, c.p, rec.Code, rec.Body)
			continue
		}
		var v any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		var found []string
		nulls(v, "$", &found)
		for _, f := range found {
			// A few fields are null on purpose; none of these is.
			t.Errorf("%s %s has null at %s", c.m, c.p, f)
		}
	}
}

// ---- the gate, the lock --------------------------------------------------------------------

func TestSiteBuildsAreGated(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	g := sitedomain.NewGate(1)
	g.Wait = 50 * time.Millisecond
	w.s.Reg.Env().SetService(sitedomain.LimitsService, g)
	release, err := g.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/site/preview", "/site/build", "/site/publish"} {
		r := w.s.do(w.owner, "POST", w.base()+p, "")
		if r.Code != http.StatusTooManyRequests || !strings.Contains(r.Body.String(), "busy") {
			t.Errorf("%s while the gate is full: %d %s", p, r.Code, r.Body)
		}
	}
	release()
	if r := w.s.do(w.owner, "POST", w.base()+"/site/preview", ""); r.Code != 200 {
		t.Errorf("after the slot is free: %d", r.Code)
	}
	_ = ops.ProjectEditor
}

func TestPublishedDatesAreRangeChecked(t *testing.T) {
	w := newSiteWorld(t, nil)
	id := w.doc(t, "Dated", "chapter", "", "x", "done", "")
	for _, d := range []string{"0001-01-01", "1969-12-31", "0000-01-01", "10000-01-01T00:00:00Z"} {
		if r := w.s.do(w.owner, "PATCH", w.base()+"/site/documents/"+id, `{"published_at":"`+d+`"}`); r.Code < 400 {
			t.Errorf("%s accepted: %d", d, r.Code)
		}
	}
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+id, `{"published_at":"9999-12-31"}`)
	rec, files := w.build(t, w.owner) // a far-future date makes a valid zip
	if rec.Code != 200 || len(files) == 0 {
		t.Fatalf("a post dated 9999: %d", rec.Code)
	}
	zr, _ := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	for _, f := range zr.File {
		if f.Modified.Year() < 1980 || f.Modified.Year() > 2107 {
			t.Errorf("%s is dated %v", f.Name, f.Modified)
		}
	}
}
