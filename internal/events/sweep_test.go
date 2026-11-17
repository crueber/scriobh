package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/testutil"
)

// The event log keeps 90 days: older rows go, in chunks; the newest row stays
// so max(seq) never goes backwards; and while a webhook is on, rows its feeder
// has not read stay.
func TestSweepKeepsTheWindowTheNewestAndTheWebhookBacklog(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	add := func(n int, at time.Time) {
		for i := 0; i < n; i++ {
			if _, err := d.Writer().Exec(`INSERT INTO events(type, payload_json, created_at) VALUES ('doc.updated', '{}', ?)`, db.Format(at)); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func() (n int, maxSeq int64) {
		d.Read().QueryRow(`SELECT count(*), COALESCE(max(seq),0) FROM events`).Scan(&n, &maxSeq)
		return
	}
	add(4500, now.Add(-100*24*time.Hour)) // more than two chunks of old rows
	add(10, now.Add(-time.Hour))
	_, top := count()
	got, err := events.Sweep(ctx, d, events.Keep, now)
	if err != nil {
		t.Fatal(err)
	}
	if n, m := count(); got != 4500 || n != 10 || m != top {
		t.Errorf("removed %d, left %d (max seq %d, was %d)", got, n, m, top)
	}

	// Everything old: the newest row stays.
	if got, err := events.Sweep(ctx, d, events.Keep, now.Add(200*24*time.Hour)); err != nil || got != 9 {
		t.Errorf("all old: removed %d, %v", got, err)
	}
	if n, m := count(); n != 1 || m != top {
		t.Errorf("all old: left %d rows, max seq %d (want 1, %d)", n, m, top)
	}

	// A webhook that is on holds back what its feeder has not read.
	later := now.Add(200 * 24 * time.Hour)
	add(5, now.Add(50*24*time.Hour)) // old by then
	add(1, later)
	cursor := top + 2 // the feeder has read the row left above and the first two of these
	if _, err := d.Writer().Exec(`UPDATE webhook_state SET cursor = ? WHERE id = 1`, cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','i','s','e@x','N','writer','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO webhooks(id, project_id, name, url, events_json, enabled, secret_sealed, secret_hint, created_at, updated_at) VALUES ('wh_1','prj_1','h','https://x','[]',1,'s','h','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if got, err := events.Sweep(ctx, d, events.Keep, later); err != nil || got != 3 {
		t.Errorf("with a webhook on: removed %d, %v; want the 3 rows its feeder has read", got, err)
	}
	// A parked feeder (webhooks switched off on the server; the cursor is negative) holds nothing back,
	// although a webhook row is still enabled.
	if _, err := d.Writer().Exec(`UPDATE webhook_state SET cursor = -1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if got, err := events.Sweep(ctx, d, events.Keep, later); err != nil || got != 3 {
		t.Errorf("with the feeder parked: removed %d, %v; want the 3 rows left behind the newest", got, err)
	}
	if n, _ := count(); n != 1 {
		t.Errorf("with the feeder parked: %d rows left, want the newest only", n)
	}
}
