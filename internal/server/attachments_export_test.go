package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// A Markdown bundle carries the files the exported documents link to, byte for
// byte, under attachments/ with deterministic names, and not the ones nothing
// links to. Two exports of the same state are the same bytes.
func TestBundleExportCarriesReferencedAttachments(t *testing.T) {
	f := newAttFixture(t, nil)
	linked := decodeAtt(t, f.upload(f.owner, f.pid, "filename=Harbour%20map.png", pngBytes(t, 12, 12, 50), ""))
	unlinked := decodeAtt(t, f.upload(f.owner, f.pid, "filename=unused.png", pngBytes(t, 12, 12, 51), ""))
	f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/docs", `{"title":"Chapter","kind":"chapter","content":"See ![the map](`+linked.Item.URL+`).\n"}`)
	export := func(body string) (*httptest.ResponseRecorder, map[string][]byte) {
		rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/export", body)
		if rec.Code != 200 {
			t.Fatalf("export: %d %s", rec.Code, rec.Body)
		}
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for _, zf := range zr.File {
			rc, _ := zf.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[zf.Name] = b
		}
		return rec, out
	}
	rec1, files := export(`{"mode":"bundle"}`)
	name := "attachments/" + linked.Item.SHA256[:12] + "-harbour-map.png"
	if !bytes.Equal(files[name], pngBytes(t, 12, 12, 50)) {
		var names []string
		for n := range files {
			names = append(names, n)
		}
		t.Fatalf("the attachment is not in the bundle byte for byte: %v", names)
	}
	for n := range files {
		if strings.Contains(n, unlinked.Item.SHA256[:12]) {
			t.Errorf("an unlinked attachment is in the bundle: %s", n)
		}
	}
	var mf struct {
		Attachments []struct{ ID, Path, SHA256, Alt string }
	}
	if err := json.Unmarshal(files["scriobh.json"], &mf); err != nil || len(mf.Attachments) != 1 || mf.Attachments[0].ID != linked.Item.ID || mf.Attachments[0].Path != name {
		t.Errorf("manifest: %s", files["scriobh.json"])
	}
	kept := false
	for n, b := range files {
		kept = kept || (strings.HasPrefix(n, "manuscript/") && strings.Contains(string(b), "![the map]("+linked.Item.URL+")"))
	}
	if !kept {
		t.Error("the Markdown was rewritten: it must stay exactly as stored")
	}
	rec2, _ := export(`{"mode":"bundle"}`)
	if !bytes.Equal(rec1.Body.Bytes(), rec2.Body.Bytes()) {
		t.Error("two exports of the same state differ")
	}
	if _, without := export(`{"mode":"bundle","attachments":false}`); len(without) == 0 || firstWith(without, "attachments/") != "" {
		t.Errorf("attachments:false still carried them")
	}
	if rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/export", `{"mode":"single"}`); strings.Contains(rec.Body.String(), "\x89PNG") { // the signature, not the letters: an id in the body can spell PNG
		t.Error("a single file holds attachment bytes")
	}
}

func firstWith(m map[string][]byte, prefix string) string {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return k
		}
	}
	return ""
}
