package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
)

// ---- fixtures for the template lint (the rows are in web_test.go) --------------------------------

func attRowsFixture() []attRow {
	utc := time.UTC
	img := attachments.Attachment{ID: "att_img", OwnerKind: "project", Filename: "harbour map.png", Alt: "The harbour at low tide", Mime: "image/png", Kind: "image", Bytes: 183000, Width: 800, Height: 600, URL: "/files/att_img", Refs: 2, CreatedAt: "2026-11-12T10:00:00.000Z"}
	pdf := attachments.Attachment{ID: "att_pdf", OwnerKind: "project", Filename: "tide tables.pdf", Mime: "application/pdf", Kind: "pdf", Bytes: 2400000, URL: "/files/att_pdf", CreatedAt: "2026-11-11T10:00:00.000Z"}
	txt := attachments.Attachment{ID: "att_txt", OwnerKind: "project", Filename: "notes [old].md", Mime: "text/markdown", Kind: "text", Bytes: 900, URL: "/files/att_txt", CreatedAt: "2026-11-10T10:00:00.000Z"}
	return []attRow{attRowOf(img, utc), attRowOf(pdf, utc), attRowOf(txt, utc)}
}

func attPageFixture(compendium, edit, empty bool, kind string) attPageData {
	o := newAttOwner("project", "prj_a", "p-a", "The Salt Road", edit, edit)
	d := attPageData{Owner: o, Slug: o.Slug, CSRF: "tok_writer", Kind: kind, MaxAccept: "image/png",
		Usage: attUsage{Used: "2.6 MB", Limit: "1.0 GB", Files: 3, Pct: 1, HasLimit: true, You: "1.2 MB", YouLimit: "2.0 GB", YouHasLimit: true, MaxFile: "25.0 MB", MaxFileBytes: 26214400}}
	if compendium {
		d.Owner = newAttOwner("compendium", "cmp_a", "the-salt-bible", "The Salt Road Bible", edit, edit)
		d.Slug = d.Owner.Slug
		d.Compend = compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible", Role: "owner"}
	} else {
		d.Project = ownerFixture(fixtureProjects()[0])
	}
	if !empty {
		d.Rows = attRowsFixture()
		d.Next = "abc123"
	}
	return d
}

func attFullFixture() attPageData {
	d := attPageFixture(false, true, false, "")
	d.Usage.Pct, d.Usage.Near, d.Usage.Full = 100, true, true
	d.Saved = "The file is deleted."
	return d
}

func attDetailFixture(compendium, edit, refused bool) attDetailData {
	o := newAttOwner("project", "prj_a", "p-a", "The Salt Road", edit, edit)
	d := attDetailData{Owner: o, Slug: o.Slug, CSRF: "tok_writer", Att: attRowsFixture()[0], Values: map[string]string{}, Saved: "Saved.",
		Refs: []attRefRow{
			{Ref: attachments.Ref{Kind: "doc", ID: "doc_a", Title: "The Harbour"}, URL: o.Base + "/d/doc_a", Label: "The Harbour"},
			{Ref: attachments.Ref{Kind: "research", ID: "rn_a", Title: "Tide notes"}, URL: o.Base + "/research/rn_a", Label: "Tide notes"}}}
	if refused {
		d.Refused, d.Typed = true, "harbour"
		d.Error, d.Field = "Documents or notes still link to this attachment.", "confirm"
	}
	if compendium {
		d.Owner = newAttOwner("compendium", "cmp_a", "the-salt-bible", "The Salt Road Bible", edit, edit)
		d.Slug = d.Owner.Slug
		d.Refs = nil
		d.Compend = compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible", Role: "owner"}
	} else {
		d.Project = ownerFixture(fixtureProjects()[0])
	}
	return d
}

func attPDFDetailFixture() attDetailData {
	d := attDetailFixture(false, true, false)
	d.Att = attRowsFixture()[1]
	d.CanPreview = true
	return d
}

// ---- words and the policy -------------------------------------------------------------------------

func TestAttachmentMarkdownAndSizes(t *testing.T) {
	img := attachments.Attachment{Filename: "map [old].png", Alt: "A map", Kind: "image", URL: "/files/att_x"}
	if got := attMarkdown(img); got != `![A map](/files/att_x)` {
		t.Errorf("image markdown: %s", got)
	}
	img.Alt = ""
	if got := attMarkdown(img); got != `![map \[old\].png](/files/att_x)` {
		t.Errorf("an image with no alt text uses its name, escaped: %s", got)
	}
	pdf := attachments.Attachment{Filename: "tides.pdf", Kind: "pdf", URL: "/files/att_y"}
	if got := attMarkdown(pdf); got != `[tides.pdf](/files/att_y)` {
		t.Errorf("link markdown: %s", got)
	}
	for n, want := range map[int64]string{500: "500 bytes", 2048: "2 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"} {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %s, want %s", n, got, want)
		}
	}
	if pct(5, 0) != 0 || pct(50, 100) != 50 || pct(500, 100) != 100 {
		t.Error("pct")
	}
}

func slugOf(t *testing.T, f *attFixture, kind, id string) string {
	t.Helper()
	rec := f.get(f.owner, "/api/v1/"+kind+"/"+id)
	var out struct {
		Slug string
		Item struct{ Slug string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if out.Slug != "" {
		return out.Slug
	}
	if out.Item.Slug == "" {
		t.Fatalf("no slug in %s", rec.Body)
	}
	return out.Item.Slug
}

// Frames are allowed on the attachment pages and nowhere else: every other page keeps the policy that forbids them.
func TestFramesAreAllowedOnlyOnTheAttachmentPages(t *testing.T) {
	if strings.Contains(csp, "frame-src") {
		t.Fatal("the app's own policy allows frames")
	}
	if strings.Replace(cspFrames, "frame-src 'self'; ", "", 1) != csp || !strings.Contains(cspFrames, "frame-src 'self'") || !strings.Contains(cspFrames, "default-src 'none'") {
		t.Fatalf("the attachment pages' policy: %s", cspFrames)
	}
	f := newAttFixture(t, nil)
	slug := slugOf(t, f, "projects", f.pid)
	for path, want := range map[string]bool{"/p/" + slug + "/files": true, "/": false, "/p/" + slug: false, "/p/" + slug + "/outline": false, "/settings": false} {
		rec := f.get(f.owner, path)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		h := rec.Header().Get("Content-Security-Policy")
		if got := strings.Contains(h, "frame-src 'self'"); got != want {
			t.Errorf("%s: frame-src allowed = %v, want %v", path, got, want)
		}
		if !strings.Contains(h, "frame-ancestors 'none'") || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s may be framed", path)
		}
	}
	// A PDF is the one thing that may be framed, and only by this origin.
	up := decodeAtt(t, f.upload(f.owner, f.pid, "filename=t.pdf", tinyPDF, "application/pdf"))
	rec := f.get(f.owner, "/files/"+up.Item.ID)
	if h := rec.Header().Get("Content-Security-Policy"); !strings.Contains(h, "frame-ancestors 'self'") || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("the PDF's own headers: %s / %s", h, rec.Header().Get("X-Frame-Options"))
	}
	// The detail page of that PDF offers the panel and is allowed to show it.
	detail := f.get(f.owner, "/p/"+slug+"/files/"+up.Item.ID)
	if detail.Code != 200 || !strings.Contains(detail.Header().Get("Content-Security-Policy"), "frame-src 'self'") || !strings.Contains(detail.Body.String(), `id="att-preview"`) {
		t.Errorf("the PDF's page: %d", detail.Code)
	}
}

// ---- the project's pages, for every kind of caller -----------------------------------------------

func TestProjectAttachmentPages(t *testing.T) {
	f := newAttFixture(t, nil)
	slug := slugOf(t, f, "projects", f.pid)
	img := decodeAtt(t, f.upload(f.owner, f.pid, "filename=harbour.png&alt=Low%20tide", pngBytes(t, 20, 20, 3), "image/png"))
	list, one := "/p/"+slug+"/files", "/p/"+slug+"/files/"+img.Item.ID

	if rec := f.get(caller{name: "anon"}, list); rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	for _, p := range []string{list, one, "/ui/projects/" + f.pid + "/files/list"} {
		if rec := f.get(f.stranger, p); rec.Code != 404 {
			t.Errorf("a stranger on %s: %d", p, rec.Code)
		}
	}
	for _, who := range []caller{f.owner, f.editor} {
		body := f.get(who, list).Body.String()
		for _, need := range []string{`id="att-drop"`, "harbour.png", `data-attach="/api/v1/projects/` + f.pid + `/attachments"`, "Low tide"} {
			if !strings.Contains(body, need) {
				t.Errorf("%s's list lacks %q", who.name, need)
			}
		}
		lint(t, who.name+" list", body)
	}
	rbody := f.get(f.reader, list).Body.String()
	if !strings.Contains(rbody, "harbour.png") || strings.Contains(rbody, `id="att-drop"`) {
		t.Error("a reader's list offers uploading or hides the files")
	}
	rone := f.get(f.reader, one).Body.String()
	if strings.Contains(rone, "/files/"+img.Item.ID+"/delete") || strings.Contains(rone, "/files/"+img.Item.ID+"/update") || !strings.Contains(rone, "Only editors can rename or delete a file.") {
		t.Error("a reader's file page has write controls")
	}
	obody := f.get(f.owner, one).Body.String()
	for _, need := range []string{"![Low tide](/files/" + img.Item.ID + ")", `action="/ui/projects/` + f.pid + `/files/` + img.Item.ID + `/update"`, `/files/` + img.Item.ID + `/delete`} {
		if !strings.Contains(obody, need) {
			t.Errorf("the file page lacks %q", need)
		}
	}
	lint(t, "file page", obody)
	// A file of another project is as missing as one that never existed.
	other := decodeAtt(t, f.upload(f.owner, f.other, "filename=x.png", pngBytes(t, 8, 8, 9), "image/png"))
	if rec := f.get(f.owner, "/p/"+slug+"/files/"+other.Item.ID); rec.Code != 404 {
		t.Errorf("another project's file through this project's page: %d", rec.Code)
	}
	// Filters and the fragment.
	if body := f.get(f.owner, list+"?kind=pdf").Body.String(); strings.Contains(body, "harbour.png") || !strings.Contains(body, "No files of this kind yet") {
		t.Error("the PDF filter shows an image")
	}
	frag := f.get(f.owner, "/ui/projects/"+f.pid+"/files/list")
	if frag.Code != 200 || !strings.Contains(frag.Body.String(), `id="att-list"`) || strings.Contains(frag.Body.String(), "<html") || frag.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the list fragment: %d", frag.Code)
	}
}

func TestProjectAttachmentChanges(t *testing.T) {
	f := newAttFixture(t, nil)
	slug := slugOf(t, f, "projects", f.pid)
	img := decodeAtt(t, f.upload(f.owner, f.pid, "filename=harbour.png&alt=Low%20tide", pngBytes(t, 20, 20, 4), "image/png"))
	id := img.Item.ID
	base := "/ui/projects/" + f.pid + "/files/" + id + "/"

	form := url.Values{"filename": {"low tide.png"}, "alt": {"The harbour at low tide"}}
	if rec := f.s.post(t, f.owner, base+"update", form, "-", false); rec.Code != 403 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := f.s.post(t, f.reader, base+"update", form, "", false); rec.Code != 403 {
		t.Errorf("a reader renamed a file: %d", rec.Code)
	}
	if rec := f.s.post(t, f.stranger, base+"update", form, "", false); rec.Code != 404 {
		t.Errorf("a stranger renamed a file: %d", rec.Code)
	}
	if rec := f.s.post(t, f.editor, base+"update", form, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("an editor's rename: %d %s", rec.Code, rec.Body)
	}
	if body := f.get(f.owner, "/p/"+slug+"/files/"+id).Body.String(); !strings.Contains(body, "low tide.png") || !strings.Contains(body, "The harbour at low tide") {
		t.Error("the rename was not saved")
	}
	long := f.s.post(t, f.owner, base+"update", url.Values{"filename": {"a.png"}, "alt": {strings.Repeat("x", 600)}}, "", false)
	if long.Code != 422 {
		t.Errorf("a long alt text: %d %s", long.Code, long.Body)
	}
	if rec := f.s.post(t, f.owner, base+"nonsense", url.Values{}, "", false); rec.Code != 422 {
		t.Errorf("an unknown action: %d", rec.Code)
	}

	// A chapter that uses the picture makes the delete refuse, say where, and ask for the name.
	doc := idOf(t, f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/docs", `{"title":"Harbour","content":"A picture: ![Low tide](/files/`+id+`)"}`).Body.Bytes())
	refused := f.s.post(t, f.owner, base+"delete", url.Values{}, "", false)
	body := refused.Body.String()
	if refused.Code != 422 || !strings.Contains(body, "/d/"+doc) || !strings.Contains(body, `name="confirm"`) || !strings.Contains(body, `name="force"`) {
		t.Errorf("a refused delete: %d; places or typed confirmation missing", refused.Code)
	}
	lint(t, "refused delete", body)
	wrong := f.s.post(t, f.owner, base+"delete", url.Values{"force": {"1"}, "confirm": {"low tide"}}, "", false)
	if wrong.Code != 422 || !strings.Contains(wrong.Body.String(), "Type the file") {
		t.Errorf("a wrongly typed name: %d", wrong.Code)
	}
	if rec := f.get(f.owner, "/files/"+id); rec.Code != 200 {
		t.Error("the file was deleted by a wrongly typed name")
	}
	if rec := f.s.post(t, f.editor, base+"delete", url.Values{"force": {"1"}, "confirm": {"low tide.png"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("a forced delete: %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(f.owner, "/files/"+id); rec.Code != 404 {
		t.Errorf("the file is still served after it was deleted: %d", rec.Code)
	}
	plain := decodeAtt(t, f.upload(f.owner, f.pid, "filename=plain.png", pngBytes(t, 9, 9, 7), "image/png"))
	if rec := f.s.post(t, f.owner, "/ui/projects/"+f.pid+"/files/"+plain.Item.ID+"/delete", url.Values{}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("deleting an unused file: %d", rec.Code)
	}
}

// ---- a Compendium's pages: no project involved -------------------------------------------------------

func TestCompendiumAttachmentPages(t *testing.T) {
	f := newAttFixture(t, nil)
	f.s.addCompendiumMember(t, f.cid, f.editor.userID, "editor")
	f.s.addCompendiumMember(t, f.cid, f.reader.userID, "reader")
	slug := slugOf(t, f, "compendiums", f.cid)
	up := decodeAtt(t, f.uploadTo(f.owner, "/api/v1/compendiums/"+f.cid+"/attachments", "filename=portrait.png&alt=A%20portrait", pngBytes(t, 12, 12, 5), "image/png"))
	list, one := "/c/"+slug+"/files", "/c/"+slug+"/files/"+up.Item.ID

	for _, p := range []string{list, one, "/ui/compendiums/" + f.cid + "/files/list"} {
		if rec := f.get(f.stranger, p); rec.Code != 404 {
			t.Errorf("a stranger on %s: %d", p, rec.Code)
		}
	}
	for _, who := range []caller{f.owner, f.editor} {
		body := f.get(who, list).Body.String()
		for _, need := range []string{`id="att-drop"`, "portrait.png", `data-attach="/api/v1/compendiums/` + f.cid + `/attachments"`} {
			if !strings.Contains(body, need) {
				t.Errorf("%s's Compendium list lacks %q", who.name, need)
			}
		}
		lint(t, who.name+" compendium list", body)
	}
	rb := f.get(f.reader, list).Body.String()
	if !strings.Contains(rb, "portrait.png") || strings.Contains(rb, `id="att-drop"`) {
		t.Error("a Compendium reader's list offers uploading or hides the files")
	}
	if rec := f.s.post(t, f.reader, "/ui/compendiums/"+f.cid+"/files/"+up.Item.ID+"/update", url.Values{"filename": {"x.png"}}, "", false); rec.Code != 403 {
		t.Errorf("a Compendium reader renamed a file: %d", rec.Code)
	}
	if rec := f.s.post(t, f.editor, "/ui/compendiums/"+f.cid+"/files/"+up.Item.ID+"/update", url.Values{"filename": {"queen.png"}, "alt": {"The queen"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("a Compendium editor's rename: %d %s", rec.Code, rec.Body)
	}
	if body := f.get(f.owner, one).Body.String(); !strings.Contains(body, "queen.png") || !strings.Contains(body, "![The queen](/files/"+up.Item.ID+")") {
		t.Error("the Compendium file page does not show the saved name")
	}
	// A project member who is not on the Compendium finds nothing, and a Compendium's file is not a project's.
	pslug := slugOf(t, f, "projects", f.pid)
	if rec := f.get(f.reader, "/p/"+pslug+"/files/"+up.Item.ID); rec.Code != 404 {
		t.Errorf("a Compendium file through a project page: %d", rec.Code)
	}
	if rec := f.s.post(t, f.stranger, "/ui/compendiums/"+f.cid+"/files/"+up.Item.ID+"/delete", url.Values{}, "", false); rec.Code != 404 {
		t.Errorf("a stranger deleted a Compendium file: %d", rec.Code)
	}
	if rec := f.s.post(t, f.editor, "/ui/compendiums/"+f.cid+"/files/"+up.Item.ID+"/delete", url.Values{}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("a Compendium editor's delete: %d", rec.Code)
	}
}

// More than one page of files: the first page holds attPageSize, a link carries the cursor, the second page holds the
// rest, and the fragment for the second page keeps its cursor so a refresh does not jump back to the first.
func TestAttachmentListPages(t *testing.T) {
	f := newAttFixture(t, nil)
	slug := slugOf(t, f, "projects", f.pid)
	for i := 0; i < attPageSize+1; i++ {
		if rec := f.upload(f.owner, f.pid, "filename=p"+strings.Repeat("x", 1)+url.QueryEscape(string(rune('a'+i%26)))+".png", pngBytes(t, 4+i, 4, i), "image/png"); rec.Code != 201 {
			t.Fatalf("upload %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	list := "/p/" + slug + "/files"
	first := f.get(f.owner, list).Body.String()
	if n := strings.Count(first, `class="att-card"`); n != attPageSize {
		t.Fatalf("first page has %d cards, want %d", n, attPageSize)
	}
	m := regexp.MustCompile(`href="[^"]*files\?cursor=([^"&]+)`).FindStringSubmatch(first)
	if m == nil {
		t.Fatal("no link to the next page")
	}
	second := f.get(f.owner, list+"?cursor="+m[1]).Body.String()
	if n := strings.Count(second, `class="att-card"`); n != 1 || strings.Contains(second, "Show more files") {
		t.Errorf("second page: %d cards, or a further link", n)
	}
	frag := f.get(f.owner, "/ui/projects/"+f.pid+"/files/list?cursor="+m[1]).Body.String()
	if !strings.Contains(frag, `data-list="/ui/projects/`+f.pid+`/files/list?cursor=`+m[1]) {
		t.Errorf("the fragment of page two forgets its cursor")
	}
	if rec := f.get(f.owner, list+"?cursor=not-a-cursor"); rec.Code != 422 && rec.Code != 400 {
		t.Errorf("a bad cursor: %d", rec.Code)
	}
}
