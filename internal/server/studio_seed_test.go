package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// TestSeedStudioHistory is not a test. The browser scripts (scripts/seed-studio.mjs, for e2e-studio.mjs and the accessibility
// walk) run it against a running development server's data directory to give a person a back-dated writing history, which no
// op can do (an op writes at the present). It writes the rows the save loop keeps, one day row and one session a day, as the
// bench fixture does (internal/bench/studio.go), through a connection of its own that waits for the server's writer. It does
// nothing unless the variables are set:
//
//	SCRIOBH_SEED_DIR   the data directory
//	SCRIOBH_SEED_ROWS  a JSON file: {"user": id, "project": id, "rows": [{"day": "2026-03-01", "words": 120, "net": 120, "minutes": 5, "hour": 9, "target": 100, "hit": true}]}
func TestSeedStudioHistory(t *testing.T) {
	dir, file := os.Getenv("SCRIOBH_SEED_DIR"), os.Getenv("SCRIOBH_SEED_ROWS")
	if dir == "" || file == "" {
		t.Skip("a helper for scripts/seed-studio.mjs; set SCRIOBH_SEED_DIR and SCRIOBH_SEED_ROWS to use it")
	}
	var in struct {
		User    string `json:"user"`
		Project string `json:"project"`
		Rows    []struct {
			Day     string `json:"day"`
			Words   int    `json:"words"`
			Net     int    `json:"net"`
			Minutes int    `json:"minutes"`
			Hour    int    `json:"hour"`
			Target  int    `json:"target"`
			Hit     bool   `json:"hit"`
		} `json:"rows"`
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	c, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "scriobh.db")+"?_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tx, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // a commit below makes this a no-op
	now := db.Now()
	for _, r := range in.Rows {
		day, err := time.Parse("2006-01-02", r.Day)
		if err != nil {
			t.Fatal(err)
		}
		secs, hit := r.Minutes*60, 0
		if r.Hit {
			hit = 1
		}
		if _, err := tx.Exec(`INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, goal_target, goal_hit, updated_at)
			VALUES (?,?,?,?,?,?,1,?,?,?,?)`, in.User, in.Project, r.Day, r.Words, r.Net, r.Minutes, secs, r.Target, hit, now); err != nil {
			t.Fatal(fmt.Errorf("day %s: %w", r.Day, err))
		}
		start := day.Add(time.Duration(r.Hour)*time.Hour + 15*time.Minute)
		if _, err := tx.Exec(`INSERT INTO writing_sessions(id, user_id, project_id, document_id, day, tz, started_at, ended_at, words_added, words_net, active_seconds, saves)
			VALUES (?,?,?,NULL,?,'UTC',?,?,?,?,?,3)`, db.NewID("ses"), in.User, in.Project, r.Day, db.Format(start), db.Format(start.Add(time.Duration(secs)*time.Second)), r.Words, r.Net, secs); err != nil {
			t.Fatal(fmt.Errorf("session %s: %w", r.Day, err))
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
