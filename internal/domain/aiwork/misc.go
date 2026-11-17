package aiwork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- the style guide -------------------------------------------------------------------------

// MaxStyleChars is the style guide's limit, the same as the policy's.
const MaxStyleChars = 8000

// StyleGetIn names the project.
type StyleGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in StyleGetIn) ProjectRef() string { return in.ProjectID }

// StyleOut is the style guide.
type StyleOut struct {
	StyleGuide string `json:"style_guide" desc:"The author's voice rules. A recipe sends it only as the style guide chip, which the preview shows and the person can leave out of any request."`
	UpdatedAt  string `json:"updated_at,omitempty"`
	MaxChars   int    `json:"max_chars"`
}

func (s *Service) styleGet(c *ops.Ctx, in StyleGetIn) (StyleOut, error) {
	out := StyleOut{MaxChars: MaxStyleChars}
	err := c.DB().Read().QueryRowContext(c, `SELECT style_guide, updated_at FROM ai_policies WHERE project_id = ?`, in.ProjectID).Scan(&out.StyleGuide, &out.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, ops.Internal(err)
	}
	return out, nil
}

// StyleSetIn replaces the style guide.
type StyleSetIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	StyleGuide string `json:"style_guide" desc:"Up to 8,000 characters. Empty clears it."`
}

// ProjectRef lets the registry authorize by membership.
func (in StyleSetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the text.
func (in *StyleSetIn) Validate() error {
	in.StyleGuide = strings.TrimSpace(in.StyleGuide)
	if utf8.RuneCountInString(in.StyleGuide) > MaxStyleChars {
		return ops.Invalid("style_guide", "The style guide is limited to 8,000 characters.")
	}
	if !utf8.ValidString(in.StyleGuide) || strings.ContainsRune(in.StyleGuide, 0) {
		return ops.Invalid("style_guide", "The style guide is not valid text.")
	}
	return nil
}

// styleSet is the editor's way to keep the style guide. It is ordinary project
// text (it lives beside the policy and is read by ai.policy.get), so editors may
// write it; changing the mode, the providers or the budget stays with the owner.
func (s *Service) styleSet(c *ops.Ctx, in StyleSetIn) (StyleOut, error) {
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		if _, err := t.ExecContext(c, `INSERT INTO ai_policies(project_id, style_guide, updated_by, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(project_id) DO UPDATE SET style_guide = excluded.style_guide, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			in.ProjectID, in.StyleGuide, c.P.UserID, now()); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "ai.style.set", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"chars": utf8.RuneCountInString(in.StyleGuide)}})
		return c.FlushTo(t)
	})
	if err != nil {
		return StyleOut{}, ops.Internal(err)
	}
	return s.styleGet(c, StyleGetIn{ProjectID: in.ProjectID})
}

// ---- default chains ------------------------------------------------------------------------------

// DefaultsGetIn names the project.
type DefaultsGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in DefaultsGetIn) ProjectRef() string { return in.ProjectID }

// EffectiveChain is the chain a class resolves to and whose it is.
type EffectiveChain struct {
	Source string              `json:"source" enum:"project,user,instance,none"`
	Chain  []aicore.ChainEntry `json:"chain"`
}

// DefaultsOut is the default provider chains per model class.
type DefaultsOut struct {
	Classes   []string                       `json:"classes"`
	Project   map[string][]aicore.ChainEntry `json:"project" desc:"The project's chains by class, set by its owner (stored in the policy as class:<class>)."`
	User      map[string][]aicore.ChainEntry `json:"user" desc:"Your own."`
	Instance  map[string][]aicore.ChainEntry `json:"instance" desc:"The administrator's."`
	Effective map[string]EffectiveChain      `json:"effective" desc:"For each class: what a recipe of that class uses when nothing more specific applies. The order is the request's own provider choice, then a chain the project names for the recipe or its action, then the project's class chain, your class chain, the instance's."`
}

func classChains(m map[string][]aicore.ChainEntry) map[string][]aicore.ChainEntry {
	out := map[string][]aicore.ChainEntry{}
	for k, v := range m {
		if strings.HasPrefix(k, "class:") && len(v) > 0 {
			out[strings.TrimPrefix(k, "class:")] = v
		}
	}
	return out
}

func (s *Service) defaultsGet(c *ops.Ctx, in DefaultsGetIn) (DefaultsOut, error) {
	out := DefaultsOut{Classes: aicore.Classes, Project: map[string][]aicore.ChainEntry{}, User: map[string][]aicore.ChainEntry{}, Instance: map[string][]aicore.ChainEntry{}, Effective: map[string]EffectiveChain{}}
	var raw string
	if err := c.DB().Read().QueryRowContext(c, `SELECT chains_json FROM ai_policies WHERE project_id = ?`, in.ProjectID).Scan(&raw); err == nil {
		var m map[string][]aicore.ChainEntry
		_ = json.Unmarshal([]byte(raw), &m)
		out.Project = classChains(m)
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT class, chain_json FROM ai_user_defaults WHERE user_id = ?`, c.P.UserID)
	if err != nil {
		return out, ops.Internal(err)
	}
	for rows.Next() {
		var cl, ch string
		if err := rows.Scan(&cl, &ch); err != nil {
			rows.Close()
			return out, ops.Internal(err)
		}
		var chain []aicore.ChainEntry
		if json.Unmarshal([]byte(ch), &chain) == nil && len(chain) > 0 {
			out.User[cl] = chain
		}
	}
	rows.Close()
	if err := c.DB().Read().QueryRowContext(c, `SELECT value FROM settings WHERE key = ?`, aicore.SettingChainsKey).Scan(&raw); err == nil {
		var m map[string][]aicore.ChainEntry
		_ = json.Unmarshal([]byte(raw), &m)
		out.Instance = classChains(m)
	}
	for _, cl := range aicore.Classes {
		switch {
		case len(out.Project[cl]) > 0:
			out.Effective[cl] = EffectiveChain{"project", out.Project[cl]}
		case len(out.User[cl]) > 0:
			out.Effective[cl] = EffectiveChain{"user", out.User[cl]}
		case len(out.Instance[cl]) > 0:
			out.Effective[cl] = EffectiveChain{"instance", out.Instance[cl]}
		default:
			out.Effective[cl] = EffectiveChain{"none", []aicore.ChainEntry{}}
		}
	}
	return out, nil
}

// DefaultsSetIn sets one class's chain at one level.
type DefaultsSetIn struct {
	ProjectID string              `path:"id" json:"-" desc:"Project id."`
	Scope     string              `json:"scope" enum:"project,user,instance" desc:"project: the project's owner. user: yourself. instance: an administrator."`
	Class     string              `json:"class" enum:"cheap,strong,fast,local" desc:"The model class a recipe prefers (ai.recipe.list shows each recipe's)."`
	Chain     []aicore.ChainEntry `json:"chain" desc:"Providers and models to try in order, at most 6. Empty removes the default. A failure retrying can fix (timeout, 5xx, rate limit) moves to the next entry; a refusal never does."`
}

// ProjectRef lets the registry authorize by membership.
func (in DefaultsSetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *DefaultsSetIn) Validate() error {
	switch in.Scope {
	case "project", "user", "instance":
	default:
		return ops.Invalid("scope", "The scope is project, user or instance.")
	}
	if !aicore.ValidClass(in.Class) {
		return ops.Invalid("class", "The class is cheap, strong, fast or local.")
	}
	if err := aicore.ValidateChain(in.Chain); err != nil {
		return err
	}
	return nil
}

func (s *Service) defaultsSet(c *ops.Ctx, in DefaultsSetIn) (DefaultsOut, error) {
	if s.ai == nil {
		return DefaultsOut{}, ops.Conflict("AI is not available on this server.")
	}
	if err := s.ai.Gate(c, c.DB()); err != nil {
		return DefaultsOut{}, err
	}
	switch in.Scope {
	case "project":
		if role, ok := c.Env.ProjectRole(c, c.P.UserID, in.ProjectID); !ok || (role != ops.ProjectOwner && c.P.Role != ops.RoleAdmin) {
			return DefaultsOut{}, ops.Forbidden("Only the project's owner sets its default providers.")
		}
	case "instance":
		if c.P.Role != ops.RoleAdmin {
			return DefaultsOut{}, ops.Forbidden("Only an administrator sets the instance's default providers.")
		}
	}
	// Every entry must be a provider this person may name; an instance chain names instance providers; a local-only project's chain names local ones only.
	refs, err := s.ai.UsableProviders(c, c.DB(), c.P.UserID)
	if err != nil {
		return DefaultsOut{}, ops.Internal(err)
	}
	byID := map[string]aicore.ProviderRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	pv, err := s.ai.PolicyOf(c, c.DB(), in.ProjectID)
	if err != nil {
		return DefaultsOut{}, ops.Internal(err)
	}
	for _, e := range in.Chain {
		r, ok := byID[e.ProviderID]
		switch {
		case !ok:
			return DefaultsOut{}, ops.Invalid("chain", "A chain names a provider you can use.")
		case in.Scope == "instance" && !r.Instance:
			return DefaultsOut{}, ops.Invalid("chain", "An instance chain names instance providers.")
		case in.Scope == "project" && pv.Mode == aicore.ModeLocalOnly && !r.Local:
			return DefaultsOut{}, ops.Invalid("chain", "This project is local-only, so its chains may name only local providers.")
		case in.Class == "local" && !r.Local:
			return DefaultsOut{}, ops.Invalid("chain", "The local class names local providers only.")
		}
	}
	key := "class:" + in.Class
	err = c.DB().WithTx(c, func(t *sql.Tx) error {
		switch in.Scope {
		case "user":
			if len(in.Chain) == 0 {
				_, err := t.ExecContext(c, `DELETE FROM ai_user_defaults WHERE user_id = ? AND class = ?`, c.P.UserID, in.Class)
				return err
			}
			_, err := t.ExecContext(c, `INSERT INTO ai_user_defaults(user_id, class, chain_json, updated_at) VALUES (?,?,?,?)
				ON CONFLICT(user_id, class) DO UPDATE SET chain_json = excluded.chain_json, updated_at = excluded.updated_at`, c.P.UserID, in.Class, toJSON(in.Chain), now())
			return err
		case "project":
			var raw string
			m := map[string][]aicore.ChainEntry{}
			if err := t.QueryRowContext(c, `SELECT chains_json FROM ai_policies WHERE project_id = ?`, in.ProjectID).Scan(&raw); err == nil {
				_ = json.Unmarshal([]byte(raw), &m)
			}
			setChain(m, key, in.Chain)
			_, err := t.ExecContext(c, `INSERT INTO ai_policies(project_id, chains_json, updated_by, updated_at) VALUES (?,?,?,?)
				ON CONFLICT(project_id) DO UPDATE SET chains_json = excluded.chains_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				in.ProjectID, toJSON(m), c.P.UserID, now())
			return err
		default:
			var raw string
			m := map[string][]aicore.ChainEntry{}
			if err := t.QueryRowContext(c, `SELECT value FROM settings WHERE key = ?`, aicore.SettingChainsKey).Scan(&raw); err == nil {
				_ = json.Unmarshal([]byte(raw), &m)
			}
			setChain(m, key, in.Chain)
			_, err := t.ExecContext(c, `INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				aicore.SettingChainsKey, toJSON(m), now())
			return err
		}
	})
	if err != nil {
		return DefaultsOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "ai.defaults.set", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"scope": in.Scope, "class": in.Class, "entries": len(in.Chain)}})
	return s.defaultsGet(c, DefaultsGetIn{ProjectID: in.ProjectID})
}

func setChain(m map[string][]aicore.ChainEntry, key string, chain []aicore.ChainEntry) {
	if len(chain) == 0 {
		delete(m, key)
		return
	}
	m[key] = chain
}

// ---- chats ----------------------------------------------------------------------------------------------

// A chat is held by the browser unless the person asks to keep it. A kept chat
// belongs to its author alone, in this project, until deleted. What is kept is
// the conversation text; nothing in it can reach the manuscript except through
// ai.accept.edit on a run's own suggestion.

const (
	maxChats     = 50
	maxChatTurns = 200
	// MaxChatBytes and MaxPersonChatBytes bound the text a person keeps: a chat, and all of
	// a person's chats on a project. (Fixed limits; there is no setting for them.)
	MaxChatBytes       = 2 << 20
	MaxPersonChatBytes = 20 << 20
	chatContextMsgs    = 20 // the most recent turns sent with a new message
)

// ChatCreateIn keeps a conversation.
type ChatCreateIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Title     string `json:"title,omitempty" desc:"Up to 100 characters."`
}

// ProjectRef lets the registry authorize by membership.
func (in ChatCreateIn) ProjectRef() string { return in.ProjectID }

// Validate checks the title.
func (in *ChatCreateIn) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if utf8.RuneCountInString(in.Title) > 100 || strings.ContainsAny(in.Title, "\n\r\x00") {
		return ops.Invalid("title", "A title is one line of up to 100 characters.")
	}
	return nil
}

// ChatTurn is one message of a kept chat.
type ChatTurn struct {
	Seq       int    `json:"seq"`
	Role      string `json:"role" enum:"user,assistant"`
	Content   string `json:"content" desc:"Untrusted: show it as text."`
	RunID     string `json:"run_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// ChatOut is a kept chat.
type ChatOut struct {
	ID        string     `json:"chat_id"`
	Title     string     `json:"title"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	Turns     []ChatTurn `json:"turns,omitempty"`
	TurnCount int        `json:"turn_count"`
}

// ChatItem wraps a chat.
type ChatItem struct {
	Item ChatOut `json:"item"`
}

func (s *Service) chatCreate(c *ops.Ctx, in ChatCreateIn) (ChatItem, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return ChatItem{}, err
	}
	var out ChatOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		var n int
		_ = t.QueryRowContext(c, `SELECT count(*) FROM ai_chats WHERE user_id = ? AND project_id = ?`, c.P.UserID, in.ProjectID).Scan(&n)
		if n >= maxChats {
			return ops.Conflict("You keep at most 50 conversations on a project. Delete one first.")
		}
		id := db.NewID("cht")
		at := now()
		if _, err := t.ExecContext(c, `INSERT INTO ai_chats(id, project_id, user_id, title, created_at, updated_at) VALUES (?,?,?,?,?,?)`, id, in.ProjectID, c.P.UserID, in.Title, at, at); err != nil {
			return err
		}
		out = ChatOut{ID: id, Title: in.Title, CreatedAt: at, UpdatedAt: at}
		return nil
	})
	return ChatItem{Item: out}, mapErr(err)
}

// ChatListIn lists the caller's kept chats.
type ChatListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ChatListIn) ProjectRef() string { return in.ProjectID }

// ChatListOut lists kept chats, most recent first.
type ChatListOut struct {
	Items []ChatOut `json:"items"`
}

// chatList, chatGet and chatDelete apply only the agents rule, not the kill
// switch: a kept chat is the person's own record, and reading or deleting it
// sends nothing, so it works while AI is switched off.
func (s *Service) chatList(c *ops.Ctx, in ChatListIn) (ChatListOut, error) {
	if err := s.gateAgents(c, in.ProjectID); err != nil {
		return ChatListOut{}, err
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT c.id, c.title, c.created_at, c.updated_at, (SELECT count(*) FROM ai_chat_turns t WHERE t.chat_id = c.id)
		FROM ai_chats c WHERE c.user_id = ? AND c.project_id = ? ORDER BY c.updated_at DESC, c.id DESC`, c.P.UserID, in.ProjectID)
	if err != nil {
		return ChatListOut{}, ops.Internal(err)
	}
	defer rows.Close()
	out := ChatListOut{Items: []ChatOut{}}
	for rows.Next() {
		var ch ChatOut
		if err := rows.Scan(&ch.ID, &ch.Title, &ch.CreatedAt, &ch.UpdatedAt, &ch.TurnCount); err != nil {
			return out, ops.Internal(err)
		}
		out.Items = append(out.Items, ch)
	}
	return out, nil
}

// ChatRefIn names a chat.
type ChatRefIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ChatID    string `query:"chat_id"`
}

// ProjectRef lets the registry authorize by membership.
func (in ChatRefIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *ChatRefIn) Validate() error { return checkID("chat_id", in.ChatID, "cht_") }

func loadChat(ctx context.Context, q dbq, projectID, userID, id string, withTurns bool) (ChatOut, error) {
	var ch ChatOut
	err := q.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM ai_chats WHERE id = ? AND user_id = ? AND project_id = ?`, id, userID, projectID).
		Scan(&ch.ID, &ch.Title, &ch.CreatedAt, &ch.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ch, ops.NotFound("chat")
	}
	if err != nil {
		return ch, err
	}
	rows, err := q.QueryContext(ctx, `SELECT seq, role, content, run_id, created_at FROM ai_chat_turns WHERE chat_id = ? ORDER BY seq`, id)
	if err != nil {
		return ch, err
	}
	defer rows.Close()
	for rows.Next() {
		var t ChatTurn
		if err := rows.Scan(&t.Seq, &t.Role, &t.Content, &t.RunID, &t.CreatedAt); err != nil {
			return ch, err
		}
		ch.TurnCount++
		if withTurns {
			ch.Turns = append(ch.Turns, t)
		}
	}
	return ch, rows.Err()
}

func (s *Service) chatGet(c *ops.Ctx, in ChatRefIn) (ChatItem, error) {
	if err := s.gateAgents(c, in.ProjectID); err != nil {
		return ChatItem{}, err
	}
	ch, err := loadChat(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.ChatID, true)
	return ChatItem{Item: ch}, mapErr(err)
}

func (s *Service) chatDelete(c *ops.Ctx, in ChatRefIn) (DeletedOut, error) {
	if err := s.gateAgents(c, in.ProjectID); err != nil {
		return DeletedOut{}, err
	}
	var n int64
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		r, err := t.ExecContext(c, `DELETE FROM ai_chats WHERE id = ? AND user_id = ? AND project_id = ?`, in.ChatID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	if err != nil {
		return DeletedOut{}, ops.Internal(err)
	}
	if n == 0 {
		return DeletedOut{}, ops.NotFound("chat")
	}
	return DeletedOut{Deleted: 1}, nil
}

// ChatAppendIn adds a finished exchange to a kept chat.
type ChatAppendIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ChatID    string `json:"chat_id"`
	RunID     string `json:"run_id" desc:"Your chat run (streamed or not). The answer must be the one that run produced: its hash is checked against the run's record."`
	Message   string `json:"message" desc:"What you said, as you sent it. Up to 4,000 characters."`
	Answer    string `json:"answer" desc:"What the run answered."`
}

// ProjectRef lets the registry authorize by membership.
func (in ChatAppendIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *ChatAppendIn) Validate() error {
	if err := checkID("chat_id", in.ChatID, "cht_"); err != nil {
		return err
	}
	if err := checkRun(in.RunID); err != nil {
		return err
	}
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > 4000 || len(in.Answer) > 64<<10 || in.Answer == "" {
		return ops.Invalid("message", "A message of up to 4,000 characters and its answer.")
	}
	return nil
}

func (s *Service) chatAppend(c *ops.Ctx, in ChatAppendIn) (ChatItem, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return ChatItem{}, err
	}
	var out ChatOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		if _, err := loadChat(c, t, in.ProjectID, c.P.UserID, in.ChatID, false); err != nil {
			return err
		}
		var runRecipe string
		var runProject string
		if err := t.QueryRowContext(c, `SELECT recipe, project_id FROM ai_runs WHERE id = ? AND user_id = ?`, in.RunID, c.P.UserID).Scan(&runRecipe, &runProject); err != nil || runProject != in.ProjectID || runRecipe != "chat" {
			return ops.NotFound("run")
		}
		// the answer is what the run produced: the run recorded its hash
		if err := verifyHash(c, t, in.RunID, c.P.UserID, sha(in.Answer)); err != nil {
			return ops.Invalid("answer", "That is not the answer this run gave.")
		}
		var seq int
		_ = t.QueryRowContext(c, `SELECT COALESCE(MAX(seq),0) FROM ai_chat_turns WHERE chat_id = ?`, in.ChatID).Scan(&seq)
		if seq+2 > maxChatTurns {
			return ops.Conflict("This conversation is full (200 messages). Start another.")
		}
		var chatBytes, personBytes int64
		_ = t.QueryRowContext(c, `SELECT COALESCE(SUM(length(CAST(content AS BLOB))),0) FROM ai_chat_turns WHERE chat_id = ?`, in.ChatID).Scan(&chatBytes)
		_ = t.QueryRowContext(c, `SELECT COALESCE(SUM(length(CAST(t.content AS BLOB))),0) FROM ai_chat_turns t JOIN ai_chats h ON h.id = t.chat_id WHERE h.user_id = ? AND h.project_id = ?`, c.P.UserID, in.ProjectID).Scan(&personBytes)
		add := int64(len(in.Message) + len(in.Answer))
		if chatBytes+add > MaxChatBytes {
			return ops.Conflict("This conversation is full (2 MB of text). Start another.")
		}
		if personBytes+add > MaxPersonChatBytes {
			return ops.Conflict("You keep 20 MB of conversations on this project. Delete one first.")
		}
		at := now()
		for i, tn := range []struct{ role, text, run string }{{"user", in.Message, ""}, {"assistant", in.Answer, in.RunID}} {
			if _, err := t.ExecContext(c, `INSERT INTO ai_chat_turns(chat_id, seq, role, content, run_id, created_at) VALUES (?,?,?,?,?,?)`, in.ChatID, seq+1+i, tn.role, tn.text, tn.run, at); err != nil {
				return err
			}
		}
		if _, err := t.ExecContext(c, `UPDATE ai_chats SET updated_at = ?, title = CASE WHEN title = '' THEN ? ELSE title END WHERE id = ?`, at, clip(in.Message, 60), in.ChatID); err != nil {
			return err
		}
		var err error
		out, err = loadChat(c, t, in.ProjectID, c.P.UserID, in.ChatID, true)
		return err
	})
	return ChatItem{Item: out}, mapErr(err)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ChatSendIn is one message of a chat: the same fields as ai.context.preview
// and ai.run, with the conversation taken from a kept chat when chat_id is set.
type ChatSendIn struct {
	ProjectID   string            `path:"id" json:"-" desc:"Project id."`
	ChatID      string            `json:"chat_id,omitempty" desc:"A kept chat: its earlier messages are sent as the conversation and this exchange is added to it. Empty: nothing is kept; send turns yourself."`
	Message     string            `json:"message" desc:"What you say."`
	Turns       []aicore.TurnIn   `json:"turns,omitempty" desc:"Earlier messages, when the chat is not kept."`
	DocumentID  string            `json:"document_id,omitempty"`
	Selection   *aicore.Selection `json:"selection,omitempty"`
	Chips       []aicore.ChipSpec `json:"chips,omitempty" desc:"Context chips for this message. Omit for the defaults (the selection, the brief, the style guide)."`
	Provider    string            `json:"provider,omitempty"`
	Model       string            `json:"model,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Nonce       string            `json:"nonce,omitempty" desc:"Preview: leave empty. Send: the preview's."`
	PayloadHash string            `json:"payload_hash,omitempty" desc:"Send: the preview's. Required."`
}

// ProjectRef lets the registry authorize by membership.
func (in ChatSendIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *ChatSendIn) Validate() error {
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > 4000 {
		return ops.Invalid("message", "A message of up to 4,000 characters.")
	}
	if in.ChatID != "" {
		if err := checkID("chat_id", in.ChatID, "cht_"); err != nil {
			return err
		}
		if len(in.Turns) > 0 {
			return ops.Invalid("turns", "A kept chat supplies its own earlier messages.")
		}
	}
	return nil
}

func (s *Service) chatRunIn(c *ops.Ctx, in ChatSendIn) (aicore.RunIn, error) {
	ri := aicore.RunIn{ProjectID: in.ProjectID, Recipe: "chat", DocumentID: in.DocumentID, Selection: in.Selection, Chips: in.Chips, Params: map[string]string{"message": in.Message},
		Turns: in.Turns, Provider: in.Provider, Model: in.Model, MaxTokens: in.MaxTokens, Nonce: in.Nonce, PayloadHash: in.PayloadHash}
	if in.ChatID != "" {
		ch, err := loadChat(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.ChatID, true)
		if err != nil {
			return ri, err
		}
		turns := ch.Turns
		if len(turns) > chatContextMsgs {
			turns = turns[len(turns)-chatContextMsgs:]
		}
		for len(turns) > 0 && turns[0].Role != "user" {
			turns = turns[1:]
		}
		for _, t := range turns {
			ri.Turns = append(ri.Turns, aicore.TurnIn{Role: t.Role, Content: t.Content})
		}
	}
	return ri, nil
}

func (s *Service) chatPreview(c *ops.Ctx, in ChatSendIn) (aicore.PreviewOut, error) {
	ri, err := s.chatRunIn(c, in)
	if err != nil {
		return aicore.PreviewOut{}, mapErr(err)
	}
	return s.ai.Preview(c, c.DB(), c.P, ri)
}

func (s *Service) chatSend(c *ops.Ctx, in ChatSendIn) (aicore.RunItem, error) {
	ri, err := s.chatRunIn(c, in)
	if err != nil {
		return aicore.RunItem{}, mapErr(err)
	}
	out, err := s.ai.Run(c, c.DB(), c.P, ri)
	if err != nil {
		return aicore.RunItem{}, err
	}
	if in.ChatID != "" && out.Status == "ok" && out.Text != "" {
		if _, aerr := s.chatAppend(c, ChatAppendIn{ProjectID: in.ProjectID, ChatID: in.ChatID, RunID: out.RunID, Message: in.Message, Answer: out.Text}); aerr != nil {
			out.Warnings = append(out.Warnings, "The answer could not be added to the kept chat; it is shown here and not kept.")
		}
	}
	return aicore.RunItem{Item: out}, nil
}

var _ = lib.ActionChat
