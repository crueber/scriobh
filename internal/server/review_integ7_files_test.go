package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// Every file the compile, export, site and share paths produce from one chapter that holds a marker in every
// hidden shape (and a document that is marked do not compile) must hold none of the markers.
func TestReviewInteg7OutboundFilesCarryNoHiddenMarker(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	rec := s.do(owner, "POST", "/api/v1/projects", `{"title":"Canary Book"}`)
	var pj struct{ Item struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pj)
	pid := pj.Item.ID
	base := "/api/v1/projects/" + pid
	mk := func(title, content string) string {
		b, _ := json.Marshal(map[string]any{"title": title, "kind": "chapter", "content": content})
		r := s.do(owner, "POST", base+"/docs", string(b))
		if r.Code >= 300 {
			t.Fatalf("doc: %d %s", r.Code, r.Body)
		}
		var d struct{ Item struct{ ID string } }
		_ = json.Unmarshal(r.Body.Bytes(), &d)
		return d.Item.ID
	}
	one := mk("One", rvDoc)
	oneID := one
	if r := s.do(owner, "PATCH", "/api/v1/docs/"+one, `{"status":"done","base_revision":1}`); r.Code >= 300 {
		t.Fatalf("status: %d %s", r.Code, r.Body)
	}
	dnc := mk("Hidden", "DNCCANARY words that must not compile.")
	if r := s.do(owner, "PATCH", "/api/v1/docs/"+dnc, `{"include_in_compile":false,"base_revision":1}`); r.Code >= 300 {
		t.Fatalf("flag: %d %s", r.Code, r.Body)
	}
	markers := append(append([]string{}, rvMarkers...), "DNCCANARY")
	files := map[string][]byte{}
	add := func(name string, body []byte) {
		for n, b := range unzip2(body) {
			files[name+":"+n] = b
		}
		files[name] = body
	}
	for _, preset := range []string{"plain-markdown", "shunn-us", "styled-docx", "epub3", "paged-html"} {
		r := s.do(owner, "POST", base+"/export/compile", `{"preset":"`+preset+`"}`)
		if r.Code != 200 {
			t.Logf("compile %s: %d %.200s", preset, r.Code, r.Body)
			continue
		}
		add("compile/"+preset, r.Body.Bytes())
	}
	for name, path := range map[string]string{"export.html": base + "/export/html", "site.build": base + "/site/build", "export.submission": base + "/export/submission"} {
		body := ""
		if name == "export.html" {
			body = `{"ghosts":"omit"}` // the default keeps ghosted text, by design
		}
		r := s.do(owner, "POST", path, body)
		if r.Code != 200 {
			t.Logf("%s: %d %.200s", name, r.Code, r.Body)
			continue
		}
		add(name, r.Body.Bytes())
	}
	// a share link, the reading page and the download
	r := s.do(owner, "POST", base+"/shares", `{"allow_download":true}`)
	var sh struct{ Token string }
	_ = json.Unmarshal(r.Body.Bytes(), &sh)
	if sh.Token == "" {
		t.Fatalf("share: %d %s", r.Code, r.Body)
	}
	for name, p := range map[string]string{
		"share/manifest": "/s/" + sh.Token + "/api/manifest",
		"share/download": "/s/" + sh.Token + "/api/download",
		"share/doc":      "/s/" + sh.Token + "/api/docs/" + oneID,
	} {
		rr := s.do(caller{}, "GET", p, "")
		if rr.Code != 200 {
			t.Errorf("%s: %d", name, rr.Code)
		}
		files[name] = rr.Body.Bytes()
	}
	// the positive control: the visible text must be in the outputs, or "no marker" proves nothing
	withText := 0
	for name, b := range files {
		if strings.Contains(string(b), "Visible para") {
			withText++
		} else if name == "export.html" || name == "compile/plain-markdown" {
			t.Errorf("control: %s does not hold the chapter's visible text", name)
		}
	}
	if withText < 8 {
		t.Errorf("control: only %d outputs hold the visible text", withText)
	}
	for name, b := range files {
		var hit []string
		for _, m := range markers {
			if strings.Contains(string(b), m) {
				hit = append(hit, m)
			}
		}
		if len(hit) > 0 {
			t.Errorf("%-42s carries %v", name, hit)
		}
	}
	t.Logf("%d files scanned", len(files))
}

func unzip2(b []byte) map[string][]byte {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		d, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = d
	}
	return out
}

// What an AI request carries of a chapter: the exact payload the preview shows.
func TestReviewInteg7AIPayloadCarriesNoHiddenMarker(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	rec := s.do(owner, "POST", "/api/v1/projects", `{"title":"AI Canary Book"}`)
	var pj struct{ Item struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pj)
	pid := pj.Item.ID
	b, _ := json.Marshal(map[string]any{"title": "One", "kind": "chapter", "content": rvDoc})
	r := s.do(owner, "POST", "/api/v1/projects/"+pid+"/docs", string(b))
	var d struct{ Item struct{ ID string } }
	_ = json.Unmarshal(r.Body.Bytes(), &d)
	if r := s.do(owner, "POST", "/api/v1/ai/providers", `{"name":"P","kind":"openai_compatible","base_url":"https://api.example.com/v1","api_key":"sk-fixture-key-0123456789","default_model":"m"}`); r.Code >= 300 {
		t.Fatalf("provider: %d %s", r.Code, r.Body)
	}
	_ = admin
	if r := s.do(owner, "PUT", "/api/v1/projects/"+pid+"/ai/policy", `{"mode":"ask"}`); r.Code >= 300 {
		t.Fatalf("policy: %d %s", r.Code, r.Body)
	}
	nb, _ := json.Marshal(map[string]any{"title": "Never sent", "kind": "chapter", "content": "NEVERSENDCANARY words."})
	r2 := s.do(owner, "POST", "/api/v1/projects/"+pid+"/docs", string(nb))
	var nd struct{ Item struct{ ID string } }
	_ = json.Unmarshal(r2.Body.Bytes(), &nd)
	if r := s.do(owner, "PUT", "/api/v1/projects/"+pid+"/ai/exclude", `{"kind":"document","id":"`+nd.Item.ID+`","exclude":true}`); r.Code >= 300 {
		t.Fatalf("exclude: %d %s", r.Code, r.Body)
	}
	// control: the never-send flag is honoured when the document is asked for by name
	nbody := `{"recipe":"freeform","document_id":"` + d.Item.ID + `","params":{"instruction":"x"},"chips":[{"kind":"document","document_id":"` + nd.Item.ID + `"}]}`
	if rr := s.do(owner, "POST", "/api/v1/projects/"+pid+"/ai/preview", nbody); rr.Code != 422 || strings.Contains(rr.Body.String(), "NEVERSENDCANARY") {
		t.Errorf("a never-send document asked for by name: %d %.200s", rr.Code, rr.Body)
	}
	body := `{"recipe":"freeform","document_id":"` + d.Item.ID + `","params":{"instruction":"summarise"},"chips":[{"kind":"document","document_id":"` + d.Item.ID + `"}]}`
	r = s.do(owner, "POST", "/api/v1/projects/"+pid+"/ai/preview", body)
	if r.Code != 200 {
		t.Fatalf("preview: %d %.400s", r.Code, r.Body)
	}
	got := r.Body.String()
	if !strings.Contains(got, "Visible para") {
		t.Fatalf("the preview does not hold the chapter, so this test proves nothing: %.400s", got)
	}
	if strings.Contains(got, "NEVERSENDCANARY") {
		t.Errorf("a document flagged never-send is in the AI payload")
	}
	for _, m := range rvFound(got) {
		t.Errorf("the AI payload preview carries %s (ai/context.go is unchanged since v1.0)", m)
	}
}
