package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// Providers. An instance provider belongs to the administrators and may be used
// by anyone the project policy allows, without holding its key. A personal
// provider belongs to one person and is invisible to everyone else, an
// administrator included. Their keys are sealed under different contexts (see
// sealContext), so a ciphertext copied from one to the other does not open.

type provider struct {
	ID, UserID, Kind, Name, BaseURL, Cipher, Hint, DefaultModel string
	ModelsJSON, ModelsFetchedAt, SettingsJSON                   string
	Enabled, Local                                              bool
	TimeoutSec, MaxConc                                         int
	CreatedBy, CreatedAt, UpdatedAt                             string
}

func (p provider) instance() bool { return p.UserID == "" }

const providerCols = `id, COALESCE(user_id,''), kind, name, base_url, api_key_cipher, key_hint, default_model, models_json, models_fetched_at,
	settings_json, enabled, local, timeout_seconds, max_concurrency, COALESCE(created_by,''), created_at, updated_at`

func scanProvider(sc interface{ Scan(...any) error }) (provider, error) {
	var p provider
	var en, lo int
	err := sc.Scan(&p.ID, &p.UserID, &p.Kind, &p.Name, &p.BaseURL, &p.Cipher, &p.Hint, &p.DefaultModel, &p.ModelsJSON, &p.ModelsFetchedAt,
		&p.SettingsJSON, &en, &lo, &p.TimeoutSec, &p.MaxConc, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	p.Enabled, p.Local = en == 1, lo == 1
	return p, err
}

type providerSettings struct {
	Headers   []lib.Header `json:"headers,omitempty"`
	Routing   lib.Routing  `json:"routing"`
	Username  string       `json:"username,omitempty"`
	KeepAlive string       `json:"keep_alive,omitempty"`
}

func (p provider) settings() providerSettings {
	var s providerSettings
	_ = json.Unmarshal([]byte(p.SettingsJSON), &s)
	return s
}

// ModelInfo is a model in a provider's list. Manual entries survive a refresh.
type ModelInfo struct {
	lib.Model
	Manual bool `json:"manual,omitempty"`
}

func (p provider) models() []ModelInfo {
	var m []ModelInfo
	_ = json.Unmarshal([]byte(p.ModelsJSON), &m)
	return m
}

// owner is the part of the seal context that says whose provider it is.
func (p provider) owner() string {
	if p.UserID == "" {
		return "instance"
	}
	return "user:" + p.UserID
}

// normAddr is the address a key is bound to: scheme, host, port and path, folded.
func normAddr(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port) + strings.TrimRight(u.EscapedPath(), "/")
}

// sealContext binds a key to the row, its owner and its address.
func sealContext(p provider) string {
	return "ai_providers/" + p.ID + "/" + p.owner() + "/api_key/" + normAddr(p.BaseURL)
}

// checkBaseURL validates a typed address and returns it cleaned. local says the
// provider is marked local (only an administrator can do that).
func (s *Service) checkBaseURL(raw string, local bool) (string, *ops.Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 500 {
		return "", ops.Invalid("base_url", "Give the provider's address, like https://openrouter.ai/api/v1.")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ops.Invalid("base_url", "The address cannot contain spaces or control characters.")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ops.Invalid("base_url", "The address must start with http:// or https://.")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", ops.Invalid("base_url", "The address cannot hold a user name or password. Put the key in the key field.")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", ops.Invalid("base_url", "The address cannot have a query string or a fragment.")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", ops.Invalid("base_url", "The address has no host.")
	}
	ip := net.ParseIP(host)
	if ip != nil && s.classify(ip) == netguard.Blocked {
		return "", ops.Invalid("base_url", "That address is never allowed (it is reserved or link-local).")
	}
	isLocalName := host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && s.classify(ip) == netguard.Local)
	if isLocalName && !local {
		return "", ops.Invalid("base_url", "That address is on this machine or a private network. An administrator has to mark the provider as local for it to be used.")
	}
	if u.Scheme == "http" && !local && !s.allowHTTP {
		return "", ops.Invalid("base_url", "Use https: text is not sent in the clear to a provider that is not local.")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

func checkName(s string) (string, *ops.Error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 80 {
		return "", ops.Invalid("name", "Give the provider a name of up to 80 characters.")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", ops.Invalid("name", "The name cannot contain control characters.")
		}
	}
	return s, nil
}

func checkKey(s string) (string, *ops.Error) {
	s = strings.TrimSpace(s)
	if len(s) > 1000 {
		return "", ops.Invalid("api_key", "That key is longer than any provider issues.")
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return "", ops.Invalid("api_key", "A key is printable ASCII with no spaces.")
		}
	}
	return s, nil
}

// keyHint shows the last four characters of a key long enough that four of them
// are a small part of it; a short key gets none.
func keyHint(k string) string {
	if len(k) < 20 {
		return ""
	}
	return "…" + k[len(k)-4:]
}

// ---- wire shapes ----------------------------------------------------------------------------------

// Provider is a provider as callers see it. The key is never in it.
type Provider struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Kind            string       `json:"kind" enum:"openai_compatible,openrouter,opencode,ollama"`
	Scope           string       `json:"scope" enum:"instance,user" desc:"instance: managed by administrators, usable by anyone the project policy allows; user: yours alone."`
	BaseURL         string       `json:"base_url,omitempty" desc:"Omitted from an instance provider when you are not an administrator: internal addresses are not shown to editors."`
	Enabled         bool         `json:"enabled"`
	Local           bool         `json:"local" desc:"Declared by an administrator: the endpoint is on this machine or its network. Text sent to it stays there, and local-only projects may use it."`
	HasKey          bool         `json:"has_key"`
	KeyHint         string       `json:"key_hint,omitempty" desc:"The last four characters, so you can tell which key is stored. The key itself is never returned."`
	DefaultModel    string       `json:"default_model,omitempty"`
	Models          []ModelInfo  `json:"models"`
	ModelsFetchedAt string       `json:"models_fetched_at,omitempty"`
	TimeoutSeconds  int          `json:"timeout_seconds" desc:"0 uses the server default."`
	MaxConcurrency  int          `json:"max_concurrency" desc:"0 uses the server default."`
	Headers         []lib.Header `json:"headers,omitempty"`
	Routing         *lib.Routing `json:"routing,omitempty" desc:"OpenRouter only."`
	Username        string       `json:"username,omitempty" desc:"OpenCode only: the basic-auth user name (default opencode)."`
	KeepAlive       string       `json:"keep_alive,omitempty" desc:"Ollama only: how long the model stays loaded."`
	Editable        bool         `json:"editable" desc:"You may change or delete it."`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
}

func (p provider) wire(c ops.Principal) Provider {
	st := p.settings()
	w := Provider{ID: p.ID, Name: p.Name, Kind: p.Kind, Scope: "user", Enabled: p.Enabled, Local: p.Local, HasKey: p.Cipher != "", KeyHint: p.Hint,
		DefaultModel: p.DefaultModel, Models: p.models(), ModelsFetchedAt: p.ModelsFetchedAt, TimeoutSeconds: p.TimeoutSec, MaxConcurrency: p.MaxConc,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if w.Models == nil {
		w.Models = []ModelInfo{}
	}
	if p.Kind == lib.KindOpenRouter {
		r := st.Routing
		w.Routing = &r
	}
	if p.instance() {
		w.Scope = "instance"
	}
	w.Editable = p.editableBy(c)
	// The address, the extra headers, the user name and the key hint are for whoever manages the
	// provider: an editor who may use an instance provider does not see how it is configured.
	if w.Editable {
		w.BaseURL, w.Headers, w.Username, w.KeepAlive = p.BaseURL, st.Headers, st.Username, st.KeepAlive
	} else {
		w.KeyHint = ""
	}
	return w
}

// ---- store ------------------------------------------------------------------------------------------

// providersFor lists the providers a person may see: the instance's and their own.
func providersFor(ctx context.Context, d *db.DB, userID string) ([]provider, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT `+providerCols+` FROM ai_providers WHERE user_id IS NULL OR user_id = ? ORDER BY COALESCE(user_id,'') , lower(name), id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// providerByID returns a provider the caller may see, or not-found: another
// person's provider looks exactly like one that does not exist.
func providerByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, id string) (provider, error) {
	p, err := scanProvider(q.QueryRowContext(ctx, `SELECT `+providerCols+` FROM ai_providers WHERE id = ? AND (user_id IS NULL OR user_id = ?)`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return provider{}, ops.NotFound("provider")
	}
	if err != nil {
		return provider{}, ops.Internal(err)
	}
	return p, nil
}

func (p provider) editableBy(c ops.Principal) bool {
	if p.instance() {
		return c.Role == ops.RoleAdmin
	}
	return p.UserID == c.UserID
}

func providerID(id string) *ops.Error {
	if !strings.HasPrefix(id, "aip_") || len(id) > 40 {
		return ops.Invalid("id", "That is not a provider id.")
	}
	return nil
}

// ---- inputs -------------------------------------------------------------------------------------------

// ProviderListOut lists the providers visible to the caller.
type ProviderListOut struct {
	Items         []Provider `json:"items"`
	PersonalOK    bool       `json:"personal_providers_allowed" desc:"False when the administrator switched personal providers off."`
	CanSetLocal   bool       `json:"can_set_local" desc:"The caller is an administrator and may mark a provider local."`
	AllowedHeader []string   `json:"allowed_headers"`
}

// ProviderItem wraps one provider.
type ProviderItem struct {
	Item Provider `json:"item"`
}

// ProviderIn names a provider.
type ProviderIn struct {
	ID string `path:"id" json:"-" desc:"Provider id (aip_...)."`
}

// Validate checks the id.
func (in ProviderIn) Validate() error {
	if e := providerID(in.ID); e != nil {
		return e
	}
	return nil
}

// ProviderCreateIn creates a provider.
type ProviderCreateIn struct {
	Name           string       `json:"name"`
	Kind           string       `json:"kind" enum:"openai_compatible,openrouter,opencode,ollama"`
	Scope          string       `json:"scope,omitempty" enum:"instance,user" desc:"instance (administrators only) or user (yours alone, the default for a non-administrator)."`
	BaseURL        string       `json:"base_url" desc:"For example https://openrouter.ai/api/v1, https://api.openai.com/v1, http://192.168.1.20:11434 (Ollama, local only), http://127.0.0.1:4096 (OpenCode, local only). No credentials in the address."`
	APIKey         string       `json:"api_key,omitempty" desc:"Write-only: sealed with the server's secret key and never returned. Some local servers need none."`
	DefaultModel   string       `json:"default_model,omitempty"`
	Enabled        *bool        `json:"enabled,omitempty" desc:"Default true."`
	Local          bool         `json:"local,omitempty" desc:"Administrators only: this endpoint is on this machine or its network. Without it loopback, private and link-local addresses are refused at connect time."`
	TimeoutSeconds int          `json:"timeout_seconds,omitempty" desc:"Per call; 0 for the server default."`
	MaxConcurrency int          `json:"max_concurrency,omitempty" desc:"Simultaneous calls to this provider; 0 for the server default."`
	Headers        []lib.Header `json:"headers,omitempty" desc:"Extra headers: HTTP-Referer, X-Title, OpenAI-Organization, OpenAI-Project. No others."`
	Routing        *lib.Routing `json:"routing,omitempty" desc:"OpenRouter routing preferences. Data collection is denied unless allow_data_collection is set."`
	Username       string       `json:"username,omitempty" desc:"OpenCode: the basic-auth user name."`
	KeepAlive      string       `json:"keep_alive,omitempty" desc:"Ollama: keep_alive, like 5m."`
}

func checkCommon(headers []lib.Header, tos, conc int, model string) *ops.Error {
	if len(headers) > 8 {
		return ops.Invalid("headers", "At most 8 extra headers.")
	}
	for _, h := range headers {
		if err := lib.CheckHeader(h); err != nil {
			return ops.Invalid("headers", err.Error())
		}
	}
	if tos < 0 || tos > 600 {
		return ops.Invalid("timeout_seconds", "Between 0 and 600 seconds.")
	}
	if conc < 0 || conc > 32 {
		return ops.Invalid("max_concurrency", "Between 0 and 32.")
	}
	if model != "" && !lib.ValidModel(model) {
		return ops.Invalid("default_model", "That is not a usable model name.")
	}
	return nil
}

// Validate normalises the input.
func (in *ProviderCreateIn) Validate() error {
	var e *ops.Error
	if in.Name, e = checkName(in.Name); e != nil {
		return e
	}
	if !lib.ValidKind(in.Kind) {
		return ops.Invalid("kind", "Choose openai_compatible, openrouter, opencode or ollama.")
	}
	if in.Scope != "" && in.Scope != "instance" && in.Scope != "user" {
		return ops.Invalid("scope", "Scope is instance or user.")
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return ops.Invalid("base_url", "Give the provider's address.")
	}
	if in.APIKey != "" {
		var e *ops.Error
		if in.APIKey, e = checkKey(in.APIKey); e != nil {
			return e
		}
	}
	if e := checkCommon(in.Headers, in.TimeoutSeconds, in.MaxConcurrency, strings.TrimSpace(in.DefaultModel)); e != nil {
		return e
	}
	in.DefaultModel = strings.TrimSpace(in.DefaultModel)
	if len(in.Username) > 100 || len(in.KeepAlive) > 20 {
		return ops.Invalid("username", "Too long.")
	}
	return nil
}

// ProviderUpdateIn changes a provider. Only the fields sent change.
type ProviderUpdateIn struct {
	ID             string        `path:"id" json:"-" desc:"Provider id."`
	Name           *string       `json:"name,omitempty"`
	BaseURL        *string       `json:"base_url,omitempty" desc:"Changing the address (scheme, host, port or path) drops the stored key; send api_key too to store one for the new address."`
	APIKey         *string       `json:"api_key,omitempty" desc:"A new key, write-only. An empty string removes the stored key."`
	DefaultModel   *string       `json:"default_model,omitempty"`
	Enabled        *bool         `json:"enabled,omitempty"`
	Local          *bool         `json:"local,omitempty" desc:"Administrators only."`
	TimeoutSeconds *int          `json:"timeout_seconds,omitempty"`
	MaxConcurrency *int          `json:"max_concurrency,omitempty"`
	Headers        *[]lib.Header `json:"headers,omitempty"`
	Routing        *lib.Routing  `json:"routing,omitempty"`
	Username       *string       `json:"username,omitempty"`
	KeepAlive      *string       `json:"keep_alive,omitempty"`
}

// Validate checks the id and the fields that stand alone.
func (in *ProviderUpdateIn) Validate() error {
	if e := providerID(in.ID); e != nil {
		return e
	}
	if in.Name != nil {
		n, e := checkName(*in.Name)
		if e != nil {
			return e
		}
		in.Name = &n
	}
	if in.APIKey != nil && *in.APIKey != "" {
		k, e := checkKey(*in.APIKey)
		if e != nil {
			return e
		}
		in.APIKey = &k
	}
	var hs []lib.Header
	if in.Headers != nil {
		hs = *in.Headers
	}
	tos, conc, model := 0, 0, ""
	if in.TimeoutSeconds != nil {
		tos = *in.TimeoutSeconds
	}
	if in.MaxConcurrency != nil {
		conc = *in.MaxConcurrency
	}
	if in.DefaultModel != nil {
		model = strings.TrimSpace(*in.DefaultModel)
		in.DefaultModel = &model
	}
	if e := checkCommon(hs, tos, conc, model); e != nil {
		return e
	}
	return nil
}

// ---- ops: list, get ---------------------------------------------------------------------------------

func listProviders(s *Service) func(*ops.Ctx, struct{}) (ProviderListOut, error) {
	return func(c *ops.Ctx, _ struct{}) (ProviderListOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ProviderListOut{}, err
		}
		ps, err := providersFor(c, c.DB(), c.P.UserID)
		if err != nil {
			return ProviderListOut{}, ops.Internal(err)
		}
		out := ProviderListOut{Items: make([]Provider, 0, len(ps)), PersonalOK: !s.cfg.NoUserProviders, CanSetLocal: c.P.Role == ops.RoleAdmin, AllowedHeader: lib.AllowedExtraHeaders()}
		for _, p := range ps {
			out.Items = append(out.Items, p.wire(c.P))
		}
		return out, nil
	}
}

func getProvider(s *Service) func(*ops.Ctx, ProviderIn) (ProviderItem, error) {
	return func(c *ops.Ctx, in ProviderIn) (ProviderItem, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ProviderItem{}, err
		}
		p, err := providerByID(c, c.DB().Read(), c.P.UserID, in.ID)
		if err != nil {
			return ProviderItem{}, err
		}
		return ProviderItem{Item: p.wire(c.P)}, nil
	}
}

// ---- ops: create, update, delete ---------------------------------------------------------------------

func (s *Service) sealKey(p provider, key string) (string, error) {
	if !s.box.Enabled() {
		return "", ops.Conflict(secrets.ErrNoKey.Error())
	}
	sealed, err := s.box.Seal([]byte(key), sealContext(p))
	if err != nil {
		return "", ops.Internal(err)
	}
	return sealed, nil
}

func createProvider(s *Service) func(*ops.Ctx, ProviderCreateIn) (ProviderItem, error) {
	return func(c *ops.Ctx, in ProviderCreateIn) (ProviderItem, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ProviderItem{}, err
		}
		admin := c.P.Role == ops.RoleAdmin
		scope := in.Scope
		if scope == "" {
			scope = "user"
			if admin {
				scope = "instance"
			}
		}
		if scope == "instance" && !admin {
			return ProviderItem{}, ops.Forbidden("Only an administrator can add an instance provider.")
		}
		if scope == "user" && s.cfg.NoUserProviders {
			return ProviderItem{}, ops.Conflict("Personal providers are switched off on this server. Ask an administrator to add one.")
		}
		if in.Local && !admin {
			return ProviderItem{}, ops.Forbidden("Only an administrator can mark a provider as local.")
		}
		base, e := s.checkBaseURL(in.BaseURL, in.Local)
		if e != nil {
			return ProviderItem{}, e
		}
		p := provider{ID: db.NewID("aip"), Kind: in.Kind, Name: in.Name, BaseURL: base, DefaultModel: in.DefaultModel, ModelsJSON: "[]",
			Enabled: in.Enabled == nil || *in.Enabled, Local: in.Local, TimeoutSec: in.TimeoutSeconds, MaxConc: in.MaxConcurrency,
			CreatedBy: c.P.UserID, CreatedAt: now()}
		p.UpdatedAt = p.CreatedAt
		if scope == "user" {
			p.UserID = c.P.UserID
		}
		st := providerSettings{Headers: in.Headers, Username: in.Username, KeepAlive: in.KeepAlive}
		if in.Routing != nil {
			st.Routing = *in.Routing
		}
		raw, _ := json.Marshal(st)
		p.SettingsJSON = string(raw)
		if in.APIKey != "" {
			var err error
			if p.Cipher, err = s.sealKey(p, in.APIKey); err != nil {
				return ProviderItem{}, err
			}
			p.Hint = keyHint(in.APIKey)
		}
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO ai_providers(id, user_id, kind, name, base_url, api_key_cipher, key_hint, default_model, models_json, models_fetched_at,
				settings_json, enabled, local, timeout_seconds, max_concurrency, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				p.ID, nullStr(p.UserID), p.Kind, p.Name, p.BaseURL, p.Cipher, p.Hint, p.DefaultModel, p.ModelsJSON, "", p.SettingsJSON, b2i(p.Enabled), b2i(p.Local),
				p.TimeoutSec, p.MaxConc, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
			if err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.provider.create", TargetType: "ai_provider", TargetID: p.ID,
				Detail: map[string]any{"kind": p.Kind, "scope": scope, "local": p.Local, "has_key": p.Cipher != ""}})
			return c.FlushTo(tx)
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return ProviderItem{}, ops.Conflict("You already have a provider with that name.")
			}
			return ProviderItem{}, ops.Internal(err)
		}
		return ProviderItem{Item: p.wire(c.P)}, nil
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func updateProvider(s *Service) func(*ops.Ctx, ProviderUpdateIn) (ProviderItem, error) {
	return func(c *ops.Ctx, in ProviderUpdateIn) (ProviderItem, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ProviderItem{}, err
		}
		admin := c.P.Role == ops.RoleAdmin
		var out provider
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			p, err := providerByID(c, tx, c.P.UserID, in.ID)
			if err != nil {
				return err
			}
			if !p.editableBy(c.P) {
				return ops.Forbidden("Only an administrator can change an instance provider.")
			}
			if in.Local != nil && *in.Local != p.Local {
				if !admin {
					return ops.Forbidden("Only an administrator can mark a provider as local.")
				}
				p.Local = *in.Local
			}
			addrChanged := false
			if in.BaseURL != nil {
				base, e := s.checkBaseURL(*in.BaseURL, p.Local)
				if e != nil {
					return e
				}
				addrChanged = normAddr(base) != normAddr(p.BaseURL)
				p.BaseURL = base
			} else if _, e := s.checkBaseURL(p.BaseURL, p.Local); e != nil && in.Local != nil {
				return ops.Invalid("local", "This provider's address needs it to stay local: "+e.Message)
			}
			if in.Name != nil {
				p.Name = *in.Name
			}
			if in.DefaultModel != nil {
				p.DefaultModel = *in.DefaultModel
			}
			if in.Enabled != nil {
				p.Enabled = *in.Enabled
			}
			if in.TimeoutSeconds != nil {
				p.TimeoutSec = *in.TimeoutSeconds
			}
			if in.MaxConcurrency != nil {
				p.MaxConc = *in.MaxConcurrency
			}
			st := p.settings()
			if in.Headers != nil {
				st.Headers = *in.Headers
			}
			if in.Routing != nil {
				st.Routing = *in.Routing
			}
			if in.Username != nil {
				st.Username = *in.Username
			}
			if in.KeepAlive != nil {
				st.KeepAlive = *in.KeepAlive
			}
			raw, _ := json.Marshal(st)
			p.SettingsJSON = string(raw)
			keyDropped := false
			switch {
			case in.APIKey != nil && *in.APIKey != "":
				if p.Cipher, err = s.sealKey(p, *in.APIKey); err != nil {
					return err
				}
				p.Hint = keyHint(*in.APIKey)
			case in.APIKey != nil: // an empty key removes it
				p.Cipher, p.Hint, keyDropped = "", "", true
			case addrChanged && p.Cipher != "":
				// A key belongs to one address: the old one is not carried to the new.
				p.Cipher, p.Hint, keyDropped = "", "", true
			}
			if addrChanged {
				p.ModelsJSON, p.ModelsFetchedAt = keepManual(p.ModelsJSON), ""
			}
			p.UpdatedAt = now()
			if _, err := tx.Exec(`UPDATE ai_providers SET name=?, base_url=?, api_key_cipher=?, key_hint=?, default_model=?, models_json=?, models_fetched_at=?,
				settings_json=?, enabled=?, local=?, timeout_seconds=?, max_concurrency=?, updated_at=? WHERE id = ?`,
				p.Name, p.BaseURL, p.Cipher, p.Hint, p.DefaultModel, p.ModelsJSON, p.ModelsFetchedAt, p.SettingsJSON, b2i(p.Enabled), b2i(p.Local),
				p.TimeoutSec, p.MaxConc, p.UpdatedAt, p.ID); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.provider.update", TargetType: "ai_provider", TargetID: p.ID,
				Detail: map[string]any{"address_changed": addrChanged, "key_dropped": keyDropped, "local": p.Local, "enabled": p.Enabled}})
			out = p
			return c.FlushTo(tx)
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return ProviderItem{}, ops.Conflict("You already have a provider with that name.")
			}
			return ProviderItem{}, ops.AsError(err)
		}
		if !c.DryRun() {
			s.forget(out.ID)
		}
		return ProviderItem{Item: out.wire(c.P)}, nil
	}
}

// keepManual keeps the entries a person typed in, dropping what the provider listed.
func keepManual(models string) string {
	var in, out []ModelInfo
	_ = json.Unmarshal([]byte(models), &in)
	for _, m := range in {
		if m.Manual {
			out = append(out, m)
		}
	}
	b, _ := json.Marshal(out)
	if out == nil {
		return "[]"
	}
	return string(b)
}

// RemovedOut says what was removed.
type RemovedOut struct {
	Removed bool `json:"removed"`
}

func deleteProvider(s *Service) func(*ops.Ctx, ProviderIn) (RemovedOut, error) {
	return func(c *ops.Ctx, in ProviderIn) (RemovedOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return RemovedOut{}, err
		}
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			p, err := providerByID(c, tx, c.P.UserID, in.ID)
			if err != nil {
				return err
			}
			if !p.editableBy(c.P) {
				return ops.Forbidden("Only an administrator can delete an instance provider.")
			}
			if _, err := tx.Exec(`DELETE FROM ai_providers WHERE id = ?`, p.ID); err != nil {
				return err
			}
			// A policy that names it no longer does: it must not keep pointing at a
			// row that a later provider could not reuse (ids are never reused, but
			// a stale list is confusing).
			if _, err := tx.Exec(`DELETE FROM ai_prices WHERE provider_id = ?`, p.ID); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.provider.delete", TargetType: "ai_provider", TargetID: p.ID, Detail: map[string]any{"kind": p.Kind}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return RemovedOut{}, ops.AsError(err)
		}
		if !c.DryRun() {
			s.forget(in.ID)
		}
		return RemovedOut{Removed: true}, nil
	}
}

// ---- client cache -------------------------------------------------------------------------------------

// forget drops the cached clients of a provider (it changed or went away).
func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.clients {
		if strings.HasPrefix(k, id+"|") {
			c.Close()
			delete(s.clients, k)
		}
	}
}

// modeFor is what a connection to this provider may reach. A provider declared local
// reaches local addresses only, always: if its name starts to resolve to a public
// address it is refused (or its key and text would travel to it as a "local" provider,
// over http, and be charged as free). A provider that is not declared local reaches
// public addresses only. A project in local_only mode asks for the first kind.
func modeFor(p provider, requireLocal bool) netguard.Mode {
	if p.Local || requireLocal {
		return netguard.RequireLocal
	}
	return netguard.PublicOnly
}

func (s *Service) limitsFor(p provider) lib.Limits {
	l := lib.Limits{Dial: s.cfg.DialTimeout, FirstByte: s.cfg.RequestTimeout, Request: s.cfg.RequestTimeout, Idle: s.cfg.StreamIdle,
		StreamMax: s.cfg.StreamMax, MaxResponse: s.cfg.MaxResponseBytes}
	if p.TimeoutSec > 0 {
		l.Request = time.Duration(p.TimeoutSec) * time.Second
		l.FirstByte = l.Request
	}
	return l
}

// client returns the client for a provider, building it (and opening the key)
// on first use. The key is opened only here and lives only in the client.
func (s *Service) client(p provider, requireLocal bool) (*lib.Client, *ops.Error) {
	mode := modeFor(p, requireLocal)
	key := p.ID + "|" + strconv.Itoa(int(mode)) + "|" + p.UpdatedAt
	s.mu.Lock()
	if c, ok := s.clients[key]; ok {
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	apiKey := ""
	if p.Cipher != "" {
		b, err := s.box.Open(p.Cipher, sealContext(p))
		if err != nil {
			return nil, ops.Conflict("The stored key for this provider cannot be read with this installation's secret key. Enter the key again.")
		}
		apiKey = string(b)
	}
	st := p.settings()
	ep := lib.Endpoint{Kind: p.Kind, BaseURL: p.BaseURL, APIKey: apiKey, Username: st.Username, Headers: st.Headers, Routing: st.Routing, KeepAlive: st.KeepAlive, Mode: mode}
	c, err := lib.NewClient(ep, lib.ClientOptions{Log: s.log, Limits: s.limitsFor(p), Guard: s.guard})
	if err != nil {
		return nil, ops.Invalid("base_url", "The provider's address is not usable.")
	}
	s.mu.Lock()
	for k, old := range s.clients { // an older version of this provider
		if strings.HasPrefix(k, p.ID+"|") && !strings.HasSuffix(k, "|"+p.UpdatedAt) {
			old.Close()
			delete(s.clients, k)
		}
	}
	s.clients[key] = c
	s.mu.Unlock()
	return c, nil
}

// ---- models -------------------------------------------------------------------------------------------

// ModelsOut lists a provider's models.
type ModelsOut struct {
	Items     []ModelInfo `json:"items"`
	FetchedAt string      `json:"fetched_at,omitempty"`
}

func listModels(s *Service) func(*ops.Ctx, ProviderIn) (ModelsOut, error) {
	return func(c *ops.Ctx, in ProviderIn) (ModelsOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ModelsOut{}, err
		}
		p, err := providerByID(c, c.DB().Read(), c.P.UserID, in.ID)
		if err != nil {
			return ModelsOut{}, err
		}
		m := p.models()
		if m == nil {
			m = []ModelInfo{}
		}
		return ModelsOut{Items: m, FetchedAt: p.ModelsFetchedAt}, nil
	}
}

func refreshModels(s *Service) func(*ops.Ctx, ProviderIn) (ModelsOut, error) {
	return func(c *ops.Ctx, in ProviderIn) (ModelsOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return ModelsOut{}, err
		}
		p, err := providerByID(c, c.DB().Read(), c.P.UserID, in.ID)
		if err != nil {
			return ModelsOut{}, err
		}
		if !p.editableBy(c.P) {
			return ModelsOut{}, ops.Forbidden("Only an administrator can refresh an instance provider's models.")
		}
		if e := s.allow("test:"+c.P.UserID, 10); e != nil {
			return ModelsOut{}, e
		}
		cl, e := s.client(p, false)
		if e != nil {
			return ModelsOut{}, e
		}
		listed, lerr := cl.Models(c)
		if lerr != nil {
			return ModelsOut{}, s.providerFailure(c.P, p, "models", lerr)
		}
		merged := mergeModels(p.models(), listed)
		raw, _ := json.Marshal(merged)
		at := now()
		if err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			// keyed on the version we read: a provider changed meanwhile keeps its own list
			_, err := tx.Exec(`UPDATE ai_providers SET models_json = ?, models_fetched_at = ? WHERE id = ? AND updated_at = ?`, string(raw), at, p.ID, p.UpdatedAt)
			return err
		}); err != nil {
			return ModelsOut{}, ops.Internal(err)
		}
		return ModelsOut{Items: merged, FetchedAt: at}, nil
	}
}

func mergeModels(old []ModelInfo, listed []lib.Model) []ModelInfo {
	manual := map[string]ModelInfo{}
	for _, m := range old {
		if m.Manual {
			manual[m.ID] = m
		}
	}
	var out []ModelInfo
	seen := map[string]bool{}
	for _, m := range listed {
		if mm, ok := manual[m.ID]; ok { // a typed-in entry keeps the person's numbers
			out = append(out, mm)
		} else {
			out = append(out, ModelInfo{Model: m})
		}
		seen[m.ID] = true
	}
	for _, m := range old {
		if m.Manual && !seen[m.ID] {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []ModelInfo{}
	}
	return out
}

// ModelSetIn adds or changes a model entered by hand.
type ModelSetIn struct {
	ID            string `path:"id" json:"-" desc:"Provider id."`
	Model         string `json:"model" desc:"The model id exactly as the provider names it."`
	Label         string `json:"label,omitempty"`
	ContextWindow int    `json:"context_window,omitempty" desc:"Tokens; lets the preview warn before a request is too long."`
	MaxOutput     int    `json:"max_output,omitempty"`
}

// Validate checks the input.
func (in *ModelSetIn) Validate() error {
	if e := providerID(in.ID); e != nil {
		return e
	}
	in.Model = strings.TrimSpace(in.Model)
	if !lib.ValidModel(in.Model) {
		return ops.Invalid("model", "That is not a usable model name.")
	}
	if in.ContextWindow < 0 || in.ContextWindow > 100_000_000 || in.MaxOutput < 0 || in.MaxOutput > 100_000_000 || len(in.Label) > 200 {
		return ops.Invalid("context_window", "Numbers must be between 0 and 100,000,000.")
	}
	return nil
}

// ModelRemoveIn removes a model.
type ModelRemoveIn struct {
	ID    string `path:"id" json:"-" desc:"Provider id."`
	Model string `query:"model" desc:"The model id."`
}

// Validate checks the input.
func (in *ModelRemoveIn) Validate() error {
	if e := providerID(in.ID); e != nil {
		return e
	}
	if !lib.ValidModel(in.Model) {
		return ops.Invalid("model", "Name the model to remove.")
	}
	return nil
}

func editModels(s *Service, c *ops.Ctx, id string, fn func([]ModelInfo) []ModelInfo) (ModelsOut, error) {
	if err := s.gate(c, c.DB()); err != nil {
		return ModelsOut{}, err
	}
	var out []ModelInfo
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		p, err := providerByID(c, tx, c.P.UserID, id)
		if err != nil {
			return err
		}
		if !p.editableBy(c.P) {
			return ops.Forbidden("Only an administrator can change an instance provider.")
		}
		out = fn(p.models())
		if len(out) > 2000 {
			return ops.Invalid("model", "A provider holds at most 2,000 models.")
		}
		raw, _ := json.Marshal(out)
		_, err = tx.Exec(`UPDATE ai_providers SET models_json = ? WHERE id = ?`, string(raw), p.ID)
		return err
	})
	if err != nil {
		return ModelsOut{}, ops.AsError(err)
	}
	if out == nil {
		out = []ModelInfo{}
	}
	return ModelsOut{Items: out}, nil
}

func setModel(s *Service) func(*ops.Ctx, ModelSetIn) (ModelsOut, error) {
	return func(c *ops.Ctx, in ModelSetIn) (ModelsOut, error) {
		return editModels(s, c, in.ID, func(old []ModelInfo) []ModelInfo {
			m := ModelInfo{Model: lib.Model{ID: in.Model, Label: in.Label, ContextWindow: in.ContextWindow, MaxOutput: in.MaxOutput}, Manual: true}
			for i, x := range old {
				if x.ID == in.Model {
					m.InputMicros, m.OutputMicros = x.InputMicros, x.OutputMicros
					old[i] = m
					return old
				}
			}
			return append(old, m)
		})
	}
}

func removeModel(s *Service) func(*ops.Ctx, ModelRemoveIn) (ModelsOut, error) {
	return func(c *ops.Ctx, in ModelRemoveIn) (ModelsOut, error) {
		return editModels(s, c, in.ID, func(old []ModelInfo) []ModelInfo {
			var out []ModelInfo
			for _, x := range old {
				if x.ID != in.Model {
					out = append(out, x)
				}
			}
			return out
		})
	}
}

// ---- test ---------------------------------------------------------------------------------------------

// TestOut is the result of testing a provider. A failure is a result, not an
// error: the person is told in plain words what went wrong.
type TestOut struct {
	OK         bool   `json:"ok"`
	Class      string `json:"class,omitempty" desc:"blocked, unreachable, timeout, auth, credit, rate, unavailable, not_found, rejected, bad_response, too_large."`
	Message    string `json:"message" desc:"A plain sentence. It never contains an address, a key or the provider's own error text."`
	Status     int    `json:"status,omitempty" desc:"The HTTP status the provider answered with, when it answered."`
	Models     int    `json:"models,omitempty" desc:"How many models the provider listed."`
	LatencyMS  int64  `json:"latency_ms"`
	Local      bool   `json:"local" desc:"The provider is declared local."`
	CanListing bool   `json:"model_listing" desc:"The provider has a model list (otherwise enter models by hand)."`
}

func testProvider(s *Service) func(*ops.Ctx, ProviderIn) (TestOut, error) {
	return func(c *ops.Ctx, in ProviderIn) (TestOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return TestOut{}, err
		}
		p, err := providerByID(c, c.DB().Read(), c.P.UserID, in.ID)
		if err != nil {
			return TestOut{}, err
		}
		if !p.editableBy(c.P) {
			return TestOut{}, ops.Forbidden("Only an administrator can test an instance provider.")
		}
		if e := s.allow("test:"+c.P.UserID, 10); e != nil {
			return TestOut{}, e
		}
		cl, e := s.client(p, false)
		if e != nil {
			return TestOut{}, e
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(c, 20*time.Second)
		defer cancel()
		out := TestOut{Local: p.Local}
		models, merr := cl.Models(ctx)
		if merr == nil {
			out.OK, out.Models, out.CanListing = true, len(models), true
			out.Message = "The provider answered and listed " + strconv.Itoa(len(models)) + " models."
		} else if pe, ok := lib.AsProviderError(merr); ok && pe.Class == lib.ClassNotFound && p.DefaultModel != "" {
			// No model list (a plain OpenAI-compatible server): ask a tiny question instead.
			body, eerr := cl.Encode(p.DefaultModel, lib.Payload{Messages: []lib.Message{{Role: lib.RoleUser, Content: "Reply with the single word: ok"}}, MaxTokens: 8}, false)
			if eerr == nil {
				if _, cerr := cl.Chat(ctx, body); cerr == nil {
					out.OK, out.Message = true, "The provider answered a small test request. It has no model list: enter the models by hand."
				} else {
					merr = cerr
				}
			}
		}
		out.LatencyMS = time.Since(start).Milliseconds()
		if !out.OK {
			pe, ok := lib.AsProviderError(merr)
			if !ok {
				pe = &lib.ProviderError{Class: lib.ClassUnreachable}
			}
			s.logFailure(p, "test", pe)
			out.Class, out.Message, out.Status = pe.Class, pe.Message(), pe.Status
			if c.P.Role != ops.RoleAdmin {
				// To a person who is not an administrator a test is an oracle on the server's network:
				// blocked, refused and unreachable would tell them what is where. They get "it failed".
				out.Class, out.Status, out.LatencyMS = "", 0, 0 // the time to fail is a signal too
				out.Message = "The provider could not be used. Check its address and key, or ask an administrator."
			}
			if pe.Class == lib.ClassNotFound && p.DefaultModel == "" && c.P.Role == ops.RoleAdmin {
				out.Message = "The provider has no model list at this address. Check the address, or set a default model and test again."
			}
		}
		return out, nil
	}
}

// providerFailure turns a provider error into the op error a caller sees:
// generic, with the detail in the log.
func (s *Service) providerFailure(who ops.Principal, p provider, what string, err error) *ops.Error {
	pe, ok := lib.AsProviderError(err)
	if !ok {
		pe = &lib.ProviderError{Class: lib.ClassUnreachable}
	}
	s.logFailure(p, what, pe)
	if who.Role != ops.RoleAdmin { // see ai.provider.test: the class and status would map the network
		return &ops.Error{Code: ops.CodeProvider, Message: "The provider could not be used. Check its address and key, or ask an administrator."}
	}
	e := &ops.Error{Code: ops.CodeProvider, Message: pe.Message(), Data: map[string]any{"provider_class": pe.Class}}
	if pe.Status > 0 {
		e.Data["status"] = pe.Status
	}
	if pe.Class == lib.ClassRate && pe.RetryAfter > 0 {
		e.Data["retry_after"] = pe.RetryAfter
	}
	return e
}

func (s *Service) logFailure(p provider, what string, pe *lib.ProviderError) {
	// The class and the status, never the provider's own words: an error message can echo
	// part of the request, and the request is the manuscript.
	s.log.Warn("ai provider call failed", "provider", p.ID, "kind", p.Kind, "what", what, "class", pe.Class, "status", pe.Status)
}

// classify is the address classifier, with the test seam the guard carries.
func (s *Service) classify(ip net.IP) netguard.Class {
	if s.guard.Classify != nil {
		return s.guard.Classify(ip)
	}
	return netguard.Classify(ip)
}
