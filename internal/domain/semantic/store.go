package semantic

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// state is a project's row in embed_projects. A project with no row has the
// zero state: off.
type state struct {
	ProjectID                                  string
	Enabled                                    bool
	ProviderID, Model, EndpointKey             string
	AllowRemote                                bool
	EnabledBy, ConsentAt                       string
	ConsentChunks, ConsentBytes                int
	IndexVersion, Dim                          int
	Gen                                        int64
	PausedReason                               string
	LastErrorClass, LastErrorAt, NextAttemptAt string
	FailCount                                  int
	IndexedAt, CreatedAt, UpdatedAt            string
	exists                                     bool
}

// active says the index is on and not paused: the worker may send text and a
// search may use it.
func (st state) active() bool { return st.exists && st.Enabled && st.PausedReason == "" }

const stateCols = `project_id, enabled, provider_id, model, endpoint_key, allow_remote, enabled_by, consent_at, consent_chunks, consent_bytes, index_version, dim, gen,
	paused_reason, last_error_class, last_error_at, fail_count, next_attempt_at, indexed_at, created_at, updated_at`

func loadState(ctx context.Context, q queryer, projectID string) (state, error) {
	var st state
	var en, rem int
	err := q.QueryRowContext(ctx, `SELECT `+stateCols+` FROM embed_projects WHERE project_id = ?`, projectID).Scan(&st.ProjectID, &en, &st.ProviderID, &st.Model, &st.EndpointKey, &rem,
		&st.EnabledBy, &st.ConsentAt, &st.ConsentChunks, &st.ConsentBytes, &st.IndexVersion, &st.Dim, &st.Gen, &st.PausedReason, &st.LastErrorClass, &st.LastErrorAt,
		&st.FailCount, &st.NextAttemptAt, &st.IndexedAt, &st.CreatedAt, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state{ProjectID: projectID}, nil
	}
	if err != nil {
		return state{}, err
	}
	st.exists, st.Enabled, st.AllowRemote = true, en == 1, rem == 1
	return st, nil
}

// purgeChunks deletes every embedding of a project and the record of what was
// embedded, and bumps the generation so that no in-memory copy is used again.
func purgeChunks(tx *sql.Tx, projectID string) (chunks int64, err error) {
	res, err := tx.Exec(`DELETE FROM embed_chunks WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, err
	}
	chunks, _ = res.RowsAffected()
	if _, err = tx.Exec(`DELETE FROM embed_docs WHERE project_id = ?`, projectID); err != nil {
		return chunks, err
	}
	_, err = tx.Exec(`UPDATE embed_projects SET dim = 0, gen = gen + 1, indexed_at = '' WHERE project_id = ?`, projectID)
	return chunks, err
}

// counts is what status and the worker need to know about the index.
type counts struct {
	Docs      map[string]string // document id -> embed_docs.status
	DocHash   map[string]string
	DocKey    map[string]string
	DocVer    map[string]int
	DocAt     map[string]string
	DocErr    map[string]string
	Chunks    int
	dim       int
	VecBytes  int64
	Truncated int
	Failed    int
	Empty     int
}

func loadCounts(ctx context.Context, q queryer, projectID string) (counts, error) {
	c := counts{Docs: map[string]string{}, DocHash: map[string]string{}, DocKey: map[string]string{}, DocVer: map[string]int{}, DocAt: map[string]string{}, DocErr: map[string]string{}}
	rows, err := q.QueryContext(ctx, `SELECT document_id, content_hash, model_key, index_version, status, embedded_at, error_class FROM embed_docs WHERE project_id = ?`, projectID)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var id, hash, key, status, at, ec string
		var ver int
		if err := rows.Scan(&id, &hash, &key, &ver, &status, &at, &ec); err != nil {
			rows.Close()
			return c, err
		}
		c.Docs[id], c.DocHash[id], c.DocKey[id], c.DocVer[id], c.DocAt[id], c.DocErr[id] = status, hash, key, ver, at, ec
		switch status {
		case "failed":
			c.Failed++
		case "empty":
			c.Empty++
		case "truncated":
			c.Truncated++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}
	// Every vector of an index is `dim` bytes, so the size is the count times the dimension: no pass over the vectors.
	err = q.QueryRowContext(ctx, `SELECT count(*), COALESCE(MAX(dim), 0) FROM embed_chunks WHERE project_id = ?`, projectID).Scan(&c.Chunks, &c.dim)
	c.VecBytes = int64(c.Chunks) * int64(c.dim)
	return c, err
}

// ---- the in-memory copy ------------------------------------------------------------------------

// snapshot is a project's vectors in memory, flat, so that a search is a loop
// over one slice. It is valid for one generation of the chunks.
type snapshot struct {
	project string
	gen     int64
	dim     int
	model   string

	docs   []string // distinct document ids, in the order they were read
	docIdx []int32  // per chunk: index into docs
	ord    []int32
	start  []int32
	end    []int32
	hash   [][16]byte // the text hash of each chunk, to check a snippet against
	mult   []float32
	vec    []int8 // n * dim

	byDoc map[string][2]int // document id -> [first chunk, one past the last]
	bytes int64
	used  time.Time
}

func (sn *snapshot) n() int { return len(sn.mult) }

func (sn *snapshot) vector(i int) []int8 { return sn.vec[i*sn.dim : (i+1)*sn.dim] }

// vecCache keeps a bounded number of snapshots, least recently used out.
type vecCache struct {
	mu    sync.Mutex
	max   int64
	m     map[string]*snapshot
	locks map[string]*sync.Mutex
}

func newVecCache(max int64) *vecCache {
	return &vecCache{max: max, m: map[string]*snapshot{}, locks: map[string]*sync.Mutex{}}
}

func (c *vecCache) lockFor(project string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[project]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[project] = l
	}
	return l
}

func (c *vecCache) get(project string, gen int64) *snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sn := c.m[project]; sn != nil && sn.gen == gen {
		sn.used = time.Now()
		return sn
	}
	return nil
}

func (c *vecCache) put(sn *snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sn.used = time.Now()
	c.m[sn.project] = sn
	var total int64
	for _, x := range c.m {
		total += x.bytes
	}
	for total > c.max && len(c.m) > 1 {
		var oldest *snapshot
		for _, x := range c.m {
			if x.project != sn.project && (oldest == nil || x.used.Before(oldest.used)) {
				oldest = x
			}
		}
		if oldest == nil {
			break
		}
		total -= oldest.bytes
		delete(c.m, oldest.project)
	}
}

func (c *vecCache) drop(project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, project)
}

// snapshotFor returns the project's vectors at the generation st names,
// reading them from the database when no copy of that generation is held. One
// load at a time per project: a second search waits for the first's result.
func (s *Service) snapshotFor(ctx context.Context, d *db.DB, st state) (*snapshot, error) {
	if sn := s.cache.get(st.ProjectID, st.Gen); sn != nil {
		return sn, nil
	}
	l := s.cache.lockFor(st.ProjectID)
	l.Lock()
	defer l.Unlock()
	if sn := s.cache.get(st.ProjectID, st.Gen); sn != nil {
		return sn, nil
	}
	// Read the generation and the chunks in one read transaction, so the copy is
	// of the generation it says.
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cur, err := loadState(ctx, tx, st.ProjectID)
	if err != nil {
		return nil, err
	}
	sn := &snapshot{project: st.ProjectID, gen: cur.Gen, dim: cur.Dim, model: cur.Model, byDoc: map[string][2]int{}}
	if cur.Dim == 0 {
		s.cache.put(sn)
		return sn, nil
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM embed_chunks WHERE project_id = ? AND model = ?`, st.ProjectID, cur.Model).Scan(&total); err != nil {
		return nil, err
	}
	sn.vec = make([]int8, 0, total*cur.Dim)
	rows, err := tx.QueryContext(ctx, `SELECT document_id, ord, start_pos, end_pos, text_hash, dim, vec, mult FROM embed_chunks WHERE project_id = ? AND model = ? ORDER BY document_id, ord`, st.ProjectID, cur.Model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	last := ""
	for rows.Next() {
		var doc, th string
		var ord, a, b, dim int
		var vec []byte
		var mult float64
		if err := rows.Scan(&doc, &ord, &a, &b, &th, &dim, &vec, &mult); err != nil {
			return nil, err
		}
		if dim != cur.Dim || len(vec) != dim || len(th) != 32 || mult <= 0 {
			continue // a row that does not fit the index is not used
		}
		if doc != last {
			if last != "" {
				r := sn.byDoc[last]
				r[1] = sn.n()
				sn.byDoc[last] = r
			}
			sn.docs = append(sn.docs, doc)
			sn.byDoc[doc] = [2]int{sn.n(), 0}
			last = doc
		}
		sn.docIdx = append(sn.docIdx, int32(len(sn.docs)-1))
		sn.ord = append(sn.ord, int32(ord))
		sn.start = append(sn.start, int32(a))
		sn.end = append(sn.end, int32(b))
		var h [16]byte
		for i := 0; i < 16; i++ {
			h[i] = unhex(th[2*i])<<4 | unhex(th[2*i+1])
		}
		sn.hash = append(sn.hash, h)
		sn.mult = append(sn.mult, float32(mult))
		for _, x := range vec {
			sn.vec = append(sn.vec, int8(x))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if last != "" {
		r := sn.byDoc[last]
		r[1] = sn.n()
		sn.byDoc[last] = r
	}
	sn.bytes = int64(len(sn.vec)) + int64(sn.n())*(4+4+4+4+16+4)
	s.cache.put(sn)
	return sn, nil
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 0
}
