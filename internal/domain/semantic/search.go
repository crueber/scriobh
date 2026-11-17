package semantic

import (
	"context"
	"database/sql"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

const (
	defaultLimit  = 10
	maxLimit      = 50
	maxQueryBytes = 2000
	defaultPerDoc = 2
	maxPerDoc     = 5
	maxCandidates = 600
)

// SearchIn is a semantic search.
type SearchIn struct {
	ProjectID   string `path:"id" json:"-" desc:"Project id."`
	Q           string `query:"q" desc:"What to look for, in your own words: a sentence or a question, up to 2,000 bytes. It is sent to the embedding endpoint."`
	Limit       int    `query:"limit" desc:"Results: 1 to 50, default 10."`
	PerDocument int    `query:"per_document" desc:"At most this many chunks from one document: 1 to 5, default 2."`
}

// ProjectRef names the project.
func (in SearchIn) ProjectRef() string { return in.ProjectID }

// Hit is one chunk of the manuscript.
type Hit struct {
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Chunk      int     `json:"chunk" desc:"The chunk's number within its document."`
	Start      int     `json:"start" desc:"Character offset of the chunk in the document's plain text, as search.query's offset counts."`
	End        int     `json:"end"`
	Score      float64 `json:"score" desc:"Cosine similarity, -1 to 1; only the order is meaningful, and it depends on the model."`
	Snippet    string  `json:"snippet" desc:"The start of the chunk, cut from the text as it is now."`
}

// SearchOut is a page of results.
type SearchOut struct {
	Items   []Hit  `json:"items"`
	Model   string `json:"model"`
	Chunks  int    `json:"indexed_chunks" desc:"Chunks searched."`
	Pending int    `json:"queue_depth" desc:"Documents not yet embedded: what they say is not searched yet."`
	Partial bool   `json:"partial,omitempty" desc:"True when documents are still waiting to be embedded."`
}

// verified cuts a chunk's text from the document as it is now and checks it is
// what was embedded. A chunk whose text changed since (or became private) is not
// shown: the index may be a few seconds behind, and it must never put words on a
// screen that are no longer the document's.
func verified(body string, start, end int32, want [16]byte) (string, bool) {
	text := runeSlice(body, int(start), int(end))
	if text == "" {
		return "", false
	}
	got := Hash(text)
	return text, got == hex.EncodeToString(want[:])
}

// round4 rounds a score to four places. A score is always a finite number: JSON cannot carry anything else.
func round4(f float32) float64 {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1e4) / 1e4
}

// docFilter returns, for each document in the snapshot, whether it may be
// shown right now, and the document rows. The rule is the worker's own, applied
// at the moment of the search, so a flag set a second ago hides the chunks
// before the worker has deleted them.
func docFilter(ctx context.Context, d *db.DB, projectID string, sn *snapshot) ([]bool, map[string]*docRow, error) {
	docs, err := loadDocs(ctx, d.Read(), projectID)
	if err != nil {
		return nil, nil, err
	}
	ok := make([]bool, len(sn.docs))
	for i, id := range sn.docs {
		ok[i] = docs[id] != nil && skipReason(docs, id) == ""
	}
	return ok, docs, nil
}

func (sn *snapshot) scan(q []float32, ok []bool, k int) []hit {
	tk := newTopK(k)
	dim := sn.dim
	for i, n := 0, sn.n(); i < n; i++ {
		if !ok[sn.docIdx[i]] {
			continue
		}
		score := dot(sn.vec[i*dim:(i+1)*dim], q) * sn.mult[i]
		tk.offer(hit{i, score})
	}
	return tk.sorted()
}

// texts gives the current text of documents to cut snippets from. The first
// source is doc_text.body, which is written with the document and is, for
// ordinary text, exactly what was embedded (it costs nothing to read). A chunk
// whose hash is not found there is checked against the embeddable text made
// afresh from the document's Markdown, which is the rule's own result for the
// rare document with a malformed private note. A chunk found in neither is stale.
type texts struct {
	ctx     context.Context
	d       *db.DB
	project string
	plain   map[string]string
	canon   map[string]string
}

func newTexts(ctx context.Context, d *db.DB, projectID string, ids []string) (*texts, error) {
	t := &texts{ctx: ctx, d: d, project: projectID, plain: map[string]string{}, canon: map[string]string{}}
	if len(ids) == 0 {
		return t, nil
	}
	args := []any{projectID}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	rows, err := d.Read().QueryContext(ctx, `SELECT document_id, body FROM doc_text WHERE project_id = ? AND document_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		t.plain[id] = body
	}
	return t, rows.Err()
}

// chunk returns the text of a chunk as it is now, if it is the text that was embedded.
func (t *texts) chunk(id string, start, end int32, want [16]byte) (string, bool) {
	if text, ok := verified(t.plain[id], start, end, want); ok {
		return text, true
	}
	body, ok := t.canon[id]
	if !ok {
		var content string
		if err := t.d.Read().QueryRowContext(t.ctx, `SELECT content FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, t.project).Scan(&content); err == nil {
			body = EmbedBody(content)
		}
		t.canon[id] = body
	}
	return verified(body, start, end, want)
}

func cleanQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", ops.Invalid("q", "Write what you are looking for.")
	}
	if len(q) > maxQueryBytes {
		return "", ops.Invalid("q", "That is too long to search for: up to 2,000 bytes.")
	}
	if !utf8.ValidString(q) {
		return "", ops.Invalid("q", "The question is not valid text.")
	}
	q = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, q)
	return strings.TrimSpace(q), nil
}

func (s *Service) semanticSearch(c *ops.Ctx, in SearchIn) (SearchOut, error) {
	q, err := cleanQuery(in.Q)
	if err != nil {
		return SearchOut{}, err
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	per := in.PerDocument
	switch {
	case per <= 0:
		per = defaultPerDoc
	case per > maxPerDoc:
		per = maxPerDoc
	}
	ai, st, err := s.ready(c, in.ProjectID)
	if err != nil {
		return SearchOut{}, err
	}
	if !s.allow("q:"+c.P.UserID, s.cfg.QueryRatePerMinute) {
		return SearchOut{}, ops.RateLimited(5)
	}
	out := SearchOut{Items: []Hit{}, Model: st.Model}
	if st.Dim == 0 { // nothing has been embedded yet: there is nothing to compare with, and no reason to send the question
		out.Partial = true
		out.Pending = s.queue(c, in.ProjectID, st)
		return out, nil
	}
	// The question goes out as the project's redaction setting allows, by the same map an AI request uses.
	red, rerr := ai.NewRedactor(c, c.DB(), in.ProjectID)
	if rerr != nil {
		return SearchOut{}, ops.Internal(rerr)
	}
	red.Learn(q)
	spec := s.spec(st, aicore.EmbedQuery, 0)
	spec.ChargeUserID, spec.ViaAPIKey = c.P.UserID, c.P.Via == "apikey"
	spec.CheckRedaction, spec.Redaction = true, red.Mode()
	spec.Guard = func(gctx context.Context) error { return s.stillActive(gctx, st) } // the last look before it leaves
	res, eerr := ai.Embed(c, c.DB(), spec, []string{red.Apply(q)})
	if eerr != nil {
		return SearchOut{}, providerFailure(c.P, eerr)
	}
	if res.Dim != st.Dim {
		s.pauseNow(c, st, "model_changed")
		return SearchOut{}, ops.ConflictWith("The embedding model now answers with vectors of a different size than the index holds. The index is paused; its owner can rebuild it.", map[string]any{"semantic": "paused", "reason": "model_changed"})
	}
	qv, ok := unit(res.Vectors[0])
	if !ok {
		return SearchOut{}, &ops.Error{Code: ops.CodeProvider, Message: "The embedding endpoint could not be used."}
	}
	items, chunks, err := s.searchVector(c, in.ProjectID, qv, limit, per)
	if err != nil {
		return SearchOut{}, err
	}
	out.Items, out.Chunks = items, chunks
	out.Pending = s.queue(c, in.ProjectID, st)
	out.Partial = out.Pending > 0
	return out, nil
}

// SearchVector is the part of a search that comes after the question has been
// embedded: the project's vectors in memory, the documents that may be shown
// right now, the best chunks and their snippets. The budget (p95 under 80 ms
// for 10,000 chunks of 768 dimensions) is for this part, as the question's
// embedding is one request to the endpoint. q must be a unit vector of the
// index's dimension. It reads only: no endpoint, no authorization (the caller
// has done that).
func (s *Service) SearchVector(ctx context.Context, projectID string, q []float32, limit, per int) ([]Hit, int, error) {
	items, n, err := s.searchVector(ctx, projectID, q, limit, per)
	if err != nil {
		return nil, 0, err
	}
	return items, n, nil
}

func (s *Service) searchVector(ctx context.Context, projectID string, qv []float32, limit, per int) ([]Hit, int, error) {
	// The index may have moved while the question was out; read the generation again.
	st, err := loadState(ctx, s.db().Read(), projectID)
	if err != nil || !st.active() {
		return nil, 0, ops.Conflict("Semantic search was switched off while the search ran.")
	}
	sn, err := s.snapshotFor(ctx, s.db(), st)
	if err != nil {
		return nil, 0, ops.Internal(err)
	}
	if sn.dim != len(qv) {
		return nil, 0, ops.Conflict("The index changed while the search ran. Try again.")
	}
	okDocs, docs, err := docFilter(ctx, s.db(), projectID, sn)
	if err != nil {
		return nil, 0, ops.Internal(err)
	}
	hits := sn.scan(qv, okDocs, min(maxCandidates, limit*per*3+10))
	items, err := s.assemble(ctx, projectID, sn, docs, hits, limit, per)
	if err != nil {
		return nil, 0, err
	}
	return items, sn.n(), nil
}

// assemble turns scored chunks into results: at most per from a document, the
// text cut from the current body and checked, the best limit of them.
func (s *Service) assemble(ctx context.Context, projectID string, sn *snapshot, docs map[string]*docRow, hits []hit, limit, per int) ([]Hit, error) {
	var ids []string
	seen := map[string]bool{}
	for _, h := range hits {
		id := sn.docs[sn.docIdx[h.idx]]
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	tx, err := newTexts(ctx, s.db(), projectID, ids)
	if err != nil {
		return nil, ops.Internal(err)
	}
	items := []Hit{}
	perDoc := map[string]int{}
	for _, h := range hits {
		if len(items) >= limit {
			break
		}
		id := sn.docs[sn.docIdx[h.idx]]
		if perDoc[id] >= per {
			continue
		}
		text, ok := tx.chunk(id, sn.start[h.idx], sn.end[h.idx], sn.hash[h.idx])
		if !ok {
			continue // changed since it was embedded
		}
		x := docs[id]
		perDoc[id]++
		items = append(items, Hit{DocumentID: id, Title: x.Title, Kind: x.Kind, Status: x.Status, Chunk: int(sn.ord[h.idx]), Start: int(sn.start[h.idx]), End: int(sn.end[h.idx]),
			Score: round4(h.score), Snippet: snippetOf(text)})
	}
	return items, nil
}

// queue counts the documents not yet embedded (or embedded under another endpoint, rules or text). It reads the
// document rows and embed_docs only, never the chunks: a search must not pass over the vectors twice.
func (s *Service) queue(ctx context.Context, projectID string, st state) int {
	docs, err := loadDocs(ctx, s.db().Read(), projectID)
	if err != nil {
		return 0
	}
	elig, _ := eligible(docs)
	rows, err := s.db().Read().QueryContext(ctx, `SELECT document_id, content_hash, model_key, index_version FROM embed_docs WHERE project_id = ?`, projectID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	type rec struct {
		hash, key string
		ver       int
	}
	have := map[string]rec{}
	for rows.Next() {
		var id string
		var r rec
		if err := rows.Scan(&id, &r.hash, &r.key, &r.ver); err != nil {
			return 0
		}
		have[id] = r
	}
	dk := st.EndpointKey + s.redTag(ctx, projectID)
	n := 0
	for id := range elig {
		if r, ok := have[id]; !ok || r.hash != docs[id].Hash || r.key != dk || r.ver != IndexVersion {
			n++
		}
	}
	return n
}

// pauseNow pauses an index because the endpoint's model changed under it.
func (s *Service) pauseNow(ctx context.Context, st state, reason string) {
	_, _ = s.db().Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE embed_projects SET paused_reason = ?, last_error_class = ?, last_error_at = ? WHERE project_id = ? AND paused_reason = ''`,
		reason, reason, db.Now(), st.ProjectID)
}

// ---- scenes like this one ------------------------------------------------------------------

// SimilarIn asks for documents like one.
type SimilarIn struct {
	ProjectID         string `path:"id" json:"-" desc:"Project id."`
	DocumentID        string `query:"document_id" desc:"The scene (or chapter) to find others like."`
	Chunk             string `query:"chunk" desc:"Instead of the whole document: the number of one of its chunks (a search result gives it). Empty: the whole document."`
	Limit             int    `query:"limit" desc:"Results: 1 to 50, default 10."`
	IncludeNeighbours bool   `query:"include_neighbours" desc:"By default the documents just before and after this one in reading order are left out (they are close in meaning because they are close in the story). true keeps them."`
}

// ProjectRef names the project.
func (in SimilarIn) ProjectRef() string { return in.ProjectID }

// SimilarHit is a document like the one asked about.
type SimilarHit struct {
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Chunk      int     `json:"chunk" desc:"The chunk of that document that is closest."`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
	Snippet    string  `json:"snippet"`
}

// SimilarOut is the documents, best first.
type SimilarOut struct {
	Items   []SimilarHit `json:"items"`
	Source  string       `json:"document_id" desc:"The document asked about."`
	Chunk   *int         `json:"chunk,omitempty"`
	Model   string       `json:"model"`
	Pending int          `json:"queue_depth"`
	Partial bool         `json:"partial,omitempty"`
}

func (s *Service) similar(c *ops.Ctx, in SimilarIn) (SimilarOut, error) {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if in.DocumentID == "" {
		return SimilarOut{}, ops.Invalid("document_id", "Say which document.")
	}
	var want *int
	if in.Chunk != "" {
		n, err := strconv.Atoi(strings.TrimSpace(in.Chunk))
		if err != nil || n < 0 {
			return SimilarOut{}, ops.Invalid("chunk", "The chunk is a number from 0.")
		}
		want = &n
	}
	_, st, err := s.ready(c, in.ProjectID)
	if err != nil {
		return SimilarOut{}, err
	}
	sn, err := s.snapshotFor(c, c.DB(), st)
	if err != nil {
		return SimilarOut{}, ops.Internal(err)
	}
	okDocs, docs, err := docFilter(c, c.DB(), in.ProjectID, sn)
	if err != nil {
		return SimilarOut{}, ops.Internal(err)
	}
	src := docs[in.DocumentID]
	if src == nil || src.Trashed {
		return SimilarOut{}, ops.NotFound("document")
	}
	out := SimilarOut{Items: []SimilarHit{}, Source: in.DocumentID, Chunk: want, Model: st.Model}
	out.Pending = s.queue(c, in.ProjectID, st)
	out.Partial = out.Pending > 0
	rng, indexed := sn.byDoc[in.DocumentID]
	if !indexed || rng[1] <= rng[0] || !isLive(sn, okDocs, in.DocumentID) {
		if r := skipReason(docs, in.DocumentID); r != "" {
			return SimilarOut{}, ops.ConflictWith("That document is not part of the semantic index ("+reasonWords(r)+").", map[string]any{"reason": r})
		}
		return SimilarOut{}, ops.ConflictWith("That document has not been embedded yet. Try again in a moment.", map[string]any{"reason": "not_indexed"})
	}
	// The query: one chunk, or the average direction of all of the document's chunks.
	qv := make([]float32, sn.dim)
	first, last := rng[0], rng[1]
	if want != nil {
		found := false
		for i := first; i < last; i++ {
			if int(sn.ord[i]) == *want {
				first, last, found = i, i+1, true
				break
			}
		}
		if !found {
			return SimilarOut{}, ops.Invalid("chunk", "That document has no such chunk.")
		}
	}
	for i := first; i < last; i++ {
		u := sn.vector(i)
		m := sn.mult[i]
		for k := range qv {
			qv[k] += float32(u[k]) * m
		}
	}
	qv, ok := unit(qv)
	if !ok {
		return SimilarOut{}, ops.Conflict("That document has no usable embedding. Rebuild the index.")
	}
	skip := map[string]bool{in.DocumentID: true}
	if !in.IncludeNeighbours {
		order := readingOrder(docs)
		for i, id := range order {
			if id != in.DocumentID {
				continue
			}
			for j := i - 1; j >= 0; j-- { // the nearest earlier document that holds text
				if _, ok := sn.byDoc[order[j]]; ok {
					skip[order[j]] = true
					break
				}
			}
			for j := i + 1; j < len(order); j++ {
				if _, ok := sn.byDoc[order[j]]; ok {
					skip[order[j]] = true
					break
				}
			}
			break
		}
	}
	// One pass: the best chunk of each document.
	best := make([]float32, len(sn.docs))
	bestAt := make([]int, len(sn.docs))
	for i := range best {
		best[i], bestAt[i] = -2, -1
	}
	dim := sn.dim
	for i, n := 0, sn.n(); i < n; i++ {
		di := sn.docIdx[i]
		if !okDocs[di] || skip[sn.docs[di]] {
			continue
		}
		score := dot(sn.vec[i*dim:(i+1)*dim], qv) * sn.mult[i]
		if score > best[di] {
			best[di], bestAt[di] = score, i
		}
	}
	tk := newTopK(min(maxCandidates, limit*2+10))
	for di, at := range bestAt {
		if at >= 0 {
			tk.offer(hit{at, best[di]})
		}
	}
	hits := tk.sorted()
	var ids []string
	for _, h := range hits {
		ids = append(ids, sn.docs[sn.docIdx[h.idx]])
	}
	tx, err := newTexts(c, s.db(), in.ProjectID, ids)
	if err != nil {
		return SimilarOut{}, ops.Internal(err)
	}
	for _, h := range hits {
		if len(out.Items) >= limit {
			break
		}
		id := sn.docs[sn.docIdx[h.idx]]
		text, ok := tx.chunk(id, sn.start[h.idx], sn.end[h.idx], sn.hash[h.idx])
		if !ok {
			continue
		}
		x := docs[id]
		out.Items = append(out.Items, SimilarHit{DocumentID: id, Title: x.Title, Kind: x.Kind, Status: x.Status, Chunk: int(sn.ord[h.idx]),
			Start: int(sn.start[h.idx]), End: int(sn.end[h.idx]), Score: round4(h.score), Snippet: snippetOf(text)})
	}
	return out, nil
}

func isLive(sn *snapshot, ok []bool, id string) bool {
	for i, d := range sn.docs {
		if d == id {
			return ok[i]
		}
	}
	return false
}

func reasonWords(r string) string {
	switch r {
	case skipNeverSend:
		return "it is flagged never-send, or is inside a document that is"
	case skipNoCompile:
		return "it is marked do not compile, or is inside a document that is"
	case skipNotes:
		return "notes and research are not embedded"
	}
	return r
}

var _ = sql.ErrNoRows
