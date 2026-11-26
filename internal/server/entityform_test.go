package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The two form parsers the full page and the Compendium sidebar share: each sets only what the form sent.

func TestEntityUpdateFromFormSetsOnlyWhatWasSent(t *testing.T) {
	mk := func(v url.Values) (name, sort, summary, body, color *string, fields map[string]any, rev *int) {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = r.ParseForm()
		in := entityUpdateFromForm(r, "ent_a")
		return in.Name, in.SortName, in.Summary, in.Body, in.Color, in.Fields, in.BaseRevision
	}
	// A sidebar save: summary, one field and the body. The name, sort name and colour are not in it, so they are left alone.
	name, sort, summary, body, color, fields, rev := mk(url.Values{"summary": {"  A ferrywoman. "}, "f.age": {" 34 "}, "body": {"a\r\nb"}, "base_revision": {"4"}})
	if name != nil || sort != nil || color != nil {
		t.Errorf("a sidebar save sets the name, sort name or colour: %v %v %v", name, sort, color)
	}
	if summary == nil || *summary != "  A ferrywoman. " || body == nil || *body != "a\nb" {
		t.Errorf("summary and body: %v %v", summary, body)
	}
	if len(fields) != 1 || fields["age"] != "34" {
		t.Errorf("fields: %v", fields)
	}
	if rev == nil || *rev != 4 {
		t.Errorf("revision: %v", rev)
	}
	// The full page sends everything, an empty colour too (no colour), and it sets everything.
	name, sort, summary, body, color, fields, rev = mk(url.Values{"name": {"Máire"}, "sort_name": {""}, "summary": {""}, "body": {""}, "color": {""}, "f.age": {""}})
	if name == nil || *name != "Máire" || sort == nil || summary == nil || body == nil || color == nil || *color != "" {
		t.Errorf("a full-page save leaves something out: %v %v %v %v %v", name, sort, summary, body, color)
	}
	if v, ok := fields["age"]; !ok || v != "" {
		t.Errorf("an emptied field is cleared: %v", fields)
	}
	if rev != nil {
		t.Errorf("no revision was sent: %v", rev)
	}
	// A field the form does not carry is not in the update, so it is left as it is.
	_, _, _, _, _, fields, _ = mk(url.Values{"f.age": {"34"}})
	if _, ok := fields["role"]; ok || len(fields) != 1 {
		t.Errorf("an absent field is in the update: %v", fields)
	}
}

func TestOverrideFromFormSetsOnlyWhatWasSent(t *testing.T) {
	mk := func(v url.Values) (name, summary, notes *string, fields map[string]any) {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = r.ParseForm()
		in := overrideFromForm(r, "prj_a", "ent_a")
		if in.ID != "prj_a" || in.EntityID != "ent_a" {
			t.Errorf("ids: %q %q", in.ID, in.EntityID)
		}
		return in.Name, in.Summary, in.Notes, in.Fields
	}
	name, summary, notes, fields := mk(url.Values{"notes": {" for book two "}})
	if name != nil || summary != nil || fields != nil || notes == nil || *notes != "for book two" {
		t.Errorf("notes only: %v %v %v %v", name, summary, notes, fields)
	}
	name, summary, notes, fields = mk(url.Values{"name": {"Mairín"}, "summary": {""}, "notes": {""}, "f.age": {"40"}, "f.role": {""}, "f.voice": {"ignored"}, "blank.voice": {"1"}})
	if name == nil || *name != "Mairín" || summary == nil || notes == nil {
		t.Errorf("the book's sheet sends all three: %v %v %v", name, summary, notes)
	}
	if fields["age"] != "40" || fields["role"] != nil || fields["voice"] != "" || len(fields) != 3 {
		t.Errorf("an empty box stops overriding, a blank box blanks: %#v", fields)
	}
	if v, ok := fields["role"]; !ok || v != nil {
		t.Errorf("role must be present as nil: %#v", fields)
	}
}
