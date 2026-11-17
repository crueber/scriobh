package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
)

// TestEveryEventPayloadCarriesIdsAndCountsOnly runs every op that has a
// dry-run fixture for real, as its owner, then reads the events table: every
// payload key must be on the allow-list in internal/events/payload.go and every
// value an id, a number, a flag or a short token. The stream drops anything
// else, so this is also what proves the stream is not hiding a payload a client
// needed. The fixtures are the same ones the dry-run sweep drives, so a new op
// with a fixture is audited without anyone remembering to.
func TestEveryEventPayloadCarriesIdsAndCountsOnly(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	fx := f.fixtures(t)
	t.Cleanup(func() { sweepChildren = map[string]string{} })
	driven := 0
	for _, e := range s.Reg.Entries() {
		d, ok := fx[e.Op.Name]
		if !ok || e.Op.Method == "GET" || e.Op.NoDryRun {
			continue
		}
		who := f.owner
		if e.Op.Auth.Kind == ops.AuthAdmin {
			who = admin
		}
		s.do(who, e.Op.Method, d.path, d.body) // whether it succeeds is not the point; what it writes is
		driven++
	}
	if driven < 60 {
		t.Fatalf("only %d ops were driven; the fixtures should cover far more", driven)
	}
	rows, err := s.DB.Read().Query(`SELECT type, payload_json FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen, withPayload := 0, 0
	for rows.Next() {
		var typ, raw string
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		seen++
		var p map[string]any
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("%s: payload is not JSON: %s", typ, raw)
		}
		if len(p) > 0 {
			withPayload++
		}
		for k, v := range p {
			if !events.ValidPayloadKey(k) {
				t.Errorf("%s: payload key %q is not on the allow-list (ids, counts and short tokens only)", typ, k)
			}
			if !events.ValidValue(v) && v != "" {
				t.Errorf("%s: payload %q = %v is not an id, a number, a flag or a token", typ, k, v)
			}
			if str, ok := v.(string); ok && strings.ContainsAny(str, " \t\n") {
				t.Errorf("%s: payload %q holds text: %q", typ, k, str)
			}
		}
	}
	if seen < 80 || withPayload < 40 {
		t.Errorf("only %d events (%d with a payload) were audited", seen, withPayload)
	}
}
