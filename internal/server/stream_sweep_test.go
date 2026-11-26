package server

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// streamRegistrations reads the source for every uiRouteStream call: its literal pattern and whether the authorize
// argument is a real function (a method value or a function literal; never nil, never missing).
func streamRegistrations(t *testing.T) (patterns []string, bad []string) {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "uiRouteStream" {
				return true
			}
			if len(ce.Args) != 4 {
				bad = append(bad, f+": uiRouteStream with "+types.ExprString(ce.Fun)+" has the wrong number of arguments")
				return true
			}
			switch a := ce.Args[1].(type) {
			case *ast.SelectorExpr, *ast.FuncLit:
			case *ast.Ident:
				bad = append(bad, f+": the authorize function of "+types.ExprString(ce.Args[0])+" is the bare name "+a.Name+" (nil or a variable): pass a method or a function literal")
			default:
				bad = append(bad, f+": the authorize function of "+types.ExprString(ce.Args[0])+" is "+types.ExprString(a))
			}
			if vals, ok := evalPattern(f, ce.Args[0]); ok {
				patterns = append(patterns, vals...)
			} else if f != "handlers.go" {
				bad = append(bad, f+": the pattern "+types.ExprString(ce.Args[0])+" is not a literal")
			}
			return true
		})
	}
	return patterns, bad
}

// countedBody is a request body that says how much of it was read.
type countedBody struct {
	r io.Reader
	n int
}

func (c *countedBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// multipartBody is a form with the token first (or not at all), a field, and a picture's worth of bytes.
func multipartBody(tok string) (io.Reader, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if tok != "" {
		_ = mw.WriteField("_csrf", tok)
	}
	_ = mw.WriteField("shape", "sides")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="front_file"; filename="x.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(bytes.Repeat([]byte{7}, 256<<10))
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

// Every route that streams a body authorizes with a real function, and does it before any of the body is read: a
// multipart post from a person who is not a member is a 404, from a reader a 403, from a member of a locked project the
// unlock page (or a locked refusal), each with not one byte of the body taken. The urlencoded sweeps do not reach the
// multipart branch; this does, and fails on an authorize that is nil, permissive or placed after the read.
func TestGateStreamRoutesAuthorizeBeforeTheBody(t *testing.T) {
	patterns, bad := streamRegistrations(t)
	for _, b := range bad {
		t.Error(b)
	}
	if len(patterns) == 0 {
		t.Fatal("no uiRouteStream registration was found: is the scan stale?")
	}
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	for _, pat := range patterns {
		method, path, _ := strings.Cut(pat, " ")
		path = w.pageFill(path, "")
		if pat == "POST /ui/import/stage" {
			// The import chooser's stage form names its project in the query: the same four callers are held to it as to any project's form.
			path += "?into=" + w.slug
		}
		for _, c := range []struct {
			who    caller
			header bool
			want   func(*httptest.ResponseRecorder) bool
			what   string
		}{
			{w.outsider, true, func(r *httptest.ResponseRecorder) bool { return r.Code == 404 }, "an outsider (token in the header): 404"},
			{w.outsider, false, func(r *httptest.ResponseRecorder) bool { return r.Code == 404 }, "an outsider (token in the form): 404"},
			{reader, true, func(r *httptest.ResponseRecorder) bool { return r.Code == 403 }, "a reader: 403"},
			{w.editor, true, func(r *httptest.ResponseRecorder) bool {
				return (r.Code == 303 && strings.HasPrefix(r.Header().Get("Location"), "/p/"+w.slug+"/unlock")) || (r.Code == 403 && lockedBody(r.Body.String()))
			}, "an editor of the locked project: the unlock page or a locked refusal"},
		} {
			tok := w.csrf(c.who)
			form := tok
			if c.header {
				form = ""
			}
			body, ct := multipartBody(form)
			cb := &countedBody{r: body}
			req := httptest.NewRequest(method, path, cb)
			req.Header.Set("Content-Type", ct)
			if c.header {
				req.Header.Set("X-CSRF-Token", tok)
			}
			req.AddCookie(c.who.cookie)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if !c.want(rec) {
				t.Errorf("%s %s as %s: %d %.150s (Location %q)", method, path, c.what, rec.Code, rec.Body, rec.Header().Get("Location"))
			}
			if cb.n != 0 {
				t.Errorf("%s %s as %s: %d bytes of the body were read before the answer", method, path, c.what, cb.n)
			}
		}
	}
	_ = http.StatusOK
}
