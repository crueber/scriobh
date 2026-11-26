package server

import (
	"bytes"
	"html"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// streamWorld is a project with an owner, a reader and an outsider, behind a real listener, for the tests that need to
// see how much of a body the server took.
type streamWorld struct {
	s                      *Server
	srv                    *httptest.Server
	owner, reader, outside caller
	pid, form              string
	read                   atomic.Int64 // bytes the server has read off its connections, all requests
}

// countingListener counts the bytes the server reads from each connection it accepts.
type countingListener struct {
	net.Listener
	n *atomic.Int64
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return countingConn{c, l.n}, nil
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countingConn) Read(b []byte) (int, error) {
	k, err := c.Conn.Read(b)
	c.n.Add(int64(k))
	return k, err
}

// refusedBodyLimit is how much of a refused body the server may have read. A body it refuses before it reads a byte is read by nobody,
// and net/http reads at most maxPostHandlerReadBytes (256 KB) of an unread body after the answer, to keep the connection. The client's
// side is no measure: the kernel holds several MB of what a client writes in its socket buffers before the server reads any of it.
const refusedBodyLimit = 1 << 20

func newStreamWorld(t *testing.T, env map[string]string) *streamWorld {
	t.Helper()
	e := map[string]string{"SCRIOBH_RATE_LIMIT": "0"}
	for k, v := range env {
		e[k] = v
	}
	s, err := New(Deps{Cfg: testutil.Config(t, e), DB: testutil.DB(t)})
	if err != nil {
		t.Fatal(err)
	}
	s.Webhooks.SetSender(offlineSender())
	s.Webhooks.SetPingInterval(-1)
	w := &streamWorld{s: s}
	w.owner, w.reader, w.outside = s.newCaller(t, "owner", ops.RoleWriter), s.newCaller(t, "reader", ops.RoleReader), s.newCaller(t, "outsider", ops.RoleWriter)
	w.pid = idOf(t, s.do(w.owner, "POST", "/api/v1/projects", `{"title":"The Salt Road"}`).Body.Bytes())
	s.addMember(t, w.pid, w.reader.userID, ops.ProjectReader)
	w.srv = httptest.NewUnstartedServer(s.Handler())
	w.srv.Listener = countingListener{w.srv.Listener, &w.read}
	w.srv.Start()
	t.Cleanup(w.srv.Close)
	w.form = w.srv.URL + "/ui/projects/" + w.pid + "/cover"
	return w
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// post sends a multipart body that is made as it is sent (the client holds none of it) and reports the status, the
// page, and how many bytes the server read off its connection while it answered (see countingListener).
func (w *streamWorld) post(t *testing.T, c caller, header bool, fields [][2]string, file string, size int64) (int, string, int64) {
	t.Helper()
	before := w.read.Load()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	tok := w.s.Sess.CSRFToken(sessionOf(t, w.s, c))
	go func() {
		if !header {
			_ = mw.WriteField("_csrf", tok)
		}
		for _, kv := range fields {
			_ = mw.WriteField(kv[0], kv[1])
		}
		if file != "" {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="`+file+`"; filename="junk.jpg"`)
			h.Set("Content-Type", "image/jpeg")
			part, _ := mw.CreatePart(h)
			chunk := bytes.Repeat([]byte{0xAB}, 64<<10)
			for n := int64(0); n < size; n += int64(len(chunk)) {
				if _, err := part.Write(chunk); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
		}
		_ = mw.Close()
		_ = pw.Close()
	}()
	req, _ := http.NewRequest("POST", w.form, pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if header {
		req.Header.Set("X-CSRF-Token", tok)
	}
	req.AddCookie(c.cookie)
	resp, err := noRedirect.Do(req)
	if err != nil {
		_ = pr.Close()
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	_ = pr.Close()
	return resp.StatusCode, string(b), w.read.Load() - before
}

// Nothing of a body is read for someone who may not send it: a person who is not a member, a reader, a form with a wrong
// token. The server answers from the headers (and, for a form without the header, the first field), so a 74 MB body
// costs it a few buffers, not 74 MB.
func TestCoverFormReadsNoBodyBeforeItAuthorizes(t *testing.T) {
	w := newStreamWorld(t, nil)
	const size = 60 << 20
	for _, c := range []struct {
		name   string
		who    caller
		header bool
		want   int
	}{
		{"an outsider", w.outside, true, 404},
		{"an outsider with the token in the form", w.outside, false, 404},
		{"a reader", w.reader, true, 403},
	} {
		code, _, took := w.post(t, c.who, c.header, [][2]string{{"shape", "sides"}}, "front_file", size)
		if code != c.want {
			t.Errorf("%s: %d, want %d", c.name, code, c.want)
		}
		if took > refusedBodyLimit {
			t.Errorf("%s: the server read %d KB of a %d MB body before it said no", c.name, took>>10, size>>20)
		}
	}
	// a wrong token in the header is refused before the body too
	before := w.read.Load()
	pr, pw := io.Pipe()
	go func() {
		chunk := bytes.Repeat([]byte{1}, 64<<10)
		for n := 0; n < size; n += len(chunk) {
			if _, err := pw.Write(chunk); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	req, _ := http.NewRequest("POST", w.form, pr)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("X-CSRF-Token", "not-the-token")
	req.AddCookie(w.owner.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_ = pr.Close()
	if took := w.read.Load() - before; resp.StatusCode != 403 || took > refusedBodyLimit {
		t.Errorf("a wrong token: %d after the server read %d KB", resp.StatusCode, took>>10)
	}
}

// A body past the route's limit is answered in the cover form's own sentence, with 413, and the form's fields that came
// before are kept.
func TestCoverFormBodyTooLargeAnswersInTheFormsWords(t *testing.T) {
	w := newStreamWorld(t, map[string]string{"SCRIOBH_UPLOAD_MAX_MB": "1"})
	// the limit is 3 MB + 1 MB; each picture may be 1 MB: a 6 MB body is over the first and the second
	code, body, _ := w.post(t, w.owner, true, [][2]string{{"shape", "sides"}, {"alt", "A door"}}, "front_file", 6<<20)
	if code != 413 || !strings.Contains(body, `id="cv-msg"`) || !strings.Contains(body, strings.Split(i18n.Default.Raw("en", "cover.err.body_large"), "{mb}")[0]) {
		t.Errorf("a body over the limit: %d\n%.500s", code, body)
	}
	// a picture over the one-file limit, in a body inside the route's: the file's own sentence, 413 too, the draft kept
	code, body, _ = w.post(t, w.owner, true, [][2]string{{"shape", "sides"}, {"front_mode", "image"}}, "front_file", 2<<20)
	if code != 413 || !strings.Contains(body, strings.Split(i18n.Default.Raw("en", "cover.err.too_large"), "{mb}")[0]) {
		t.Errorf("a picture over the file limit: %d\n%.500s", code, body)
	}
}

// Only a few cover bodies are read at once; another is told so in the page's words, without a byte of it read.
func TestCoverFormBodiesInFlightAreBounded(t *testing.T) {
	w := newStreamWorld(t, nil)
	for i := 0; i < bodySlotCount; i++ {
		w.s.bodySlots <- struct{}{}
	}
	code, body, took := w.post(t, w.owner, true, [][2]string{{"shape", "sides"}}, "front_file", 40<<20)
	if code != 429 || !strings.Contains(body, i18n.Default.Raw("en", "cover.err.busy_form")) || took > refusedBodyLimit {
		t.Errorf("no place: %d after the server read %d KB\n%.300s", code, took>>10, body)
	}
	for i := 0; i < bodySlotCount; i++ {
		<-w.s.bodySlots
	}
	if code, _, _ := w.post(t, w.owner, true, [][2]string{{"shape", "generated"}, {"action", "save"}}, "", 0); code != 303 {
		t.Errorf("with a place free again: %d", code)
	}
}

// A picture the chosen shape does not use is not added to the project; the page says so. The token in the header and the
// token as the first field both pass; a form whose token is not first is refused.
func TestCoverFormIgnoresAPictureTheShapeDoesNotUse(t *testing.T) {
	w := newStreamWorld(t, nil)
	pic := coverPicture(1200, 1800)
	count := func() int {
		out := w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/attachments", "").Body.String()
		return strings.Count(out, `"id":"att_`)
	}
	before := count()
	// a front picture under the wrap shape; a wrap picture under the sides shape
	for _, c := range []struct{ shape, file, key string }{{"wrap", "front_file", "cover.note.ignored_sides"}, {"sides", "wrap_file", "cover.note.ignored_wrap"}} {
		rec := w.s.multipartPOST(t, w.owner, "/ui/projects/"+w.pid+"/cover", map[string]string{"shape": c.shape, "action": "upload", "revision": "0"}, map[string][]byte{c.file: pic}, true)
		want := strings.Split(i18n.Default.Raw("en", c.key), "{")[0]
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) || strings.Contains(rec.Body.String(), `class="cv-note" role="status">Added`) {
			t.Errorf("%s under %s: %d\n%.400s", c.file, c.shape, rec.Code, rec.Body)
		}
	}
	if after := count(); after != before {
		t.Errorf("a picture the shape does not use was stored: %d attachments, %d before", after, before)
	}
	// generated: the first picture decides the shape, so a front and a wrap together use the front only
	rec := w.s.multipartPOST(t, w.owner, "/ui/projects/"+w.pid+"/cover", map[string]string{"shape": "generated", "action": "upload", "revision": "0"}, map[string][]byte{"front_file": pic}, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="cv-shape-sides" value="sides" checked`) {
		t.Errorf("a picture under the generated cover: %d\n%.300s", rec.Code, rec.Body)
	}

	// the token as a header, and a token that is not the first field
	if code, _, _ := w.post(t, w.owner, true, [][2]string{{"shape", "generated"}, {"action", "save"}}, "", 0); code != 303 {
		t.Errorf("the token in the header: %d", code)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("shape", "generated")
	_ = mw.WriteField("_csrf", w.s.Sess.CSRFToken(sessionOf(t, w.s, w.owner)))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/cover", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(w.owner.cookie)
	rr := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Errorf("a token that is not the first field: %d", rr.Code)
	}
}

// The fields that decide how a picture is read must come before it; the page's form does, and a client that sends the
// bleed after the wrap is told so. A form that is not multipart is read as before.
func TestCoverFormFieldOrderAndUrlencoded(t *testing.T) {
	w := newStreamWorld(t, nil)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("_csrf", w.s.Sess.CSRFToken(sessionOf(t, w.s, w.owner)))
	_ = mw.WriteField("shape", "wrap")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="wrap_file"; filename="w.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(coverPicture(1913, 1388))
	_ = mw.WriteField("bleed", "0")
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/cover", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(w.owner.cookie)
	rr := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Errorf("a bleed after the wrap: %d", rr.Code)
	}
	// the page's own order: the shape and the trim, the front's picture, then (in the wrap's section) the bleed and the wrap
	body.Reset()
	mw = multipart.NewWriter(&body)
	_ = mw.WriteField("_csrf", w.s.Sess.CSRFToken(sessionOf(t, w.s, w.owner)))
	_ = mw.WriteField("shape", "sides")
	_ = mw.WriteField("trim", "6x9")
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="front_file"; filename="f.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	part, _ = mw.CreatePart(h)
	_, _ = part.Write(coverPicture(1200, 1800))
	_ = mw.WriteField("bleed", "0.125")
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="wrap_file"; filename=""`)
	h.Set("Content-Type", "application/octet-stream")
	_, _ = mw.CreatePart(h)
	_ = mw.WriteField("alt", "A door")
	_ = mw.WriteField("action", "upload")
	_ = mw.Close()
	req = httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/cover", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(w.owner.cookie)
	rr = httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "chapter.jpg") && !strings.Contains(rr.Body.String(), "f.jpg") || !strings.Contains(rr.Body.String(), ">A door</textarea>") {
		t.Errorf("the page's own field order: %d\n%.400s", rr.Code, rr.Body)
	}
	rec := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/cover", url.Values{"shape": {"generated"}, "action": {"save"}})
	if rec.Code != 303 {
		t.Errorf("urlencoded: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.outside, "/ui/projects/"+w.pid+"/cover", url.Values{"shape": {"generated"}}); rec.Code != 404 {
		t.Errorf("urlencoded, an outsider: %d", rec.Code)
	}
}

// Save refuses a placement that the live preview does not draw (a picture more than eight times the page at actual
// size), in the catalogue's sentence, and saves nothing: the layout op itself would accept it and the derivative would
// fail later.
func TestCoverSaveRefusesAPlacementThePreviewDoesNotDraw(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	pid := idOf(t, s.do(owner, "POST", "/api/v1/projects", `{"title":"Strip"}`).Body.Bytes())
	rec := s.multipartPOST(t, owner, "/ui/projects/"+pid+"/cover", map[string]string{"shape": "sides", "front_mode": "page", "front_fit": "actual", "front_scale": "100", "front_margin": "0",
		"front_pos": "center", "alt": "A strip", "action": "save", "revision": "0"}, map[string][]byte{"front_file": coverPicture(16000, 200)}, true)
	if rec.Code != 422 || !strings.Contains(html.UnescapeString(rec.Body.String()), i18n.Default.Raw("en", "cover.err.picture_extreme")) {
		t.Fatalf("an extreme placement: %d\n%s", rec.Code, inside(rec.Body.String(), `id="cv-msg"`, "</div>"))
	}
	if out := s.do(owner, "GET", "/api/v1/projects/"+pid+"/cover-layout", "").Body.String(); !strings.Contains(out, `"layout":null`) {
		t.Errorf("nothing is saved: %s", out)
	}
}
