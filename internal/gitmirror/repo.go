package gitmirror

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// Publish a project to a GitHub or Forgejo repository (F-368), in one reviewable flow:
//
//	repo.account.connect  a person's token at a host, verified and stored sealed (write-only)
//	repo.list             the repositories that token can write to
//	repo.plan             a dry run: what would be created, which files would be pushed and which are withheld,
//	                      the visibility, anything that must not go public. It makes read requests only.
//	repo.publish          creates the repository (or takes an empty one), registers it as a remote of the
//	                      project and queues the first push through the engine. It is bound to the plan by a
//	                      token, needs the repository's name typed for a public repository, and is for the
//	                      owner, in a browser session: an API key of any scope is refused.
//
// The token is the person's own: it is never used for anyone else, and a remote made here keeps a sealed copy
// of its own, so disconnecting the account does not break it.

const maxAccountsPerUser = 5

// Account is a connected account as ops return it. The token is never returned.
type Account struct {
	ID         string `json:"id"`
	Provider   string `json:"provider" enum:"github,forgejo"`
	Host       string `json:"host" desc:"The web host: github.com, or your Forgejo or Gitea host."`
	Login      string `json:"login" desc:"The account the token belongs to, as the host reported it."`
	Label      string `json:"label,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// AccountList lists a person's connected accounts.
type AccountList struct {
	Items []Account `json:"items"`
}

// AccountConnectIn connects an account.
type AccountConnectIn struct {
	Provider string `json:"provider" enum:"github,forgejo" desc:"github, or forgejo (Gitea speaks the same API)."`
	Host     string `json:"host,omitempty" desc:"github.com by default for GitHub (or a GitHub Enterprise Server host); required for Forgejo: the host, like git.example.org or git.example.org:3000, without https://."`
	Token    string `json:"token" desc:"A personal access token. GitHub: a fine-grained token with Administration (to create) and Contents (to push) on the repositories, or a classic one with repo. Forgejo and Gitea: an access token with repository write. Write-only: stored encrypted, never returned."`
	Label    string `json:"label,omitempty" desc:"A name for the account in lists."`
}

// AccountRef names a connected account.
type AccountRef struct {
	AccountID string `query:"account_id" json:"-" desc:"The connected account."`
}

// RepoRef says which repository.
type RepoRef struct {
	Provider   string `json:"provider" enum:"github,forgejo"`
	Host       string `json:"host"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	FullName   string `json:"full_name"`
	Visibility string `json:"visibility" enum:"private,public"`
	URL        string `json:"url" desc:"The address of the repository's Git endpoint, as the remote will hold it."`
	WebURL     string `json:"web_url" desc:"The repository's page at the host."`
}

// RepoListIn lists the repositories an account can write to.
type RepoListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	AccountID string `json:"account_id" desc:"The connected account."`
	Page      int    `json:"page,omitempty" desc:"The host's page, from 1. A page holds up to 50 repositories."`
	Query     string `json:"q,omitempty" desc:"Keep only repositories whose name contains this."`
}

func (in RepoListIn) ProjectRef() string { return in.ProjectID }

// RepoListItem is one repository of a list.
type RepoListItem struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Empty         bool   `json:"empty" desc:"The host says it has no content. repo.plan checks for sure."`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// RepoList is a page of repositories.
type RepoList struct {
	Account  Account        `json:"account"`
	Items    []RepoListItem `json:"items"`
	NextPage int            `json:"next_page,omitempty" desc:"Pass as page for the next one."`
}

// repoInput is what repo.plan and repo.publish both take.
type repoInput struct {
	ProjectID, AccountID, Mode, Owner, Name, Description, Visibility string
	Branch, PathPrefix, Audience, PushMode                           string
	OnlyDone, SignCommits, IncludeNotes, IncludeCover                bool
}

// RepoPlanIn asks what publishing would do.
type RepoPlanIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	AccountID    string `json:"account_id" desc:"The connected account (repo.account.list)."`
	Mode         string `json:"mode,omitempty" enum:"create,existing" desc:"create (the default) makes a new repository and refuses a name that exists; existing takes a repository that is empty."`
	Owner        string `json:"owner,omitempty" desc:"The user or organization the repository belongs to. Default: the account itself."`
	Name         string `json:"name" desc:"The repository's name."`
	Description  string `json:"description,omitempty" desc:"For a new repository, up to 350 characters."`
	Visibility   string `json:"visibility,omitempty" enum:"private,public" desc:"For a new repository. Default private. public needs the repository's name typed in repo.publish's confirm."`
	Branch       string `json:"branch,omitempty" desc:"Default main."`
	PathPrefix   string `json:"path_prefix,omitempty" desc:"Place the files in this subdirectory of the repository."`
	Audience     string `json:"audience,omitempty" enum:"public,private" desc:"public (the default, the strictest): only what a reader of the book sees. private: the full mirror; refused for a public repository."`
	OnlyDone     bool   `json:"only_done,omitempty" desc:"Public audience: only documents whose status is done."`
	IncludeNotes bool   `json:"include_notes,omitempty" desc:"Private audience only: push the project's notes too."`
	IncludeCover bool   `json:"include_cover,omitempty" desc:"Public audience: push the project's own cover (the layout and its pictures) too; the plan lists its files. A private audience always carries it."`
	SignCommits  bool   `json:"sign_commits,omitempty" desc:"Sign the commits with the project's signing key."`
	PushMode     string `json:"push_mode,omitempty" enum:"manual,on_save,interval" desc:"How the remote pushes after the first push. Default manual."`
}

func (in RepoPlanIn) ProjectRef() string { return in.ProjectID }

func (in RepoPlanIn) input() repoInput {
	return repoInput{ProjectID: in.ProjectID, AccountID: in.AccountID, Mode: in.Mode, Owner: in.Owner, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, Branch: in.Branch, PathPrefix: in.PathPrefix, Audience: in.Audience, PushMode: in.PushMode,
		OnlyDone: in.OnlyDone, SignCommits: in.SignCommits, IncludeNotes: in.IncludeNotes, IncludeCover: in.IncludeCover}
}

// RepoPublishIn publishes. It carries the plan's fields, the plan's token and the confirmation.
type RepoPublishIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	AccountID    string `json:"account_id"`
	Mode         string `json:"mode,omitempty" enum:"create,existing"`
	Owner        string `json:"owner,omitempty"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Visibility   string `json:"visibility,omitempty" enum:"private,public"`
	Branch       string `json:"branch,omitempty"`
	PathPrefix   string `json:"path_prefix,omitempty"`
	Audience     string `json:"audience,omitempty" enum:"public,private"`
	OnlyDone     bool   `json:"only_done,omitempty"`
	IncludeNotes bool   `json:"include_notes,omitempty"`
	IncludeCover bool   `json:"include_cover,omitempty"`
	SignCommits  bool   `json:"sign_commits,omitempty"`
	PushMode     string `json:"push_mode,omitempty" enum:"manual,on_save,interval"`
	PlanToken    string `json:"plan_token" desc:"The token repo.plan returned for exactly these fields. The call refuses (409 plan_changed) when anything the plan covered is different now: the settings, the files that would be pushed, what is withheld."`
	Confirm      string `json:"confirm,omitempty" desc:"For a public repository, or a public audience with include_cover: the repository's name, typed exactly."`
}

func (in RepoPublishIn) ProjectRef() string { return in.ProjectID }

func (in RepoPublishIn) input() repoInput {
	return RepoPlanIn{ProjectID: in.ProjectID, AccountID: in.AccountID, Mode: in.Mode, Owner: in.Owner, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, Branch: in.Branch, PathPrefix: in.PathPrefix, Audience: in.Audience, OnlyDone: in.OnlyDone,
		IncludeNotes: in.IncludeNotes, IncludeCover: in.IncludeCover, SignCommits: in.SignCommits, PushMode: in.PushMode}.input()
}

// Blocker is something that stops a publish.
type Blocker struct {
	Code    string `json:"code" desc:"name_taken, repo_not_found, repo_not_empty, read_only, archived, public_full_mirror, secret_in_tree, mirror_not_ready, too_many_remotes, no_signing_key, no_secret_key, nothing_to_push, remote_differs."`
	Message string `json:"message"`
}

// PlanFile is a file the push would hold.
type PlanFile struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// RepoPlan is what repo.plan returns.
type RepoPlan struct {
	Account      Account             `json:"account"`
	Repository   RepoRef             `json:"repository"`
	Exists       bool                `json:"exists" desc:"The repository exists now."`
	WillCreate   bool                `json:"will_create"`
	Empty        bool                `json:"empty" desc:"The repository has no refs at all."`
	Branch       string              `json:"branch"`
	PathPrefix   string              `json:"path_prefix,omitempty"`
	Audience     string              `json:"audience" enum:"public,private"`
	OnlyDone     bool                `json:"only_done"`
	IncludeNotes bool                `json:"include_notes"`
	IncludeCover bool                `json:"include_cover" desc:"The first push carries the project's own cover."`
	SignCommits  bool                `json:"sign_commits"`
	PushMode     string              `json:"push_mode"`
	RemoteName   string              `json:"remote_name" desc:"The name the remote will have in the project."`
	Registered   bool                `json:"already_registered" desc:"The project already has a remote at this address: publish reuses it and pushes."`
	Requests     []string            `json:"requests" desc:"What publishing will do, in order: create_repository, register_remote, push."`
	FileCount    int                 `json:"file_count"`
	TotalBytes   int64               `json:"total_bytes"`
	Files        []PlanFile          `json:"files" desc:"The files the first push would hold (at most 300 are listed; file_count is all)."`
	Withheld     []Withheld          `json:"withheld" desc:"Documents a public remote does not carry, and why."`
	Findings     []SecretFindingView `json:"findings" desc:"Anything that looks like a secret in what would be pushed; each is a blocker."`
	Blockers     []Blocker           `json:"blockers"`
	NeedsConfirm bool                `json:"needs_confirm" desc:"The repository is public, or a public audience carries the cover: repo.publish needs confirm, the name typed exactly."`
	ConfirmWith  string              `json:"confirm_with,omitempty"`
	PlanToken    string              `json:"plan_token" desc:"Give it to repo.publish with the same fields."`
}

// RepoPublishOut is what publishing did.
type RepoPublishOut struct {
	Repository RepoRef `json:"repository"`
	Created    bool    `json:"created" desc:"The repository was created by this call."`
	Remote     Remote  `json:"remote"`
	Push       Push    `json:"push" desc:"The queued first push; read git.push_get for its outcome."`
}

func registerRepo(r *ops.Registry) {
	session := ops.Authenticated().ForSessions()
	owner := ops.RequireProject(ops.ProjectOwner).ForSessions()
	ops.Register(r, ops.Op{Name: "repo.account.list", Method: "GET", Path: "/api/v1/git/accounts",
		Desc: "Your connected accounts at code hosts (GitHub, Forgejo, Gitea): host and login, never the token. For a browser session only.",
		Auth: session}, listAccounts)
	ops.Register(r, ops.Op{NoDryRun: true, SecretInput: true, Name: "repo.account.connect", Method: "POST", Path: "/api/v1/git/accounts", Status: 201,
		Desc: "Connect your account at GitHub or a Forgejo or Gitea host with a personal access token. The token is checked with the host (one request to its API), stored encrypted, bound to that host, and never returned. It is yours: no one else, an administrator included, can use it. For a browser session only. Needs the server's secret key.",
		Auth: session}, connectAccount)
	ops.Register(r, ops.Op{NoDryRun: true, Permanent: true, Name: "repo.account.disconnect", Method: "DELETE", Path: "/api/v1/git/accounts",
		Desc: "Forget a connected account and its stored token. Remotes made with it keep their own copy of the token and are not touched. For a browser session only.",
		Auth: session}, disconnectAccount)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "repo.list", Method: "POST", Path: "/api/v1/projects/{id}/repo/list",
		Desc: "The repositories your connected account can write to, one page of up to 50 from the host. A POST because it makes a request to the host with your token, which a project in the trash may not. Owner only, browser session only.",
		Auth: owner}, listRepos)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "repo.plan", Method: "POST", Path: "/api/v1/projects/{id}/repo/plan", Scope: ops.ScopeWrite,
		Desc: "A dry run of publishing the project to a repository: what will be created, the visibility, which files the first push holds (count, size, paths), which documents a public remote withholds and why, anything that looks like a secret, and whether a public repository needs the name typed. Reads from the host (does the repository exist, is it empty); changes nothing, there or here. Returns the plan_token repo.publish needs. Owner only, browser session only.",
		Auth: owner}, planRepo)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "repo.publish", Method: "POST", Path: "/api/v1/projects/{id}/repo/publish", Status: 201,
		Desc: "Publish the project to a repository: create it (or take an empty one), register it as a remote of the project and queue the first push. Needs the plan_token of repo.plan for the same fields, and for a public repository its name typed in confirm. Owner only, browser session only: an API key of any scope is refused. If the repository was created and a later step failed, the error says so and the call can be repeated with mode existing.",
		Auth: owner}, publishRepo)
}

// ---- accounts ----------------------------------------------------------------------

func accountContext(id string, t providerTarget) string {
	return "git_accounts/" + id + "/token/" + t.api
}

func (s *Service) accountView(a accountRow) Account {
	return Account{ID: a.ID, Provider: a.Provider, Host: a.Host, Login: a.Login, Label: a.Label, CreatedAt: a.CreatedAt, LastUsedAt: a.LastUsedAt}
}

type accountRow struct {
	ID, UserID, Provider, Host, Login, Label, Sealed, CreatedAt, LastUsedAt string
}

func (s *Service) accountsOf(ctx context.Context, userID string) ([]accountRow, error) {
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT id, user_id, provider, host, login, label, token_sealed, created_at, COALESCE(last_used_at,'') FROM git_accounts WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []accountRow
	for rows.Next() {
		var a accountRow
		if err := rows.Scan(&a.ID, &a.UserID, &a.Provider, &a.Host, &a.Login, &a.Label, &a.Sealed, &a.CreatedAt, &a.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// accountFor finds one of the caller's own accounts. Anyone else's, or none, is the same 404.
func (s *Service) accountFor(ctx context.Context, userID, id string) (accountRow, error) {
	if userID == "" || id == "" {
		return accountRow{}, ops.Invalid("account_id", "Name the connected account with account_id.")
	}
	var a accountRow
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT id, user_id, provider, host, login, label, token_sealed, created_at, COALESCE(last_used_at,'') FROM git_accounts WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&a.ID, &a.UserID, &a.Provider, &a.Host, &a.Login, &a.Label, &a.Sealed, &a.CreatedAt, &a.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return accountRow{}, ops.NotFound("account")
	}
	if err != nil {
		return accountRow{}, ops.Internal(err)
	}
	return a, nil
}

// openAccount opens the token of an account for one flow, and gives a client for its host.
func (s *Service) openAccount(ctx context.Context, userID, id string) (accountRow, providerTarget, string, error) {
	a, err := s.accountFor(ctx, userID, id)
	if err != nil {
		return a, providerTarget{}, "", err
	}
	t, e := newTarget(a.Provider, a.Host)
	if e != nil {
		return a, t, "", failure("", e)
	}
	plain, err := s.o.Box.Open(a.Sealed, accountContext(a.ID, t))
	if err != nil {
		return a, t, "", ops.Conflict("The stored token cannot be read with this installation's secret key. Disconnect the account and connect it again.")
	}
	defer clear(plain)
	return a, t, string(plain), nil
}

func listAccounts(c *ops.Ctx, _ struct{}) (AccountList, error) {
	s, err := service(c)
	if err != nil {
		return AccountList{}, err
	}
	rows, err := s.accountsOf(c, c.P.UserID)
	if err != nil {
		return AccountList{}, ops.Internal(err)
	}
	out := AccountList{Items: make([]Account, 0, len(rows))}
	for _, a := range rows {
		out.Items = append(out.Items, s.accountView(a))
	}
	return out, nil
}

func connectAccount(c *ops.Ctx, in AccountConnectIn) (Account, error) {
	s, err := service(c)
	if err != nil {
		return Account{}, err
	}
	if !s.Enabled() {
		return Account{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	t, e := newTarget(in.Provider, in.Host)
	if e != nil {
		return Account{}, failure("host", e)
	}
	tok, err := checkToken(in.Token)
	if err != nil {
		return Account{}, err
	}
	label := strings.TrimSpace(in.Label)
	if utf8.RuneCountInString(label) > 80 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return Account{}, ops.Invalid("label", "The label is too long or has control characters.")
	}
	existing, err := s.accountsOf(c, c.P.UserID)
	if err != nil {
		return Account{}, ops.Internal(err)
	}
	login, e := newProviderClient(t, tok).user(c)
	if e != nil {
		// One answer for every way this can fail (the address is not allowed, nothing listens, the handshake fails, the
		// host is not an API, the token is refused, the host is slow): the caller picks the host and port, and a
		// message that told them which would let any signed-in person map the services behind the server. The detail is
		// in the server log at debug level. (How long it took is the one thing still visible.)
		s.log.Debug("git: could not connect an account", "host", t.host, "code", e.Code)
		return Account{}, ops.Invalid("host", "The account could not be connected: the host could not be reached or did not accept the token. Check the host name and the token.")
	}
	id, replacing := db.NewID("gac"), false
	for _, a := range existing {
		if a.Provider == t.kind && a.Host == t.host && strings.EqualFold(a.Login, login) {
			id, replacing = a.ID, true
		}
	}
	if !replacing && len(existing) >= maxAccountsPerUser {
		return Account{}, ops.Conflict(fmt.Sprintf("You can connect up to %d accounts. Disconnect one first.", maxAccountsPerUser))
	}
	sealed, err := s.o.Box.Seal([]byte(tok), accountContext(id, t))
	if err != nil {
		return Account{}, ops.Internal(err)
	}
	now := stamp(s.o.Clock.Now())
	if replacing {
		_, err = s.o.DB.Writer().ExecContext(c, `UPDATE git_accounts SET token_sealed = ?, label = CASE WHEN ? <> '' THEN ? ELSE label END WHERE id = ? AND user_id = ?`, sealed, label, label, id, c.P.UserID)
	} else {
		_, err = s.o.DB.Writer().ExecContext(c, `INSERT INTO git_accounts(id, user_id, provider, host, login, label, token_sealed, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			id, c.P.UserID, t.kind, t.host, login, label, sealed, now)
	}
	if err != nil {
		return Account{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "repo.account.connect", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"account_id": id, "provider": t.kind, "host": t.host, "replaced": replacing}})
	a, err := s.accountFor(c, c.P.UserID, id)
	if err != nil {
		return Account{}, err
	}
	return s.accountView(a), nil
}

func disconnectAccount(c *ops.Ctx, in AccountRef) (struct {
	Deleted bool `json:"deleted"`
}, error) {
	type out = struct {
		Deleted bool `json:"deleted"`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	a, err := s.accountFor(c, c.P.UserID, in.AccountID)
	if err != nil {
		return out{}, err
	}
	if _, err := s.o.DB.Writer().ExecContext(c, `DELETE FROM git_accounts WHERE id = ? AND user_id = ?`, a.ID, c.P.UserID); err != nil {
		return out{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "repo.account.disconnect", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"account_id": a.ID, "provider": a.Provider, "host": a.Host}})
	return out{Deleted: true}, nil
}

// ---- listing ------------------------------------------------------------------------

func listRepos(c *ops.Ctx, in RepoListIn) (RepoList, error) {
	s, err := service(c)
	if err != nil {
		return RepoList{}, err
	}
	a, t, tok, err := s.openAccount(c, c.P.UserID, in.AccountID)
	if err != nil {
		return RepoList{}, err
	}
	page := in.Page
	if page < 1 {
		page = 1
	}
	if page > 200 {
		return RepoList{}, ops.Invalid("page", "That page is out of range.")
	}
	repos, more, e := newProviderClient(t, tok).listRepos(c, page)
	if e != nil {
		return RepoList{}, failure("", e)
	}
	_, _ = s.o.DB.Writer().ExecContext(c, `UPDATE git_accounts SET last_used_at = ? WHERE id = ?`, stamp(s.o.Clock.Now()), a.ID)
	q := strings.ToLower(strings.TrimSpace(in.Query))
	out := RepoList{Account: s.accountView(a), Items: []RepoListItem{}}
	for _, r := range repos {
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) {
			continue
		}
		out.Items = append(out.Items, RepoListItem{Owner: r.Owner.Login, Name: r.Name, FullName: r.Owner.Login + "/" + r.Name, Private: r.Private, Archived: r.Archived,
			Fork: r.Fork, Empty: r.Empty || r.Size == 0, DefaultBranch: cleanDefaultBranch(r.DefaultBranch)})
	}
	if more {
		out.NextPage = page + 1
	}
	return out, nil
}

func cleanDefaultBranch(b string) string {
	if v, e := cleanBranch(b); e == nil {
		return v
	}
	return ""
}

// ---- the plan -----------------------------------------------------------------------

type planned struct {
	plan    RepoPlan
	client  *providerClient
	target  providerTarget
	token   string
	account accountRow
	in      repoInput
	desc    string
	snap    *snapshot
	sameRem *remoteRow
	digest  string
}

func checkDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > 350 || strings.IndexFunc(d, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return "", ops.Invalid("description", "The description is longer than 350 characters or has control characters.")
	}
	return strings.Join(strings.Fields(d), " "), nil
}

// buildPlan validates the input, asks the host what it needs to know (reads only) and works out what a publish
// would do. It writes nothing. The returned plan's blockers say what stops it.
func (s *Service) buildPlan(ctx context.Context, userID string, in repoInput) (*planned, error) {
	p := &planned{in: in}
	if in.Mode == "" {
		in.Mode = "create"
	}
	if in.Mode != "create" && in.Mode != "existing" {
		return nil, ops.Invalid("mode", "mode is create or existing.")
	}
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	if in.Visibility != "private" && in.Visibility != "public" {
		return nil, ops.Invalid("visibility", "visibility is private or public.")
	}
	if !validRepoName(in.Name) {
		return nil, ops.Invalid("name", "A repository name has letters, digits, dots, dashes and underscores, and is at most 100 characters.")
	}
	if in.Owner != "" && !validOwner(in.Owner) {
		return nil, ops.Invalid("owner", "That is not a user or organization name.")
	}
	branch, e := cleanBranch(in.Branch)
	if e != nil {
		return nil, failure("branch", e)
	}
	prefix, e := cleanPrefix(in.PathPrefix)
	if e != nil {
		return nil, failure("path_prefix", e)
	}
	desc, err := checkDescription(in.Description)
	if err != nil {
		return nil, err
	}
	audience := in.Audience
	switch audience {
	case "":
		audience = AudiencePublic
	case AudiencePublic, AudiencePrivate:
	default:
		return nil, ops.Invalid("audience", "audience is public or private.")
	}
	if audience == AudiencePublic && in.IncludeNotes {
		return nil, ops.Invalid("include_notes", "A public remote never carries notes. Choose the private audience to push them.")
	}
	push := in.PushMode
	if push == "" {
		push = ModeManual
	}
	if push != ModeManual && push != ModeOnSave && push != ModeInterval {
		return nil, ops.Invalid("push_mode", "push_mode is manual, on_save or interval.")
	}
	if audience == AudiencePrivate {
		in.IncludeCover = true // the whole copy, cover included
	}
	in.Branch, in.PathPrefix, in.Audience, in.PushMode = branch, prefix, audience, push
	p.in, p.desc = in, desc

	a, t, tok, err := s.openAccount(ctx, userID, in.AccountID)
	if err != nil {
		return nil, err
	}
	p.account, p.target, p.token = a, t, tok
	p.client = newProviderClient(t, tok)
	owner := in.Owner
	if owner == "" {
		owner = a.Login
	}
	pl := &p.plan
	pl.Account = s.accountView(a)
	pl.Branch, pl.PathPrefix, pl.Audience, pl.OnlyDone, pl.IncludeNotes, pl.SignCommits, pl.PushMode = branch, prefix, audience, in.OnlyDone, in.IncludeNotes, in.SignCommits, push
	pl.IncludeCover = in.IncludeCover
	pl.Files, pl.Withheld, pl.Findings, pl.Blockers, pl.Requests = []PlanFile{}, []Withheld{}, []SecretFindingView{}, []Blocker{}, []string{}
	block := func(code, msg string) { pl.Blockers = append(pl.Blockers, Blocker{Code: code, Message: msg}) }
	pl.Repository = RepoRef{Provider: t.kind, Host: t.host, Owner: owner, Name: in.Name, FullName: owner + "/" + in.Name, Visibility: in.Visibility,
		URL: t.repoURL(owner, in.Name), WebURL: "https://" + t.host + "/" + owner + "/" + in.Name}
	pl.RemoteName = s.remoteNameFor(t, owner, in.Name)

	if !s.Enabled() {
		block("no_secret_key", secrets.ErrNoKey.Error())
	}

	// What the host says (reads only).
	info, exists, he := p.client.repo(ctx, owner, in.Name)
	if he != nil {
		return nil, failure("", he)
	}
	pl.Exists = exists
	switch {
	case in.Mode == "create" && exists:
		block("name_taken", fmt.Sprintf("%s/%s already exists at %s. Choose another name, or choose an empty repository with mode existing.", owner, in.Name, t.host))
	case in.Mode == "create":
		pl.WillCreate = true
		pl.Empty = true
		pl.Requests = append(pl.Requests, "create_repository")
	case !exists:
		block("repo_not_found", fmt.Sprintf("%s/%s does not exist at %s, or the token has no access to it.", owner, in.Name, t.host))
	default:
		pl.Repository.Visibility = "private"
		if !info.Private {
			pl.Repository.Visibility = "public"
		}
		switch {
		case info.Archived:
			block("archived", "That repository is archived: nothing can be pushed to it.")
		case !(info.Permissions.Push || info.Permissions.Admin):
			block("read_only", "The token cannot push to that repository.")
		default:
			cr := &credentials{url: pl.Repository.URL, host: t.host, keyName: a.Login, auth: &githttp.BasicAuth{Username: a.Login, Password: tok}, scrub: []string{tok}}
			_, has, empty, herr := remoteHead(ctx, cr.url, cr, branch)
			switch {
			case herr != nil:
				return nil, failure("", classify(herr, t.host, a.Login, tok))
			case empty:
				pl.Empty = true
			default:
				_ = has
				block("repo_not_empty", "That repository already has content. Scríobh only pushes: it never merges with what is there. Choose an empty repository, or create a new one.")
			}
		}
	}
	if pl.Repository.Visibility == "public" && audience == AudiencePrivate {
		block("public_full_mirror", "A public repository cannot hold the full mirror (notes, alternatives, outline details). Use the public audience, or a private repository.")
	}
	if audience == AudiencePrivate && in.OnlyDone {
		block("public_full_mirror", "only_done is for the public audience.")
	}
	// The repository's name typed: for a public repository, and for a public audience that carries the cover, which lets more
	// reach a repository people can read (F-861), whatever the repository's visibility is now.
	pl.NeedsConfirm = pl.Repository.Visibility == "public" || (audience == AudiencePublic && in.IncludeCover)
	if pl.NeedsConfirm {
		pl.ConfirmWith = in.Name
	}

	// What is here: remotes, the signing key, the files.
	remotes, rerr := s.remotesOf(ctx, in.ProjectID)
	if rerr != nil {
		return nil, ops.Internal(rerr)
	}
	ep, _ := parseURL(pl.Repository.URL, false)
	for _, r := range remotes {
		if rep, e := parseURL(r.URL, true); e == nil && addrIdentity(rep) == addrIdentity(ep) {
			p.sameRem, pl.Registered = r, true
		}
	}
	if sr := p.sameRem; sr != nil {
		// A remote already at this address is reused, but only if it carries what this plan says it carries: a
		// private remote on a repository that is about to be public would push the full mirror there.
		if sr.Audience != audience || sr.OnlyDone != in.OnlyDone || sr.Branch != branch || sr.PathPrefix != prefix || sr.IncludeNotes != (audience == AudiencePrivate && in.IncludeNotes) || sr.IncludeCover != in.IncludeCover {
			block("remote_differs", "The project already has a remote at this address (\""+sr.Name+"\") with other settings than this plan: what it carries, its branch or its folder. Change that remote's settings, or remove it, then plan again.")
		}
	}
	if !pl.Registered {
		pl.Requests = append(pl.Requests, "register_remote")
		if len(remotes) >= maxRemotesPerProject {
			block("too_many_remotes", fmt.Sprintf("The project already has %d remotes, the most it can have. Remove one first.", maxRemotesPerProject))
		}
		for _, r := range remotes {
			if strings.EqualFold(r.Name, pl.RemoteName) {
				pl.RemoteName = uniqueRemoteName(remotes, pl.RemoteName)
				break
			}
		}
	}
	pl.Requests = append(pl.Requests, "push")
	if in.SignCommits {
		if _, ok := s.signingKey(ctx, in.ProjectID); !ok {
			block("no_signing_key", "Generate the project's signing key first (git.signing.generate), or switch signing off.")
		}
	}
	rr := &remoteRow{ProjectID: in.ProjectID, Audience: audience, OnlyDone: in.OnlyDone, PathPrefix: prefix, IncludeNotes: audience == AudiencePrivate && in.IncludeNotes, IncludeCover: in.IncludeCover}
	sn, withheld, se := s.snapshotFor(ctx, rr)
	if se != nil {
		block("mirror_not_ready", se.Message)
	} else {
		p.snap = sn
		pl.Withheld = append(pl.Withheld, withheld...)
		pl.FileCount = len(sn.files)
		paths := make([]string, 0, len(sn.files))
		for path, b := range sn.files {
			pl.TotalBytes += int64(len(b))
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for i, path := range paths {
			if i >= 300 {
				break
			}
			pl.Files = append(pl.Files, PlanFile{Path: path, Bytes: len(sn.files[path])})
		}
		if len(sn.docs) == 0 {
			block("nothing_to_push", "Nothing in the project would be pushed: every document is left out of this audience, or the project is empty.")
		}
		for _, f := range append(scanForSecrets(sn.scannable()), scanMeta(sn)...) {
			pl.Findings = append(pl.Findings, SecretFindingView{Path: f.Path, Rule: f.Rule, SHA: f.SHA})
		}
		if len(pl.Findings) > 0 {
			block("secret_in_tree", "Something in what would be pushed looks like a secret. Change the text first: "+secretError(findingsOf(pl.Findings)).Message)
		}
	}
	p.digest = planToken(p)
	pl.PlanToken = s.sealPlan(userID, in.ProjectID, p.digest)
	return p, nil
}

func findingsOf(v []SecretFindingView) []SecretFinding {
	out := make([]SecretFinding, 0, len(v))
	for _, f := range v {
		out = append(out, SecretFinding{Path: f.Path, Rule: f.Rule, SHA: f.SHA})
	}
	return out
}

// planToken binds publish to the plan: the host, the repository and how it is made, the account, the rules, and
// the set of files and withheld documents. Not the files' content: the push is of the mirror when it runs, as
// any push is, and a person typing in the meantime does not void a plan; what the plan promised about what is
// left out, and which files go, is what is bound.
func planToken(p *planned) string {
	h := sha256.New()
	pl := &p.plan
	fmt.Fprintf(h, "v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%v\x00%v\x00%s\x00%s\x00%s\x00%v\x00%v\x00%v\x00%v\x00%s\n", p.target.kind, p.target.host, pl.Repository.FullName, p.in.Mode, pl.Repository.Visibility, pl.Branch,
		pl.OnlyDone, pl.IncludeNotes, pl.PathPrefix, pl.Audience, pl.PushMode, pl.SignCommits, pl.Exists, pl.Empty, pl.Registered, p.account.ID)
	fmt.Fprintf(h, "cover %v\n", pl.IncludeCover)
	var paths []string
	for _, f := range pl.Files {
		paths = append(paths, f.Path)
	}
	fmt.Fprintf(h, "files %d\n%s\n", pl.FileCount, strings.Join(paths, "\n"))
	var w []string
	for _, x := range pl.Withheld {
		w = append(w, x.Path+"|"+x.Reason)
	}
	sort.Strings(w)
	fmt.Fprintf(h, "withheld\n%s\n", strings.Join(w, "\n"))
	var b []string
	for _, x := range pl.Blockers {
		b = append(b, x.Code)
	}
	fmt.Fprintf(h, "blockers %s\n", strings.Join(b, ","))
	return hex.EncodeToString(h.Sum(nil))[:40]
}

// planTTL is how long a plan may be published: long enough to read it, short enough that a plan is a recent look.
const planTTL = 30 * time.Minute

// sealPlan makes the token a plan hands out: when it was made and what it covered, sealed with the instance key under
// a context that names the project and the person. repo.publish opens it with the same context, so a plan from
// another project, another person or another day does not open. (The plan is also recomputed at publish and must
// match; the token is the proof that this person looked at this plan recently, not the only check.)
func (s *Service) sealPlan(userID, projectID, digest string) string {
	tok, err := s.o.Box.Seal([]byte(strconv.FormatInt(s.o.Clock.Now().Unix(), 10)+"|"+digest), "repo.plan/"+projectID+"/"+userID)
	if err != nil {
		return ""
	}
	return tok
}

func (s *Service) planValid(token, userID, projectID, digest string) bool {
	plain, err := s.o.Box.Open(strings.TrimSpace(token), "repo.plan/"+projectID+"/"+userID)
	if err != nil {
		return false
	}
	at, rest, ok := strings.Cut(string(plain), "|")
	sec, perr := strconv.ParseInt(at, 10, 64)
	if !ok || perr != nil || rest != digest {
		return false
	}
	age := s.o.Clock.Now().Sub(time.Unix(sec, 0))
	return age >= -time.Minute && age <= planTTL
}

func (s *Service) remoteNameFor(t providerTarget, owner, name string) string {
	label := "GitHub"
	if t.kind == ProviderForgejo {
		label = "Forgejo"
	}
	n := label + ": " + owner + "/" + name
	if r := []rune(n); len(r) > maxNameRunes {
		n = string(r[:maxNameRunes])
	}
	return n
}

func uniqueRemoteName(existing []*remoteRow, base string) string {
	taken := map[string]bool{}
	for _, r := range existing {
		taken[strings.ToLower(r.Name)] = true
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		n := base
		if r := []rune(n); len(r) > maxNameRunes-len(suffix) {
			n = string(r[:maxNameRunes-len(suffix)])
		}
		if n += suffix; !taken[strings.ToLower(n)] {
			return n
		}
	}
}

func planRepo(c *ops.Ctx, in RepoPlanIn) (RepoPlan, error) {
	s, err := service(c)
	if err != nil {
		return RepoPlan{}, err
	}
	p, err := s.buildPlan(c, c.P.UserID, in.input())
	if err != nil {
		return RepoPlan{}, err
	}
	return p.plan, nil
}

// ---- publish ------------------------------------------------------------------------

func publishRepo(c *ops.Ctx, in RepoPublishIn) (RepoPublishOut, error) {
	s, err := service(c)
	if err != nil {
		return RepoPublishOut{}, err
	}
	p, err := s.buildPlan(c, c.P.UserID, in.input())
	if err != nil {
		return RepoPublishOut{}, err
	}
	pl := &p.plan
	if len(pl.Blockers) > 0 {
		codes := make([]string, 0, len(pl.Blockers))
		for _, b := range pl.Blockers {
			codes = append(codes, b.Code)
		}
		return RepoPublishOut{}, ops.ConflictWith(pl.Blockers[0].Message, map[string]any{"git_code": "plan_blocked", "blockers": codes})
	}
	if strings.TrimSpace(in.PlanToken) == "" {
		return RepoPublishOut{}, ops.Invalid("plan_token", "Run repo.plan first and send its plan_token.")
	}
	if !s.planValid(in.PlanToken, c.P.UserID, in.ProjectID, p.digest) {
		return RepoPublishOut{}, failure("", gerr(CodePlanChanged, "Something the plan covered is different now (the settings, the files that would be pushed, what is withheld, or the repository at the host). Run the plan again and review it."))
	}
	if pl.NeedsConfirm && in.Confirm != pl.ConfirmWith {
		msg := "This repository is public: anyone can read what is pushed. Type its name exactly to confirm: " + pl.ConfirmWith
		if pl.Repository.Visibility != "public" {
			msg = "This sends the book's cover to a remote that is meant to be read by anyone. Type the repository's name exactly to confirm: " + pl.ConfirmWith
		}
		return RepoPublishOut{}, ops.Invalid("confirm", msg)
	}
	owner := pl.Repository.Owner
	created := false
	if pl.WillCreate {
		if _, e := p.client.createRepo(c, p.account.Login, owner, in.Name, p.desc, pl.Repository.Visibility != "public"); e != nil {
			return RepoPublishOut{}, failure("", e)
		}
		created = true
		c.Audit(ops.AuditEntry{Action: "repo.publish.create", TargetType: "project", TargetID: in.ProjectID,
			Detail: map[string]any{"provider": p.target.kind, "host": p.target.host, "repository": pl.Repository.FullName, "visibility": in.Visibility, "audience": pl.Audience}})
		if h := afterCreateHook.Load(); h != nil {
			if herr := (*h)(); herr != nil {
				return RepoPublishOut{}, createdButIncomplete(pl, "connecting the project to it", false)
			}
		}
	}
	r, err := s.registerPublished(c, p)
	if err != nil {
		if created {
			return RepoPublishOut{}, createdButIncomplete(pl, "connecting the project to it", false)
		}
		return RepoPublishOut{}, err
	}
	s.recordPublic(c, r)
	row, already, err := s.enqueue(c, r, TriggerManual, c.P.UserID, false)
	if err != nil {
		if created {
			return RepoPublishOut{}, createdButIncomplete(pl, "queueing the first push", true)
		}
		return RepoPublishOut{}, ops.Internal(err)
	}
	_ = already
	_, _ = s.o.DB.Writer().ExecContext(c, `UPDATE git_accounts SET last_used_at = ? WHERE id = ?`, stamp(s.o.Clock.Now()), p.account.ID)
	c.Audit(ops.AuditEntry{Action: "repo.publish", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "provider": p.target.kind, "host": p.target.host, "repository": pl.Repository.FullName, "visibility": pl.Repository.Visibility,
			"audience": pl.Audience, "created": created, "files": pl.FileCount, "withheld": len(pl.Withheld)}})
	rr, err := s.loadRemote(c, in.ProjectID, r.ID)
	if err != nil {
		return RepoPublishOut{}, ops.Internal(err)
	}
	return RepoPublishOut{Repository: pl.Repository, Created: created, Remote: s.view(c, rr), Push: pushView(row, false)}, nil
}

// afterCreateHook is a test seam: an error here is a failure after the repository was created.
var afterCreateHook atomic.Pointer[func() error]

// createdButIncomplete is the answer when the repository exists but a later step failed: it says what was and was
// not changed, and how to go on.
func createdButIncomplete(pl *RepoPlan, step string, registered bool) error {
	state := "No remote was registered in this project."
	if registered {
		state = "The project's remote for it was registered, but no push was queued."
	}
	return ops.ConflictWith(fmt.Sprintf("The repository %s was created at %s (empty, and %s). The step that failed was %s. %s Nothing was pushed. Run the plan again with mode existing to carry on: it takes the empty repository and, if the remote is there, uses it.",
		pl.Repository.FullName, pl.Repository.Host, pl.Repository.Visibility, step, state),
		map[string]any{"git_code": CodePublishIncomplete, "created_repository": pl.Repository.FullName, "resume_mode": "existing", "step": step, "remote_registered": registered})
}

// registerPublished registers the repository as a remote of the project (or reuses the one already at that
// address), with its own sealed copy of the token.
func (s *Service) registerPublished(c *ops.Ctx, p *planned) (*remoteRow, error) {
	pl := &p.plan
	if p.sameRem != nil {
		return p.sameRem, nil
	}
	ep, e := parseURL(pl.Repository.URL, false)
	if e != nil {
		return nil, failure("name", e)
	}
	r := &remoteRow{ID: db.NewID("grm"), ProjectID: p.in.ProjectID, Name: pl.RemoteName, Preset: p.target.kind, URL: pl.Repository.URL, Branch: pl.Branch, PathPrefix: pl.PathPrefix,
		AuthKind: AuthHTTPS, Username: p.account.Login, PushMode: pl.PushMode, IntervalMinutes: defaultInterval, IncludeNotes: pl.Audience == AudiencePrivate && pl.IncludeNotes,
		Enabled: true, State: StateNew, CreatedAt: stamp(s.o.Clock.Now()), Audience: pl.Audience, OnlyDone: pl.OnlyDone, SignCommits: pl.SignCommits, CreatedVia: "publish", IncludeCover: pl.IncludeCover}
	var err error
	if r.SecretCipher, err = s.o.Box.Seal([]byte(p.token), sealContext(r.ID, ep)); err != nil {
		return nil, ops.Internal(err)
	}
	if err := s.insertRemote(c, r, c.P.UserID); err != nil {
		switch {
		case errors.Is(err, errTooManyRemotes):
			return nil, ops.Conflict(err.Error())
		case db.IsUniqueViolation(err):
			return nil, ops.Conflict("This project already has a remote with that name.")
		}
		return nil, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "git.remote.create", TargetType: "project", TargetID: p.in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "name": r.Name, "host": ep.Host, "auth_kind": AuthHTTPS, "push_mode": r.PushMode, "audience": r.Audience, "include_cover": r.IncludeCover, "via": "publish"}})
	return s.loadRemote(c, p.in.ProjectID, r.ID)
}
