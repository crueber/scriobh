package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// multipartPOST posts a form with files the way a browser without script does, as one multipart body.
func (s *Server) multipartPOST(t *testing.T, c caller, path string, fields map[string]string, files map[string][]byte, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if csrf {
		_ = mw.WriteField("_csrf", s.Sess.CSRFToken(sessionOf(t, s, c)))
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for k, b := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+k+`"; filename="chapter.jpg"`)
		h.Set("Content-Type", "image/jpeg")
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(b)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The cover page works as a form with no script: a picture and the rest in one multipart post that passes the CSRF check,
// a refusal in the catalogue's words, a stale revision answered with the other person's version, and the way back.
func TestCoverPageWorksAsAPlainForm(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	pid := idOf(t, s.do(owner, "POST", "/api/v1/projects", `{"title":"The Salt Road","cover_color":"#3B5B7A"}`).Body.Bytes())
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	slug := "the-salt-road"
	page := "/p/" + slug + "/cover"
	form := "/ui/projects/" + pid + "/cover"

	code, body := s.getPage(t, owner, page)
	if code != 200 || !strings.Contains(body, `enctype="multipart/form-data"`) || !strings.Contains(body, `name="front_file"`) || !strings.Contains(body, `id="cv-shape-generated"`) {
		t.Fatalf("the page: %d\n%.800s", code, body)
	}
	if code, body := s.getPage(t, reader, page); code != 200 || strings.Contains(body, `<form class="cv-form"`) || !strings.Contains(body, "Only an editor") {
		t.Errorf("a reader sees the form: %d", code)
	}
	if code, _ := s.getPage(t, outsider, page); code != 404 {
		t.Errorf("an outsider: %d", code)
	}

	// a form without the token is refused, with the picture in it or not
	if rec := s.multipartPOST(t, owner, form, map[string]string{"shape": "sides"}, nil, false); rec.Code != 403 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := s.multipartPOST(t, reader, form, map[string]string{"shape": "sides"}, nil, true); rec.Code != 403 {
		t.Errorf("a reader: %d", rec.Code)
	}
	if rec := s.multipartPOST(t, outsider, form, map[string]string{"shape": "sides"}, nil, true); rec.Code != 404 {
		t.Errorf("an outsider: %d", rec.Code)
	}

	// a picture over the default 2 MB body limit, with no alt text: the picture is added, the save is refused in words
	big := noisyPicture(1500, 2250)
	if len(big) < 2<<20 {
		t.Fatalf("the picture is only %d bytes: the test needs one over the default body limit", len(big))
	}
	rec := s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "action": "save", "revision": "0"}, map[string][]byte{"front_file": big}, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), i18n.Default.Raw("en", "cover.err.alt_required")) {
		t.Fatalf("no alt text: %d\n%.600s", rec.Code, rec.Body)
	}
	att := inside(rec.Body.String(), `name="front_att" value="`, `"`)
	if !strings.HasPrefix(att, "att_") || !strings.Contains(rec.Body.String(), `aria-invalid="true"`) || !strings.Contains(rec.Body.String(), `<svg class="cv-svg`) {
		t.Fatalf("the draft keeps the picture and flags the field: %q", att)
	}
	// the same form again, with the picture's id and the description: saved, once
	rec = s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "front_att": att, "alt": "A red door in the snow", "action": "save", "revision": "0"}, nil, true)
	if rec.Code != 303 || rec.Header().Get("Location") != page {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	_, body = s.getPage(t, owner, page)
	if !strings.Contains(body, "The cover is saved.") || !strings.Contains(body, `<img src="/api/v1/projects/`+pid+`/cover?v=`) || !strings.Contains(body, `alt="A red door in the snow"`) {
		t.Errorf("the saved page:\n%.2500s", body)
	}
	if _, tree := s.getPage(t, owner, "/ui/projects/"+pid+"/tree"); !strings.Contains(tree, `class="tree-cover"`) || !strings.Contains(tree, `/cover?size=thumb`) || strings.Contains(tree, `class="tree-row`+"\" role=\"treeitem\" id=\"tr-cover") {
		t.Errorf("the tree's Cover row:\n%.900s", tree)
	}

	// a stale revision: the writer's version stays in the form, the saved one is shown beside it, both named, and a
	// save from that page (which carries the revision it met) puts the writer's in place
	rec = s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "front_att": att, "alt": "Mine", "action": "save", "revision": "0"}, nil, true)
	cb := rec.Body.String()
	if rec.Code != 409 || !strings.Contains(cb, i18n.Default.Raw("en", "cover.err.conflict")) || !strings.Contains(cb, `href="/p/`+slug+`/cover"`) {
		t.Fatalf("stale: %d\n%.600s", rec.Code, rec.Body)
	}
	if !strings.Contains(cb, ">Mine</textarea>") || !strings.Contains(cb, ">"+i18n.Default.Raw("en", "cover.ver_yours")+"<") || !strings.Contains(cb, ">"+i18n.Default.Raw("en", "cover.ver_saved")+"<") ||
		!strings.Contains(cb, "A red door in the snow") || !strings.Contains(cb, `name="revision" value="1"`) || inside(cb, `name="front_att" value="`, `"`) != att {
		t.Errorf("a conflict keeps the writer's version in the form, and shows the saved one beside it:\n%s", inside(cb, `<div class="cv-versions">`, `</form>`))
	}
	if rec := s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "front_att": att, "alt": "Mine", "action": "save", "revision": "1"}, nil, true); rec.Code != 303 {
		t.Errorf("saving the kept version: %d", rec.Code)
	}
	if _, body = s.getPage(t, owner, page); !strings.Contains(body, `alt="Mine"`) {
		t.Error("the kept version was not saved")
	}
	rec = s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "front_att": att, "alt": "A red door in the snow", "action": "save", "revision": "2"}, nil, true)
	if rec.Code != 303 {
		t.Fatalf("restoring the description: %d", rec.Code)
	}

	// an SVG is refused in the catalogue's sentence and nothing is added
	rec = s.multipartPOST(t, owner, form, map[string]string{"shape": "sides", "front_mode": "image", "action": "upload", "revision": "3"}, map[string][]byte{"front_file": []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)}, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "An SVG is not accepted as a cover") || !strings.Contains(rec.Body.String(), `id="cv-msg"`) {
		t.Errorf("svg: %d\n%.600s", rec.Code, rec.Body)
	}

	// the way back
	if rec := s.postForm(t, owner, "/ui/projects/"+pid+"/cover/clear", nil); rec.Code != 303 {
		t.Fatalf("clear: %d", rec.Code)
	}
	if _, body = s.getPage(t, owner, page); !strings.Contains(body, "cv-generated") || strings.Contains(body, `alt="A red door in the snow"`) {
		t.Error("the page still shows the cleared cover")
	}
	if rec := s.postForm(t, reader, "/ui/projects/"+pid+"/cover/clear", nil); rec.Code != 403 {
		t.Errorf("a reader clears: %d", rec.Code)
	}
}

func inside(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return ""
}

var _ = http.StatusOK

// noisyPicture is a JPEG of random pixels: it does not compress, so it is larger than the 2 MB default body limit.
func noisyPicture(w, h int) []byte {
	r := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

// The cover page, in every state it has, rendered in the pseudo-locale, holds no run of plain English: each word on it came
// from the message catalogue, the sentences of the refusals included. What a person wrote is theirs, and the test writes only
// words the sweep lets through.
func TestThePseudoLocaleLeavesNoEnglishOnTheCoverPage(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	if rec := w.s.do(w.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	base := "/api/v1/projects/" + w.pid
	page, form := "/p/shared-book/cover", "/ui/projects/"+w.pid+"/cover"
	check := func(name string, rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		body := rec.Body.String()
		if rec.Code != want {
			t.Errorf("%s: %d, want %d\n%.400s", name, rec.Code, want, body)
			return
		}
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale at all", name)
		}
		if words := englishIn(body); len(words) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(words, " "))
		}
	}
	get := func(name string) { t.Helper(); check(name, w.s.do(w.owner, "GET", page, ""), 200) }
	if rec := w.s.do(w.reviewer, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	get("generated")
	check("generated, as a reviewer", w.s.do(w.reviewer, "GET", page, ""), 200)
	// a saved layout of sides, then a wrap
	up := w.s.rawPOST(w.owner, base+"/cover-layout/images?part=front", coverPicture(1200, 1800))
	att := inside(up.Body.String(), `"attachment_id":"`, `"`)
	if up.Code != 201 || att == "" {
		t.Fatalf("upload: %d %s", up.Code, up.Body)
	}
	if rec := w.s.do(w.owner, "PUT", base+"/cover-layout", `{"revision":0,"shape":"sides","front":{"mode":"page","attachment":"`+att+`","fit":"contain","scale":80,"position":"top","margin_in":0.5},"alt":"shared book chapter one"}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	get("saved sides")
	wp := w.s.rawPOST(w.owner, base+"/cover-layout/images?part=wrap&trim=6x9&bleed_in=0.125", coverPicture(1913, 1388))
	wa := inside(wp.Body.String(), `"attachment_id":"`, `"`)
	if wp.Code != 201 || wa == "" {
		t.Fatalf("wrap upload: %d %s", wp.Code, wp.Body)
	}
	if rec := w.s.do(w.owner, "PUT", base+"/cover-layout", `{"revision":1,"shape":"wrap","trim":"6x9","wrap":{"attachment":"`+wa+`","bleed_in":0.125},"alt":"shared book chapter one"}`); rec.Code != 200 {
		t.Fatalf("set wrap: %d %s", rec.Code, rec.Body)
	}
	get("saved wrap")
	fields := func(m map[string]string) map[string]string {
		for k, v := range map[string]string{"shape": "sides", "front_mode": "image", "revision": "2"} {
			if _, ok := m[k]; !ok {
				m[k] = v
			}
		}
		return m
	}
	// the answers to the form: a picture added, each refusal, a stale save
	check("a picture added", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "upload", "alt": "shared book chapter one"}), map[string][]byte{"front_file": coverPicture(1200, 1800)}, true), 200)
	check("an svg", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "upload"}), map[string][]byte{"front_file": []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)}, true), 422)
	check("no description", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "save", "front_att": att}), nil, true), 422)
	check("no picture", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "save", "alt": "shared book chapter one"}), nil, true), 422)
	check("a scale out of range", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "save", "front_mode": "page", "front_att": att, "front_scale": "500", "alt": "shared book chapter one"}), nil, true), 422)
	check("a wrap for the wrong trim", w.s.multipartPOST(t, w.owner, form, map[string]string{"shape": "wrap", "trim": "a4", "action": "upload", "revision": "2"}, map[string][]byte{"wrap_file": coverPicture(1913, 1388)}, true), 422)
	check("a stale save", w.s.multipartPOST(t, w.owner, form, fields(map[string]string{"action": "save", "revision": "0", "front_att": att, "alt": "shared book chapter one"}), nil, true), 409)
	// the fixed Cover row of the tree, in the person's language
	if body := w.s.do(w.owner, "GET", "/ui/projects/"+w.pid+"/tree", "").Body.String(); !strings.Contains(body, "⟦") || strings.Contains(body, ">Cover<") || strings.Contains(body, ">Picture<") {
		t.Errorf("the tree's Cover row is not in the language:\n%.700s", body)
	}
}

// Every stable code the cover back end can give has its sentence in the catalogue. The codes are every "cover_..." string
// literal of the back end's source, however they reach a response (in a Refusal, a Warning, or a variable that is
// assigned one first), so a code added there without words fails here. A code is a refusal's reason (cover.err.*) or a
// warning's code (a sentence of its own, never the general one that names the code); a code that the layout check can
// give as a warning, which is every one MeasureWrap (geometry.go) refuses with and the one it falls back to, needs its warning sentence.
func TestEveryCoverCodeHasItsWords(t *testing.T) {
	s := newAuthzServer(t)
	src, _ := filepath.Glob("../domain/coverlayout/*.go")
	reLit, reWrap := regexp.MustCompile(`"(cover_[a-z_]+)"`), regexp.MustCompile(`Refusal\{"(cover_wrap_[a-z_]+)"`)
	codes, wraps := map[string]bool{}, map[string]bool{"cover_wrap_invalid": true}
	for _, f := range src {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reLit.FindAllStringSubmatch(string(b), -1) {
			codes[m[1]] = true
		}
		if filepath.Base(f) == "geometry.go" { // MeasureWrap's refusals, which the layout check turns into warnings
			for _, m := range reWrap.FindAllStringSubmatch(string(b), -1) {
				wraps[m[1]] = true
			}
		}
	}
	if len(codes) < 30 || len(wraps) < 5 {
		t.Fatalf("found %d codes and %d wrap codes in the source: is the pattern stale?", len(codes), len(wraps))
	}
	req := httptest.NewRequest("GET", "/p/x/cover", nil)
	warned := func(c string) bool {
		return s.coverWarning(req, coverlayout.Warning{Code: c, Part: "front"}, nil, "6x9") != s.tr(req, "cover.warn.unknown", "code", c)
	}
	for c := range codes {
		if k := "cover.err." + strings.TrimPrefix(c, "cover_"); !i18n.Default.Has(k) && !warned(c) {
			t.Errorf("the code %s has no words: neither a refusal sentence %s nor a warning's", c, k)
		}
	}
	for c := range wraps {
		got := s.coverWarning(req, coverlayout.Warning{Code: c, Part: "wrap"}, nil, "6x9")
		if got == s.tr(req, "cover.warn.unknown", "code", c) || strings.Contains(got, "{") {
			t.Errorf("the wrap code %s has no sentence as a warning (or leaves a placeholder open): %q", c, got)
		}
	}
	// the wrap's low-resolution warning speaks of the wrap's own measure, not of a picture placed on a page
	wrap := s.coverWarning(req, coverlayout.Warning{Code: "cover_low_resolution", Part: "wrap"}, []coverlayout.PictureInfo{{Part: "wrap", DPI: 187}}, "6x9")
	page := s.coverWarning(req, coverlayout.Warning{Code: "cover_low_resolution", Part: "front"}, []coverlayout.PictureInfo{{Part: "front", DPI: 187}}, "6x9")
	if !strings.Contains(wrap, "187") || strings.Contains(wrap, "placed on") || strings.Contains(wrap, "enlarged") || wrap == page {
		t.Errorf("the wrap's low-resolution sentence: %q", wrap)
	}
}
