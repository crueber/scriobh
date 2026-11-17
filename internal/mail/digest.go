package mail

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// The digest (F-249).
//
// What it says, the minimum that is useful: for each project the person is on, how many new mentions, comments
// and suggestions are waiting for them, by the project's title, and a link to the app's own address. It is made
// from the person's own unread notifications, so it tells them nothing they could not already see in the app.
// It never carries manuscript text, comment text, a document title, an entity name or the name of another
// person. The subject is fixed. A digest with nothing to report is not sent, and the period it covers is
// marked done all the same.

// Digest frequencies.
const (
	Daily  = "daily"
	Weekly = "weekly"
)

// digestHour is the local hour a digest is due; each person's own offset (up to half an hour, from their id)
// spreads the sends.
const digestHour = 7

// DigestSubject is the subject of every digest.
const DigestSubject = "Your Scríobh digest"

const maxDigestProjects = 40

// periodKey names the period a moment falls in for a person's zone: the day, or the ISO week.
func periodKey(freq string, t time.Time) string {
	if freq == Weekly {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	}
	return t.Format("2006-01-02")
}

// slotStart is when a person's digest for the period of t is due: the day at digestHour, or the Monday of the week.
func slotStart(freq, userID string, t time.Time) time.Time {
	d := t
	if freq == Weekly {
		d = t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	jitter := time.Duration(h.Sum32()%1800) * time.Second
	return time.Date(d.Year(), d.Month(), d.Day(), digestHour, 0, 0, 0, t.Location()).Add(jitter)
}

func locationOf(tz string) *time.Location {
	if l, err := time.LoadLocation(tz); err == nil && tz != "" {
		return l
	}
	return time.UTC
}

// visibleProjectsFor is the one place that decides which projects a digest may say anything about: the live
// projects the person is a member of, trashed ones excluded for owners too. It returns id -> title.
//
// A project with a passphrase (a project_gates row) is left out for everyone: a digest has no session, so there is
// no unlock to honour, and a protected project is never named, not even by its counts. This is the default, not an
// option: Options.VisibleProjects replaces it only for a test. The builder uses only this function's answer, both
// when a digest is made and again when it is sent.
func (s *Service) visibleProjectsFor(ctx context.Context, userID string) (map[string]string, error) {
	if s.o.VisibleProjects != nil {
		return s.o.VisibleProjects(ctx, userID)
	}
	return s.defaultVisible(ctx, userID)
}

// defaultVisible is the live projects a person is a member of.
func (s *Service) defaultVisible(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT p.id, p.title FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.user_id = ? AND p.deleted_at IS NULL AND p.trashed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM project_gates g WHERE g.project_id = p.id)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

type digestCounts struct{ mentions, comments, suggestions int }

// BuildDigest makes the text of a digest for a person from their unread notifications created after since and up
// to until. It returns "" when there is nothing to report.
func (s *Service) BuildDigest(ctx context.Context, userID, since, until string) (string, error) {
	visible, err := s.visibleProjectsFor(ctx, userID)
	if err != nil {
		return "", err
	}
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT project_id, kind, COALESCE(sum(count), 0) FROM notifications
		WHERE user_id = ? AND read_at IS NULL AND created_at > ? AND created_at <= ? GROUP BY project_id, kind`, userID, since, until)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	per := map[string]*digestCounts{}
	for rows.Next() {
		var pid, kind string
		var n int
		if err := rows.Scan(&pid, &kind, &n); err != nil {
			return "", err
		}
		if _, ok := visible[pid]; !ok {
			continue
		}
		c := per[pid]
		if c == nil {
			c = &digestCounts{}
			per[pid] = c
		}
		switch kind {
		case "mention":
			c.mentions += n
		case "suggestion", "suggestion-decided":
			c.suggestions += n
		default: // comment, reply, resolved
			c.comments += n
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(per) == 0 {
		return "", nil
	}
	type item struct {
		title string
		c     *digestCounts
		id    string
	}
	var items []item
	total := 0
	for id, c := range per {
		items = append(items, item{flatText(visible[id], 80), c, id})
		total += c.mentions + c.comments + c.suggestions
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].title != items[j].title {
			return items[i].title < items[j].title
		}
		return items[i].id < items[j].id
	})
	var b strings.Builder
	b.WriteString("What is new for you in Scríobh.\n\n")
	fmt.Fprintf(&b, "You have %s waiting.\n\n", plural(total, "new notification", "new notifications"))
	for i, it := range items {
		if i == maxDigestProjects {
			fmt.Fprintf(&b, "  and %s more\n", plural(len(items)-i, "project", "projects"))
			break
		}
		var parts []string
		if it.c.mentions > 0 {
			parts = append(parts, plural(it.c.mentions, "mention", "mentions"))
		}
		if it.c.comments > 0 {
			parts = append(parts, plural(it.c.comments, "comment", "comments"))
		}
		if it.c.suggestions > 0 {
			parts = append(parts, plural(it.c.suggestions, "suggestion", "suggestions"))
		}
		fmt.Fprintf(&b, "  %s\n    %s\n", it.title, strings.Join(parts, ", "))
	}
	b.WriteString("\n")
	if base := s.baseURL(); base != "" {
		fmt.Fprintf(&b, "Open Scríobh: %s/\n", base)
		fmt.Fprintf(&b, "Stop these emails, or change how often they come: %s/settings\n", base)
	} else {
		b.WriteString("Open Scríobh to read them. You can stop these emails, or change how often they come, in your settings there.\n")
	}
	return b.String(), nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Service) digestLoop(ctx context.Context) {
	// A minute, and a little more or less, so instances do not tick together.
	t := time.NewTimer(time.Duration(float64(time.Minute) * (0.8 + 0.4*s.o.Rand())))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.DigestOnce(ctx)
		s.ProgressOnce(ctx)
		t.Reset(time.Duration(float64(time.Minute) * (0.8 + 0.4*s.o.Rand())))
	}
}

// DigestOnce makes the digests that are due now. A period is handled once: the person's row is advanced in the
// same transaction that queues the message (or finds nothing to say), so a restart never makes a second one.
// It returns how many messages it queued.
func (s *Service) DigestOnce(ctx context.Context) int {
	if s.blockedByRestore(ctx) {
		return 0
	}
	var any int
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mail_digest)`).Scan(&any); err != nil || any == 0 {
		return 0
	}
	cfg, err := loadConfig(ctx, s.o.DB.Read())
	if err != nil || !cfg.Enabled {
		return 0
	}
	type person struct {
		id, freq, last, cursor, email, tz, address string
	}
	// A person who has not been seen for 90 days (or who the identity provider removed, which ends their sessions)
	// gets no digest: it would go to someone who may not be able to sign in to turn it off.
	active := s.stamp(s.now().AddDate(0, 0, -90))
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT d.user_id, d.frequency, d.last_period, d.last_cursor, COALESCE(u.email,''), u.timezone, d.address
		FROM mail_digest d JOIN users u ON u.id = d.user_id WHERE u.deleted_at IS NULL AND u.disabled_at IS NULL AND u.is_agent = 0
		AND COALESCE(u.last_seen_at, u.created_at) >= ?`, active)
	if err != nil {
		return 0
	}
	var people []person
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.freq, &p.last, &p.cursor, &p.email, &p.tz, &p.address); err == nil {
			people = append(people, p)
		}
	}
	rows.Close()
	now := s.now()
	queued := 0
	for _, p := range people {
		if ctx.Err() != nil {
			return queued
		}
		local := now.In(locationOf(p.tz))
		key := periodKey(p.freq, local)
		if p.last >= key || local.Before(slotStart(p.freq, p.id, local)) {
			continue
		}
		if p.email == "" {
			continue // nothing to send to; the period stays open in case an address appears
		}
		if !strings.EqualFold(p.address, p.email) {
			continue // the account's address is not the one this opt-in was made for: it waits for the person to turn it on again
		}
		if _, err := ValidAddress(p.email); err != nil {
			continue
		}
		until := s.stamp(now)
		body, err := s.BuildDigest(ctx, p.id, p.cursor, until)
		if err != nil {
			s.o.Log.Warn("mail: building a digest failed", "err", err)
			continue
		}
		err = s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE mail_digest SET last_period = ?, last_cursor = ?, updated_at = ? WHERE user_id = ? AND last_period < ?`, key, until, until, p.id, key)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrDuplicate // another pass got here first
			}
			if body == "" {
				return nil
			}
			return s.EnqueueTx(ctx, tx, Job{Kind: KindDigest, PersonID: p.id, Recipient: p.email, Subject: DigestSubject, Body: body, Since: p.cursor, Until: until, DedupeKey: "digest:" + p.id + ":" + key})
		})
		switch {
		case err == nil && body != "":
			queued++
		case err == nil, err == ErrDuplicate:
		default:
			var le *LimitError
			if asLimit(err, &le) {
				s.warnOnce("digest-limit", "mail: a digest was held back by a limit", "scope", le.Scope)
			} else {
				s.o.Log.Warn("mail: queueing a digest failed", "err", err)
			}
		}
	}
	if queued > 0 {
		s.Wake()
	}
	return queued
}

// resetDigests makes every person's next digest the first one after now: used when mail is switched on, so
// nothing that happened while it was off is ever sent.
func resetDigests(tx *sql.Tx, now time.Time) error {
	rows, err := tx.Query(`SELECT d.user_id, d.frequency, u.timezone FROM mail_digest d JOIN users u ON u.id = d.user_id`)
	if err != nil {
		return err
	}
	type r struct{ id, freq, tz string }
	var all []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.freq, &x.tz); err != nil {
			rows.Close()
			return err
		}
		all = append(all, x)
	}
	rows.Close()
	for _, x := range all {
		key := periodKey(x.freq, now.In(locationOf(x.tz)))
		if _, err := tx.Exec(`UPDATE mail_digest SET last_period = ?, last_cursor = ?, updated_at = ? WHERE user_id = ?`, key, db.Format(now), db.Format(now), x.id); err != nil {
			return err
		}
	}
	return nil
}
