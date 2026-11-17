package server

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/goals"
)

// TestSeedGoalsHistory is not a test. The screenshot script (scripts/screens-goals.mjs) runs it
// against a stopped server's data directory to give a person a back-dated writing history, which
// no op can do (an op writes at the present). It goes through goals.RecordSave, the same function
// a save uses, with Save.At set to each past day. It does nothing unless the variables are set:
//
//	SCRIOBH_SEED_DIR, SCRIOBH_SEED_PROJECT, SCRIOBH_SEED_DOC, SCRIOBH_SEED_USER (an id)
func TestSeedGoalsHistory(t *testing.T) {
	dir := os.Getenv("SCRIOBH_SEED_DIR")
	if dir == "" {
		t.Skip("a helper for scripts/screens-goals.mjs; set SCRIOBH_SEED_DIR to use it")
	}
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	user, project, doc := os.Getenv("SCRIOBH_SEED_USER"), os.Getenv("SCRIOBH_SEED_PROJECT"), os.Getenv("SCRIOBH_SEED_DOC")
	quiet := []int{0, 620, 310, 780, 540, 0, 180, 910, 460}
	var text []string
	prevWords := 0
	if err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
		for k := 75; k >= 1; k-- {
			n := quiet[k%len(quiet)]
			if k <= 4 {
				n = []int{800, 520, 700, 600}[k-1]
			}
			if k > 30 && k%2 == 0 {
				n = n / 2
			}
			if n == 0 {
				continue
			}
			old := strings.Join(text, " ")
			for i := 0; i < n; i++ {
				text = append(text, fmt.Sprintf("w%d_%d", k, i))
			}
			at := time.Now().UTC().AddDate(0, 0, -k).Truncate(24 * time.Hour).Add(11*time.Hour + 20*time.Minute)
			if err := goals.RecordSave(tx, goals.Save{UserID: user, ProjectID: project, DocumentID: doc, OldText: old, NewText: strings.Join(text, " "),
				OldWords: prevWords, NewWords: prevWords + n, At: at}); err != nil {
				return err
			}
			prevWords += n
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
