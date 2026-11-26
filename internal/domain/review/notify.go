package review

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
)

// Notifications are in-app only: a row per mention, reply, comment on your
// project, thread resolved, suggestion to decide and decision on yours. Nothing
// is emailed, pushed or sent to a webhook (the privacy invariant), no event
// announces one (a per-person stream does not exist; the screens poll
// notification.count), and the rows hold ids only: the list reads the comment
// when it is shown, so a deleted or reworded comment never leaves its words here,
// and it shows only what the person can still read.

func registerNotifications(r *ops.Registry) {
	me := ops.Authenticated().ForSessions()
	ops.Register(r, ops.Op{Name: "notification.list", Method: "GET", Path: "/api/v1/notifications", Auth: me,
		Desc: "Your notifications, newest first: a mention, a reply to your comment, a thread of yours resolved, a comment on a project you own, a suggestion waiting for you, a decision on yours. Each names the project, document, person and, for a comment, the first 140 characters of its text. Only projects you are still on appear. For a person signed in with a browser."},
		func(c *ops.Ctx, in NotificationListIn) (NotificationListOut, error) { return notificationList(c, in) })
	ops.Register(r, ops.Op{Name: "notification.count", Method: "GET", Path: "/api/v1/notifications/count", Auth: me,
		Desc: "How many notifications you have not read, in all and per project: the badge. There is no push; poll this (when the tab is visible, now and then)."},
		func(c *ops.Ctx, in NotificationCountIn) (NotificationCountOut, error) {
			return notificationCount(c, in)
		})
	ops.Register(r, ops.Op{Name: "notification.mark_read", Method: "POST", Path: "/api/v1/notifications/read", Auth: me,
		Desc: "Mark notifications read: the ids you send (ones that are not yours are ignored). Answers how many changed."},
		func(c *ops.Ctx, in MarkReadIn) (MarkReadOut, error) { return markRead(c, in) })
	ops.Register(r, ops.Op{Name: "notification.read_all", Method: "POST", Path: "/api/v1/notifications/read-all", Auth: me,
		Desc: "Mark all your notifications read, or only those of one project."},
		func(c *ops.Ctx, in ReadAllIn) (MarkReadOut, error) { return readAll(c, in) })
}

// NotificationOut is one notification.
type NotificationOut struct {
	ID           string `json:"id"`
	Kind         string `json:"kind" enum:"mention,reply,comment,resolved,suggestion,suggestion-decided"`
	ProjectID    string `json:"project_id"`
	ProjectTitle string `json:"project_title"`
	DocumentID   string `json:"document_id,omitempty"`
	DocumentName string `json:"document_title,omitempty"`
	CommentID    string `json:"comment_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	SuggestionID string `json:"suggestion_id,omitempty"`
	Actor        string `json:"actor,omitempty" desc:"Who did it: a name, or Guest: Name; empty when the account is gone."`
	Excerpt      string `json:"excerpt,omitempty" desc:"The first 140 characters of the comment, when you can still read it."`
	Count        int    `json:"count" desc:"How many comments this stands for: guests commenting in one thread within an hour are one notification."`
	Read         bool   `json:"read"`
	CreatedAt    string `json:"created_at"`
}

// NotificationListIn lists notifications.
type NotificationListIn struct {
	Unread    bool   `query:"unread" desc:"Only the ones you have not read."`
	ProjectID string `query:"project_id" desc:"Only this project's."`
	Limit     int    `query:"limit" desc:"Per page (default 50, at most 200)."`
	Offset    int    `query:"offset"`
}

// NotificationListOut is a page.
type NotificationListOut struct {
	Items  []NotificationOut `json:"items"`
	Unread int               `json:"unread" desc:"In all, not only on this page."`
}

func notificationList(c *ops.Ctx, in NotificationListIn) (NotificationListOut, error) {
	limit, offset := pageOf(in.Limit, in.Offset)
	// what a project with a passphrase has to say is for the person who has unlocked it (F-252)
	hidden, err := projectgate.LockedProjects(c)
	if err != nil {
		return NotificationListOut{}, err
	}
	gateSQL, gateArgs := projectgate.NotIn("n.project_id", hidden)
	where := `WHERE n.user_id = ? AND ` + stillOn("n") + ` AND ` + gateSQL
	args := append([]any{c.P.UserID}, gateArgs...)
	if in.Unread {
		where += ` AND n.read_at IS NULL`
	}
	if in.ProjectID != "" {
		where += ` AND n.project_id = ?`
		args = append(args, in.ProjectID)
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT n.id, n.kind, n.project_id, p.title, COALESCE(n.document_id,''), COALESCE(d.title,''), COALESCE(n.comment_id,''), COALESCE(root.id, ''),
		COALESCE(n.suggestion_id,''), COALESCE(n.actor_id,''), n.count, n.read_at IS NOT NULL, n.created_at, COALESCE(cm.body,''), COALESCE(cm.share_id,''), COALESCE(cm.guest_name,''), COALESCE(NULLIF(au.display_name,''), CASE WHEN au.id IS NULL THEN '' ELSE 'Member' END),
		COALESCE(m.role, '')
		FROM notifications n JOIN projects p ON p.id = n.project_id
		LEFT JOIN documents d ON d.id = n.document_id
		LEFT JOIN comments cm ON cm.id = n.comment_id AND cm.deleted_at IS NULL AND (cm.visibility = 'shared' OR cm.author_id = n.user_id)
		LEFT JOIN comments root ON root.id = COALESCE(cm.parent_id, cm.id) AND (root.visibility = 'shared' OR root.author_id = n.user_id)
		LEFT JOIN users au ON au.id = n.actor_id AND au.deleted_at IS NULL
		LEFT JOIN project_members m ON m.project_id = n.project_id AND m.user_id = n.user_id
		`+where+` ORDER BY n.created_at DESC, n.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return NotificationListOut{}, err
	}
	defer rows.Close()
	out := NotificationListOut{Items: []NotificationOut{}}
	for rows.Next() {
		var n NotificationOut
		var actorID, body, share, guest, actorName, role string
		if err := rows.Scan(&n.ID, &n.Kind, &n.ProjectID, &n.ProjectTitle, &n.DocumentID, &n.DocumentName, &n.CommentID, &n.ThreadID, &n.SuggestionID, &actorID, &n.Count, &n.Read, &n.CreatedAt,
			&body, &share, &guest, &actorName, &role); err != nil {
			return out, err
		}
		n.Actor = actorName
		if actorID == "" && share != "" {
			n.Actor = "Guest: " + guest
		}
		if body != "" && atLeast(role, ops.ProjectReviewer) {
			n.Excerpt = excerpt(body, 140)
		}
		out.Items = append(out.Items, n)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Unread, _, err = unreadCounts(c, c.DB().Read(), c.P.UserID, "", hidden)
	return out, err
}

// stillOn is the condition that the person of a notification is still on its project and the
// project is still there: a member loses sight of a project in the trash (only its owner keeps
// it, as for every other op), and of one that was purged.
func stillOn(alias string) string {
	return `EXISTS (SELECT 1 FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.project_id = ` + alias + `.project_id AND m.user_id = ` + alias + `.user_id AND p.deleted_at IS NULL AND (p.trashed_at IS NULL OR m.role = 'owner'))`
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

// unreadCounts counts a person's unread notifications on projects they are still on.
func unreadCounts(ctx context.Context, q querier, userID, projectID string, hidden []string) (int, []ProjectCount, error) {
	gateSQL, gateArgs := projectgate.NotIn("n.project_id", hidden)
	rows, err := q.QueryContext(ctx, `SELECT n.project_id, count(*) FROM notifications n
		WHERE n.user_id = ? AND n.read_at IS NULL AND (? = '' OR n.project_id = ?)
		AND `+stillOn("n")+` AND `+gateSQL+`
		GROUP BY n.project_id ORDER BY n.project_id`, append([]any{userID, projectID, projectID}, gateArgs...)...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	total := 0
	per := []ProjectCount{}
	for rows.Next() {
		var pc ProjectCount
		if err := rows.Scan(&pc.ProjectID, &pc.Unread); err != nil {
			return 0, nil, err
		}
		total += pc.Unread
		per = append(per, pc)
	}
	return total, per, rows.Err()
}

// ProjectCount is the unread notifications of one project.
type ProjectCount struct {
	ProjectID string `json:"project_id"`
	Unread    int    `json:"unread"`
}

// NotificationCountIn asks for the badge.
type NotificationCountIn struct{}

// NotificationCountOut is the badge.
type NotificationCountOut struct {
	Unread    int            `json:"unread"`
	ByProject []ProjectCount `json:"by_project"`
}

func notificationCount(c *ops.Ctx, _ NotificationCountIn) (NotificationCountOut, error) {
	hidden, err := projectgate.LockedProjects(c)
	if err != nil {
		return NotificationCountOut{}, err
	}
	total, per, err := unreadCounts(c, c.DB().Read(), c.P.UserID, "", hidden)
	return NotificationCountOut{Unread: total, ByProject: per}, err
}

// MarkReadIn names notifications.
type MarkReadIn struct {
	IDs []string `json:"ids" desc:"Notification ids (at most 200)."`
}

// Validate bounds the list.
func (in MarkReadIn) Validate() error {
	if len(in.IDs) == 0 {
		return ops.Invalid("ids", "Send the ids to mark.")
	}
	if len(in.IDs) > 200 {
		return ops.Invalid("ids", "At most 200 at a time.")
	}
	return nil
}

// MarkReadOut says how many changed.
type MarkReadOut struct {
	Changed int `json:"changed"`
}

func markRead(c *ops.Ctx, in MarkReadIn) (MarkReadOut, error) {
	var out MarkReadOut
	hidden, err := projectgate.LockedProjects(c)
	if err != nil {
		return out, err
	}
	gateSQL, gateArgs := projectgate.NotIn("project_id", hidden)
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(c, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL AND `+gateSQL+` AND id IN (`+placeholders(len(in.IDs))+`)`,
			append(append([]any{db.Now(), c.P.UserID}, gateArgs...), idArgs(in.IDs)...)...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out.Changed = int(n)
		return nil
	})
	return out, err
}

// ReadAllIn marks everything read.
type ReadAllIn struct {
	ProjectID string `json:"project_id,omitempty" desc:"Only this project's."`
}

func readAll(c *ops.Ctx, in ReadAllIn) (MarkReadOut, error) {
	var out MarkReadOut
	hidden, err := projectgate.LockedProjects(c)
	if err != nil {
		return out, err
	}
	gateSQL, gateArgs := projectgate.NotIn("project_id", hidden)
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(c, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL AND (? = '' OR project_id = ?) AND `+gateSQL,
			append([]any{db.Now(), c.P.UserID, in.ProjectID, in.ProjectID}, gateArgs...)...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out.Changed = int(n)
		return nil
	})
	return out, err
}

// keepReadDays is how long a read notification is kept.
const keepReadDays = 60

// tombstoneDays is how long a deleted comment's row is kept (it keeps a thread's shape while the
// thread is read) before the sweep removes it.
const tombstoneDays = 30

// Sweep deletes notifications that were read more than keepReadDays ago and any
// older than a year; the cap per person is enforced as they are written.
func Sweep(ctx context.Context, d *db.DB) (int, error) {
	now := db.NowTime()
	read := db.Format(now.AddDate(0, 0, -keepReadDays))
	old := db.Format(now.AddDate(-1, 0, 0))
	n := 0
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE (read_at IS NOT NULL AND read_at < ?) OR created_at < ?`, read, old)
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		n = int(k)
		// Private comments whose author's account is gone (soft-deleted, or removed so that author_id is NULL) are removed: no one
		// else could ever read them. A Word import's comments have a NULL author too, and are shared, so they are not touched.
		res, err = tx.ExecContext(ctx, `DELETE FROM comments WHERE visibility = 'private' AND (author_id IS NULL OR author_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL))`)
		if err != nil {
			return err
		}
		k, _ = res.RowsAffected()
		n += int(k)
		// the old tombstones: replies first (they are leaves), then threads with nothing live left in them
		gone := db.Format(now.AddDate(0, 0, -tombstoneDays))
		for _, q := range []string{
			`DELETE FROM comments WHERE deleted_at IS NOT NULL AND deleted_at < ? AND parent_id IS NOT NULL`,
			`DELETE FROM comments WHERE deleted_at IS NOT NULL AND deleted_at < ? AND parent_id IS NULL AND NOT EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = comments.id)`,
		} {
			res, err := tx.ExecContext(ctx, q, gone)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	return n, err
}
