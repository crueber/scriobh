// Package review is the review half of Phase 5: comments anchored to the text,
// threads, the writer's review queue, suggestions from a reviewer, and the
// in-app notifications they raise. A share link's guest comments (internal/
// domain/share) are written by the same code (InsertComment), so a guest and a
// member cannot be held to different rules.
//
// What review data is not: it is not document content. It is never in documents
// .content, an export, the Markdown mirror, a Git push, the search text, a word
// count or an AI context, and the events about it carry ids and counts only.
// See docs/OPEN-QUESTIONS.md, "Phase 5 review and sharing decisions".
package review

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Limits. They are constants, not settings: they exist so that a project's review
// data stays small enough to list in tens of milliseconds.
const (
	MaxBody               = 10000 // characters in a comment
	MaxGuestBody          = 2000
	MaxGuestName          = 60
	MaxMentions           = 20
	MaxCommentsPerProject = 20000 // live ones: a deleted comment is not counted
	// MaxCommentsPerMemberPerHour bounds what one signed-in person writes on one project in an hour.
	MaxCommentsPerMemberPerHour = 60
	MaxRepliesPerThread         = 500
	EditWindow                  = 15 * time.Minute // how long the author of a comment may reword it
	DefaultPage                 = 50
	MaxPage                     = 200
)

// Comment kinds.
const (
	KindComment        = "comment"
	KindQuestion       = "question"
	KindSuggestionNote = "suggestion-note"
)

// Statuses of a thread.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// Register adds the review ops.
func Register(r *ops.Registry) {
	l := docs.LeasesOf(r.Env())
	registerComments(r)
	registerQueue(r)
	registerSuggestions(r, l)
	registerNotifications(r)
}

// ---- text of a comment ------------------------------------------------------------------------

// CleanBody validates and normalises the text of a comment: valid UTF-8, line
// endings as LF, no control characters, no invisible or direction-overriding
// characters (a comment is shown next to the manuscript and must not be able to
// disguise itself), trimmed, and within the limit.
func CleanBody(s string, max int) (string, *ops.Error) {
	if !utf8.ValidString(s) {
		return "", ops.Invalid("body", "That text is not valid UTF-8.")
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = text.StripInvisible(s)
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "", ops.Invalid("body", "A comment cannot hold control characters.")
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ops.Invalid("body", "Write something.")
	}
	if utf8.RuneCountInString(s) > max {
		return "", ops.Invalid("body", "A comment is limited to "+itoa(max)+" characters.")
	}
	return s, nil
}

// CleanGuestName validates the name a guest of a share link gives.
func CleanGuestName(s string) (string, *ops.Error) {
	s = strings.Join(strings.Fields(text.StripInvisible(s)), " ")
	if s == "" {
		return "", ops.Invalid("name", "Say who you are: a first name is enough.")
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxGuestName {
		return "", ops.Invalid("name", "A name is limited to "+itoa(MaxGuestName)+" characters.")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "", ops.Invalid("name", "A name cannot hold control characters.")
		}
	}
	return s, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---- people ----------------------------------------------------------------------------------------

// Person is a member who can be mentioned.
type Person struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// querier is what the read helpers need; a *sql.DB and a *sql.Tx both are one.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// memberRole returns a person's role on a project, "" when they have none. An
// instance administrator acts as the owner, as ops.Env.ProjectRole does.
func memberRole(ctx context.Context, q querier, userID, projectID string) string {
	var role string
	err := q.QueryRowContext(ctx, `SELECT m.role FROM project_members m WHERE m.project_id = ? AND m.user_id = ?`, projectID, userID).Scan(&role)
	if err == nil {
		return role
	}
	var inst string
	if q.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&inst) == nil && inst == ops.RoleAdmin {
		return ops.ProjectOwner
	}
	return ""
}

func atLeast(role, min string) bool {
	rank := map[string]int{ops.ProjectReader: 1, ops.ProjectReviewer: 2, ops.ProjectEditor: 3, ops.ProjectOwner: 4}
	return rank[role] >= rank[min] && rank[role] > 0
}

// commenters are the people who can see comments: the owner and the editors and
// reviewers. A reader cannot, so a reader is never mentioned.
func commenters(ctx context.Context, q querier, projectID string) ([]Person, error) {
	rows, err := q.QueryContext(ctx, `SELECT u.id, COALESCE(NULLIF(u.display_name, ''), 'Member') FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ? AND m.role IN ('owner','editor','reviewer') AND u.deleted_at IS NULL AND u.disabled_at IS NULL ORDER BY u.display_name, u.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.UserID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// mentionsIn finds the people a body mentions with @name: the longest display
// name of a commenter that follows an @ at a word boundary, compared without
// regard to case or spacing. Only people who can see comments are resolved, so
// this tells a writer nothing they could not already list with comment.people.
func mentionsIn(body string, people []Person) []string {
	if !strings.Contains(body, "@") || len(people) == 0 {
		return nil
	}
	fold := func(s string) string { return text.FoldTitle(s) }
	var found []string
	seen := map[string]bool{}
	for i := 0; i < len(body); i++ {
		if body[i] != '@' {
			continue
		}
		if i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(body[:i]); r != ' ' && r != '\n' && r != '\t' && r != '(' && r != '"' {
				continue
			}
		}
		rest := body[i+1:]
		best, bestLen := "", 0
		for _, p := range people {
			n := len(p.Name)
			if n == 0 || n > len(rest) || !utf8.ValidString(rest[:n]) || fold(rest[:n]) != fold(p.Name) {
				continue
			}
			if n < len(rest) {
				if r, _ := utf8.DecodeRuneInString(rest[n:]); isWordRune(r) {
					continue
				}
			}
			if n > bestLen {
				best, bestLen = p.UserID, n
			}
		}
		if best != "" && !seen[best] {
			seen[best] = true
			found = append(found, best)
		}
	}
	return found
}

func isWordRune(r rune) bool {
	return r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 0x80 && !isSpaceRune(r)
}

func isSpaceRune(r rune) bool {
	return r == 0xa0 || r == 0x2009 || r == 0x202f || r == 0x3000
}

// ---- notifications ---------------------------------------------------------------------------

// maxNotificationsPerUser caps the rows one person keeps that came from people; the oldest go first.
// Rows that came from the guests of a share link have a cap of their own, so a flood of guest
// comments can never push a mention or a decision out.
const (
	maxNotificationsPerUser = 500
	maxGuestNotifications   = 100
)

// notifyGuest records that guests commented in a thread: one row per (link, thread) and hour, with a
// count, unread; a new one when the last was read or is older than an hour.
func notifyGuest(tx *sql.Tx, userID, projectID, shareID, threadID, documentID, now string) error {
	if userID == "" {
		return nil
	}
	hour := db.Format(db.NowTime().Add(-time.Hour))
	res, err := tx.Exec(`UPDATE notifications SET count = count + 1, created_at = ? WHERE user_id = ? AND share_id = ? AND comment_id = ? AND kind = 'comment' AND read_at IS NULL AND created_at > ?`,
		now, userID, shareID, threadID, hour)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO notifications(id, user_id, project_id, kind, comment_id, document_id, share_id, count, created_at) VALUES (?,?,?,'comment',?,?,?,1,?)`,
		db.NewID("ntf"), userID, projectID, threadID, nullStr(documentID), shareID, now); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM notifications WHERE user_id = ? AND share_id IS NOT NULL AND id IN (SELECT id FROM notifications WHERE user_id = ? AND share_id IS NOT NULL ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`,
		userID, userID, maxGuestNotifications)
	return err
}

// notify writes one notification row and trims the person's oldest beyond the cap.
func notify(tx *sql.Tx, userID, projectID, kind, commentID, suggestionID, documentID, actorID, now string) error {
	if userID == "" || userID == actorID {
		return nil
	}
	var cid, sid, did, aid any
	if commentID != "" {
		cid = commentID
	}
	if suggestionID != "" {
		sid = suggestionID
	}
	if documentID != "" {
		did = documentID
	}
	if actorID != "" {
		aid = actorID
	}
	if _, err := tx.Exec(`INSERT INTO notifications(id, user_id, project_id, kind, comment_id, suggestion_id, document_id, actor_id, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		db.NewID("ntf"), userID, projectID, kind, cid, sid, did, aid, now); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM notifications WHERE user_id = ? AND share_id IS NULL AND id IN (SELECT id FROM notifications WHERE user_id = ? AND share_id IS NULL ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`,
		userID, userID, maxNotificationsPerUser)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
