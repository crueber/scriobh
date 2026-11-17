package semantic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
)

// The worker. It keeps each active project's embeddings current:
//
//   - It polls rather than hooking the save path, so that saving is exactly what
//     it was: it compares each live document's content hash (and the endpoint and
//     the rules the embedding was made under) with what the index recorded, and
//     the documents that must not be embedded (never-send, do-not-compile, in the
//     trash, notes) with what the index holds, and deletes those.
//   - A document is embedded after it has been left alone for Quiet, so typing is
//     not a stream of requests. The text is read in one read transaction (the
//     hash and the body together), sent in batches outside any transaction, and
//     written in one short writer transaction that first checks that the document
//     is still the one that was embedded and still eligible, and the index still
//     on, under the same endpoint. A result that no longer fits is thrown away.
//   - A failure is recorded as a class (never the endpoint's words, which can
//     echo the text) and the project is left alone for a back-off that doubles to
//     a limit. A document the endpoint refuses is marked failed and not tried
//     again until its text changes; three in a row stop the pass.
//   - Whatever happens here is contained: a panic is logged and the loop goes on,
//     nothing waits on this loop, and the editorial path never reads its tables.

// run is the worker loop. Each tick it starts a visit for every project that is due and not already being visited,
// the longest-waiting first, up to ProjectWorkers at once, and does not wait for them: a project whose endpoint
// is slow or dead holds its own visit (which has a deadline, ProjectTimeout), never another project's.
func (s *Service) run(ctx context.Context) {
	tick := time.NewTicker(s.cfg.Poll)
	defer tick.Stop()
	for {
		s.launch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
	}
}

// due lists the projects that may be visited now, those visited longest ago first (round robin): a project in
// back-off is not due, and one already being visited is left to its visit.
func (s *Service) due(ctx context.Context) []string {
	if s.restorePending() {
		s.honourMarker(ctx) // pauses everything, and only then is anything sent again
		if s.restorePending() {
			return nil
		}
	}
	rows, err := s.db().Read().QueryContext(ctx, `SELECT project_id FROM embed_projects WHERE enabled = 1 AND paused_reason = '' AND (next_attempt_at = '' OR next_attempt_at <= ?)`, db.Now())
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("semantic: could not look for projects to index", "err", err)
		}
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ids[:0]
	for _, id := range ids {
		if !s.busy[id] {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := s.visited[out[i]], s.visited[out[j]]
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i] < out[j]
	})
	return out
}

// claim takes a place for a visit of a project, or says there is none.
func (s *Service) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] || len(s.busy) >= s.cfg.ProjectWorkers {
		return false
	}
	s.busy[id] = true
	return true
}

func (s *Service) unclaim(id string) {
	s.mu.Lock()
	delete(s.busy, id)
	s.visited[id] = time.Now()
	s.mu.Unlock()
}

// launch starts a visit for each due project there is room for, and returns at once.
func (s *Service) launch(ctx context.Context) {
	for _, id := range s.due(ctx) {
		if ctx.Err() != nil || !s.claim(id) {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		s.wg.Add(1)
		go func(id string) {
			defer s.wg.Done()
			defer s.unclaim(id)
			if s.visit(ctx, id) {
				s.Wake() // documents are still waiting and nothing is wrong
			}
		}(id)
	}
}

// visit is one visit to a project, with its own deadline and its panics contained. It reports whether the project
// has more to do at once.
func (s *Service) visit(ctx context.Context, id string) (more bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("semantic: the worker recovered from a panic", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			more = false
		}
	}()
	vctx, cancel := context.WithTimeout(ctx, s.cfg.ProjectTimeout)
	defer cancel()
	before := s.progressOf(id)
	more = s.indexProject(vctx, id)
	if vctx.Err() != nil && ctx.Err() == nil {
		if s.progressOf(id) > before {
			return true // slow but alive: it embedded documents in the time, and goes on at the next visit
		}
		// The visit ran out of time and embedded nothing: the endpoint is too slow (or one document needs more time
		// than ProjectTimeout, and will until it is raised). It is recorded as a failure, so the project waits in
		// back-off and the others are not held up by it again soon.
		if st, err := loadState(ctx, s.db().Read(), id); err == nil && st.active() {
			s.fail(ctx, st, "timeout", s.cfg.BackoffBase, true)
		}
		return false
	}
	return more
}

func (s *Service) progressOf(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress[id]
}

func (s *Service) progressed(id string) {
	s.mu.Lock()
	s.progress[id]++
	s.mu.Unlock()
}

// pass visits every due project once, concurrently (at most ProjectWorkers at once), waits for them and says whether
// any has more to do. Tests and the command line use it through Settle.
func (s *Service) pass(ctx context.Context) (more bool) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	ids := s.due(ctx)
	for len(ids) > 0 && ctx.Err() == nil {
		var left []string
		for _, id := range ids {
			if !s.claim(id) {
				left = append(left, id)
				continue
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				defer s.unclaim(id)
				if s.visit(ctx, id) {
					mu.Lock()
					more = true
					mu.Unlock()
				}
			}(id)
		}
		if len(left) == len(ids) { // every place is taken: wait for one
			wg.Wait()
			time.Sleep(2 * time.Millisecond)
		}
		ids = left
	}
	wg.Wait()
	return more
}

// Settle runs passes until no project has more to do or none can make progress. Tests and the command line use it
// to wait for the index.
func (s *Service) Settle(ctx context.Context) {
	for i := 0; i < 10_000 && ctx.Err() == nil; i++ {
		if !s.pass(ctx) {
			return
		}
	}
}

func (s *Service) spec(st state, purpose string, docs int) aicore.EmbedSpec {
	return aicore.EmbedSpec{ProjectID: st.ProjectID, UserID: st.EnabledBy, ProviderID: st.ProviderID, Model: st.Model, Purpose: purpose,
		AllowRemote: st.AllowRemote, ExpectKey: st.EndpointKey, Documents: docs}
}

// indexProject does one visit to a project. It reports whether there is more
// to do at once (documents are still waiting and nothing is wrong).
func (s *Service) indexProject(ctx context.Context, projectID string) bool {
	d := s.db()
	st, err := loadState(ctx, d.Read(), projectID)
	if err != nil || !st.active() {
		return false
	}
	ai, ok := s.ai()
	if !ok {
		return false
	}
	// Conditions that are the owner's or administrator's own switches are waited out without
	// a mark: switching AI back on resumes the index, and the status says what it is waiting for.
	if ai.EmbedGate(ctx, d) != nil {
		return false
	}
	var trashed int
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = ? AND deleted_at IS NULL AND trashed_at IS NULL`, projectID).Scan(&trashed); err != nil || trashed == 0 {
		return false
	}
	if _, err := ai.EmbedResolve(ctx, d, s.spec(st, aicore.EmbedIndex, 0)); err != nil {
		class, _ := aicore.EmbedFailure(err)
		if class == "policy_off" {
			return false
		}
		s.fail(ctx, st, class, 15*time.Second, false)
		return false
	}
	docs, err := loadDocs(ctx, d.Read(), projectID)
	if err != nil {
		return false
	}
	elig, _ := eligible(docs)
	cnt, err := loadCounts(ctx, d.Read(), projectID)
	if err != nil {
		return false
	}
	// What the index holds that it must not: documents that are no longer eligible.
	var gone []string
	for id := range cnt.Docs {
		if !elig[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		if err := s.dropDocs(ctx, projectID, gone); err != nil && ctx.Err() == nil {
			s.log.Warn("semantic: could not drop documents that are no longer eligible", "project", projectID, "err", err)
			return false
		}
	}
	// What is owed. An embedding made under another redaction setting, or with other names hidden, is out of date.
	dk := st.EndpointKey + s.redTag(ctx, projectID)
	var owed []*docRow
	for id := range elig {
		x := docs[id]
		if _, have := cnt.Docs[id]; have && cnt.DocHash[id] == x.Hash && cnt.DocKey[id] == dk && cnt.DocVer[id] == IndexVersion {
			// A document the endpoint refused is tried again after a while: the refusal may have been the
			// endpoint's trouble and not the text's.
			// A document over the project's chunk limit is not tried again by itself: more would be sent for nothing.
			if cnt.Docs[id] != "failed" || cnt.DocErr[id] == "project_cap" || !retryDue(cnt.DocAt[id], s.cfg.FailedRetry) {
				continue
			}
		}
		owed = append(owed, x)
	}
	if len(owed) == 0 {
		if st.IndexedAt == "" || st.FailCount > 0 || st.LastErrorClass != "" {
			_, _ = d.Writer().ExecContext(ctx, `UPDATE embed_projects SET indexed_at = ?, fail_count = 0, last_error_class = '', last_error_at = '', next_attempt_at = '' WHERE project_id = ? AND enabled = 1`, db.Now(), projectID)
		}
		return false
	}
	sort.Slice(owed, func(i, j int) bool {
		if owed[i].UpdatedAt != owed[j].UpdatedAt {
			return owed[i].UpdatedAt < owed[j].UpdatedAt
		}
		return owed[i].ID < owed[j].ID
	})
	var ready []*docRow
	cut := db.NowTime().Add(-s.cfg.Quiet)
	for _, x := range owed {
		if s.cfg.Quiet > 0 {
			if t, err := db.Parse(x.UpdatedAt); err == nil && t.After(cut) {
				continue // still being written
			}
		}
		ready = append(ready, x)
	}
	if len(ready) == 0 {
		return false
	}
	caughtUp := len(ready) == len(owed) && len(ready) <= s.cfg.DocsPerPass // everything owed is in this pass
	more := len(ready) > s.cfg.DocsPerPass
	if more {
		ready = ready[:s.cfg.DocsPerPass]
	}

	type result struct {
		id  string
		err error
	}
	results := make([]result, len(ready))
	sem := make(chan struct{}, s.cfg.Parallel)
	var wg sync.WaitGroup
	var stop sync.Once
	stopped := make(chan struct{})
	var docFails, embedded atomic.Int64
	for i, x := range ready {
		select {
		case <-stopped:
		case <-ctx.Done():
		case sem <- struct{}{}:
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if r := recover(); r != nil {
						s.log.Error("semantic: embedding a document panicked", "panic", fmt.Sprint(r))
						results[i] = result{id, errors.New("panic")}
					}
				}()
				n, err := s.embedDoc(ctx, st, id)
				results[i] = result{id, err}
				switch {
				case err == nil:
					embedded.Add(int64(n))
				case isDocLevel(err):
					// Three refusals in a row with nothing embedded: it is the endpoint, not the texts.
					if docFails.Add(1) >= 3 && embedded.Load() == 0 {
						stop.Do(func() { close(stopped) })
					}
				default:
					stop.Do(func() { close(stopped) }) // the endpoint is the problem: no more requests this pass
				}
			}(i, x.ID)
			continue
		}
		break
	}
	wg.Wait()
	if ctx.Err() != nil {
		return false
	}
	var docErrs []result
	var projectErr error
	ok2 := 0
	for _, r := range results {
		switch {
		case r.id == "":
		case r.err == nil:
			ok2++
		case isDocLevel(r.err):
			docErrs = append(docErrs, r)
		default:
			if projectErr == nil {
				projectErr = r.err
			}
		}
	}
	switch {
	case projectErr != nil:
		s.handle(ctx, st, projectErr)
		return false
	case len(docErrs) >= 3 && embedded.Load() == 0:
		class, _ := aicore.EmbedFailure(docErrs[0].err)
		s.fail(ctx, st, class, s.cfg.BackoffBase, true)
		return false
	}
	docFailures := len(docErrs)
	for _, r := range docErrs {
		class, _ := aicore.EmbedFailure(r.err)
		_ = s.markFailed(ctx, st, dk, r.id, class)
	}
	if ok2 > 0 || docFailures > 0 {
		_, _ = d.Writer().ExecContext(ctx, `UPDATE embed_projects SET fail_count = 0, last_error_class = '', last_error_at = '', next_attempt_at = '' WHERE project_id = ?`, projectID)
	}
	if caughtUp {
		_, _ = d.Writer().ExecContext(ctx, `UPDATE embed_projects SET indexed_at = ? WHERE project_id = ?`, db.Now(), projectID)
	}
	return more
}

// retryDue says a failed document is due another try: it failed longer ago than d.
func retryDue(at string, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t, err := db.Parse(at)
	return err != nil || db.NowTime().Sub(t) >= d
}

// redTag is what the project's redaction setting adds to the key an embedding is recorded under: "" when it is off.
func (s *Service) redTag(ctx context.Context, projectID string) string {
	ai, ok := s.ai()
	if !ok {
		return ""
	}
	r, err := ai.NewRedactor(ctx, s.db(), projectID)
	if err != nil || r.Fingerprint() == "" {
		return ""
	}
	return "|red:" + r.Fingerprint()
}

// errDimChanged says the endpoint now answers with vectors of another length
// than the ones the index holds: its model changed under the same name.
var errDimChanged = errors.New("semantic: the vectors changed length")

// isDocLevel says an error is about one document's text (the endpoint refused
// it, or answered nonsense for it) and not about the endpoint being there.
func isDocLevel(err error) bool {
	if pe, ok := lib.AsProviderError(err); ok {
		switch pe.Class {
		case lib.ClassRejected, lib.ClassBadResponse, lib.ClassTooLarge:
			return true
		}
	}
	return false
}

// handle records a failure that stops the project for a while.
func (s *Service) handle(ctx context.Context, st state, err error) {
	if errors.Is(err, errDimChanged) {
		s.pause(ctx, st, "model_changed")
		return
	}
	class, retryable := aicore.EmbedFailure(err)
	base := s.cfg.BackoffBase
	switch class {
	case "budget_exceeded", "price_unknown":
		base = 10 * time.Minute
	case lib.ClassAuth, lib.ClassNotFound, lib.ClassCredit, lib.ClassBlocked, "key_unreadable":
		base = 5 * time.Minute
	case "cancelled":
		return // the server is stopping
	}
	if !retryable && base < 5*time.Minute {
		base = 5 * time.Minute
	}
	s.fail(ctx, st, class, base, true)
}

// fail records a failure class and the time before the next attempt. grow doubles
// the wait with each failure in a row, up to BackoffMax.
func (s *Service) fail(ctx context.Context, st state, class string, base time.Duration, grow bool) {
	n := st.FailCount + 1
	wait := base
	if grow {
		for i := 1; i < n && wait < s.cfg.BackoffMax; i++ {
			wait *= 2
		}
	}
	wait = min(wait, s.cfg.BackoffMax)
	next := db.Format(db.NowTime().Add(wait))
	_, err := s.db().Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE embed_projects SET last_error_class = ?, last_error_at = ?, fail_count = ?, next_attempt_at = ? WHERE project_id = ?`,
		class, db.Now(), n, next, st.ProjectID)
	if err != nil {
		s.log.Warn("semantic: could not record a failure", "project", st.ProjectID, "err", err)
		return
	}
	s.log.Warn("semantic: indexing failed, will retry", "project", st.ProjectID, "class", class, "wait", wait.String())
}

// pause stops a project until its owner acts: the reason is shown in the status.
func (s *Service) pause(ctx context.Context, st state, reason string) {
	_, err := s.db().Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE embed_projects SET paused_reason = ?, last_error_class = ?, last_error_at = ? WHERE project_id = ?`, reason, reason, db.Now(), st.ProjectID)
	if err != nil {
		s.log.Warn("semantic: could not pause an index", "project", st.ProjectID, "err", err)
		return
	}
	s.log.Warn("semantic: an index was paused", "project", st.ProjectID, "reason", reason)
}

// markFailed records that a document's text was refused, so it is not tried
// again until it changes.
func (s *Service) markFailed(ctx context.Context, st state, dk, docID, class string) error {
	return s.db().WithTx(ctx, func(tx *sql.Tx) error {
		var hash string
		if err := tx.QueryRow(`SELECT content_hash FROM documents WHERE id = ? AND project_id = ?`, docID, st.ProjectID).Scan(&hash); err != nil {
			return nil
		}
		if _, err := tx.Exec(`DELETE FROM embed_chunks WHERE document_id = ?`, docID); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO embed_docs(document_id, project_id, content_hash, model_key, index_version, chunks, status, error_class, embedded_at) VALUES (?,?,?,?,?,0,'failed',?,?)
			ON CONFLICT(document_id) DO UPDATE SET content_hash = excluded.content_hash, model_key = excluded.model_key, index_version = excluded.index_version, chunks = 0,
				status = 'failed', error_class = excluded.error_class, embedded_at = excluded.embedded_at`, docID, st.ProjectID, hash, dk, IndexVersion, class, db.Now())
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE embed_projects SET gen = gen + 1 WHERE project_id = ?`, st.ProjectID)
		return err
	})
}

// dropDocs deletes the embeddings of documents that may no longer be embedded.
func (s *Service) dropDocs(ctx context.Context, projectID string, ids []string) error {
	return s.db().WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM embed_chunks WHERE document_id = ?`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM embed_docs WHERE document_id = ?`, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE embed_projects SET gen = gen + 1 WHERE project_id = ?`, projectID)
		return err
	})
}

type vecRow struct {
	q    []int8
	mult float32
}

// embedDoc embeds one document and stores the result. A nil error with nothing
// stored means the document was gone, changed or no longer eligible; the next
// pass sees it as it is.
func (s *Service) embedDoc(ctx context.Context, st state, id string) (int, error) {
	d := s.db()
	ai, ok := s.ai()
	if !ok {
		return 0, errors.New("no AI service")
	}
	var hash, content string
	err := d.Read().QueryRowContext(ctx, `SELECT content_hash, content FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, st.ProjectID).Scan(&hash, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	body := EmbedBody(content)
	chunks, truncated := Split(body)
	// What goes out is the text as the project's redaction setting allows (the same map an AI request uses); the
	// chunk's hash and offsets stay those of the plain text, which is what a snippet is checked against. The key the
	// embedding is recorded under holds the setting's fingerprint, so a change of it, or of the names it hides,
	// embeds the document again.
	red, err := ai.NewRedactor(ctx, d, st.ProjectID)
	if err != nil {
		return 0, err
	}
	dk := st.EndpointKey
	if fp := red.Fingerprint(); fp != "" {
		dk += "|red:" + fp
		red.Learn(body)
	}
	if len(chunks) == 0 {
		return 0, s.writeDoc(ctx, st, dk, id, hash, nil, nil, "empty")
	}
	// Over the project's limit: said before anything is sent, and not tried again until the text changes.
	var others int
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM embed_chunks WHERE project_id = ? AND document_id <> ?`, st.ProjectID, id).Scan(&others); err != nil {
		return 0, err
	}
	if others+len(chunks) > s.cfg.MaxChunksPerProject {
		return 0, s.markFailed(ctx, st, dk, id, "project_cap")
	}
	rows := make([]vecRow, 0, len(chunks))
	dim := 0
	for i := 0; i < len(chunks); i += s.cfg.BatchSize {
		j := min(i+s.cfg.BatchSize, len(chunks))
		texts := make([]string, 0, j-i)
		for _, c := range chunks[i:j] {
			texts = append(texts, red.Apply(c.Text))
		}
		spec := s.spec(st, aicore.EmbedIndex, 1)
		spec.CheckRedaction, spec.Redaction = true, red.Mode()
		// Before every request, the last moment: is this still ours to send?
		spec.Guard = func(gctx context.Context) error { return s.stillOK(gctx, st, id, hash) }
		res, err := ai.Embed(ctx, d, spec, texts)
		if err != nil {
			if class, _ := aicore.EmbedFailure(err); class == "consent_changed" || class == "redaction_changed" {
				return 0, nil // stopped before sending; the next pass sees how things are
			}
			return 0, err
		}
		if dim != 0 && res.Dim != dim || st.Dim != 0 && res.Dim != st.Dim {
			return 0, errDimChanged
		}
		dim = res.Dim
		for _, v := range res.Vectors {
			q, mult, ok := quantise(v)
			if !ok {
				return 0, &lib.ProviderError{Class: lib.ClassBadResponse, Detail: "a vector with no direction"}
			}
			rows = append(rows, vecRow{q, mult})
		}
	}
	status := "ok"
	if truncated {
		status = "truncated"
	}
	err = s.writeDoc(ctx, st, dk, id, hash, chunks, rows, status)
	if err == nil {
		s.progressed(st.ProjectID)
	}
	return len(rows), err
}

var errDiscard = errors.New("discard")

// writeDoc replaces a document's chunks. It writes only if everything the
// result was made under still holds.
func (s *Service) writeDoc(ctx context.Context, st state, dk, id, hash string, chunks []Chunk, rows []vecRow, status string) error {
	dim := 0
	if len(rows) > 0 {
		dim = len(rows[0].q)
	}
	err := s.db().WithTx(ctx, func(tx *sql.Tx) error {
		cur, err := loadState(ctx, tx, st.ProjectID)
		if err != nil {
			return err
		}
		if !cur.active() || cur.EndpointKey != st.EndpointKey {
			return errDiscard // switched off, paused, or another endpoint: nothing it made is kept
		}
		var now string
		if err := tx.QueryRow(`SELECT content_hash FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, st.ProjectID).Scan(&now); err != nil || now != hash {
			return errDiscard // gone or changed since it was read
		}
		if ok, err := liveEligible(tx, id); err != nil {
			return err
		} else if !ok {
			return errDiscard // flagged never-send, or moved somewhere that is not embedded, during the call
		}
		if dim > 0 {
			var others int
			if err := tx.QueryRow(`SELECT count(*) FROM embed_chunks WHERE project_id = ? AND document_id <> ?`, st.ProjectID, id).Scan(&others); err != nil {
				return err
			}
			if others+len(rows) > s.cfg.MaxChunksPerProject {
				if _, err := tx.Exec(`DELETE FROM embed_chunks WHERE document_id = ?`, id); err != nil {
					return err
				}
				return s.putDoc(tx, st, dk, id, hash, 0, "failed", "project_cap")
			}
			res, err := tx.Exec(`UPDATE embed_projects SET dim = ? WHERE project_id = ? AND (dim = 0 OR dim = ?)`, dim, st.ProjectID, dim)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errDimChanged
			}
		}
		if _, err := tx.Exec(`DELETE FROM embed_chunks WHERE document_id = ?`, id); err != nil {
			return err
		}
		if len(rows) > 0 {
			stmt, err := tx.Prepare(`INSERT INTO embed_chunks(document_id, project_id, ord, start_pos, end_pos, words, text_hash, model, dim, vec, mult) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for i, r := range rows {
				c := chunks[i]
				blob := make([]byte, len(r.q))
				for k, x := range r.q {
					blob[k] = byte(x)
				}
				if _, err := stmt.Exec(id, st.ProjectID, i, c.Start, c.End, c.Words, Hash(c.Text), st.Model, dim, blob, float64(r.mult)); err != nil {
					return err
				}
			}
		}
		return s.putDoc(tx, st, dk, id, hash, len(rows), status, "")
	})
	if errors.Is(err, errDiscard) {
		return nil
	}
	return err
}

func (s *Service) putDoc(tx *sql.Tx, st state, dk, id, hash string, chunks int, status, class string) error {
	if _, err := tx.Exec(`INSERT INTO embed_docs(document_id, project_id, content_hash, model_key, index_version, chunks, status, error_class, embedded_at) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(document_id) DO UPDATE SET content_hash = excluded.content_hash, model_key = excluded.model_key, index_version = excluded.index_version,
			chunks = excluded.chunks, status = excluded.status, error_class = excluded.error_class, embedded_at = excluded.embedded_at`,
		id, st.ProjectID, hash, dk, IndexVersion, chunks, status, class, db.Now()); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE embed_projects SET gen = gen + 1 WHERE project_id = ?`, st.ProjectID)
	return err
}

// liveEligible walks a document's chain of parents to the root in one recursive query and applies the same rule
// as skipReason, so the answer is as of this moment and not of the moment the document was read. It goes to the root
// however deep; a chain that reaches the safety limit, or loops, is not embedded.
func liveEligible(tx *sql.Tx, id string) (bool, error) {
	rows, err := tx.Query(`WITH RECURSIVE chain(id, parent_id, kind, ex, inc, del, depth) AS (
			SELECT id, COALESCE(parent_id,''), kind, ai_exclude, include_in_compile, deleted_at IS NOT NULL, 0 FROM documents WHERE id = ?
			UNION ALL
			SELECT d.id, COALESCE(d.parent_id,''), d.kind, d.ai_exclude, d.include_in_compile, d.deleted_at IS NOT NULL, c.depth + 1
			FROM documents d JOIN chain c ON d.id = c.parent_id WHERE c.depth < ?)
		SELECT id, parent_id, kind, ex, inc, del FROM chain`, id, safetyDepth)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	docs := map[string]*docRow{}
	n := 0
	last := ""
	for rows.Next() {
		var x docRow
		var ex, comp, tr int
		if err := rows.Scan(&x.ID, &x.Parent, &x.Kind, &ex, &comp, &tr); err != nil {
			return false, err
		}
		x.AIExclude, x.InCompile, x.Trashed = ex == 1, comp == 1, tr == 1
		docs[x.ID] = &x
		n++
		last = x.Parent
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if n == 0 || (n > safetyDepth && last != "") {
		return false, nil // a document that is not there, or a chain too long to trust
	}
	return skipReason(docs, id) == "", nil
}

// errStale says what was about to be sent may no longer be sent.
var errStale = errors.New("semantic: the consent or the document changed")

// stillOK is the check made before every request that carries a document's text, in one short read transaction: the
// index is on and not paused, it is the endpoint the owner confirmed, the project is neither in the trash nor
// purged, the document is still eligible (not flagged never-send itself or anywhere above it, not do-not-compile,
// not trashed) and its text is the text that was read. Anything else and nothing more is sent from this document.
// (The AI policy, the kill switch and the agent rules are re-read by Service.Embed itself, in the same call.)
func (s *Service) stillOK(ctx context.Context, st state, docID, hash string) error {
	tx, err := s.db().Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled, live int
	var paused, key string
	var now sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT p.enabled, p.paused_reason, p.endpoint_key,
			(SELECT count(*) FROM projects WHERE id = p.project_id AND deleted_at IS NULL AND trashed_at IS NULL),
			(SELECT content_hash FROM documents WHERE id = ? AND project_id = p.project_id AND deleted_at IS NULL)
		FROM embed_projects p WHERE p.project_id = ?`, docID, st.ProjectID).Scan(&enabled, &paused, &key, &live, &now)
	if err != nil {
		return errStale // no index row: purged
	}
	if s.restorePending() || enabled != 1 || paused != "" || key != st.EndpointKey || live != 1 || !now.Valid || now.String != hash {
		return errStale
	}
	if ok, err := liveEligible(tx, docID); err != nil || !ok {
		return errStale
	}
	return nil
}

// stillActive is the same check for a request that carries no document, a search's question.
func (s *Service) stillActive(ctx context.Context, st state) error {
	var enabled, live int
	var paused, key string
	err := s.db().Read().QueryRowContext(ctx, `SELECT p.enabled, p.paused_reason, p.endpoint_key,
			(SELECT count(*) FROM projects WHERE id = p.project_id AND deleted_at IS NULL AND trashed_at IS NULL)
		FROM embed_projects p WHERE p.project_id = ?`, st.ProjectID).Scan(&enabled, &paused, &key, &live)
	if s.restorePending() || err != nil || enabled != 1 || paused != "" || key != st.EndpointKey || live != 1 {
		return errStale
	}
	return nil
}
