package presence

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// PrefKey is the per-person setting that switches presence on. It is off for a new account.
//
// Sharing is symmetric by design: a person with it off is not shown to anyone and is not shown anyone. The alternative
// (hide myself but still watch) would make the setting a way to look without being looked at, which is the one thing a
// setting called "share" must not be; and a solo writer who never opens the setting has nothing broadcast and no extra
// request made from the page. The preference is read by the same code on every path (a heartbeat, a list, a stream), so
// the two halves cannot drift.
const PrefKey = "presence.share"

// Binding is what the service needs from the process to answer for a person.
type Binding struct {
	DB *db.DB
	// Gate is the project passphrase gate (internal/ops/gate.go); asked on behalf of each person shown.
	Gate ops.ProjectGateFunc
	// Trashed says whether a project is in the trash.
	Trashed func(ctx context.Context, projectID string) bool
}

// Bind connects the service to the database, the gate and the trash. Without it only the table works.
func (s *Service) Bind(b Binding) { s.b = b }

// Item is one person who is here. No field of it is ever typed by a client.
type Item struct {
	UserID   string `json:"user_id" desc:"The person's id, as comment.people gives it."`
	Name     string `json:"name" desc:"Display name. Never an email address."`
	Document *Doc   `json:"document,omitempty" desc:"The document they have open. Only owners and editors are told, and only a document that is in this project and not in the trash."`
}

// Doc names a document the viewer may read.
type Doc struct {
	ID    string `json:"id"`
	Title string `json:"title" desc:"The document's title, read when the list is made; may be empty."`
}

// Reasons a View carries no list.
const (
	ReasonOff       = "off"        // the caller has presence.share off
	ReasonNotMember = "not_member" // the caller is not on the project (an administrator who is not a member, for one)
	ReasonTrashed   = "trashed"    // the project is in the trash
)

// View is what one person is told. It is built for that person; it is never shared between viewers.
type View struct {
	Enabled bool   `json:"enabled" desc:"True when the caller has presence.share on and is on this project. When false nothing was recorded for them and items is empty."`
	Reason  string `json:"reason,omitempty" enum:"off,not_member,trashed" desc:"Why enabled is false."`
	// Names is false for a reader: a reader cannot see the names of the people on a book anywhere else in the application
	// (project.member.list is the owner's and comment.people leaves readers out), so presence shows them no one either.
	Names            bool   `json:"sees_people" desc:"False for a reader, who is shown no one (and is still shown to the people who may see them)."`
	Chapters         bool   `json:"sees_documents" desc:"True for an owner or an editor, who are told which document each person has open. A reviewer is told who is here, not where."`
	Items            []Item `json:"items" desc:"The others who are here now, by name. Never the caller."`
	HeartbeatSeconds int    `json:"heartbeat_seconds" desc:"How often the page sends a beat while it is open and visible."`
	ExpiresSeconds   int    `json:"expires_seconds" desc:"How long after the last beat a person is still shown."`
}

func empty(reason string) View {
	return View{Reason: reason, Items: []Item{}, HeartbeatSeconds: HeartbeatSeconds, ExpiresSeconds: int(TTL.Seconds())}
}

// self is what the database says about the caller.
type self struct {
	member bool
	role   string
	name   string
	share  bool
	docOK  bool // the document asked about is a live document of this project
}

// loadSelf reads the caller's membership, name and switch, and in the same statement whether docID names a live
// document of the project (one read instead of two on every beat).
func (s *Service) loadSelf(ctx context.Context, userID, projectID, docID string) (self, error) {
	var out self
	var pref sql.NullString
	var doc sql.NullInt64
	// The membership row itself, not ops.Env.ProjectRole: that answers "owner" for an instance administrator who is not on
	// the project, and presence is for the people on it.
	err := s.b.DB.Read().QueryRowContext(ctx, `SELECT m.role, u.display_name, up.value,
			(SELECT 1 FROM documents d WHERE d.id = ?3 AND d.project_id = m.project_id AND d.deleted_at IS NULL)
		FROM project_members m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL AND u.disabled_at IS NULL
		LEFT JOIN user_prefs up ON up.user_id = m.user_id AND up.key = ?4
		WHERE m.project_id = ?1 AND m.user_id = ?2`, projectID, userID, docID, PrefKey).Scan(&out.role, &out.name, &pref, &doc)
	if err == sql.ErrNoRows {
		return self{}, nil
	}
	if err != nil {
		return self{}, err
	}
	out.member, out.share, out.docOK = true, pref.Valid && pref.String == "true", doc.Valid
	return out, nil
}

// seesNames and seesChapters are the whole of the per-role policy.
//
//	owner     everyone, and the document each is on        (project.member.list is the owner's)
//	editor    owner, editors and reviewers; the documents  (comment.people)
//	reviewer  owner, editors and reviewers; no documents   (comment.people)
//	reader    no one                                        (a reader sees no names anywhere)
//
// A reader who shares is still shown to the owner, and no one else is told a reader exists: comment.people leaves
// readers out on purpose.
func seesNames(viewer string) bool { return viewer != "" && viewer != ops.ProjectReader }

func seesChapters(viewer string) bool {
	return viewer == ops.ProjectOwner || viewer == ops.ProjectEditor
}

func nameVisible(viewer, subject string) bool {
	switch viewer {
	case ops.ProjectOwner:
		return true
	case ops.ProjectEditor, ops.ProjectReviewer:
		return subject != ops.ProjectReader
	}
	return false
}

func (s *Service) trashed(ctx context.Context, projectID string) bool {
	return s.b.Trashed != nil && s.b.Trashed(ctx, projectID)
}

// ListFor is the one read behind a heartbeat, the list and the stream: who else is on this project, as this person
// may be told. p must be a session principal; the caller has been authorized for the project.
func (s *Service) ListFor(ctx context.Context, p ops.Principal, projectID string) (View, error) {
	return s.view(ctx, p, projectID, false, "")
}

// Beat records that the caller has the project open (on docID, when it names a live document of the project) and
// answers like ListFor. Nothing is recorded when the caller has presence off.
func (s *Service) Beat(ctx context.Context, p ops.Principal, projectID, docID string) (View, error) {
	return s.view(ctx, p, projectID, true, docID)
}

func (s *Service) view(ctx context.Context, p ops.Principal, projectID string, beat bool, docID string) (View, error) {
	if s.b.DB == nil || p.UserID == "" || p.SessionID == "" || p.KeyID != "" {
		return empty(ReasonNotMember), nil
	}
	if !db.ValidID(docID) {
		docID = ""
	}
	me, err := s.loadSelf(ctx, p.UserID, projectID, docID)
	if err != nil {
		return View{}, err
	}
	switch {
	case !me.member:
		return empty(ReasonNotMember), nil
	case s.trashed(ctx, projectID):
		s.DropProject(projectID)
		return empty(ReasonTrashed), nil
	case !me.share:
		s.Leave(projectID, p.UserID)
		return empty(ReasonOff), nil
	}
	if beat {
		if !me.docOK {
			docID = "" // not in this project, or in the trash: it is not recorded and nothing says why
		}
		s.touch(projectID, p.UserID, p.SessionID, docID)
	}
	v := empty("")
	v.Enabled, v.Names, v.Chapters = true, seesNames(me.role), seesChapters(me.role)
	if !v.Names {
		return v, nil
	}
	items, err := s.others(ctx, p, projectID, me.role)
	if err != nil {
		return View{}, err
	}
	v.Items = items
	return v, nil
}

type member struct{ role, name string }

// others builds the list for a viewer of the given role: the project's unexpired entries, minus the viewer, minus
// everyone who may no longer be shown, minus everyone this role may not see, with titles for the documents it may be
// told about. Each check that can be wrong in memory (a membership ended, a session revoked, a switch turned off, a
// person disabled, a project locked) is asked of the database or the gate here, so a list is never older than the
// question.
func (s *Service) others(ctx context.Context, viewer ops.Principal, projectID, role string) ([]Item, error) {
	raw := s.live(projectID)
	live := raw[:0]
	for _, e := range raw {
		if e.user != viewer.UserID {
			live = append(live, e)
		}
	}
	if len(live) == 0 {
		return []Item{}, nil
	}
	q := s.b.DB.Read()
	members := map[string]member{}
	var gated bool
	rows, err := q.QueryContext(ctx, `SELECT m.user_id, m.role, u.display_name, EXISTS(SELECT 1 FROM project_gates g WHERE g.project_id = m.project_id)
		FROM project_members m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL AND u.disabled_at IS NULL
		JOIN user_prefs up ON up.user_id = m.user_id AND up.key = ? AND up.value = 'true'
		WHERE m.project_id = ?`, PrefKey, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var m member
		var g bool
		if err := rows.Scan(&id, &m.role, &m.name, &g); err != nil {
			rows.Close()
			return nil, err
		}
		members[id] = m
		gated = gated || g
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sessions, err := s.liveSessions(ctx, live)
	if err != nil {
		return nil, err
	}
	gated = gated && s.b.Gate != nil
	var items []Item
	docOf := map[string]string{} // user -> document id
	var docIDs []string
	for _, e := range live {
		m, ok := members[e.user]
		if ok && !sessions[e.session] {
			ok = false
		}
		if ok && gated && !s.gateAllows(ctx, ops.Principal{UserID: e.user, SessionID: e.session, Via: "session"}, projectID) {
			ok = false // the person locked the project, or their unlock ran out: they are not shown, and a heartbeat would be refused
		}
		if !ok {
			s.Leave(projectID, e.user)
			continue
		}
		if !nameVisible(role, m.role) {
			continue
		}
		items = append(items, Item{UserID: e.user, Name: m.name})
		if seesChapters(role) && e.doc != "" {
			docOf[e.user] = e.doc
			docIDs = append(docIDs, e.doc)
		}
	}
	if len(docIDs) > 0 {
		titles, err := s.titles(ctx, projectID, docIDs)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if id := docOf[items[i].UserID]; id != "" {
				if t, ok := titles[id]; ok {
					items[i].Document = &Doc{ID: id, Title: t}
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if a != b {
			return a < b
		}
		return items[i].UserID < items[j].UserID
	})
	if items == nil {
		items = []Item{}
	}
	return items, nil
}

// liveSessions answers which of the entries' sessions still stand: not revoked, not expired, not idle for too long.
func (s *Service) liveSessions(ctx context.Context, es []entry) (map[string]bool, error) {
	args := make([]any, 0, len(es)+2)
	seen := map[string]bool{}
	var ph []string
	now := db.Now()
	args = append(args, now, now)
	for _, e := range es {
		if !seen[e.session] {
			seen[e.session] = true
			ph = append(ph, "?")
			args = append(args, e.session)
		}
	}
	rows, err := s.b.DB.Read().QueryContext(ctx, `SELECT id FROM sessions WHERE revoked_at IS NULL AND expires_at > ? AND (idle_expires_at IS NULL OR idle_expires_at > ?) AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// titles reads the titles of the live documents of the project among ids.
func (s *Service) titles(ctx context.Context, projectID string, ids []string) (map[string]string, error) {
	args := []any{projectID}
	ph := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			ph = append(ph, "?")
			args = append(args, id)
		}
	}
	rows, err := s.b.DB.Read().QueryContext(ctx, `SELECT id, title FROM documents WHERE project_id = ? AND deleted_at IS NULL AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}
