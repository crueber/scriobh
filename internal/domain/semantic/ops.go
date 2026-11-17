package semantic

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/time/rate"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// Register adds the semantic search ops and the service the worker runs.
// Call it after the AI core is registered: embeddings go through it.
func Register(r *ops.Registry, cfg Config) *Service {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("semantic: entropy source failed: " + err.Error())
	}
	cfg = cfg.fill()
	s := &Service{env: r.Env(), cfg: cfg, log: slog.Default(), cache: newVecCache(cfg.MaxCacheBytes), wake: make(chan struct{}, 1),
		rates: map[string]*rate.Limiter{}, hmac: key, busy: map[string]bool{}, visited: map[string]time.Time{}, progress: map[string]int{}}
	if r.Env() != nil {
		if r.Env().Log != nil {
			s.log = r.Env().Log
		}
		r.Env().SetService(ServiceName, s)
	}
	owner := ops.RequireProject(ops.ProjectOwner).ForSessions()
	reader := ops.RequireProject(ops.ProjectReader)
	editor := ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "search.semantic", Method: "GET", Path: "/api/v1/projects/{id}/search/semantic", Auth: editor, NoDryRun: true,
		Desc: "Search a project by meaning: the chunks of the manuscript's chosen wording whose embedding is closest to the question, with document, position, score and a snippet. For the project's owner and editors only: the question is sent through the index's own provider (it may be the owner's personal one), so readers, reviewers and guests use search.query and search.similar. Needs the project's semantic index switched on by its owner. The question is sent to the embedding endpoint the owner confirmed, which spends that endpoint's budget, so an API key may use it only where the project's policy lets agents use AI; it is limited per person. Private notes, ghosted text, comments, alternatives not chosen, and documents flagged never-send or do-not-compile are not in the index. Not offered to agents as a tool."}, s.semanticSearch)
	ops.Register(r, ops.Op{Name: "search.similar", Method: "GET", Path: "/api/v1/projects/{id}/search/similar", Auth: reader,
		Desc: "Scenes like this one: the documents whose text is closest in meaning to a document (or to one of its chunks), best first, leaving out the document itself and its neighbours in reading order. Uses the vectors already stored: nothing is sent anywhere."}, s.similar)
	ops.Register(r, ops.Op{Name: "search.semantic.status", Method: "GET", Path: "/api/v1/projects/{id}/search/semantic/status", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "The state of the project's semantic index: off, paused (and why), waiting, indexing, in back-off after a failure, or ready; the counts of documents and chunks, the queue, the model and dimension, the last error class. The error class of the last failure is shown to the owner and administrators; a local endpoint's address to administrators only, a remote endpoint's host to the owner."}, s.status)
	ops.Register(r, ops.Op{Name: "search.semantic.preview", Method: "POST", Path: "/api/v1/projects/{id}/search/semantic/preview", Auth: owner, Scope: ops.ScopeRead,
		Desc: "Owner, browser sessions only. What switching the semantic index on would do, before anything is sent: how many documents, chunks, words and bytes would be embedded, the estimated tokens and cost, which endpoint and model, whether it is local or remote, what is never embedded, and a consent token. A remote endpoint also needs a typed confirmation. Sends nothing."}, s.preview)
	ops.Register(r, ops.Op{Name: "search.semantic.enable", Method: "POST", Path: "/api/v1/projects/{id}/search/semantic/enable", Auth: owner,
		Desc: "Owner, browser sessions only: an API key cannot change what leaves the server. Switch the semantic index on with the endpoint and model the preview showed, its consent token and, for an endpoint that is not on this machine or its network, the typed confirmation (its host). Changing the endpoint or model deletes the old embeddings first. Indexing runs in the background and never blocks saving."}, s.enable)
	ops.Register(r, ops.Op{Name: "search.semantic.disable", Method: "POST", Path: "/api/v1/projects/{id}/search/semantic/disable", Auth: owner,
		Desc: "Owner, browser sessions only. Stop indexing and refuse semantic searches. The embeddings are kept (search.semantic.purge deletes them) and are checked against the text when the index is switched on again."}, s.disable)
	ops.Register(r, ops.Op{Name: "search.semantic.rebuild", Method: "POST", Path: "/api/v1/projects/{id}/search/semantic/rebuild", Auth: owner,
		Desc: "Owner, browser sessions only. Delete every embedding and index the project again from the start, to the same confirmed endpoint. Also lifts a pause caused by the model changing its vector size."}, s.rebuild)
	ops.Register(r, ops.Op{Name: "search.semantic.purge", Method: "POST", Path: "/api/v1/projects/{id}/search/semantic/purge", Auth: owner, Permanent: true,
		Desc: "Owner, browser sessions only. Delete every embedding of the project and forget the endpoint choice and consent: the index is gone and off. Nothing is sent."}, s.purge)
	return s
}

// ---- shapes ------------------------------------------------------------------------------------------

// EndpointInfo says where embeddings go.
type EndpointInfo struct {
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Scope      string `json:"scope" enum:"instance,user"`
	Local      bool   `json:"local" desc:"Declared by an administrator: on this machine or its network. Anything else is remote."`
	Remote     bool   `json:"remote" desc:"The endpoint is not declared local: the whole manuscript would be sent to it."`
	Host       string `json:"host,omitempty" desc:"The endpoint's host and port, for the owner confirming it and for administrators. A local endpoint's address is not shown to anyone else."`
}

// hostVisible says whether a person may be shown where an endpoint is. Provider.wire hides an instance
// provider's address from everyone but administrators: an internal address is not for a project's owner, who could
// otherwise read each one off a preview. What stays visible is what the owner has to know to confirm: that it is
// local, or, for a remote endpoint, which public host (the typed confirmation is that host), and the address of
// the owner's own personal provider.
func hostVisible(admin bool, t aicore.EmbedTarget) bool {
	return admin || !t.Local || t.Scope == "user"
}

func infoOf(t aicore.EmbedTarget, showHost bool) EndpointInfo {
	e := EndpointInfo{ProviderID: t.ProviderID, Name: t.Name, Kind: t.Kind, Model: t.Model, Scope: t.Scope, Local: t.Local, Remote: !t.Local}
	if showHost {
		e.Host = t.Host
	}
	return e
}

// PreviewIn asks what switching the index on would do.
type PreviewIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	ProviderID string `json:"provider_id" desc:"A provider from ai.provider.list: an Ollama or OpenAI-compatible endpoint. An instance provider declared local is the default choice."`
	Model      string `json:"model" desc:"The embedding model, for example nomic-embed-text."`
}

// ProjectRef names the project.
func (in PreviewIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shapes.
func (in *PreviewIn) Validate() error {
	return checkChoice(in.ProviderID, in.Model)
}

func checkChoice(provider, model string) error {
	if !strings.HasPrefix(provider, "aip_") || len(provider) > 40 {
		return ops.Invalid("provider_id", "Choose a provider.")
	}
	if !lib.ValidModel(strings.TrimSpace(model)) {
		return ops.Invalid("model", "Give the embedding model's name.")
	}
	return nil
}

// Blocked says why the index cannot be switched on.
type Blocked struct {
	Code    string `json:"code" desc:"policy_off, not_local, provider_unusable, agents_not_allowed or ai_off."`
	Message string `json:"message"`
}

// PreviewOut is what would be sent and where.
type PreviewOut struct {
	Endpoint        EndpointInfo   `json:"endpoint"`
	Documents       int            `json:"documents" desc:"Documents that would be embedded (those with text)."`
	Chunks          int            `json:"chunks" desc:"Pieces of text, each one request input."`
	Words           int            `json:"words"`
	Bytes           int            `json:"bytes" desc:"The bytes of text that would be sent to the endpoint."`
	EstimatedTokens int            `json:"estimated_tokens"`
	EstimatedMicros int64          `json:"estimated_cost_micros" desc:"In micro-units of the instance currency; 0 for a local endpoint."`
	CostKnown       bool           `json:"cost_known"`
	Currency        string         `json:"currency"`
	NeverEmbedded   map[string]int `json:"not_embedded" desc:"Documents left out, by reason: never_send, do_not_compile, notes_and_research. Trashed documents are not counted."`
	TruncatedDocs   int            `json:"truncated_documents" desc:"Documents longer than the per-document limit: only the first part is embedded."`
	OverLimit       bool           `json:"over_project_limit" desc:"The project has more chunks than an index may hold; the rest would not be embedded."`
	ProjectLimit    int            `json:"project_chunk_limit"`
	Remote          bool           `json:"remote"`
	ConfirmHost     string         `json:"confirm_host,omitempty" desc:"For a remote endpoint: the host the owner must type to confirm."`
	Rules           []string       `json:"rules" desc:"What is never sent, in words."`
	Blocked         *Blocked       `json:"blocked,omitempty"`
	ConsentToken    string         `json:"consent_token,omitempty" desc:"Send it back to enable. It stands for this project, this endpoint and model, and whether it is local, and stops working when any of them changes."`
}

var rules = []string{
	"Only the chosen wording of documents is sent: the text the search index holds.",
	"Private notes (%%...%%), ghosted text, HTML comments, raw HTML, code blocks and the alternatives comment are never sent.",
	"Comments, suggestions, stashes, research notes and the Compendium are never sent.",
	"Documents flagged never-send, marked do not compile (or inside one), kept in the trash, and notes are never sent.",
	"A document's text is sent again only when it changes; the index holds vectors and offsets, not text.",
}

func (s *Service) token(projectID, key string) string {
	m := hmac.New(sha256.New, s.hmac)
	m.Write([]byte("semantic-consent\x00" + projectID + "\x00" + key))
	return hex.EncodeToString(m.Sum(nil))
}

// plan measures what would be embedded without sending anything.
type plan struct {
	docs, chunks, words, bytes, tokens int
	truncated                          int
	skipped                            map[string]int
}

func (s *Service) measure(ctx context.Context, projectID string) (plan, error) {
	p := plan{skipped: map[string]int{}}
	docs, err := loadDocs(ctx, s.db().Read(), projectID)
	if err != nil {
		return p, err
	}
	elig, skipped := eligible(docs)
	p.skipped = skipped
	rows, err := s.db().Read().QueryContext(ctx, `SELECT id, content FROM documents WHERE project_id = ? AND compendium_id IS NULL AND deleted_at IS NULL`, projectID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, content string
		if err := rows.Scan(&id, &content); err != nil {
			return p, err
		}
		if !elig[id] {
			continue
		}
		cs, tr := Split(EmbedBody(content))
		if len(cs) == 0 {
			continue
		}
		p.docs++
		if tr {
			p.truncated++
		}
		for _, c := range cs {
			p.chunks++
			p.words += c.Words
			p.bytes += len(c.Text)
			p.tokens += lib.EstimateTokens(c.Text)
		}
	}
	return p, rows.Err()
}

func (s *Service) isOwner(c *ops.Ctx, projectID string) bool {
	if c.P.Role == ops.RoleAdmin {
		return true
	}
	role, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	return ok && role == ops.ProjectOwner
}

func (s *Service) needAI(c *ops.Ctx) (*aicore.Service, error) {
	ai, ok := s.ai()
	if !ok {
		return nil, ops.Conflict("AI is not available in this build.")
	}
	return ai, nil
}

func resolveBlocked(err error) *Blocked {
	var r *aicore.EmbedRefused
	if asRefused(err, &r) {
		return &Blocked{Code: r.Code(), Message: r.Err.Message}
	}
	return nil
}

func asRefused(err error, target **aicore.EmbedRefused) bool {
	r, ok := err.(*aicore.EmbedRefused)
	if ok {
		*target = r
	}
	return ok
}

func (s *Service) preview(c *ops.Ctx, in PreviewIn) (PreviewOut, error) {
	ai, err := s.needAI(c)
	if err != nil {
		return PreviewOut{}, err
	}
	in.Model = strings.TrimSpace(in.Model)
	out := PreviewOut{Rules: rules, NeverEmbedded: map[string]int{}, ProjectLimit: s.cfg.MaxChunksPerProject, Currency: "USD"}
	if err := ai.EmbedGate(c, c.DB()); err != nil {
		out.Blocked = &Blocked{Code: "ai_off", Message: ops.AsError(err).Message}
		return out, nil
	}
	spec := aicore.EmbedSpec{ProjectID: in.ProjectID, UserID: c.P.UserID, ProviderID: in.ProviderID, Model: in.Model, Purpose: aicore.EmbedIndex, AllowRemote: true}
	t, rerr := ai.EmbedResolve(c, c.DB(), spec)
	if rerr != nil {
		if b := resolveBlocked(rerr); b != nil {
			out.Blocked = b
			out.Endpoint = infoOf(t, hostVisible(c.P.Role == ops.RoleAdmin, t))
			return out, nil
		}
		return PreviewOut{}, ops.Internal(rerr)
	}
	out.Endpoint = infoOf(t, hostVisible(c.P.Role == ops.RoleAdmin, t))
	out.Remote = !t.Local
	if out.Remote {
		out.ConfirmHost = t.Host
	}
	pl, merr := s.measure(c, in.ProjectID)
	if merr != nil {
		return PreviewOut{}, ops.Internal(merr)
	}
	out.Documents, out.Chunks, out.Words, out.Bytes, out.EstimatedTokens = pl.docs, pl.chunks, pl.words, pl.bytes, pl.tokens
	out.NeverEmbedded, out.TruncatedDocs = pl.skipped, pl.truncated
	out.OverLimit = pl.chunks > s.cfg.MaxChunksPerProject
	switch {
	case t.Local:
		out.CostKnown = true
	case t.PriceKnown:
		out.CostKnown = true
		out.EstimatedMicros = lib.Price{InputMicros: t.InputMicros}.Cost(pl.tokens, 0)
	}
	out.ConsentToken = s.token(in.ProjectID, t.Key)
	return out, nil
}

// EnableIn switches the index on.
type EnableIn struct {
	ProjectID     string `path:"id" json:"-" desc:"Project id."`
	ProviderID    string `json:"provider_id"`
	Model         string `json:"model"`
	ConsentToken  string `json:"consent_token" desc:"From search.semantic.preview, for this provider and model."`
	ConfirmRemote string `json:"confirm_remote,omitempty" desc:"For an endpoint that is not local: type its host exactly as the preview shows it. This is the owner's agreement that the whole manuscript goes there."`
}

// ProjectRef names the project.
func (in EnableIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shapes.
func (in *EnableIn) Validate() error {
	in.Model = strings.TrimSpace(in.Model)
	if err := checkChoice(in.ProviderID, in.Model); err != nil {
		return err
	}
	if in.ConsentToken == "" {
		return ops.Invalid("consent_token", "Preview first (search.semantic.preview) and send its consent token.")
	}
	return nil
}

// StatusOut is the state of an index.
type StatusOut struct {
	State       string         `json:"state" enum:"off,paused,waiting,backoff,indexing,ready" desc:"off: not switched on. paused: stopped until the owner acts (reason). waiting: a switch outside the index is off (reason). backoff: the endpoint failed and is being retried later. indexing: documents are queued. ready: current."`
	Reason      string         `json:"reason,omitempty" desc:"For paused and waiting: restore, model_changed, ai_off, policy_off, endpoint_changed, not_local, provider_unusable, project_trashed, budget_exceeded..."`
	Enabled     bool           `json:"enabled"`
	Endpoint    *EndpointInfo  `json:"endpoint,omitempty"`
	Model       string         `json:"model,omitempty"`
	Dim         int            `json:"dimension" desc:"The length of the vectors; 0 until the first one arrives."`
	Eligible    int            `json:"eligible_documents" desc:"Documents that may be embedded."`
	Indexed     int            `json:"indexed_documents"`
	QueueDepth  int            `json:"queue_depth" desc:"Documents waiting to be embedded."`
	Failed      int            `json:"failed_documents" desc:"Documents the endpoint refused; tried again when their text changes."`
	Chunks      int            `json:"chunks"`
	VectorBytes int64          `json:"vector_bytes"`
	NotEmbedded map[string]int `json:"not_embedded"`
	LastError   string         `json:"last_error_class,omitempty" desc:"Owner and administrators: the class of the last failure (timeout, unreachable, auth, bad_response...). Others see only that there was one."`
	LastErrorAt string         `json:"last_error_at,omitempty"`
	FailCount   int            `json:"consecutive_failures"`
	NextAttempt string         `json:"next_attempt_at,omitempty"`
	IndexedAt   string         `json:"indexed_at,omitempty" desc:"When the index last caught up."`
	ConsentAt   string         `json:"consent_at,omitempty"`
	Remote      bool           `json:"remote"`
	ChunkLimit  int            `json:"project_chunk_limit"`
}

// StatusIn names the project.
type StatusIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in StatusIn) ProjectRef() string { return in.ProjectID }

func (s *Service) status(c *ops.Ctx, in StatusIn) (StatusOut, error) {
	return s.statusOf(c, c.DB().Read(), in.ProjectID, s.isOwner(c, in.ProjectID), c.P.Role == ops.RoleAdmin)
}

// statusOf says the state of an index. owner says the caller owns the project (or is an administrator): they are
// shown the class of the last failure. admin says the caller is an administrator: only they are shown a local
// endpoint's address (see hostVisible).
func (s *Service) statusOf(ctx context.Context, q queryer, projectID string, owner, admin bool) (StatusOut, error) {
	st, err := loadState(ctx, q, projectID)
	if err != nil {
		return StatusOut{}, ops.Internal(err)
	}
	out := StatusOut{State: "off", NotEmbedded: map[string]int{}, ChunkLimit: s.cfg.MaxChunksPerProject, Enabled: st.exists && st.Enabled}
	if !st.exists {
		return out, nil
	}
	out.Model, out.Dim, out.IndexedAt, out.ConsentAt, out.FailCount = st.Model, st.Dim, st.IndexedAt, st.ConsentAt, st.FailCount
	out.NextAttempt, out.LastErrorAt, out.Remote = st.NextAttemptAt, st.LastErrorAt, st.AllowRemote
	if st.LastErrorClass != "" {
		out.LastError = "error"
		if owner {
			out.LastError = st.LastErrorClass
		}
	}
	docs, err := loadDocs(ctx, q, projectID)
	if err != nil {
		return StatusOut{}, ops.Internal(err)
	}
	elig, skipped := eligible(docs)
	cnt, err := loadCounts(ctx, q, projectID)
	if err != nil {
		return StatusOut{}, ops.Internal(err)
	}
	out.Eligible, out.NotEmbedded, out.Chunks, out.VectorBytes = len(elig), skipped, cnt.Chunks, cnt.VecBytes
	dk := st.EndpointKey + s.redTag(ctx, projectID)
	for id := range elig {
		x := docs[id]
		_, have := cnt.Docs[id]
		switch {
		case have && cnt.DocHash[id] == x.Hash && cnt.DocKey[id] == dk && cnt.DocVer[id] == IndexVersion:
			if cnt.Docs[id] == "failed" {
				out.Failed++
			} else {
				out.Indexed++
			}
		default:
			out.QueueDepth++
		}
	}
	if ai, ok := s.ai(); ok && st.ProviderID != "" {
		spec := aicore.EmbedSpec{ProjectID: projectID, UserID: st.EnabledBy, ProviderID: st.ProviderID, Model: st.Model, Purpose: aicore.EmbedIndex, AllowRemote: st.AllowRemote, ExpectKey: st.EndpointKey}
		t, rerr := ai.EmbedResolve(ctx, s.db(), spec)
		if t.ProviderID != "" {
			e := infoOf(t, owner && hostVisible(admin, t))
			out.Endpoint = &e
		}
		switch {
		case !st.Enabled && st.PausedReason == "":
			out.State = "off"
		case st.PausedReason != "":
			out.State, out.Reason = "paused", st.PausedReason
		case ai.EmbedGate(ctx, s.db()) != nil:
			out.State, out.Reason = "waiting", "ai_off"
		case rerr != nil:
			class, _ := aicore.EmbedFailure(rerr)
			out.State, out.Reason = "waiting", class
		default:
			s.settleState(ctx, q, projectID, &out, st)
		}
	} else if st.PausedReason != "" {
		out.State, out.Reason = "paused", st.PausedReason
	} else if st.Enabled {
		s.settleState(ctx, q, projectID, &out, st)
	}
	return out, nil
}

// settleState picks between waiting (project in the trash), backoff, indexing and ready.
func (s *Service) settleState(ctx context.Context, q queryer, projectID string, out *StatusOut, st state) {
	var live int
	_ = q.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = ? AND deleted_at IS NULL AND trashed_at IS NULL`, projectID).Scan(&live)
	switch {
	case live == 0:
		out.State, out.Reason = "waiting", "project_trashed"
	case st.NextAttemptAt != "" && st.NextAttemptAt > db.Now() && st.FailCount > 0:
		out.State = "backoff"
	case out.QueueDepth > 0:
		out.State = "indexing"
	default:
		out.State = "ready"
	}
}

func (s *Service) enable(c *ops.Ctx, in EnableIn) (StatusOut, error) {
	ai, err := s.needAI(c)
	if err != nil {
		return StatusOut{}, err
	}
	if err := ai.EmbedGate(c, c.DB()); err != nil {
		return StatusOut{}, err
	}
	spec := aicore.EmbedSpec{ProjectID: in.ProjectID, UserID: c.P.UserID, ProviderID: in.ProviderID, Model: in.Model, Purpose: aicore.EmbedIndex, AllowRemote: true}
	t, rerr := ai.EmbedResolve(c, c.DB(), spec)
	if rerr != nil {
		if r, ok := rerr.(*aicore.EmbedRefused); ok {
			return StatusOut{}, r.Err
		}
		return StatusOut{}, ops.Internal(rerr)
	}
	if !hmac.Equal([]byte(s.token(in.ProjectID, t.Key)), []byte(in.ConsentToken)) {
		return StatusOut{}, ops.ConflictWith("The endpoint, the model or the project's AI settings are not what the preview showed, or the preview is from before the server restarted. Preview again and confirm what it shows.",
			map[string]any{"reason": "consent_changed"})
	}
	remote := !t.Local
	if remote && !strings.EqualFold(strings.TrimSpace(in.ConfirmRemote), t.Host) {
		return StatusOut{}, ops.Invalid("confirm_remote", "This endpoint is not on this machine or its network. Type its host ("+t.Host+") to confirm that the whole manuscript may be sent to it.")
	}
	pl, merr := s.measure(c, in.ProjectID)
	if merr != nil {
		return StatusOut{}, ops.Internal(merr)
	}
	now := db.Now()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadState(c, tx, in.ProjectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO embed_projects(project_id, enabled, provider_id, model, endpoint_key, allow_remote, enabled_by, consent_at, consent_chunks, consent_bytes,
				index_version, dim, gen, paused_reason, last_error_class, last_error_at, fail_count, next_attempt_at, indexed_at, created_at, updated_at)
			VALUES (?,1,?,?,?,?,?,?,?,?,?,0,0,'','','',0,'','',?,?)
			ON CONFLICT(project_id) DO UPDATE SET enabled = 1, provider_id = excluded.provider_id, model = excluded.model, endpoint_key = excluded.endpoint_key,
				allow_remote = excluded.allow_remote, enabled_by = excluded.enabled_by, consent_at = excluded.consent_at, consent_chunks = excluded.consent_chunks,
				consent_bytes = excluded.consent_bytes, paused_reason = '', last_error_class = '', last_error_at = '', fail_count = 0, next_attempt_at = '', updated_at = excluded.updated_at`,
			in.ProjectID, in.ProviderID, in.Model, t.Key, b2i(remote), c.P.UserID, now, pl.chunks, pl.bytes, IndexVersion, now, now); err != nil {
			return err
		}
		// Embeddings made by another endpoint, model or set of rules are not mixed with the new ones.
		dropped := int64(0)
		if cur.exists && (cur.EndpointKey != t.Key || cur.IndexVersion != IndexVersion) {
			if dropped, err = purgeChunks(tx, in.ProjectID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE embed_projects SET index_version = ? WHERE project_id = ?`, IndexVersion, in.ProjectID); err != nil {
				return err
			}
		} else {
			// A restore or a pause left an index the owner now resumes: it is only kept if it fits the text.
			if _, err := tx.Exec(`UPDATE embed_projects SET gen = gen + 1 WHERE project_id = ?`, in.ProjectID); err != nil {
				return err
			}
		}
		c.Audit(ops.AuditEntry{Action: "search.semantic.enable", TargetType: "project", TargetID: in.ProjectID,
			Detail: map[string]any{"provider_id": in.ProviderID, "model": in.Model, "remote": remote, "documents": pl.docs, "chunks": pl.chunks, "bytes": pl.bytes, "dropped_chunks": dropped}})
		c.Emit(ops.Event{Type: "search.semantic.updated", ProjectID: in.ProjectID, Payload: map[string]any{"enabled": true}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return StatusOut{}, ops.AsError(err)
	}
	s.cache.drop(in.ProjectID)
	if !c.DryRun() {
		s.Wake()
	}
	return s.statusOf(c, c.DB().Read(), in.ProjectID, true, c.P.Role == ops.RoleAdmin)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ProjectIn names a project.
type ProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in ProjectIn) ProjectRef() string { return in.ProjectID }

func (s *Service) disable(c *ops.Ctx, in ProjectIn) (StatusOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE embed_projects SET enabled = 0, paused_reason = '', updated_at = ? WHERE project_id = ?`, db.Now(), in.ProjectID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "search.semantic.disable", TargetType: "project", TargetID: in.ProjectID})
		c.Emit(ops.Event{Type: "search.semantic.updated", ProjectID: in.ProjectID, Payload: map[string]any{"enabled": false}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return StatusOut{}, ops.AsError(err)
	}
	return s.statusOf(c, c.DB().Read(), in.ProjectID, true, c.P.Role == ops.RoleAdmin)
}

func (s *Service) rebuild(c *ops.Ctx, in ProjectIn) (StatusOut, error) {
	var n int64
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		st, err := loadState(c, tx, in.ProjectID)
		if err != nil {
			return err
		}
		if !st.exists || !st.Enabled {
			return ops.Conflict("Semantic search is off for this project. Switch it on first.")
		}
		if n, err = purgeChunks(tx, in.ProjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE embed_projects SET paused_reason = '', last_error_class = '', last_error_at = '', fail_count = 0, next_attempt_at = '', updated_at = ? WHERE project_id = ?`, db.Now(), in.ProjectID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "search.semantic.rebuild", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"deleted_chunks": n}})
		c.Emit(ops.Event{Type: "search.semantic.updated", ProjectID: in.ProjectID, Payload: map[string]any{"rebuild": true}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return StatusOut{}, ops.AsError(err)
	}
	s.cache.drop(in.ProjectID)
	if !c.DryRun() {
		s.Wake()
	}
	return s.statusOf(c, c.DB().Read(), in.ProjectID, true, c.P.Role == ops.RoleAdmin)
}

// PurgeOut says what was deleted.
type PurgeOut struct {
	DeletedChunks int64 `json:"deleted_chunks"`
}

func (s *Service) purge(c *ops.Ctx, in ProjectIn) (PurgeOut, error) {
	var n int64
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var err error
		if n, err = purgeChunks(tx, in.ProjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM embed_projects WHERE project_id = ?`, in.ProjectID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "search.semantic.purge", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"deleted_chunks": n}})
		c.Emit(ops.Event{Type: "search.semantic.updated", ProjectID: in.ProjectID, Payload: map[string]any{"enabled": false, "purged": true}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PurgeOut{}, ops.AsError(err)
	}
	s.cache.drop(in.ProjectID)
	return PurgeOut{DeletedChunks: n}, nil
}

// ---- helpers shared by the searches ----------------------------------------------------------

// ready checks everything a search needs before it reads the index. A project in
// the trash sends nothing and is refused; so is one whose index is off or paused.
func (s *Service) ready(c *ops.Ctx, projectID string) (*aicore.Service, state, error) {
	ai, err := s.needAI(c)
	if err != nil {
		return nil, state{}, err
	}
	if err := ai.EmbedGate(c, c.DB()); err != nil {
		return nil, state{}, err
	}
	if c.Env.ProjectTrashed != nil && c.Env.ProjectTrashed(c, projectID) {
		return nil, state{}, ops.ProjectInTrash()
	}
	if s.restorePending() {
		return nil, state{}, ops.ConflictWith("Semantic search is paused ("+pauseWords("restore")+").", map[string]any{"semantic": "paused", "reason": "restore"})
	}
	st, err := loadState(c, c.DB().Read(), projectID)
	if err != nil {
		return nil, state{}, ops.Internal(err)
	}
	switch {
	case !st.exists || (!st.Enabled && st.PausedReason == ""):
		return nil, st, ops.ConflictWith("Semantic search is not switched on for this project. Its owner can switch it on in the project's search settings.", map[string]any{"semantic": "off"})
	case st.PausedReason != "":
		return nil, st, ops.ConflictWith("Semantic search is paused ("+pauseWords(st.PausedReason)+"). Its owner can switch it on again in the project's search settings.", map[string]any{"semantic": "paused", "reason": st.PausedReason})
	}
	return ai, st, nil
}

func pauseWords(reason string) string {
	switch reason {
	case "restore":
		return "this copy was restored from a backup and does not send text to an endpoint until the owner switches it on"
	case "model_changed":
		return "the embedding model now answers with a different vector size, so the index no longer fits it; rebuild the index"
	}
	return reason
}

// providerFailure turns an error from the endpoint into what a caller is told:
// to a person who is not an administrator, the class would map the server's
// network, so they are told it failed.
func providerFailure(who ops.Principal, err error) *ops.Error {
	if r, ok := err.(*aicore.EmbedRefused); ok {
		return r.Err
	}
	pe, ok := lib.AsProviderError(err)
	if !ok {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return &ops.Error{Code: ops.CodeProvider, Message: "The embedding endpoint did not answer in time."}
		}
		return ops.Internal(err)
	}
	if who.Role != ops.RoleAdmin {
		return &ops.Error{Code: ops.CodeProvider, Message: "The embedding endpoint could not be used. Try again, or ask the project's owner or an administrator."}
	}
	e := &ops.Error{Code: ops.CodeProvider, Message: pe.Message(), Data: map[string]any{"provider_class": pe.Class}}
	if pe.Status > 0 {
		e.Data["status"] = pe.Status
	}
	return e
}

// snippetOf is the start of a chunk, on one line, cut at a word.
func snippetOf(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	const max = 260
	if utf8.RuneCountInString(t) <= max {
		return t
	}
	r := []rune(t)
	cut := max
	for cut > max-40 && !unicode.IsSpace(r[cut]) {
		cut--
	}
	if cut <= max-40 {
		cut = max
	}
	return strings.TrimSpace(string(r[:cut])) + "…"
}

var _ = time.Second
