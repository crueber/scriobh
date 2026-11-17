package bench

import (
	"database/sql"
	"fmt"
	"math"
	"math/rand"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/semantic"
)

// The semantic search budget is stated for a project of 500,000 words held as
// 10,000 chunks of 768 dimensions: p95 under 80 ms for everything after the
// question has been embedded (the embedding is one request to the endpoint,
// which this machine does not control). The fixture's own text is cut into
// chunks by the real chunker and given pseudo-random vectors (no model is used
// or needed to time a scan); if the text makes fewer than the target, rows are
// added that repeat real chunks' positions with other random vectors, so the
// snippets still check out against the text and the scan is over the stated
// number of vectors.
const (
	semanticChunks = 10_000
	semanticDim    = 768
)

// fillSemantic writes a synthetic index for the bench project and returns how
// many chunks it holds.
func (e *env) fillSemantic() (int, error) {
	type chunk struct {
		doc        string
		start, end int
		hash       string
		words      int
	}
	rows, err := e.d.Read().QueryContext(e.ctx, `SELECT id, content_hash, content FROM documents WHERE project_id = ? AND deleted_at IS NULL AND compendium_id IS NULL AND kind NOT IN ('note','research')`, e.fx.ProjectID)
	if err != nil {
		return 0, err
	}
	byDoc := map[string][]chunk{}
	hashes := map[string]string{}
	var order []string
	for rows.Next() {
		var id, hash, content string
		if err := rows.Scan(&id, &hash, &content); err != nil {
			rows.Close()
			return 0, err
		}
		cs, _ := semantic.Split(semantic.EmbedBody(content))
		if len(cs) == 0 {
			continue
		}
		hashes[id] = hash
		order = append(order, id)
		for _, c := range cs {
			byDoc[id] = append(byDoc[id], chunk{id, c.Start, c.End, semantic.Hash(c.Text), c.Words})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(order) == 0 {
		return 0, fmt.Errorf("the bench project has no text to index")
	}
	r := rand.New(rand.NewSource(Seed))
	now := db.Now()
	total := 0
	err = e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO embed_projects(project_id, enabled, provider_id, model, endpoint_key, enabled_by, consent_at, index_version, dim, gen, created_at, updated_at)
			VALUES (?,1,'aip_bench','bench','bench','bench',?,?,?,1,?,?)`, e.fx.ProjectID, now, semantic.IndexVersion, semanticDim, now, now); err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT INTO embed_chunks(document_id, project_id, ord, start_pos, end_pos, words, text_hash, model, dim, vec, mult) VALUES (?,?,?,?,?,?,?,'bench',?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		add := func(id string, ord int, c chunk) error {
			blob := make([]byte, semanticDim)
			var n2 float64
			for i := range blob {
				v := int8(r.Intn(255) - 127)
				blob[i] = byte(v)
				n2 += float64(v) * float64(v)
			}
			total++
			_, err := stmt.Exec(id, e.fx.ProjectID, ord, c.start, c.end, c.words, c.hash, semanticDim, blob, 1/math.Sqrt(n2))
			return err
		}
		for _, id := range order {
			for i, c := range byDoc[id] {
				if err := add(id, i, c); err != nil {
					return err
				}
			}
		}
		// Pad with repeats of real positions until the stated number.
		for pass := 1; total < semanticChunks; pass++ {
			for _, id := range order {
				cs := byDoc[id]
				if total >= semanticChunks {
					break
				}
				if err := add(id, len(cs)*pass+pass-1+r.Intn(1), cs[(pass*7)%len(cs)]); err != nil {
					return err
				}
			}
		}
		for _, id := range order {
			if _, err := tx.Exec(`INSERT INTO embed_docs(document_id, project_id, content_hash, model_key, index_version, chunks, status, embedded_at) VALUES (?,?,?,?,?,?, 'ok', ?)`,
				id, e.fx.ProjectID, hashes[id], "bench", semantic.IndexVersion, len(byDoc[id]), now); err != nil {
				return err
			}
		}
		return nil
	})
	return total, err
}

// measureSemantic times a semantic search after the embedding (p95 budget 80
// ms) and the first, cold one, which reads the vectors from the database.
func (e *env) measureSemantic(n int, repeatable func(string, int, bool, func(int) error) error, one func(string, func() error) error) error {
	svc, ok := semantic.ServiceOf(e.srv.Reg.Env())
	if !ok {
		return nil
	}
	chunks, err := e.fillSemantic()
	if err != nil {
		return err
	}
	logf(e.log, "  semantic index: %d chunks of %d dimensions\n", chunks, semanticDim)
	r := rand.New(rand.NewSource(Seed + 1))
	query := func() []float32 {
		v := make([]float32, semanticDim)
		var n2 float64
		for i := range v {
			v[i] = float32(r.NormFloat64())
			n2 += float64(v[i]) * float64(v[i])
		}
		inv := float32(1 / math.Sqrt(n2))
		for i := range v {
			v[i] *= inv
		}
		return v
	}
	// A pool of questions, so each run is a different one.
	pool := make([][]float32, 64)
	for i := range pool {
		pool[i] = query()
	}
	search := func(i int) error {
		items, got, err := svc.SearchVector(e.ctx, e.fx.ProjectID, pool[(i+len(pool)*2)%len(pool)], 20, 2)
		if err != nil {
			return err
		}
		if got != chunks || len(items) == 0 {
			return fmt.Errorf("a semantic search over %d chunks returned %d results from %d", chunks, len(items), got)
		}
		return nil
	}
	if err := one("search.semantic.load", func() error { return search(0) }); err != nil {
		return err
	}
	return repeatable("search.semantic", n, true, search)
}
