// Package goals holds writing goals and the per-person writing statistics
// behind them: sessions, daily totals, streaks, and the ops that read them.
//
// Goals count words added. A cut is work; the net change is stored beside it
// and is never used to fail a day. Everything here is per person: a member's
// ops read only their own rows, and no op takes a user id from the request.
//
// The package is a leaf (it imports db, ops and nothing from the other domain
// packages) because doc.update calls RecordSave from inside its transaction.
package goals

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"
	_ "time/tzdata" // a zone name must resolve the same on every platform, desktop builds included

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// Session rules.
const (
	// SessionGap is the longest pause between saves that keeps a session going.
	SessionGap = 30 * time.Minute
	// IdleCap caps one gap between saves when adding up active time: a person
	// who saved, made tea, and saved again was not writing for the whole gap.
	IdleCap = 5 * time.Minute
)

const dayLayout = "2006-01-02"

// Save describes one content save, for RecordSave.
type Save struct {
	UserID, ProjectID, DocumentID string
	// OldText and NewText are the document body before and after the save;
	// OldWords and NewWords are the stored word counts (text.WordCount).
	OldText, NewText   string
	OldWords, NewWords int
	// NoCredit records the change in net words but earns no words added. Use it
	// for writes that are not typing: restoring a revision, an undo of a
	// restore. The document's baseline is reset to the new text so that what was
	// restored is not credited when the person types on.
	NoCredit bool
	// At is when the save happened; zero means now (db.NowTime).
	At time.Time
}

var zones sync.Map // name -> *time.Location

// location resolves a stored zone name. An unknown or empty name is UTC: stats
// must never fail a save because a profile holds a stale zone.
func location(name string) (*time.Location, string) {
	if name == "" || name == "Local" {
		return time.UTC, "UTC"
	}
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location), name
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, "UTC"
	}
	zones.Store(name, l)
	return l, name
}

// Location resolves a stored zone name as the save loop does: an unknown or empty name is UTC. It returns the
// zone's name as used ("UTC" for the fallback).
func Location(name string) (*time.Location, string) { return location(name) }

// LocalDay is the calendar date of t in the zone named, YYYY-MM-DD.
func LocalDay(t time.Time, zone string) string {
	l, _ := location(zone)
	return t.In(l).Format(dayLayout)
}

// RecordSave adds one content save to the person's writing session and daily
// totals, inside the caller's transaction. It runs in a savepoint: if recording
// fails, only the statistics are rolled back and the error is returned for the
// caller to log. A statistics fault must never fail a save.
func RecordSave(tx *sql.Tx, s Save) (err error) {
	if s.UserID == "" || s.ProjectID == "" || s.DocumentID == "" {
		return errors.New("goals: a save needs a user, a project and a document")
	}
	if _, err := tx.Exec(`SAVEPOINT goals_record`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = tx.Exec(`ROLLBACK TO goals_record`)
		}
		_, _ = tx.Exec(`RELEASE goals_record`)
	}()
	return record(tx, s)
}

func record(tx *sql.Tx, s Save) error {
	now := s.At
	if now.IsZero() {
		now = db.NowTime()
	}
	now = now.UTC()
	var zoneName string
	if err := tx.QueryRow(`SELECT timezone FROM users WHERE id = ?`, s.UserID).Scan(&zoneName); err != nil {
		return err
	}
	loc, zoneName := location(zoneName)
	day := now.In(loc).Format(dayLayout)

	// The latest session decides whether this save continues it.
	var (
		sid, sday, stz, ended string
	)
	err := tx.QueryRow(`SELECT id, day, tz, ended_at FROM writing_sessions
		WHERE user_id = ? AND project_id = ? ORDER BY ended_at DESC, id DESC LIMIT 1`, s.UserID, s.ProjectID).
		Scan(&sid, &sday, &stz, &ended)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var gap time.Duration
	continued := false
	if found && sday == day && stz == zoneName {
		if last, perr := db.Parse(ended); perr == nil {
			gap = now.Sub(last)
			if gap < 0 {
				gap = 0 // the clock stepped back: treat it as the same instant
			}
			continued = gap <= SessionGap
		}
	}

	stamp := db.Format(now)
	newSession := !continued
	if newSession {
		prev := sid
		sid = db.NewID("ses")
		if _, err := tx.Exec(`INSERT INTO writing_sessions(id, user_id, project_id, document_id, day, tz, started_at, ended_at)
			VALUES (?,?,?,?,?,?,?,?)`, sid, s.UserID, s.ProjectID, s.DocumentID, day, zoneName, stamp, stamp); err != nil {
			return err
		}
		if found {
			// Only the latest session keeps baselines.
			if _, err := tx.Exec(`DELETE FROM writing_session_docs WHERE session_id = ?`, prev); err != nil {
				return err
			}
		}
	}

	// Credit for this document in this session.
	var (
		base           string
		baseWords      int
		carried, total int
		lastHash       string
	)
	oldHash := hashText(s.OldText)
	oldWords := s.OldWords // what the net change is measured from
	err = tx.QueryRow(`SELECT baseline_text, baseline_words, carried, credit, last_hash FROM writing_session_docs
		WHERE session_id = ? AND document_id = ?`, sid, s.DocumentID).Scan(&base, &baseWords, &carried, &total, &lastHash)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return err
	}
	// rebased means the baseline is replaced by the text as it stood before this
	// save. It happens when the session first sees the document, and when the
	// text under it is not the text the session left: another member saved or
	// restored in between, and what they wrote must not be credited to this person.
	rebased := fresh
	if fresh {
		base, carried, total = s.OldText, 0, 0
	} else if lastHash != oldHash {
		carried, base, rebased = total, s.OldText, true
	}
	if rebased {
		// The stored count may come from an older counter (a document written
		// before the current one, or by a path that does not keep it): measure
		// from the text itself.
		baseWords = text.WordCount(s.OldText)
		oldWords = baseWords
	}
	newTotal := total
	if s.NoCredit {
		// Not typing: record the net change, earn nothing, and measure from here.
		carried, base, baseWords, rebased = total, s.NewText, s.NewWords, true
	} else {
		diff := addedWords(base, s.NewText)
		if carried+diff < total {
			// Words that earned credit have left the text (cut, or rewritten).
			// Bank the credit and measure from the text as it stood before this
			// save, so that new writing adds to what was earned instead of
			// competing with it, and what was just cut and pasted back is not
			// credited a second time.
			carried, base, baseWords, rebased = total, s.OldText, oldWords, true
			diff = addedWords(base, s.NewText)
		}
		// Never credit less than the growth the authoritative word count shows.
		if grew := s.NewWords - baseWords; grew > diff {
			diff = grew
		}
		newTotal = max(total, carried+diff)
	}
	stamp0 := db.Format(now)
	if fresh {
		if _, err := tx.Exec(`INSERT INTO writing_session_docs(session_id, document_id, baseline_text, baseline_words, carried, credit, last_hash, touched_at)
			VALUES (?,?,?,?,?,?,?,?)`, sid, s.DocumentID, base, baseWords, carried, newTotal, hashText(s.NewText), stamp0); err != nil {
			return err
		}
	} else if rebased {
		if _, err := tx.Exec(`UPDATE writing_session_docs SET baseline_text = ?, baseline_words = ?, carried = ?, credit = ?, last_hash = ?, touched_at = ?
			WHERE session_id = ? AND document_id = ?`, base, baseWords, carried, newTotal, hashText(s.NewText), stamp0, sid, s.DocumentID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE writing_session_docs SET credit = ?, last_hash = ?, touched_at = ? WHERE session_id = ? AND document_id = ?`,
		newTotal, hashText(s.NewText), stamp0, sid, s.DocumentID); err != nil {
		return err
	}
	// Baselines are copies of manuscript text: do not keep those of sessions that
	// ended long ago, whoever they belonged to.
	if _, err := tx.Exec(`DELETE FROM writing_session_docs WHERE touched_at < ?`, db.Format(now.Add(-2*SessionGap))); err != nil {
		return err
	}
	added := newTotal - total
	net := s.NewWords - oldWords

	step := min(gap, IdleCap)
	if newSession {
		step = 0
	}
	seconds := int(step / time.Second)
	if _, err := tx.Exec(`UPDATE writing_sessions SET ended_at = CASE WHEN ended_at > ?1 THEN ended_at ELSE ?1 END,
		words_added = words_added + ?2, words_net = words_net + ?3, active_seconds = active_seconds + ?4, saves = saves + 1
		WHERE id = ?5`, stamp, added, net, seconds, sid); err != nil {
		return err
	}
	if err := addToDay(tx, s.UserID, s.ProjectID, day, stamp, added, net, seconds, newSession); err != nil {
		return err
	}
	// The sprint is a view of the same additions, never part of the day's own record: a fault in its table (locked, missing,
	// corrupt) is rolled back alone in a savepoint of its own and logged by class, and the day's statistics commit.
	sprintSafely(tx, s.UserID, s.ProjectID, stamp, added, net)
	return nil
}

func sprintSafely(tx *sql.Tx, userID, projectID, stamp string, added, net int) {
	if added == 0 && net == 0 {
		return
	}
	if _, err := tx.Exec(`SAVEPOINT goals_sprint`); err != nil {
		return
	}
	if err := addToSprint(tx, userID, projectID, stamp, added, net); err != nil {
		_, _ = tx.Exec(`ROLLBACK TO goals_sprint`)
		slog.Warn("goals: a save was not added to the running sprint", "class", "sprint_table")
	}
	_, _ = tx.Exec(`RELEASE goals_sprint`)
}

// addToSprint adds a save's numbers to the person's running sprint on this project, if there is one: the same
// numbers the day just received, in the same transaction, so the words are counted once in the day they were written and once
// in the sprint they were written during (a sprint that runs past local midnight holds the words of both days; no day
// holds them twice). A sprint whose closing time has passed takes nothing (the rule that ends a sprint left running); the
// one index probe is all a save pays when none is running.
func addToSprint(tx *sql.Tx, userID, projectID, stamp string, added, net int) error {
	if added == 0 && net == 0 {
		return nil
	}
	_, err := tx.Exec(`UPDATE studio_sprints SET words_added = words_added + ?, words_net = words_net + ?, saves = saves + 1
		WHERE user_id = ? AND state = 'running' AND project_id = ? AND closes_at >= ?`, added, net, userID, projectID, stamp)
	return err
}

// RecordChapterDone notes that the person finished a chapter (set its status to done) for the first time, on their
// local day. It is the record a chapters goal counts. Only a document of kind chapter is noted, and only once per
// person; it runs in a savepoint like RecordSave, so a fault in it never fails the write that called it.
func RecordChapterDone(tx *sql.Tx, userID, projectID, documentID string) (err error) {
	if _, err := tx.Exec(`SAVEPOINT goals_chapter`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = tx.Exec(`ROLLBACK TO goals_chapter`)
		}
		_, _ = tx.Exec(`RELEASE goals_chapter`)
	}()
	var zoneName string
	if err := tx.QueryRow(`SELECT timezone FROM users WHERE id = ?`, userID).Scan(&zoneName); err != nil {
		return err
	}
	loc, _ := location(zoneName)
	now := db.NowTime()
	_, err = tx.Exec(`INSERT OR IGNORE INTO studio_chapters_done(user_id, document_id, project_id, day, created_at)
		SELECT ?1, ?2, ?3, ?4, ?5 WHERE EXISTS (SELECT 1 FROM documents WHERE id = ?2 AND kind = 'chapter')`,
		userID, documentID, projectID, now.In(loc).Format(dayLayout), db.Format(now))
	return err
}

// addToDay adds a save's numbers to the day's totals and re-reads the goal.
func addToDay(tx *sql.Tx, userID, projectID, day, stamp string, added, net, seconds int, newSession bool) error {
	sessions := 0
	if newSession {
		sessions = 1
	}
	target, err := dailyTarget(tx, userID, projectID, day)
	if err != nil {
		return err
	}
	// A day that has been hit stays hit, with the target that was in force when
	// it was hit: raising the goal later never takes a finished day away.
	_, err = tx.Exec(`INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, goal_target, goal_hit, updated_at)
		VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11)
		ON CONFLICT(user_id, project_id, day) DO UPDATE SET
		  words_added = words_added + excluded.words_added,
		  words_net = words_net + excluded.words_net,
		  active_seconds = active_seconds + excluded.active_seconds,
		  minutes = (active_seconds + excluded.active_seconds + 59) / 60,
		  sessions = sessions + excluded.sessions,
		  goal_target = CASE WHEN goal_hit = 1 THEN goal_target ELSE excluded.goal_target END,
		  goal_hit = CASE WHEN goal_hit = 1 THEN 1
		                  WHEN excluded.goal_target > 0 AND words_added + excluded.words_added >= excluded.goal_target THEN 1 ELSE 0 END,
		  updated_at = excluded.updated_at`,
		userID, projectID, day, added, net, (seconds+59)/60, sessions, seconds, target, hitFor(target, added), stamp)
	return err
}

func hitFor(target, added int) int {
	if target > 0 && added >= target {
		return 1
	}
	return 0
}

// dailyTarget is the person's daily goal in force on a local day: 0 for none.
func dailyTarget(tx *sql.Tx, userID, projectID, day string) (int, error) {
	var t int
	err := tx.QueryRow(`SELECT target_words FROM goals WHERE user_id = ? AND project_id = ? AND kind = 'daily'
		AND (end_date = '' OR end_date >= ?)`, userID, projectID, day).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return t, err
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
