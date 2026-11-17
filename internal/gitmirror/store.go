package gitmirror

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// timeLayout matches db.Now: RFC 3339 in UTC with milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

const remoteCols = `id, project_id, name, preset, url, branch, path_prefix, auth_kind, username, secret_cipher, public_key, known_hosts,
	push_mode, interval_minutes, include_notes, author_name, author_email, enabled, state,
	COALESCE(last_push_at,''), COALESCE(last_attempt_at,''), COALESCE(next_attempt_at,''), last_commit_sha, local_sha, remote_sha, mirror_hash,
	last_error, last_error_code, fail_count, created_at, updated_at, audience, only_done, sign_commits, created_via, restore_paused, include_cover`

type scanner interface{ Scan(dest ...any) error }

func scanRemote(sc scanner) (*remoteRow, error) {
	var r remoteRow
	var notes, enabled, onlyDone, sign, paused, cover int
	err := sc.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Preset, &r.URL, &r.Branch, &r.PathPrefix, &r.AuthKind, &r.Username, &r.SecretCipher, &r.PublicKey, &r.KnownHosts,
		&r.PushMode, &r.IntervalMinutes, &notes, &r.AuthorName, &r.AuthorEmail, &enabled, &r.State,
		&r.LastPushAt, &r.LastAttemptAt, &r.NextAttemptAt, &r.LastCommitSHA, &r.LocalSHA, &r.RemoteSHA, &r.MirrorHash,
		&r.LastError, &r.LastErrorCode, &r.FailCount, &r.CreatedAt, &r.UpdatedAt, &r.Audience, &onlyDone, &sign, &r.CreatedVia, &paused, &cover)
	if err != nil {
		return nil, err
	}
	r.IncludeNotes, r.Enabled = notes != 0, enabled != 0
	r.OnlyDone, r.SignCommits, r.RestorePaused, r.IncludeCover = onlyDone != 0, sign != 0, paused != 0, cover != 0
	if r.Audience != AudiencePrivate {
		r.Audience = AudiencePublic // anything unknown is read as the strictest
	}
	return &r, nil
}

// errNoRemote means the remote does not exist (or belongs to another project).
var errNoRemote = errors.New("git remote not found")

// loadRemote reads one remote of a project. The project id is part of the
// lookup so an id from another project is not found.
func (s *Service) loadRemote(ctx context.Context, projectID, remoteID string) (*remoteRow, error) {
	r, err := scanRemote(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+remoteCols+` FROM git_remotes WHERE id = ? AND project_id = ?`, remoteID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoRemote
	}
	return r, err
}

// loadRemoteByID reads a remote without knowing its project (the engine).
func (s *Service) loadRemoteByID(ctx context.Context, remoteID string) (*remoteRow, error) {
	r, err := scanRemote(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+remoteCols+` FROM git_remotes WHERE id = ?`, remoteID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoRemote
	}
	return r, err
}

// remotesOf lists a project's remotes, oldest first.
func (s *Service) remotesOf(ctx context.Context, projectID string) ([]*remoteRow, error) {
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT `+remoteCols+` FROM git_remotes WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*remoteRow
	for rows.Next() {
		r, err := scanRemote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolveRemote finds the remote an op means: the one named, or the project's
// only remote when none is named. No remote at all is a conflict, not a
// missing object: the project exists, it just has no Git remote yet.
func (s *Service) resolveRemote(ctx context.Context, projectID, remoteID string) (*remoteRow, error) {
	if remoteID != "" {
		return s.loadRemote(ctx, projectID, remoteID)
	}
	rs, err := s.remotesOf(ctx, projectID)
	if err != nil {
		return nil, err
	}
	switch len(rs) {
	case 0:
		return nil, errNoRemoteConfigured
	case 1:
		return rs[0], nil
	}
	return nil, errors.New("this project has more than one remote; name one with remote_id")
}

var errNoRemoteConfigured = errors.New("no Git remote is configured for this project")

// insertRemote adds a remote, up to maxRemotesPerProject (F-362).
func (s *Service) insertRemote(ctx context.Context, r *remoteRow, createdBy string) error {
	return s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM git_remotes WHERE project_id = ?`, r.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= maxRemotesPerProject {
			return errTooManyRemotes
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO git_remotes(id, project_id, name, preset, url, branch, path_prefix, auth_kind, username, secret_cipher, public_key, known_hosts,
			push_mode, interval_minutes, include_notes, author_name, author_email, enabled, state, created_by, created_at, updated_at,
			audience, only_done, sign_commits, created_via, include_cover)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.ProjectID, r.Name, r.Preset, r.URL, r.Branch, r.PathPrefix, r.AuthKind, r.Username, r.SecretCipher, r.PublicKey, r.KnownHosts,
			r.PushMode, r.IntervalMinutes, b2i(r.IncludeNotes), r.AuthorName, r.AuthorEmail, b2i(r.Enabled), StateNew, nullIfEmpty(createdBy), r.CreatedAt, r.CreatedAt,
			r.Audience, b2i(r.OnlyDone), b2i(r.SignCommits), r.CreatedVia, b2i(r.IncludeCover))
		return err
	})
}

var errTooManyRemotes = fmt.Errorf("a project has at most %d Git remotes; remove one first", maxRemotesPerProject)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// errRemoteChanged means the remote's address changed while an op that had read
// it was working (a scan, a test), so its result no longer applies.
var errRemoteChanged = errors.New("the remote was changed while this was running")

// saveConfig writes the columns that differ between before (the row as the op
// read it) and after, and only those, so an edit made meanwhile to another
// column is not reverted by a stale copy. The write is guarded on the address
// the op worked from: if the address changed under it (a slow scan or test
// finishing after the owner re-pointed the remote) nothing is written and
// errRemoteChanged is returned. Status columns are included only when status
// is true.
func (s *Service) saveConfig(ctx context.Context, before, after *remoteRow, status bool) error {
	var sets []string
	var args []any
	str := func(col, a, b string) {
		if a != b {
			sets = append(sets, col+" = ?")
			args = append(args, b)
		}
	}
	num := func(col string, a, b int) {
		if a != b {
			sets = append(sets, col+" = ?")
			args = append(args, b)
		}
	}
	str("name", before.Name, after.Name)
	str("preset", before.Preset, after.Preset)
	str("url", before.URL, after.URL)
	str("branch", before.Branch, after.Branch)
	str("path_prefix", before.PathPrefix, after.PathPrefix)
	str("auth_kind", before.AuthKind, after.AuthKind)
	str("username", before.Username, after.Username)
	str("secret_cipher", before.SecretCipher, after.SecretCipher)
	str("public_key", before.PublicKey, after.PublicKey)
	str("known_hosts", before.KnownHosts, after.KnownHosts)
	str("push_mode", before.PushMode, after.PushMode)
	num("interval_minutes", before.IntervalMinutes, after.IntervalMinutes)
	num("include_notes", b2i(before.IncludeNotes), b2i(after.IncludeNotes))
	str("author_name", before.AuthorName, after.AuthorName)
	str("author_email", before.AuthorEmail, after.AuthorEmail)
	num("enabled", b2i(before.Enabled), b2i(after.Enabled))
	str("audience", before.Audience, after.Audience)
	num("only_done", b2i(before.OnlyDone), b2i(after.OnlyDone))
	num("include_cover", b2i(before.IncludeCover), b2i(after.IncludeCover))
	num("sign_commits", b2i(before.SignCommits), b2i(after.SignCommits))
	num("restore_paused", b2i(before.RestorePaused), b2i(after.RestorePaused))
	if status {
		str("state", before.State, after.State)
		str("remote_sha", before.RemoteSHA, after.RemoteSHA)
		str("last_commit_sha", before.LastCommitSHA, after.LastCommitSHA)
		str("local_sha", before.LocalSHA, after.LocalSHA)
		str("mirror_hash", before.MirrorHash, after.MirrorHash)
		str("last_error", before.LastError, after.LastError)
		str("last_error_code", before.LastErrorCode, after.LastErrorCode)
		num("fail_count", before.FailCount, after.FailCount)
		if before.NextAttemptAt != after.NextAttemptAt {
			sets = append(sets, "next_attempt_at = ?")
			args = append(args, nullIfEmpty(after.NextAttemptAt))
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, stamp(s.o.Clock.Now()), after.ID, before.URL)
	res, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE git_remotes SET `+strings.Join(sets, ", ")+` WHERE id = ? AND url = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errRemoteChanged
	}
	return nil
}

// statusUpdate is a change to a remote's status columns. Nil fields are left alone.
type statusUpdate struct {
	State         *string
	LastPushAt    *string
	LastAttemptAt *string
	NextAttemptAt *string // "" clears
	LastCommitSHA *string
	LocalSHA      *string
	RemoteSHA     *string
	MirrorHash    *string
	LastError     *string
	LastErrorCode *string
	FailCount     *int
	// IfURL, when set, applies the change only while the remote still has this
	// address: a push that outlives a re-pointing of its remote must not write
	// its result onto the new one.
	IfURL string
}

func ptr[T any](v T) *T { return &v }

// setStatus applies a status change.
func (s *Service) setStatus(ctx context.Context, remoteID string, u statusUpdate) error {
	var sets []string
	var args []any
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.State != nil {
		add("state", *u.State)
	}
	if u.LastPushAt != nil {
		add("last_push_at", nullIfEmpty(*u.LastPushAt))
	}
	if u.LastAttemptAt != nil {
		add("last_attempt_at", nullIfEmpty(*u.LastAttemptAt))
	}
	if u.NextAttemptAt != nil {
		add("next_attempt_at", nullIfEmpty(*u.NextAttemptAt))
	}
	if u.LastCommitSHA != nil {
		add("last_commit_sha", *u.LastCommitSHA)
	}
	if u.LocalSHA != nil {
		add("local_sha", *u.LocalSHA)
	}
	if u.RemoteSHA != nil {
		add("remote_sha", *u.RemoteSHA)
	}
	if u.MirrorHash != nil {
		add("mirror_hash", *u.MirrorHash)
	}
	if u.LastError != nil {
		add("last_error", *u.LastError)
	}
	if u.LastErrorCode != nil {
		add("last_error_code", *u.LastErrorCode)
	}
	if u.FailCount != nil {
		add("fail_count", *u.FailCount)
	}
	if len(sets) == 0 {
		return nil
	}
	q := `UPDATE git_remotes SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	args = append(args, remoteID)
	if u.IfURL != "" {
		q += ` AND url = ?`
		args = append(args, u.IfURL)
	}
	_, err := s.o.DB.Writer().ExecContext(ctx, q, args...)
	return err
}

// ---- push rows ---------------------------------------------------------------

// pushRow is a git_pushes row.
type pushRow struct {
	ID, RemoteID, ProjectID, Trigger, Status, Outcome string
	StartedAt, FinishedAt, StartedBy                  string
	CommitSHA, ParentSHA, RemoteSHA, Branch           string
	FilesChanged                                      int
	Bytes                                             int64
	Forced                                            bool
	ErrorCode, ErrorMessage                           string
	ManifestJSON, DetailJSON                          string
}

const pushCols = `id, remote_id, project_id, trigger, status, outcome, started_at, COALESCE(finished_at,''), COALESCE(started_by,''), commit_sha, parent_sha, remote_sha, branch,
	files_changed, bytes, forced, error_code, error_message, manifest_json, detail_json`

func scanPush(sc scanner) (*pushRow, error) {
	var p pushRow
	var forced int
	err := sc.Scan(&p.ID, &p.RemoteID, &p.ProjectID, &p.Trigger, &p.Status, &p.Outcome, &p.StartedAt, &p.FinishedAt, &p.StartedBy, &p.CommitSHA, &p.ParentSHA, &p.RemoteSHA, &p.Branch,
		&p.FilesChanged, &p.Bytes, &forced, &p.ErrorCode, &p.ErrorMessage, &p.ManifestJSON, &p.DetailJSON)
	p.Forced = forced != 0
	return &p, err
}

// errBusy means a push is already running for the remote.
var errBusy = errors.New("a push is already running for this remote")

// claimPush inserts the running row. The partial unique index is the arbiter:
// a second claim for the same remote fails, whichever process or goroutine
// makes it.
func (s *Service) claimPush(ctx context.Context, r *remoteRow, trigger, by string, forced bool) (*pushRow, error) {
	p := &pushRow{ID: db.NewID("gps"), RemoteID: r.ID, ProjectID: r.ProjectID, Trigger: trigger, Status: "running", StartedAt: stamp(s.o.Clock.Now()),
		StartedBy: by, Branch: r.Branch, Forced: forced, ManifestJSON: "{}", DetailJSON: "{}"}
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO git_pushes(id, remote_id, project_id, trigger, status, started_at, started_by, branch, forced) VALUES (?,?,?,?,?,?,?,?,?)`,
			p.ID, p.RemoteID, p.ProjectID, p.Trigger, p.Status, p.StartedAt, nullIfEmpty(by), p.Branch, b2i(forced))
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return nil, errBusy
		}
		return nil, err
	}
	return p, nil
}

// finishPush writes the outcome of a push.
func (s *Service) finishPush(ctx context.Context, p *pushRow) error {
	p.FinishedAt = stamp(s.o.Clock.Now())
	_, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE git_pushes SET status=?, outcome=?, finished_at=?, commit_sha=?, parent_sha=?, remote_sha=?, files_changed=?, bytes=?,
		forced=?, error_code=?, error_message=?, manifest_json=?, detail_json=? WHERE id = ?`,
		p.Status, p.Outcome, p.FinishedAt, p.CommitSHA, p.ParentSHA, p.RemoteSHA, p.FilesChanged, p.Bytes, b2i(p.Forced), p.ErrorCode, p.ErrorMessage, p.ManifestJSON, p.DetailJSON, p.ID)
	if err == nil {
		s.prunePushes(ctx, p.RemoteID, keepPushes)
	}
	return err
}

// dropPush removes a running row that turned out to have nothing to say (an
// automatic trigger with no change). Manual pushes keep theirs.
func (s *Service) dropPush(ctx context.Context, id string) {
	_, _ = s.o.DB.Writer().ExecContext(ctx, `DELETE FROM git_pushes WHERE id = ?`, id)
}

// runningPush returns the remote's running push, if any.
func (s *Service) runningPush(ctx context.Context, remoteID string) *pushRow {
	p, err := scanPush(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+pushCols+` FROM git_pushes WHERE remote_id = ? AND status = 'running'`, remoteID))
	if err != nil {
		return nil
	}
	return p
}

// loadPush reads one push of a project.
func (s *Service) loadPush(ctx context.Context, projectID, pushID string) (*pushRow, error) {
	p, err := scanPush(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+pushCols+` FROM git_pushes WHERE id = ? AND project_id = ?`, pushID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoPush
	}
	return p, err
}

var errNoPush = errors.New("push not found")

// pushesOf lists a remote's pushes, newest first. before is a push id to page from.
func (s *Service) pushesOf(ctx context.Context, remoteID, before string, limit int) ([]*pushRow, error) {
	q := `SELECT ` + pushCols + ` FROM git_pushes WHERE remote_id = ?`
	args := []any{remoteID}
	if before != "" {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.o.DB.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pushRow
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// latestWith returns the newest push of a remote with the given status.
func (s *Service) latestWith(ctx context.Context, remoteID, status string) *pushRow {
	p, err := scanPush(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+pushCols+` FROM git_pushes WHERE remote_id = ? AND status = ? ORDER BY id DESC LIMIT 1`, remoteID, status))
	if err != nil {
		return nil
	}
	return p
}

// prunePushes keeps the newest keep rows of a remote's history.
func (s *Service) prunePushes(ctx context.Context, remoteID string, keep int) {
	_, _ = s.o.DB.Writer().ExecContext(ctx, `DELETE FROM git_pushes WHERE remote_id = ? AND status <> 'running' AND id NOT IN
		(SELECT id FROM git_pushes WHERE remote_id = ? ORDER BY id DESC LIMIT ?)`, remoteID, remoteID, keep)
}

// projectTitle is the project's title, for commit messages.
func (s *Service) projectTitle(ctx context.Context, projectID string) string {
	var t string
	_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT title FROM projects WHERE id = ?`, projectID).Scan(&t)
	return t
}

// addrIdentity is what names the place a credential may be sent to: the
// scheme, the host, the port and the path, normalised (host lower case, no
// slashes at the ends, no .git).
func addrIdentity(ep endpointInfo) string {
	p := strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git")
	return ep.Proto + "|" + strings.ToLower(ep.Host) + "|" + strconv.Itoa(ep.Port) + "|" + p
}

// sealContext is what a remote's secret is sealed for: the row and the exact
// place it goes. A ciphertext copied to another row, or a row whose address was
// changed in the database, does not open, so a stolen or altered row cannot
// send a credential anywhere else.
func sealContext(remoteID string, ep endpointInfo) string {
	return "git_remotes/" + remoteID + "/secret/" + addrIdentity(ep)
}

// legacySealContext is the context rows were sealed with before the address was
// part of it. It is used once, at start, to re-seal such a row; never to open a
// credential for a transport call.
func legacySealContext(remoteID string) string { return "git_remotes/" + remoteID + "/secret" }

// sealContextOf is sealContext for a stored row.
func sealContextOf(r *remoteRow) (string, error) {
	ep, e := parseURL(r.URL, true)
	if e != nil {
		return "", e
	}
	return sealContext(r.ID, ep), nil
}

// acksOf returns the acknowledged findings of a remote as path NUL sha.
func (s *Service) acksOf(ctx context.Context, remoteID string) map[string]bool {
	out := map[string]bool{}
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT path, sha256 FROM git_scan_acks WHERE remote_id = ?`, remoteID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, h string
		if rows.Scan(&p, &h) == nil {
			out[p+"\x00"+h] = true
		}
	}
	return out
}

// lastSecretFindings returns the findings of the newest push that was refused
// for a secret.
func (s *Service) lastSecretFindings(ctx context.Context, remoteID string) []SecretFindingView {
	var detail string
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT detail_json FROM git_pushes WHERE remote_id = ? AND error_code = ? ORDER BY id DESC LIMIT 1`, remoteID, CodeSecretInTree).Scan(&detail)
	if err != nil {
		return nil
	}
	var d struct {
		Secrets []SecretFindingView `json:"secrets"`
	}
	_ = json.Unmarshal([]byte(detail), &d)
	return d.Secrets
}

// markPublic records that the project has given a public remote this address.
func (s *Service) markPublic(ctx context.Context, projectID string, ep endpointInfo) {
	_, err := s.o.DB.Writer().ExecContext(ctx, `INSERT OR IGNORE INTO git_public_addresses(project_id, address, since) VALUES (?,?,?)`, projectID, addrIdentity(ep), stamp(s.o.Clock.Now()))
	if err != nil {
		s.log.Warn("git: could not record a public address", "project", projectID, "err", cause(err))
	}
}

// everPublic reports whether the project has ever given a public remote this address.
func (s *Service) everPublic(ctx context.Context, projectID string, ep endpointInfo) bool {
	var n int
	_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM git_public_addresses WHERE project_id = ? AND address = ?`, projectID, addrIdentity(ep)).Scan(&n)
	return n > 0
}
