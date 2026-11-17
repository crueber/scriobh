package coverlayout

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/publish"
)

// Queryer is what *sql.DB and *sql.Tx share.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// A layout that was cleared leaves its row behind with an empty hash (a tombstone) that keeps the revision counting
// upward: a writer who read revision 1, and finds the cover cleared and made again, must not meet revision 1 once more
// and overwrite it. Every reader of the table skips a row with no hash.

// Load reads a project's layout. It returns nil, nil when the project has none (the generated cover).
func Load(ctx context.Context, q Queryer, projectID string) (*Layout, error) {
	l := &Layout{ProjectID: projectID}
	var front, back, wrap, createdBy string
	err := q.QueryRowContext(ctx, `SELECT shape, trim, front_json, back_json, wrap_json, alt, revision, hash, COALESCE(created_by, ''), created_at, updated_at
		FROM cover_layouts WHERE project_id = ? AND hash <> ''`, projectID).
		Scan(&l.Shape, &l.Trim, &front, &back, &wrap, &l.Alt, &l.Revision, &l.Hash, &createdBy, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l.CreatedBy = createdBy
	if l.Shape == ShapeWrap {
		l.Wrap = parseWrap(wrap)
	} else {
		l.Front, l.Back = parseSide(front), parseSide(back)
	}
	return l, nil
}

// Hash is the layout hash of a project, or "" when it has no layout. Every export's input hash includes it.
func Hash(ctx context.Context, q Queryer, projectID string) string {
	var h string
	_ = q.QueryRowContext(ctx, `SELECT hash FROM cover_layouts WHERE project_id = ? AND hash <> ''`, projectID).Scan(&h)
	return h
}

// project is what the layout needs of its project.
type project struct {
	Language   string
	CoverColor string
}

func (p project) rtl() bool { return publish.IsRTL(p.Language) }

func loadProject(ctx context.Context, q Queryer, projectID string) (project, error) {
	var p project
	err := q.QueryRowContext(ctx, `SELECT language, cover_color FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&p.Language, &p.CoverColor)
	return p, err
}

// pictureSet reads the cover-clean pictures of a project once and answers the look-ups validation and hashing make.
type pictureSet struct {
	ctx   context.Context
	q     Queryer
	pid   string
	cache map[string]*Picture
}

func newPictureSet(ctx context.Context, q Queryer, projectID string) *pictureSet {
	return &pictureSet{ctx: ctx, q: q, pid: projectID, cache: map[string]*Picture{}}
}

// look finds a picture of the project that the cover upload made. A missing, foreign or ordinary attachment is not
// found, and the caller says the same thing for all three.
func (s *pictureSet) look(id string) (Picture, bool) {
	if p, ok := s.cache[id]; ok {
		if p == nil {
			return Picture{}, false
		}
		return *p, true
	}
	var p Picture
	err := s.q.QueryRowContext(s.ctx, `SELECT id, sha256, mime, ext, width, height, bytes FROM attachments
		WHERE id = ? AND project_id = ? AND cover_clean = 1 AND kind = 'image'`, id, s.pid).Scan(&p.ID, &p.SHA256, &p.Mime, &p.Ext, &p.Width, &p.Height, &p.Bytes)
	if err != nil {
		s.cache[id] = nil
		return Picture{}, false
	}
	s.cache[id] = &p
	return p, true
}

// syncRefs makes the project's cover references match the layout (nil removes them all): one reference for each
// picture the layout names, whose ref_id is the project's id.
func syncRefs(tx *sql.Tx, projectID string, l *Layout) error {
	if _, err := tx.Exec(`DELETE FROM attachment_refs WHERE ref_kind = ? AND ref_id = ?`, attachments.RefCover, projectID); err != nil {
		return err
	}
	if l == nil {
		return nil
	}
	for _, id := range l.attachments() {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO attachment_refs(attachment_id, ref_kind, ref_id)
			SELECT id, ?, ? FROM attachments WHERE id = ? AND project_id = ?`, attachments.RefCover, projectID, id, projectID); err != nil {
			return err
		}
	}
	return nil
}

// tombRevision is the revision a cleared layout left behind (0 when the project never had one).
func tombRevision(ctx context.Context, q Queryer, projectID string) int {
	var n int
	_ = q.QueryRowContext(ctx, `SELECT revision FROM cover_layouts WHERE project_id = ? AND hash = ''`, projectID).Scan(&n)
	return n
}

// tombstone clears a layout's row but keeps the revision, one higher.
func tombstone(tx *sql.Tx, projectID string, revision int, now string) error {
	_, err := tx.Exec(`UPDATE cover_layouts SET shape = 'sides', trim = ?, front_json = '{"mode":"generated"}', back_json = '{"mode":"none"}', wrap_json = '{}',
		alt = '', revision = ?, hash = '', updated_at = ? WHERE project_id = ?`, DefaultTrim, revision+1, now, projectID)
	return err
}
