package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
)

// The progress email (F-509): a person's own weekly or monthly summary of their writing, sent to their account address.
//
// It is off until a person turns it on, bound to the address it was turned on for, and made like the digest: once per
// period (the schedule row and the queued message are written in one transaction, and the message has a dedupe key), at
// 07:00 local time plus up to half an hour, never for a period that has nothing to say, and made again from its window at
// the moment it is sent. What it says is numbers and plain words: the words added in the period and in the one before,
// the writing days, the minutes, the streak, the goals counted and the days a daily goal was met. It never carries a word
// of any text, the title of a document, the name of a project or a comment. It is built through visibleProjectsFor, so a
// project with a passphrase, one in the trash or one the person left contributes nothing; a period in which that leaves
// nothing to report sends nothing. It holds no tracking of any kind, and its only addresses are this server's own.
//
// A restore pauses it with the rest of the mail (PauseAll empties the queue and switches mail off; switching mail on again
// starts every person at the period after that moment), and an unsubscribe is the app's own settings page.

// Progress frequencies.
const (
	ProgressWeekly  = "weekly"
	ProgressMonthly = "monthly"
)

// ProgressSubject is the subject of every progress email.
const ProgressSubject = "Your Scríobh writing summary"

// coveredPeriod is the last whole period before the local moment t: the ISO week before the one t is in, or the calendar
// month before t's. key names it ("2026-W10", "2026-03"); from and to are its first and last local dates.
func coveredPeriod(freq string, t time.Time) (key, from, to string) {
	if freq == ProgressMonthly {
		first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		prev := first.AddDate(0, -1, 0)
		return prev.Format("2006-01"), prev.Format("2006-01-02"), first.AddDate(0, 0, -1).Format("2006-01-02")
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	monday := d.AddDate(0, 0, -((int(d.Weekday())+6)%7)-7)
	y, w := monday.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w), monday.Format("2006-01-02"), monday.AddDate(0, 0, 6).Format("2006-01-02")
}

// progressSlot is when a person's message for the period before t is due: Monday of t's week, or the first of t's month, at
// 07:00 local plus up to half an hour of the person's own offset.
func progressSlot(freq, userID string, t time.Time) time.Time {
	d := t
	if freq == ProgressMonthly {
		d = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	} else {
		d = t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte("progress:" + userID))
	return time.Date(d.Year(), d.Month(), d.Day(), digestHour, 0, 0, 0, t.Location()).Add(time.Duration(h.Sum32()%1800) * time.Second)
}

// BuildProgress makes the text of the message for the local dates from..to, or "" when there is nothing to say.
func (s *Service) BuildProgress(ctx context.Context, userID, from, to string) (string, error) {
	visible, err := s.visibleProjectsFor(ctx, userID)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(visible))
	for id := range visible {
		ids = append(ids, id)
	}
	v, err := studio.ViewerFor(ctx, s.o.DB.Read(), userID, ids)
	if err != nil {
		return "", err
	}
	r, err := v.Report(from, to)
	if err != nil {
		return "", err
	}
	if r.Empty() {
		return "", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your writing, %s to %s.\n\n", r.From, r.To)
	fmt.Fprintf(&b, "Words added: %d (the period before: %d)\n", r.Words, r.PreviousWords)
	// What was done, never a count of days against the days there were.
	fmt.Fprintf(&b, "You wrote on %s.\n", plural(r.WritingDays, "day", "days"))
	if r.Minutes > 0 {
		fmt.Fprintf(&b, "Time writing: %s\n", plural(r.Minutes, "minute", "minutes"))
	}
	// what was done is said; a nought is not (no "met your goal on 0 days", no "0 days in a row")
	if r.HasDailyGoal && r.GoalDaysMet > 0 {
		fmt.Fprintf(&b, "You met your daily goal on %s.\n", plural(r.GoalDaysMet, "day", "days"))
	}
	if r.HasDailyGoal && r.Streak > 0 {
		fmt.Fprintf(&b, "Days in a row now: %d (longest %d)\n", r.Streak, r.Longest)
	}
	if r.GoalsActive+r.GoalsDone > 0 {
		fmt.Fprintf(&b, "Goals: %d active, %d completed\n", r.GoalsActive, r.GoalsDone)
	}
	b.WriteString("\nThese are only numbers. Your writing is never in this email.\n\n")
	if base := s.baseURL(); base != "" {
		fmt.Fprintf(&b, "Open Scríobh: %s/\n", base)
		fmt.Fprintf(&b, "Stop these emails, or change how often they come: %s/settings\n", base)
	} else {
		b.WriteString("You can stop these emails, or change how often they come, in your settings in Scríobh.\n")
	}
	return b.String(), nil
}

// ProgressOnce queues the progress emails that are due now. It returns how many messages it queued.
func (s *Service) ProgressOnce(ctx context.Context) int {
	if s.blockedByRestore(ctx) {
		return 0
	}
	var any int
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mail_progress)`).Scan(&any); err != nil || any == 0 {
		return 0
	}
	cfg, err := loadConfig(ctx, s.o.DB.Read())
	if err != nil || !cfg.Enabled {
		return 0
	}
	type person struct{ id, freq, last, email, tz, address string }
	active := s.stamp(s.now().AddDate(0, 0, -90)) // as for the digest: not for someone who may be unable to sign in to turn it off
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT d.user_id, d.frequency, d.last_period, COALESCE(u.email,''), u.timezone, d.address
		FROM mail_progress d JOIN users u ON u.id = d.user_id WHERE u.deleted_at IS NULL AND u.disabled_at IS NULL AND u.is_agent = 0
		AND COALESCE(u.last_seen_at, u.created_at) >= ?`, active)
	if err != nil {
		return 0
	}
	var people []person
	for rows.Next() {
		var p person
		if err := rows.Scan(&p.id, &p.freq, &p.last, &p.email, &p.tz, &p.address); err == nil {
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
		key, from, to := coveredPeriod(p.freq, local)
		if p.last >= key || local.Before(progressSlot(p.freq, p.id, local)) {
			continue
		}
		if p.email == "" || !strings.EqualFold(p.address, p.email) {
			continue // the address this was turned on for is not the account's: it waits for the person to turn it on again
		}
		if _, err := ValidAddress(p.email); err != nil {
			continue
		}
		body, err := s.BuildProgress(ctx, p.id, from, to)
		if err != nil {
			s.o.Log.Warn("mail: building a progress email failed", "err", err)
			continue
		}
		err = s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE mail_progress SET last_period = ?, updated_at = ? WHERE user_id = ? AND last_period < ?`, key, s.stamp(now), p.id, key)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrDuplicate
			}
			if body == "" {
				return nil
			}
			return s.EnqueueTx(ctx, tx, Job{Kind: KindProgress, PersonID: p.id, Recipient: p.email, Subject: ProgressSubject, Body: body, Since: from, Until: to, DedupeKey: "progress:" + p.id + ":" + key})
		})
		switch {
		case err == nil && body != "":
			queued++
		case err == nil, errors.Is(err, ErrDuplicate):
		default:
			var le *LimitError
			if asLimit(err, &le) {
				s.warnOnce("progress-limit", "mail: a progress email was held back by a limit", "scope", le.Scope)
			} else {
				s.o.Log.Warn("mail: queueing a progress email failed", "err", err)
			}
		}
	}
	if queued > 0 {
		s.Wake()
	}
	return queued
}

// resetProgress makes every person's next message the first one for a period that ends after now: used when mail is
// switched on, so nothing that happened while it was off is ever sent.
func resetProgress(tx *sql.Tx, now time.Time) error {
	rows, err := tx.Query(`SELECT d.user_id, d.frequency, u.timezone FROM mail_progress d JOIN users u ON u.id = d.user_id`)
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
		key, _, _ := coveredPeriod(x.freq, now.In(locationOf(x.tz)))
		if _, err := tx.Exec(`UPDATE mail_progress SET last_period = ?, updated_at = ? WHERE user_id = ?`, key, db.Format(now), x.id); err != nil {
			return err
		}
	}
	return nil
}

// ---- the ops ------------------------------------------------------------------------------------------------

// ProgressGetIn has nothing.
type ProgressGetIn struct{}

// ProgressOut is a person's progress email setting.
type ProgressOut struct {
	MailAvailable bool   `json:"mail_available" desc:"The administrator has switched mail on."`
	Enabled       bool   `json:"enabled"`
	Frequency     string `json:"frequency,omitempty" enum:"weekly,monthly"`
	NeedsConfirm  bool   `json:"needs_confirm,omitempty" desc:"Your account address changed since you turned it on, so it stopped. Turn it on again to send it to the new address."`
	Email         string `json:"email,omitempty" desc:"Your account address from the sign-in provider: the only place it goes."`
	Contents      string `json:"contents" desc:"What it says."`
}

const progressContents = "Numbers and plain words only: the words you added in the week or month, your writing days, your streak and the goals you have. Never a word of your writing, a document's title or a project's name. Nothing is sent for a period in which you wrote nothing."

func progressGet(c *ops.Ctx, _ ProgressGetIn) (ProgressOut, error) {
	out := ProgressOut{MailAvailable: mailAvailable(c, c.DB()), Contents: progressContents}
	var email sql.NullString
	_ = c.DB().Read().QueryRowContext(c, `SELECT email FROM users WHERE id = ?`, c.P.UserID).Scan(&email)
	out.Email = email.String
	var bound string
	err := c.DB().Read().QueryRowContext(c, `SELECT frequency, address FROM mail_progress WHERE user_id = ?`, c.P.UserID).Scan(&out.Frequency, &bound)
	if err == nil {
		out.Enabled = true
		if !strings.EqualFold(bound, email.String) {
			out.Enabled, out.NeedsConfirm = false, true
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, ops.Internal(err)
	}
	return out, nil
}

// ProgressSetIn turns the progress email on or off.
type ProgressSetIn struct {
	Enabled   bool   `json:"enabled" desc:"On or off. Off removes your subscription."`
	Frequency string `json:"frequency,omitempty" enum:"weekly,monthly" desc:"weekly (Monday morning, for the week before) or monthly (the first of the month, for the month before), in your time zone. Required when turning it on."`
}

// Validate checks the frequency.
func (in *ProgressSetIn) Validate() error {
	if !in.Enabled {
		return nil
	}
	if in.Frequency != ProgressWeekly && in.Frequency != ProgressMonthly {
		return ops.Invalid("frequency", "Choose how often: weekly or monthly.")
	}
	return nil
}

func progressSet(c *ops.Ctx, in ProgressSetIn) (ProgressOut, error) {
	if !in.Enabled {
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(c, `DELETE FROM mail_progress WHERE user_id = ?`, c.P.UserID); err != nil {
				return err
			}
			_, err := tx.ExecContext(c, `UPDATE mail_outbox SET state = 'dead', last_error = 'cancelled', finished_at = ?, recipient = '', subject = '', body = '', claimed_at = ''
				WHERE person_id = ? AND kind = 'progress' AND state = 'pending'`, db.Now(), c.P.UserID)
			return err
		})
		if err != nil {
			return ProgressOut{}, ops.Internal(err)
		}
		return progressGet(c, ProgressGetIn{})
	}
	var email sql.NullString
	var agent int
	var tz string
	if err := c.DB().Read().QueryRowContext(c, `SELECT email, is_agent, timezone FROM users WHERE id = ? AND deleted_at IS NULL AND disabled_at IS NULL`, c.P.UserID).Scan(&email, &agent, &tz); err != nil {
		return ProgressOut{}, ops.NotFound("page")
	}
	if agent != 0 {
		return ProgressOut{}, ops.Forbidden("An agent account has no progress email.")
	}
	if _, err := ValidAddress(email.String); err != nil {
		return ProgressOut{}, ops.Conflict("Your account has no email address from the sign-in provider, so there is nowhere to send it.")
	}
	if !mailAvailable(c, c.DB()) {
		return ProgressOut{}, ops.ConflictWith("Email is not switched on for this server. Ask its administrator.", map[string]any{"reason": "mail_off"})
	}
	now := db.NowTime()
	key, _, _ := coveredPeriod(in.Frequency, now.In(locationOf(tz)))
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cur, bound string
		err := tx.QueryRowContext(c, `SELECT frequency, address FROM mail_progress WHERE user_id = ?`, c.P.UserID).Scan(&cur, &bound)
		if err == nil && cur == in.Frequency && strings.EqualFold(bound, email.String) {
			return nil // already on, as asked, for this address: the schedule is left alone
		}
		// A new opt-in, a new frequency or a new address: it starts now, for this address. The period just over is not
		// sent (it is the last_period); the first message is for the next whole one.
		_, err = tx.ExecContext(c, `INSERT INTO mail_progress(user_id, frequency, address, last_period, enabled_at, updated_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET frequency = excluded.frequency, address = excluded.address, last_period = excluded.last_period, updated_at = excluded.updated_at`,
			c.P.UserID, in.Frequency, strings.ToLower(email.String), key, db.Format(now), db.Format(now))
		return err
	})
	if err != nil {
		return ProgressOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "mail.progress.set", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"frequency": in.Frequency}})
	return progressGet(c, ProgressGetIn{})
}
