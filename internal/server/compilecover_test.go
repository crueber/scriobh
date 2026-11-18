package server

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// compileCoverFixture is the cover form's answer, drawn and refused, for the template lint.
func compileCoverFixture(refused bool) compileCoverOut {
	d := compileCoverOut{Pg: Page{}, Slug: "the-salt-road", Say: "The cover is measured. The spine is 0.5 in wide."}
	if refused {
		d.Err, d.Say, d.Bad, d.Focus = "Give the paper's thickness per page.", "Give the paper's thickness per page.", true, "cv-thick"
		return d
	}
	d.SVG = template.HTML(`<svg viewBox="0 0 10 10" class="cmp-wrapart" role="img" aria-label="x"><rect width="10" height="10"/></svg>`)
	d.Key = []cvKey{{"cv-g-cut", "Trim", false}, {"cv-g-bar", "Barcode box", true}}
	d.Facts = []cvFact{{"Spine", "0.5 in"}}
	d.Warns = []string{"The spine has no type."}
	return d
}

// Every code the print cover can give has its sentence on the Compile page (cmp.cv.w.*, or cmp.cv.r.* for a refusal), whatever
// way it reaches a response. The codes are read from the back end's source, so a code added there without words fails here.
func TestEveryPrintCodeHasItsWords(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	codes := map[string]bool{}
	scan := func(file string, res ...string) {
		src := read(file)
		for _, re := range res {
			for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(src, -1) {
				codes[m[1]] = true
			}
		}
	}
	scan("../publish/pdf_cover_plan.go", `p\.warn\(\s*[^,]+,\s*"([a-z_]+)"`)
	scan("../publish/pdf_cover.go", `Warn\{"([a-z_]+)"`)
	scan("../domain/coverlayout/print.go", `Code:\s*"([a-z_]+)"`)
	scan("../domain/compile/cover.go", `Code:\s*"([a-z_]+)"`)
	scan("../domain/coverlayout/ops.go", `code\s*:?=\s*"(cover_[a-z_]+)"`)
	// every "reason" a refusal of the compile package carries that the cover form or the cover download can meet: the cover's own
	// (cover_...) and the PDF's size limit
	scan("../server/compileui.go", `"reason":\s*"(form_[a-z_]+)"`)
	files, _ := filepath.Glob("../domain/compile/*.go")
	reasons := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		for _, m := range regexp.MustCompile(`"reason":\s*"((?:cover_|pdf_)[a-z_]+)"`).FindAllStringSubmatch(read(f), -1) {
			codes[m[1]] = true
			reasons++
		}
	}
	if reasons < 14 {
		t.Fatalf("found %d reasons in the compile package: is the pattern stale?", reasons)
	}
	// the geometry and picture refusals a print path can meet (ForPrint, Load, MeasureWrap, PlacePage) have this page's own words
	for _, c := range strings.Fields("cover_trim cover_wrap_missing cover_picture_missing cover_picture_damaged cover_wrap_low_dpi cover_wrap_spine_negative cover_wrap_spine_wide cover_picture_extreme cover_margin_large cover_wrap_empty") {
		codes[c] = true
		if !i18n.Default.Has("cmp.cv.r." + c) {
			t.Errorf("the refusal %s can reach the Compile page and has no cmp.cv.r.%s", c, c)
		}
	}
	// every "cover_..." and "pdf_..." string literal of the cover and compile packages, whatever form it takes (a Refusal, a
	// Warning, a reason), has words somewhere: this page's, or the Cover page's for what only an upload or a save can give
	all := map[string]bool{}
	for _, dir := range []string{"../domain/coverlayout", "../domain/compile"} {
		fs, _ := filepath.Glob(dir + "/*.go")
		for _, f := range fs {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			for _, m := range regexp.MustCompile(`"((?:cover|pdf)_[a-z_]+)"`).FindAllStringSubmatch(read(f), -1) {
				all[m[1]] = true
			}
		}
	}
	if len(all) < 50 {
		t.Fatalf("found %d literals: is the pattern stale?", len(all))
	}
	sv := newAuthzServer(t)
	rq := httptest.NewRequest("GET", "/p/x/compile", nil)
	for c := range all {
		known := i18n.Default.Has("cmp.cv.w."+c) || i18n.Default.Has("cmp.cv.r."+c) || i18n.Default.Has("cover.err."+strings.TrimPrefix(c, "cover_"))
		known = known || sv.coverWarning(rq, coverlayout.Warning{Code: c, Part: "front"}, nil, "6x9") != sv.tr(rq, "cover.warn.unknown", "code", c)
		if !known {
			t.Errorf("the code %s has no words: not on the Compile page (cmp.cv.w/r), not on the Cover page (cover.err, cover.warn)", c)
		}
	}
	if len(codes) < 14 {
		t.Fatalf("found %d codes in the source (%v): is the pattern stale?", len(codes), codes)
	}
	s := newAuthzServer(t)
	req := httptest.NewRequest("GET", "/p/x/compile", nil)
	full := printFacts{Part: "front", DPI: 187, W: 6.25, H: 9.25, Margin: 0.25, Profile: "kdp", BarcodeW: 2, BarcodeH: 1.2, Inch: 0.25, Pages: 100, FromPages: 79, Spine: 0.25, Measured: 0.5}
	for c := range codes {
		got := s.printRefusal(req, &ops.Error{Data: map[string]any{"reason": c, "layout_trim": "6x9", "page_size": "5x8", "words": 1000, "max_words": 500}}) // a refusal's reason, with every figure it may carry
		if got == "" {
			got = s.printWarning(req, c, full)
		}
		if got == "" && !i18n.Default.Has("cmp.cv.r."+c) {
			t.Errorf("the code %s has no words on the Compile page: add cmp.cv.w.%s (a warning) or cmp.cv.r.%s (a refusal)", c, c, c)
		}
		if strings.Contains(got, "{") {
			t.Errorf("the sentence for %s leaves a placeholder open: %q", c, got)
		}
	}
	// the figures reach the sentence, and the plain one stands when there are none
	if got := s.printWarning(req, "image_low_resolution", full); !strings.Contains(got, "187") || !strings.Contains(got, "6.25") || !strings.Contains(got, "The front picture") {
		t.Errorf("the low-resolution sentence: %q", got)
	}
	if got := s.printWarning(req, "image_low_resolution", printFacts{}); strings.Contains(got, "at 0 dpi") || strings.Contains(got, "{") {
		t.Errorf("the plain low-resolution sentence: %q", got)
	}
	if got := s.printWarning(req, "cover_wrap_stale", full); !strings.Contains(got, "0.5 in") || !strings.Contains(got, "100 pages") || !strings.Contains(got, "0.25 in") {
		t.Errorf("the stale sentence: %q", got)
	}
}

// The cover form's answer: the cover measured and drawn for a project with no layout, with the profile's figures; refused in
// words without the paper's thickness; drawn from the layout's pictures with a stale wrap's two sets of folds; refused in
// words for a trim the layout was not made for.
func TestTheCoverFormMeasuresAndDraws(t *testing.T) {
	w := newShareWorld(t, nil)
	post := func(v url.Values) (int, string) {
		rec := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/compile/cover-spec", v)
		return rec.Code, rec.Body.String()
	}
	form := func(kv ...string) url.Values {
		v := url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}, "profile": {"kdp"}}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	code, body := post(form("barcode", "1"))
	if code != 200 || !strings.Contains(body, "<svg") || !strings.Contains(body, `id="cv-facts"`) || !strings.Contains(body, "cv-g-fold") || !strings.Contains(body, "KDP") {
		t.Fatalf("a generated cover: %d\n%.1500s", code, body)
	}
	if strings.Contains(body, "<image") || !strings.Contains(body, "cv-g-bar") || !strings.Contains(body, "Made from") {
		t.Errorf("a generated cover has no picture and has its barcode box:\n%.1500s", body)
	}
	if strings.Contains(body, "data-proof") {
		t.Error("a generated cover has no proof to offer")
	}
	dl := func(kv ...string) *httptest.ResponseRecorder {
		v := url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/compile/cover", v)
	}
	if rec := dl("part", "proof"); rec.Code != 422 || !strings.Contains(rec.Body.String(), "The proof is of your own cover") {
		t.Errorf("a proof of a generated cover: %d %.300s", rec.Code, rec.Body)
	}
	if code, body = post(form("page_thickness_in", "")); code != 422 || !strings.Contains(body, `data-focus="cv-thick"`) || !strings.Contains(body, "Give the paper&#39;s thickness") {
		t.Errorf("no thickness: %d %.600s", code, body)
	}
	if code, body = post(form("page_thickness_in", "0.5")); code != 422 || !strings.Contains(body, "between 0.0005 and 0.01") || !strings.Contains(body, `data-focus="cv-thick"`) {
		t.Errorf("a thickness out of range: %d %.600s", code, body)
	}
	if code, body = post(form("page_count", "20.5")); code != 422 || strings.Contains(body, "<html") || !strings.Contains(body, "The page count is a whole number.") || !strings.Contains(body, `data-focus="cv-pages"`) {
		t.Errorf("a page count that is not whole: %d %.400s", code, body)
	}
	if code, _ = post(form("preset", "no-such-preset")); code != 404 && code != 422 {
		t.Errorf("an unknown preset: %d", code)
	}
	if code, body = post(form("preset", "shunn-us")); code != 422 || !strings.Contains(body, "needs a print preset") {
		t.Errorf("a Word preset is said in words: %d %.300s", code, body)
	}
	if code, body = post(form("options", `{"font_size":500}`)); code != 422 || !strings.Contains(body, "“Size (points)”") || strings.Contains(body, "font_size") || strings.Contains(body, `href="/p/`) {
		t.Errorf("an option out of range is named by its label, with no link to the Cover page: %d %.500s", code, body)
	}
	if code, body = post(form("page_count", "1")); code != 422 || !strings.Contains(body, "between 2 and 1,500") {
		t.Errorf("a page count out of range: %d %.300s", code, body)
	}
	if code, body = post(form("bleed_in", "9")); code != 422 || !strings.Contains(body, "between 0 and 0.5") {
		t.Errorf("a bleed out of range: %d %.300s", code, body)
	}
	// a wrap made for the book's spine (200 pages of 0.0025 in paper is 0.5 in)
	up := w.s.rawPOST(w.owner, "/api/v1/projects/"+w.pid+"/cover-layout/images?part=wrap&trim=6x9&bleed_in=0.125", coverPicture(1913, 1388))
	if up.Code != 201 {
		t.Fatalf("upload: %d %s", up.Code, up.Body)
	}
	var u struct {
		AttachmentID string `json:"attachment_id"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &u)
	set := w.s.do(w.owner, "PUT", "/api/v1/projects/"+w.pid+"/cover-layout", `{"revision":0,"shape":"wrap","trim":"6x9","wrap":{"attachment":"`+u.AttachmentID+`","bleed_in":0.125},"alt":"A harbour at dusk"}`)
	if set.Code != 200 {
		t.Fatalf("set: %d %s", set.Code, set.Body)
	}
	// the Cover page says what the print check finds in a saved cover: a wrap leaves the printer's barcode corner to chance
	if page := w.s.do(w.owner, "GET", "/p/shared-book/cover", "").Body.String(); !strings.Contains(page, "If you print this cover") || !strings.Contains(page, "Leave the lower right of the back clear") {
		t.Errorf("the Cover page's print notes:\n%.300s", page)
	}
	code, body = post(form())
	if code != 200 || !strings.Contains(body, `href="/files/`+u.AttachmentID+`"`) || strings.Contains(body, "cv-g-book") || !strings.Contains(body, "Your wrap") {
		t.Errorf("a wrap that fits: %d\n%.1800s", code, body)
	}
	if !strings.Contains(body, `data-proof="1"`) {
		t.Errorf("a layout has a proof to offer:\n%.300s", body)
	}
	for part, name := range map[string]string{"": "-cover.pdf", "front": "-cover-front.pdf", "proof": "-cover-proof.pdf"} {
		if rec := dl("part", part); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), name) || strings.Count(rec.Body.String(), "/Type /Page\n")+strings.Count(rec.Body.String(), "/Type /Page ") < 1 {
			t.Errorf("part %q: %d %v", part, rec.Code, rec.Header().Get("Content-Disposition"))
		}
	}
	code, body = post(form("page_count", "300"))
	if code != 200 || !strings.Contains(body, "cv-g-book") || !strings.Contains(body, "Where your book") || !strings.Contains(body, "Make the wrap again for this spine") {
		t.Errorf("a stale wrap shows the book's folds and says what to do: %d\n%.1800s", code, body)
	}
	code, body = post(form("preset", "print-5x8"))
	if code != 422 || !strings.Contains(body, "Your cover is made for 6 × 9 in, and this preset is 5 × 8 in") || !strings.Contains(body, `href="/p/`) {
		t.Errorf("a trim the layout was not made for: %d\n%.900s", code, body)
	}
	// the download says it in the same words, as an error's message
	rec := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/compile/cover", url.Values{"preset": {"print-5x8"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Make the cover again for 5 × 8 in") {
		t.Errorf("the download's refusal: %d %.300s", rec.Code, rec.Body)
	}
	// a reader never meets the form
	if rec := w.s.postForm(t, w.reviewer, "/ui/projects/"+w.pid+"/compile/cover-spec", form()); rec.Code != 403 && rec.Code != 404 {
		t.Errorf("a reviewer: %d", rec.Code)
	}
}

// The sentences for the warnings only a file has travel with the download, percent-encoded in one header, and the others
// (which the measuring already said) are left out.
func TestAFilesColourWarningsAreSaidWithTheDownload(t *testing.T) {
	s := newAuthzServer(t)
	req := httptest.NewRequest("POST", "/ui/projects/x/compile/cover", nil)
	got := s.coverFileNotes(req, []compile.Warning{{Code: "cover_profile_not_srgb", Part: "back"}, {Code: "spine_narrow"}, {Code: "cover_profile_not_srgb", Part: "back"}, {Code: "cover_profile_dropped", Part: "front"}})
	if len(got) != 2 || !strings.HasPrefix(got[0], "The back picture") || !strings.Contains(got[0], "sRGB") || !strings.HasPrefix(got[1], "The front picture") || strings.Contains(strings.Join(got, ""), "{") {
		t.Errorf("notes name the picture, once each: %q", got)
	}
	if n := s.coverFileNotes(req, []compile.Warning{{Code: "spine_narrow"}}); len(n) != 0 {
		t.Errorf("a warning the measuring said is not said again: %q", n)
	}
}

func TestThousandsAndAMissingPicture(t *testing.T) {
	for in, want := range map[any]string{1200000: "1,200,000", 999: "999", float64(250000): "250,000", 1000: "1,000"} {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%v) = %q, want %q", in, got, want)
		}
	}
	w := newShareWorld(t, nil)
	up := w.s.rawPOST(w.owner, "/api/v1/projects/"+w.pid+"/cover-layout/images?part=wrap&trim=6x9&bleed_in=0.125", coverPicture(1913, 1388))
	var u struct {
		AttachmentID string `json:"attachment_id"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &u)
	if set := w.s.do(w.owner, "PUT", "/api/v1/projects/"+w.pid+"/cover-layout", `{"revision":0,"shape":"wrap","trim":"6x9","wrap":{"attachment":"`+u.AttachmentID+`","bleed_in":0.125},"alt":"A wrap"}`); set.Code != 200 {
		t.Fatalf("set: %d %s", set.Code, set.Body)
	}
	var sha, ext string
	if err := w.s.DB.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE id = ?`, u.AttachmentID).Scan(&sha, &ext); err != nil {
		t.Fatal(err)
	}
	blob, _ := attachments.BlobPath(w.s.Cfg.DataDir, attachments.OwnerProject, w.pid, sha, ext)
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	// the download of a cover whose picture is gone says so in the page's words, as a script reads it
	vals := url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}}
	vals.Set("_csrf", w.s.Sess.CSRFToken(sessionOf(t, w.s, w.editor)))
	req := httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/compile/cover", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Scriobh-Fetch", "1")
	req.AddCookie(w.editor.cookie)
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "gone from the server") {
		t.Errorf("a picture gone from the files: %d %.300s", rec.Code, rec.Body)
	}
}
