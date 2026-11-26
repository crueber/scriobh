package server

import (
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
)

// importSourceFixture makes the record of one import in the project (once), so the ops that name an import (import.undo) have one to
// be swept with. It holds no documents: the sweeps ask who may call, and the behaviour of an undo is tested with real imports.
func (s *Server) importSourceFixture(t testing.TB, project string) {
	t.Helper()
	var id string
	err := s.DB.WithTx(t.Context(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM project_sources WHERE project_id = ? AND source_path = 'Sweep import'`, project).Scan(&id)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		id = db.NewID("src")
		_, err = tx.Exec(`INSERT INTO project_sources(id, project_id, kind, source_path, source_hash, metadata_json, imported_at) VALUES (?,?,?,?,?,?,?)`,
			id, project, "markdown", "Sweep import", "imp_sweep", "{}", db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sweepChildren["source"] = id
}
