package server

import (
	"strings"
	"testing"
)

// A scratchpad is a person's private pad. A key of theirs, at any scope and with
// any pin, must not read or write it: a leaked agent key reaches the manuscript,
// not the freewriting.
func TestScratchpadsRefuseKeys(t *testing.T) {
	pw := newPinWorld(t)
	s := pw.s
	unpinned, _ := s.mintKey(t, pw.w, `{"name":"plain write key","scope":"write"}`)
	unpinnedRead, _ := s.mintKey(t, pw.w, `{"name":"plain read key","scope":"read"}`)
	paths := []string{"/api/v1/projects/" + pw.proj + "/scratchpad", "/api/v1/compendiums/" + pw.oneID + "/scratchpad"}
	// The person, with a session, can.
	for _, path := range paths {
		if rec := s.do(pw.w, "PUT", path, `{"content":"private words","base_revision":0}`); rec.Code != 200 {
			t.Fatalf("the person saving %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	keys := map[string]string{"unpinned write": unpinned, "unpinned read": unpinnedRead,
		"project read": pw.readP, "project write": pw.writeP, "compendium read": pw.readC, "compendium write": pw.writeC}
	for who, key := range keys {
		for _, path := range paths {
			rec := s.doKey(key, "GET", path, "")
			if rec.Code == 200 || strings.Contains(rec.Body.String(), "private words") {
				t.Errorf("%s key read %s: %d %s", who, path, rec.Code, rec.Body)
			}
			if rec := s.doKey(key, "PUT", path, `{"content":"overwritten","base_revision":1}`); rec.Code == 200 {
				t.Errorf("%s key wrote %s", who, path)
			}
		}
	}
	for _, path := range paths {
		rec := s.do(pw.w, "GET", path, "")
		if !strings.Contains(rec.Body.String(), "private words") {
			t.Errorf("the person lost their pad at %s: %s", path, rec.Body)
		}
	}
	// The schema says so.
	sch := s.do(pw.w, "GET", "/api/v1/_ops", "")
	if !strings.Contains(sch.Body.String(), `"session_only":true`) {
		t.Error("the op schema does not mark the scratchpad ops as session-only")
	}
}
