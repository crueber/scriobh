package server

import (
	"context"
	"database/sql"
	"net"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/webhook"
)

// offlineSender is a webhook sender whose names never resolve, so a sweep that
// calls webhook.test as an owner does not depend on the network: the ping fails
// at once with a DNS error and is recorded as a dead delivery.
func offlineSender() *webhook.Sender {
	return webhook.NewSender(webhook.SenderOptions{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}})
}

// ensureWebhook makes (or finds) the webhook the sweeps use on a project, and a
// dead delivery in it, and returns their ids. The delivery is put back to dead
// each time, since an op the sweep drove (redeliver) may have queued it.
func (s *Server) ensureWebhook(t testing.TB, project string) (hookID, deliveryID string) {
	t.Helper()
	ctx := context.Background()
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM webhooks WHERE project_id = ? AND name = 'sweep'`, project).Scan(&hookID)
		if err == sql.ErrNoRows {
			hookID = db.NewID("whk")
			sealed, serr := s.Webhooks.Box().Seal([]byte("whsec_sweep"), "webhooks/"+hookID+"/secret")
			if serr != nil {
				return serr
			}
			now := db.Now()
			if _, err := tx.Exec(`INSERT INTO webhooks(id, project_id, name, url, events_json, enabled, secret_sealed, secret_hint, created_at, updated_at)
				VALUES (?,?, 'sweep', 'https://sweep.invalid', '["document.updated"]', 1, ?, 'whsec_swee…', ?, ?)`, hookID, project, sealed, now, now); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		err = tx.QueryRow(`SELECT id FROM webhook_deliveries WHERE webhook_id = ? AND event = 'document.updated' LIMIT 1`, hookID).Scan(&deliveryID)
		if err == sql.ErrNoRows {
			deliveryID = db.NewID("whd")
			now := db.Now()
			_, err = tx.Exec(`INSERT INTO webhook_deliveries(id, webhook_id, project_id, event, event_seq, payload_json, state, attempts, next_attempt_at, last_status, last_error, created_at)
				VALUES (?,?,?, 'document.updated', 1, '{}', 'dead', 8, ?, 500, 'server_error', ?)`, deliveryID, hookID, project, now, now)
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE webhook_deliveries SET state = 'dead', attempts = 8 WHERE id = ?`, deliveryID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hookID, deliveryID
}

// webhookFixtures fills the child ids the authorization sweeps substitute into webhook paths.
func (s *Server) webhookFixtures(t testing.TB, project string) {
	t.Helper()
	h, d := s.ensureWebhook(t, project)
	sweepChildren["webhook_id"], sweepChildren["delivery_id"] = h, d
}

// webhookDryFixtures are the dry-run sweep's requests for the webhook ops that can be previewed.
func (f *dryFixture) webhookDryFixtures(t *testing.T) map[string]drive {
	h, d := f.srv.ensureWebhook(t, f.project)
	base := "/api/v1/projects/" + f.project + "/webhooks/" + h
	return map[string]drive{
		"webhook.update":    {base, `{"name":"renamed","enabled":false}`, true},
		"webhook.delete":    {base, "", true},
		"webhook.redeliver": {base + "/deliveries/" + d + "/redeliver", "", true},
	}
}
