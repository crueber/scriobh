package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editions"
)

// editionFixtures makes (or finds) the edition the sweeps use on a project, with
// its source file on disk, and fills the child id they substitute into edition paths.
func (s *Server) editionFixtures(t testing.TB, project string) {
	t.Helper()
	file := []byte("PK sweep edition, not a real book")
	sum := editions.SumOf(file)
	var id string
	err := s.DB.WithTx(t.Context(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM editions WHERE project_id = ? AND label = 'Sweep edition'`, project).Scan(&id)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		e, err := editions.Insert(tx, editions.New{ProjectID: project, Kind: editions.KindEPUB, Label: "Sweep edition", SHA256: sum, Size: int64(len(file)),
			Chapters: []editions.NewChapter{{Kind: "chapter", Title: "One", SourcePath: "OEBPS/one.xhtml", Baseline: "Text.\n", Words: 1}}}, true)
		id = e.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := editions.Path(s.Cfg.DataDir, project, sum, "epub")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	sweepChildren["edition_id"] = id
}
