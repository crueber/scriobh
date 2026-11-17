package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// A Compendium that references another carries the other's id in the event
// payload; its stream must not pass that to a reader who is not on the other.
func TestCompendiumStreamDoesNotNameAnotherCompendium(t *testing.T) {
	f := newSSEFixture(t, nil)
	other := idOf(t, f.s.do(f.owner, "POST", "/api/v1/compendiums", `{"name":"Someone else's world"}`).Body.Bytes())
	cc, _ := openStream(t, f.ts.URL, f.owner, "", "compendium="+f.cid, "")
	cc.nextEvent()
	if rec := f.s.do(f.owner, "POST", "/api/v1/compendiums/"+f.cid+"/refs", `{"target_id":"`+other+`"}`); rec.Code >= 300 {
		t.Fatalf("ref: %d %s", rec.Code, rec.Body)
	}
	e := cc.nextEvent()
	if !strings.Contains(e.Event, "ref") {
		t.Fatalf("event %q", e.Event)
	}
	if strings.Contains(e.Data, other) || strings.Contains(e.Data, "target_compendium_id") {
		t.Errorf("the Compendium stream names another Compendium: %s", e.Data)
	}
	if rec := f.s.do(f.owner, "DELETE", "/api/v1/compendiums/"+f.cid+"/refs?target_id="+other, ""); rec.Code >= 300 {
		t.Fatalf("unref: %d", rec.Code)
	}
	if e := cc.nextEvent(); strings.Contains(e.Data, other) {
		t.Errorf("removing it names it: %s", e.Data)
	}
}

// The heartbeat's check that a session still stands is not activity: an open
// tab must not keep a session alive past the idle cap.
func TestStreamHeartbeatDoesNotRefreshTheSession(t *testing.T) {
	f := newSSEFixture(t, nil)
	cl, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
	cl.nextEvent()
	// Pretend the session was last used long ago, and is about to go idle.
	old := "2026-01-01T00:00:00.000Z"
	soon := "2099-01-01T00:00:00.000Z"
	if _, err := f.s.DB.Writer().Exec(`UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE user_id = ?`, old, soon, f.owner.userID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f.beat <- time.Now()
		for {
			if fr := cl.next(); fr.Comment == "heartbeat" {
				break
			}
		}
	}
	var last, idle string
	if err := f.s.DB.Read().QueryRow(`SELECT last_seen_at, idle_expires_at FROM sessions WHERE user_id = ?`, f.owner.userID).Scan(&last, &idle); err != nil {
		t.Fatal(err)
	}
	if last != old || idle != soon {
		t.Errorf("the heartbeat refreshed the session: last_seen_at %s, idle_expires_at %s", last, idle)
	}
}

// A text file is valid text all the way through, not only in the part detection reads.
func TestUploadChecksAllOfATextFile(t *testing.T) {
	f := newAttFixture(t, nil)
	long := bytes.Repeat([]byte("plain words here. "), 600) // well past the 4 KB detection reads
	if rec := f.upload(f.owner, f.pid, "filename=long.txt", long, ""); rec.Code != 201 {
		t.Fatalf("a long text file: %d %s", rec.Code, rec.Body)
	}
	withNUL := append(append([]byte{}, long...), 0, 'x', 'y')
	if rec := f.upload(f.owner, f.pid, "filename=nul.txt", withNUL, ""); rec.Code != 415 {
		t.Errorf("a NUL byte after the first 4 KB: %d %s", rec.Code, rec.Body)
	}
	badUTF8 := append(append([]byte{}, long...), 0xff, 0xfe, 'z')
	if rec := f.upload(f.owner, f.pid, "filename=bad.txt", badUTF8, ""); rec.Code != 415 {
		t.Errorf("invalid UTF-8 after the first 4 KB: %d %s", rec.Code, rec.Body)
	}
	_ = ops.RoleWriter
}
