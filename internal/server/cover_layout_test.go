package server

import (
	"bytes"
	"encoding/json"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

func coverPicture(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), 70, uint8(y * 255 / h), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

// rawPOST sends a file as the request body, as the CLI does for an upload.
func (s *Server) rawPOST(c caller, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func (s *Server) rawGET(c caller, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(c.cookie)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The whole of it over REST, as a browser or the CLI does it: upload, set, read the cover back with its ETag, see it
// on the library page and in the project list, and put it back.
func TestCoverLayoutOverREST(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	rec := s.do(owner, "POST", "/api/v1/projects", `{"title":"The Salt Road","cover_color":"#3B5B7A"}`)
	pid := idOf(t, rec.Body.Bytes())
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	base := "/api/v1/projects/" + pid

	if rec := s.do(owner, "GET", base+"/cover-layout", ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"layout":null}` {
		t.Fatalf("no layout yet: %d %s", rec.Code, rec.Body)
	}
	up := s.rawPOST(owner, base+"/cover-layout/images?part=front", coverPicture(1200, 1800))
	if up.Code != 201 {
		t.Fatalf("upload: %d %s", up.Code, up.Body)
	}
	var u struct {
		AttachmentID string  `json:"attachment_id"`
		DPI          float64 `json:"dpi"`
		Width        int     `json:"width_px"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &u)
	if !strings.HasPrefix(u.AttachmentID, "att_") || u.DPI != 200 || u.Width != 1200 {
		t.Fatalf("upload answer: %s", up.Body)
	}
	if rec := s.rawPOST(reader, base+"/cover-layout/images?part=front", coverPicture(10, 10)); rec.Code != 403 {
		t.Errorf("a reader uploads: %d", rec.Code)
	}
	if rec := s.rawPOST(owner, base+"/cover-layout/images?part=front", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); rec.Code != 422 || !strings.Contains(rec.Body.String(), "SVG") {
		t.Errorf("an SVG: %d %s", rec.Code, rec.Body)
	}
	set := `{"revision":0,"shape":"sides","front":{"mode":"image","attachment":"` + u.AttachmentID + `"},"alt":"A red door in the snow"}`
	if rec := s.do(reader, "PUT", base+"/cover-layout", set); rec.Code != 403 {
		t.Errorf("a reader sets: %d", rec.Code)
	}
	if rec := s.do(outsider, "PUT", base+"/cover-layout", set); rec.Code != 404 {
		t.Errorf("an outsider sets: %d", rec.Code)
	}
	rec = s.do(owner, "PUT", base+"/cover-layout", set)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"thumb_url":"/api/v1/projects/`+pid+`/cover?size=thumb\u0026v=`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "PUT", base+"/cover-layout", set); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Errorf("a stale revision: %d %s", rec.Code, rec.Body)
	}

	// the cover: a JPEG with an ETag from the layout, revalidated with 304; the generated SVG on request
	cv := s.rawGET(reader, base+"/cover", nil)
	h := cv.Header()
	if cv.Code != 200 || h.Get("Content-Type") != "image/jpeg" || h.Get("ETag") == "" || h.Get("Cache-Control") != "private, no-cache" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline;") {
		t.Fatalf("cover: %d %v", cv.Code, h)
	}
	if im, err := jpeg.DecodeConfig(bytes.NewReader(cv.Body.Bytes())); err != nil || im.Width != 1200 || im.Height != 1800 {
		t.Errorf("the front: %v %+v", err, im)
	}
	if r := s.rawGET(reader, base+"/cover", map[string]string{"If-None-Match": h.Get("ETag")}); r.Code != 304 || r.Body.Len() != 0 {
		t.Errorf("revalidation: %d", r.Code)
	}
	if r := s.rawGET(reader, base+"/cover?format=svg", nil); r.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(r.Body.String(), "<svg ") {
		t.Errorf("format=svg: %s", r.Header().Get("Content-Type"))
	}
	if r := s.rawGET(outsider, base+"/cover", nil); r.Code != 404 {
		t.Errorf("an outsider: %d", r.Code)
	}
	if r := s.rawGET(reader, base+"/cover?part=back", nil); r.Code != 404 {
		t.Errorf("no back: %d", r.Code)
	}

	// the list says so, and the library draws the thumbnail inside the tile's box
	var list struct {
		Items []struct {
			ID          string `json:"id"`
			CoverLayout bool   `json:"cover_layout"`
			CoverThumb  string `json:"cover_thumb"`
			CoverAlt    string `json:"cover_alt"`
		}
	}
	_ = json.Unmarshal(s.do(owner, "GET", "/api/v1/projects", "").Body.Bytes(), &list)
	if len(list.Items) != 1 || !list.Items[0].CoverLayout || !strings.HasPrefix(list.Items[0].CoverThumb, base+"/cover?size=thumb&v=") || list.Items[0].CoverAlt != "A red door in the snow" {
		t.Fatalf("project.list: %+v", list)
	}
	thumb := s.rawGET(owner, list.Items[0].CoverThumb, nil)
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumb.Body.Bytes())); thumb.Code != 200 || err != nil || cfg.Width != 480 || cfg.Height != 720 {
		t.Errorf("the thumb: %d %v %+v", thumb.Code, err, cfg)
	}
	page := s.do(owner, "GET", "/", "").Body.String()
	if !strings.Contains(page, `class="cover cover-image`) || !strings.Contains(page, `<img src="`+html.EscapeString(list.Items[0].CoverThumb)+`" alt="Cover: A red door in the snow"`) {
		t.Errorf("the library page does not show the cover:\n%.1500s", page)
	}

	// back to the generated cover
	if rec := s.do(owner, "DELETE", base+"/cover-layout", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cleared":true`) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if page := s.do(owner, "GET", "/", "").Body.String(); strings.Contains(page, "cover-image") || !strings.Contains(page, "cover-title") {
		t.Error("the library still shows the cleared cover")
	}
	if r := s.rawGET(reader, base+"/cover", nil); r.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("after clear: %s", r.Header().Get("Content-Type"))
	}
}

// The static site shows the project's own cover at its real size, with its alt text, as assets/cover.jpg.
func TestSiteUsesTheProjectsOwnCover(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"show_cover":true}}`)
	_, plain := w.build(t, w.owner)
	if _, ok := plain["assets/cover.svg"]; !ok {
		t.Fatalf("no generated cover: %v", keysOfBytes(plain))
	}
	up := w.s.rawPOST(w.owner, w.base()+"/cover-layout/images?part=front", coverPicture(900, 1350))
	if up.Code != 201 {
		t.Fatalf("%d %s", up.Code, up.Body)
	}
	var u struct {
		AttachmentID string `json:"attachment_id"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &u)
	w.must(t, w.owner, "PUT", w.base()+"/cover-layout", `{"revision":0,"shape":"sides","front":{"mode":"image","attachment":"`+u.AttachmentID+`"},"alt":"A boat on a grey sea"}`)
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	_, files := w.build(t, w.owner)
	if _, ok := files["assets/cover.svg"]; ok {
		t.Error("the generated cover is still in the site")
	}
	img, ok := files["assets/cover.jpg"]
	if !ok {
		t.Fatalf("no assets/cover.jpg: %v", keysOfBytes(files))
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(img)); err != nil || cfg.Width != 900 || cfg.Height != 1350 {
		t.Errorf("the picture: %v %+v", err, cfg)
	}
	idx := string(files["index.html"])
	if !strings.Contains(idx, `<img class="cover" src="assets/cover.jpg" alt="A boat on a grey sea" width="900" height="1350"/>`) {
		t.Errorf("the front page:\n%.1500s", idx)
	}
	if prev["input_hash"] == "" {
		t.Error("no input hash")
	}
	if again := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", "")); again["input_hash"] != prev["input_hash"] {
		t.Error("the hash is not stable")
	}
	// the site is built from the cover: a changed cover is a changed site
	before := prev["input_hash"]
	w.must(t, w.owner, "PUT", w.base()+"/cover-layout", `{"revision":1,"shape":"sides","front":{"mode":"image","attachment":"`+u.AttachmentID+`"},"alt":"A boat on a very grey sea"}`)
	if after := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", "")); after["input_hash"] == before {
		t.Error("a changed cover did not change the site's input hash")
	}
	// a picture that is gone (a restore made without files) does not stop a build: the generated cover stands in
	var sha, ext string
	if err := w.s.DB.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE id = ?`, u.AttachmentID).Scan(&sha, &ext); err != nil {
		t.Fatal(err)
	}
	blob, _ := attachments.BlobPath(w.s.Cfg.DataDir, attachments.OwnerProject, w.pid, sha, ext)
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(w.s.Cfg.DataDir, "files", "projects", w.pid, "covers"))
	rec2, gone := w.build(t, w.owner)
	if rec2.Code != 200 {
		t.Fatalf("a build with a missing cover picture: %d %s", rec2.Code, rec2.Body)
	}
	if _, ok := gone["assets/cover.svg"]; !ok {
		t.Errorf("the generated cover did not stand in: %v", keysOfBytes(gone))
	}
	_ = http.StatusOK
}

func keysOfBytes(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
