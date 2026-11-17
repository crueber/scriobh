package gitmirror

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// The Git ops. Every one belongs to a project and needs at least the editor
// role; a force push needs the owner. Remote ids travel in the body or the
// query, never in the path (the path carries the project), and are optional
// while a project has exactly one remote. Credentials are write-only: no op
// returns one.

// ProjectIn names the project.
type ProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectIn) ProjectRef() string { return in.ProjectID }

// RemoteQuery names a project and, optionally, one of its remotes.
type RemoteQuery struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `query:"remote_id" json:"-" desc:"The remote. Optional while the project has exactly one."`
}

func (in RemoteQuery) ProjectRef() string { return in.ProjectID }

// RemoteBody is RemoteQuery for ops that take a body.
type RemoteBody struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `json:"remote_id,omitempty" desc:"The remote. Optional while the project has exactly one."`
}

func (in RemoteBody) ProjectRef() string { return in.ProjectID }

// RemoteItem wraps a remote.
type RemoteItem struct {
	Item Remote `json:"item"`
}

// RemoteList lists a project's remotes.
type RemoteList struct {
	Items         []Remote `json:"items"`
	Enabled       bool     `json:"git_enabled" desc:"False when the server has no secret key: remotes can be listed but no credential can be stored."`
	DisabledNotes string   `json:"git_disabled_reason,omitempty"`
}

// PresetList is the static preset list.
type PresetList struct {
	Items []Preset `json:"items"`
}

// CreateIn adds a remote.
type CreateIn struct {
	ProjectID       string `path:"id" json:"-" desc:"Project id."`
	Name            string `json:"name" desc:"What the remote is for, like Backup. Also the deploy key's name."`
	URL             string `json:"url" desc:"ssh://git@host/owner/repo.git, git@host:owner/repo.git or https://host/owner/repo.git. No credentials in the address."`
	Preset          string `json:"preset,omitempty" enum:"forgejo,github,custom" desc:"How the form was filled; informational."`
	Branch          string `json:"branch,omitempty" desc:"Default main."`
	PathPrefix      string `json:"path_prefix,omitempty" desc:"Place the files in this subdirectory of the repository."`
	AuthKind        string `json:"auth_kind,omitempty" enum:"ssh_deploy_key,https_token" desc:"Default: a deploy key for ssh addresses, a token for https."`
	Username        string `json:"username,omitempty" desc:"The user name that goes with an HTTPS token."`
	Token           string `json:"token,omitempty" desc:"HTTPS token. Write-only: stored encrypted, never returned."`
	PushMode        string `json:"push_mode,omitempty" enum:"on_save,interval,manual" desc:"Default on_save: a push 30 seconds after the writing stops."`
	IntervalMinutes int    `json:"interval_minutes,omitempty" desc:"For push_mode interval: 5 to 10080, default 60."`
	IncludeNotes    *bool  `json:"include_notes,omitempty" desc:"Push the project's notes too. Default true for a private remote; a public remote never carries notes."`
	AuthorName      string `json:"author_name,omitempty" desc:"Commit author name; with author_email. Default: Scríobh <noreply@scriobh>."`
	AuthorEmail     string `json:"author_email,omitempty"`
	Audience        string `json:"audience,omitempty" enum:"public,private" desc:"public (the default, the strictest): only what a reader of the book sees: no notes, no do-not-compile documents, no ghosted text, no alternatives, no private notes, no outline details. private: the full mirror, for a backup in a repository only you can read."`
	OnlyDone        *bool  `json:"only_done,omitempty" desc:"Public remote: carry only documents whose status is done."`
	SignCommits     *bool  `json:"sign_commits,omitempty" desc:"Sign this remote's commits with the project's signing key (git.signing.generate makes it)."`
	ConfirmName     string `json:"confirm_name,omitempty" desc:"Needed only for a private remote at an address that was ever public: the new remote's name, typed exactly. Refused with 409 confirmation_required (data.git_code) without it."`
}

func (in CreateIn) ProjectRef() string { return in.ProjectID }

// UpdateIn changes a remote. Only the fields sent change.
type UpdateIn struct {
	ProjectID       string  `path:"id" json:"-" desc:"Project id."`
	RemoteID        string  `json:"remote_id,omitempty" desc:"The remote. Optional while the project has exactly one."`
	Name            *string `json:"name,omitempty"`
	URL             *string `json:"url,omitempty" desc:"Changing the address resets what is known about the remote; a changed host also drops the accepted host key."`
	Branch          *string `json:"branch,omitempty"`
	PathPrefix      *string `json:"path_prefix,omitempty"`
	Username        *string `json:"username,omitempty"`
	Token           *string `json:"token,omitempty" desc:"A new HTTPS token. Write-only."`
	PushMode        *string `json:"push_mode,omitempty" enum:"on_save,interval,manual"`
	IntervalMinutes *int    `json:"interval_minutes,omitempty"`
	IncludeNotes    *bool   `json:"include_notes,omitempty"`
	AuthorName      *string `json:"author_name,omitempty"`
	AuthorEmail     *string `json:"author_email,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty" desc:"Switch pushing off or on without losing the setup."`
	Audience        *string `json:"audience,omitempty" enum:"public,private" desc:"What the remote may carry. Changing it, or only_done, starts the remote's local history over: the earlier commits may hold more than the new setting allows, so the next push is refused as 'the remote has commits this project did not make' until you force-push (typed confirmation, a bundle of the remote's branch first) or point the remote at a new repository."`
	OnlyDone        *bool   `json:"only_done,omitempty" desc:"Public remote: only documents whose status is done. Changing it starts the local history over, as audience does."`
	SignCommits     *bool   `json:"sign_commits,omitempty" desc:"Sign this remote's commits with the project's signing key."`
	ConfirmName     string  `json:"confirm_name,omitempty" desc:"Needed when the change lets more reach a repository that is, or may be, public: a public remote made private (the whole copy), only_done taken off, notes switched on, or the address changed on such a remote or to an address that was ever public. The remote's name, typed exactly. Refused with 409 confirmation_required (data.git_code, data.confirm_field) without it or with another name; a change that only narrows what is sent needs none."`
}

func (in UpdateIn) ProjectRef() string { return in.ProjectID }

// SecretAckIn acknowledges (or withdraws the acknowledgement of) one finding of
// the pre-push check.
type SecretAckIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `json:"remote_id,omitempty"`
	Path      string `json:"path" desc:"The file, exactly as git.remote.get lists it under status.secrets."`
	SHA256    string `json:"sha256" desc:"The SHA-256 of the content that matched, exactly as listed. The acknowledgement covers this file with this content only: a changed file is checked again."`
	Revoke    bool   `json:"revoke,omitempty" desc:"Withdraw an acknowledgement."`
}

func (in SecretAckIn) ProjectRef() string { return in.ProjectID }

// KeygenIn generates a deploy key.
type KeygenIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `json:"remote_id,omitempty"`
	Replace   bool   `json:"replace,omitempty" desc:"Required when the remote already has a key: the old one stops working at the host as soon as the new one replaces it."`
}

func (in KeygenIn) ProjectRef() string { return in.ProjectID }

// KeygenOut is the new key's public half.
type KeygenOut struct {
	PublicKey   string `json:"public_key" desc:"An authorized_keys line. Add it to the repository as a deploy key with write access."`
	Fingerprint string `json:"fingerprint" desc:"SHA256:... as the host shows it."`
	Title       string `json:"title" desc:"A name to give the key at the host."`
	Remote      Remote `json:"item"`
}

// HostKeyOut is what a host key scan found.
type HostKeyOut struct {
	Host        string     `json:"host" desc:"host:port that was asked."`
	Keys        []HostKey  `json:"keys" desc:"The keys the host offered. Nothing is accepted by scanning."`
	Pinned      *PinnedKey `json:"pinned,omitempty" desc:"The key already accepted for this remote, if any."`
	PinnedFound bool       `json:"pinned_matches" desc:"The accepted key is among the ones offered now."`
	Changed     bool       `json:"changed" desc:"A key was accepted and the host no longer offers it. Pushes stop with host_key_changed until a person accepts the new key."`
}

// HostKeyAcceptIn accepts one scanned key.
type HostKeyAcceptIn struct {
	ProjectID   string `path:"id" json:"-" desc:"Project id."`
	RemoteID    string `json:"remote_id,omitempty"`
	Fingerprint string `json:"fingerprint" desc:"The SHA256:... fingerprint of the key to accept, as shown by git.remote.hostkey and compared with the host's published one. The server scans again and pins the key only if the host still offers exactly this one."`
	Replace     bool   `json:"replace,omitempty" desc:"Required when a different key was accepted before: the writer has decided the change is expected."`
}

func (in HostKeyAcceptIn) ProjectRef() string { return in.ProjectID }

// TestOut is the result of a connection test. A failed test is a result, not an
// error: ok is false and code says why.
type TestOut struct {
	OK           bool   `json:"ok"`
	Code         string `json:"code,omitempty" desc:"Why it failed: host_key_unpinned, host_key_changed, auth_failed, repo_not_found, unreachable, timeout, credentials_missing, credentials_unreadable, bad_url."`
	Message      string `json:"message" desc:"Written for a person; names the host and the key."`
	Host         string `json:"host,omitempty"`
	KeyName      string `json:"key_name,omitempty"`
	Empty        bool   `json:"empty" desc:"The repository has no commits yet: the ideal target."`
	BranchExists bool   `json:"branch_exists" desc:"The branch is already there."`
	RemoteHead   string `json:"remote_head,omitempty"`
	Warning      string `json:"warning,omitempty" desc:"For example: the branch already has commits this project did not make, so the first push would be refused."`
}

// PushOut is a queued or running push.
type PushOut struct {
	Item           Push `json:"item"`
	AlreadyRunning bool `json:"already_running" desc:"A push was already queued or running; this is it."`
}

// PushItem wraps one push.
type PushItem struct {
	Item Push `json:"item"`
}

// ForceIn force-pushes.
type ForceIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `json:"remote_id" desc:"The remote. Always named for a force push."`
	Confirm   string `json:"confirm" desc:"The remote's name, typed exactly. A force push replaces the remote branch's history."`
}

func (in ForceIn) ProjectRef() string { return in.ProjectID }

// PushHistoryIn lists pushes.
type PushHistoryIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `query:"remote_id" json:"-" desc:"The remote. Optional while the project has exactly one."`
	Limit     int    `query:"limit" json:"-" desc:"How many, 1 to 100; default 30."`
	Before    string `query:"before" json:"-" desc:"A push id: list the pushes before it."`
}

func (in PushHistoryIn) ProjectRef() string { return in.ProjectID }

// PushHistoryOut is a page of pushes, newest first.
type PushHistoryOut struct {
	Items []Push `json:"items"`
	Next  string `json:"next,omitempty" desc:"Pass as before for the next page."`
}

// PushGetIn names one push.
type PushGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	PushID    string `query:"push_id" json:"-" desc:"The push."`
}

func (in PushGetIn) ProjectRef() string { return in.ProjectID }

// Register adds the Git ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "git.presets", Method: "GET", Path: "/api/v1/git/presets",
		Desc: "Presets that prefill the Git remote form: the address shape, default branch, where the deploy key is added, and where the host publishes its host key fingerprints. Static: reading it makes no request and holds no host key.",
		Auth: ops.Authenticated()}, listPresets)
	ops.Register(r, ops.Op{Name: "git.remote.list", Method: "GET", Path: "/api/v1/projects/{id}/git/remotes",
		Desc: "A project's push-only Git remotes, with status: state, last push, what a retry waits for, what is still to set up. Credentials are never returned.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, listRemotes)
	ops.Register(r, ops.Op{NoDryRun: true, SecretInput: true, Name: "git.remote.create", Method: "POST", Path: "/api/v1/projects/{id}/git/remotes", Status: 201,
		Desc: "Owner only. Add a push-only Git remote to a project (up to five per project; a private remote at an address that was ever public needs confirm_name, the new remote's name). For an ssh address a deploy key is generated; its public half is in the result to add at the host with write access, then scan and accept the host key. For https send the token, which is stored encrypted and never returned. Needs the server's secret key.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, createRemote)
	ops.Register(r, ops.Op{Name: "git.remote.get", Method: "GET", Path: "/api/v1/projects/{id}/git/remote",
		Desc: "One Git remote with its status. remote_id is optional while the project has one remote.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, getRemote)
	ops.Register(r, ops.Op{NoDryRun: true, SecretInput: true, Name: "git.remote.update", Method: "PATCH", Path: "/api/v1/projects/{id}/git/remote",
		Desc: "Owner only. Change a remote: name, address, branch, folder, push mode, whether notes are pushed, the commit identity, a new token, or switch it off. Changing the address (scheme, host, port or path) also drops the stored token or deploy key and the accepted host key, which are entered again for the new address; changing it or the branch resets what is known about the remote.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, updateRemote)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/projects/{id}/git/remote",
		Desc: "Owner only. Remove a remote, its stored credential and its local repository. The remote repository is not touched. Name the remote with remote_id.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, deleteRemote)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.keygen", Method: "POST", Path: "/api/v1/projects/{id}/git/remote/keygen",
		Desc: "Owner only. Generate an ed25519 deploy key for an ssh remote. The private key is stored encrypted and never leaves the server; the public key is returned to add at the host as a deploy key with write access. Replacing an existing key needs replace=true.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, keygen)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.hostkey", Method: "POST", Path: "/api/v1/projects/{id}/git/remote/hostkey",
		Desc: "Scan an ssh remote's host keys and return their fingerprints, so a person can compare them with the host's published ones before accepting. Connects to the host, authenticates nothing and changes nothing.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, scanHostKey)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.hostkey_accept", Method: "POST", Path: "/api/v1/projects/{id}/git/remote/hostkey/accept",
		Desc: "Owner only. Accept (pin) one scanned host key by its fingerprint. The server scans again and pins only if the host still offers that exact key. Replacing a key accepted earlier needs replace=true. A host key that changes after being accepted stops every push with host_key_changed.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, acceptHostKey)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.secret_ack", Method: "POST", Path: "/api/v1/projects/{id}/git/remote/secret-ack",
		Desc: "Owner only. Acknowledge that a file the pre-push check refused is not a secret (a key quoted in a story, an example). Name the file and the SHA-256 of its content as listed in status.secrets; the acknowledgement covers exactly that content, is audited, and a changed file is checked again. revoke=true withdraws it.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, secretAck)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.remote.test", Method: "POST", Path: "/api/v1/projects/{id}/git/remote/test",
		Desc: "Test the connection: a read-only ls-remote with the stored credentials and the accepted host key. Returns ok or a typed failure naming the host and the key; never retries. Reports whether the repository is empty and whether the branch already has commits this project did not make.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, testRemote)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.push", Method: "POST", Path: "/api/v1/projects/{id}/git/push", Status: 202,
		Desc: "Push now. Queues a push of the project's Markdown mirror to the remote and returns at once with the running push; read git.push_get or git.remote.get for the outcome. One push runs per remote at a time. If the remote has commits this project did not make the push is refused and nothing is overwritten.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, pushNow)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.push_force", Method: "POST", Path: "/api/v1/projects/{id}/git/push/force", Status: 202,
		Desc: "Force push: replace the remote branch's history with this project's. Owner only. The body must carry confirm, the remote's name typed exactly. A bundle of the remote branch is written to data/backups/git/ and verified first; if it cannot be, nothing is pushed. Audited.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, pushForce)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.push_cancel", Method: "POST", Path: "/api/v1/projects/{id}/git/push/cancel",
		Desc: "Cancel the remote's queued or running push.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, pushCancel)
	ops.Register(r, ops.Op{Name: "git.push_history", Method: "GET", Path: "/api/v1/projects/{id}/git/pushes",
		Desc: "Pushes of a remote, newest first: status, commit, files changed, what changed, and for a refused push the remote's commits that are not ours.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, pushHistory)
	ops.Register(r, ops.Op{Name: "git.push_get", Method: "GET", Path: "/api/v1/projects/{id}/git/push",
		Desc: "One push in full, with the manifest of the exact tree that was sent (path to SHA-256).",
		Auth: ops.RequireProject(ops.ProjectEditor)}, pushGet)
	registerSigning(r)
	registerRepo(r)
	registerRelease(r)
	registerTrigger(r)
	registerImport(r)
}

func service(c *ops.Ctx) (*Service, error) {
	s, ok := ops.Service[*Service](c.Env, ServiceName)
	if !ok || s == nil {
		return nil, ops.Conflict("Git sync is not running on this server.")
	}
	return s, nil
}

// failure turns a typed Git error into an op error.
func failure(field string, e *Error) error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case CodeBadURL:
		return ops.Invalid(field, e.Message)
	case CodeNoKey, CodeCredentialsBad:
		return ops.Conflict(e.Message)
	case CodeProviderRateLimited:
		return &ops.Error{Code: ops.CodeRateLimited, Message: e.Message, RetryAfterSeconds: e.RetryAfter, Data: map[string]any{"git_code": e.Code, "host": e.Host}}
	case CodeNameTaken, CodeRepoNotEmpty, CodePlanChanged, CodeTagConflict, CodeSigningFailed:
		return ops.ConflictWith(e.Message, map[string]any{"git_code": e.Code, "host": e.Host})
	}
	return &ops.Error{Code: ops.CodeProvider, Message: e.Message, Data: map[string]any{"git_code": e.Code, "host": e.Host}}
}

// remoteFor resolves the remote an op names, with the errors the registry's
// callers expect: not found stays 404 (the project is known to the caller), and
// no remote at all is a conflict.
func (s *Service) remoteFor(ctx context.Context, projectID, remoteID string) (*remoteRow, error) {
	r, err := s.resolveRemote(ctx, projectID, remoteID)
	switch {
	case err == nil:
		return r, nil
	case errors.Is(err, errNoRemote):
		return nil, ops.NotFound("remote")
	case errors.Is(err, errNoRemoteConfigured):
		return nil, ops.Conflict("No Git remote is configured for this project yet. Add one first.")
	case strings.Contains(err.Error(), "more than one"):
		return nil, ops.Invalid("remote_id", err.Error())
	}
	return nil, ops.Internal(err)
}

func listPresets(c *ops.Ctx, _ struct{}) (PresetList, error) { return PresetList{Items: presets}, nil }

func listRemotes(c *ops.Ctx, in ProjectIn) (RemoteList, error) {
	s, err := service(c)
	if err != nil {
		return RemoteList{}, err
	}
	rs, err := s.remotesOf(c, in.ProjectID)
	if err != nil {
		return RemoteList{}, ops.Internal(err)
	}
	out := RemoteList{Items: make([]Remote, 0, len(rs)), Enabled: s.Enabled()}
	if !out.Enabled {
		out.DisabledNotes = secrets.ErrNoKey.Error()
	}
	for _, r := range rs {
		out.Items = append(out.Items, s.view(c, r))
	}
	return out, nil
}

func getRemote(c *ops.Ctx, in RemoteQuery) (RemoteItem, error) {
	s, err := service(c)
	if err != nil {
		return RemoteItem{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return RemoteItem{}, err
	}
	return RemoteItem{Item: s.view(c, r)}, nil
}

// checkToken validates a token before it is sealed.
func checkToken(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", ops.Invalid("token", "Enter the token.")
	}
	if len(t) > maxTokenBytes || strings.IndexFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", ops.Invalid("token", "That does not look like a token: it has spaces or is too long.")
	}
	return t, nil
}

func checkUsername(u string) (string, error) {
	u = strings.TrimSpace(u)
	if len(u) > 100 || strings.IndexFunc(u, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ':' || r == '@' }) >= 0 {
		return "", ops.Invalid("username", "The user name has spaces or characters a token login cannot carry.")
	}
	return u, nil
}

func checkMode(mode string, interval int) (string, int, error) {
	switch mode {
	case "":
		mode = ModeOnSave
	case ModeOnSave, ModeInterval, ModeManual:
	default:
		return "", 0, ops.Invalid("push_mode", "push_mode is on_save, interval or manual.")
	}
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < minIntervalMinutes || interval > maxIntervalMinutes {
		return "", 0, ops.Invalid("interval_minutes", fmt.Sprintf("interval_minutes is %d to %d.", minIntervalMinutes, maxIntervalMinutes))
	}
	return mode, interval, nil
}

func keyTitle(r *remoteRow) string {
	var b strings.Builder
	for _, c := range strings.ToLower(r.Name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return "scriobh-" + strings.Trim(b.String(), "-")
}

func createRemote(c *ops.Ctx, in CreateIn) (RemoteItem, error) {
	s, err := service(c)
	if err != nil {
		return RemoteItem{}, err
	}
	name, e := cleanName(in.Name)
	if e != nil {
		return RemoteItem{}, failure("name", e)
	}
	ep, e := parseURL(in.URL, s.o.AllowFile)
	if e != nil {
		return RemoteItem{}, failure("url", e)
	}
	branch, e := cleanBranch(in.Branch)
	if e != nil {
		return RemoteItem{}, failure("branch", e)
	}
	prefix, e := cleanPrefix(in.PathPrefix)
	if e != nil {
		return RemoteItem{}, failure("path_prefix", e)
	}
	an, ae, e := cleanAuthor(in.AuthorName, in.AuthorEmail)
	if e != nil {
		return RemoteItem{}, failure("author_name", e)
	}
	mode, interval, err := checkMode(in.PushMode, in.IntervalMinutes)
	if err != nil {
		return RemoteItem{}, err
	}
	preset, ok := presetID(in.Preset)
	if !ok {
		return RemoteItem{}, ops.Invalid("preset", "preset is forgejo, github or custom.")
	}
	user, err := checkUsername(in.Username)
	if err != nil {
		return RemoteItem{}, err
	}
	kind := in.AuthKind
	if kind == "" {
		kind = AuthSSH
		if ep.Proto == "https" {
			kind = AuthHTTPS
		}
	}
	switch {
	case kind != AuthSSH && kind != AuthHTTPS:
		return RemoteItem{}, ops.Invalid("auth_kind", "auth_kind is ssh_deploy_key or https_token.")
	case kind == AuthSSH && ep.Proto == "https", kind == AuthHTTPS && ep.Proto == "ssh":
		return RemoteItem{}, ops.Invalid("auth_kind", "A deploy key goes with an ssh address and a token with an https address.")
	}
	audience, err := checkAudience(in.Audience, in.IncludeNotes)
	if err != nil {
		return RemoteItem{}, err
	}
	if in.OnlyDone != nil && *in.OnlyDone && audience != AudiencePublic {
		return RemoteItem{}, ops.Invalid("only_done", "only_done is for a public remote: a private remote carries the whole copy, drafts included.")
	}
	if audience == AudiencePrivate && s.everPublic(c, in.ProjectID, ep) {
		if err := needsPerson(c); err != nil {
			return RemoteItem{}, err
		}
		// a full copy aimed at an address that was ever public is a deliberate step: the new remote's name, typed
		if strings.TrimSpace(in.ConfirmName) != name {
			return RemoteItem{}, confirmationRequired("This remote would carry the whole copy to an address that has been public. Type the remote's name exactly to confirm: "+name, name)
		}
	}
	file := ep.Proto == "file"
	r := &remoteRow{ID: db.NewID("grm"), ProjectID: in.ProjectID, Name: name, Preset: preset, URL: strings.TrimSpace(in.URL), Branch: branch, PathPrefix: prefix,
		AuthKind: kind, Username: user, PushMode: mode, IntervalMinutes: interval, IncludeNotes: audience == AudiencePrivate && (in.IncludeNotes == nil || *in.IncludeNotes),
		AuthorName: an, AuthorEmail: ae, Enabled: true, State: StateNew, CreatedAt: stamp(s.o.Clock.Now()),
		Audience: audience, OnlyDone: in.OnlyDone != nil && *in.OnlyDone}
	if in.SignCommits != nil && *in.SignCommits {
		if _, ok := s.signingKey(c, in.ProjectID); !ok {
			return RemoteItem{}, ops.Conflict("Generate the project's signing key first (git.signing.generate).")
		}
		r.SignCommits = true
	}
	if !file {
		if !s.Enabled() {
			return RemoteItem{}, ops.Conflict(secrets.ErrNoKey.Error())
		}
		switch kind {
		case AuthHTTPS:
			if in.Token != "" {
				tok, err := checkToken(in.Token)
				if err != nil {
					return RemoteItem{}, err
				}
				if r.SecretCipher, err = s.o.Box.Seal([]byte(tok), sealContext(r.ID, ep)); err != nil {
					return RemoteItem{}, ops.Internal(err)
				}
			}
		case AuthSSH:
			pem, pub, err := generateKey(keyTitle(r))
			if err != nil {
				return RemoteItem{}, ops.Internal(err)
			}
			r.SecretCipher, err = s.o.Box.Seal(pem, sealContext(r.ID, ep))
			clear(pem)
			if err != nil {
				return RemoteItem{}, ops.Internal(err)
			}
			r.PublicKey = pub
		}
	}
	if err := s.insertRemote(c, r, c.P.UserID); err != nil {
		switch {
		case errors.Is(err, errTooManyRemotes):
			return RemoteItem{}, ops.Conflict(err.Error())
		case db.IsUniqueViolation(err):
			return RemoteItem{}, ops.Conflict("This project already has a remote with that name.")
		}
		return RemoteItem{}, ops.Internal(err)
	}
	s.recordPublic(c, r)
	c.Audit(ops.AuditEntry{Action: "git.remote.create", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "name": r.Name, "host": ep.Host, "auth_kind": kind, "push_mode": mode, "audience": audience}})
	rr, err := s.loadRemote(c, in.ProjectID, r.ID)
	if err != nil {
		return RemoteItem{}, ops.Internal(err)
	}
	return RemoteItem{Item: s.view(c, rr)}, nil
}

// checkAudience validates the audience of a new remote: public by default, and never together with notes.
func checkAudience(audience string, includeNotes *bool) (string, error) {
	switch audience {
	case "", AudiencePublic:
		if includeNotes != nil && *includeNotes {
			return "", ops.Invalid("include_notes", "A public remote never carries notes. Make the remote private to push them.")
		}
		return AudiencePublic, nil
	case AudiencePrivate:
		return AudiencePrivate, nil
	}
	return "", ops.Invalid("audience", "audience is public or private.")
}

// resetBlock clears a blocked state a person has just acted on.
func resetBlockOnRow(r *remoteRow) bool { return unblock(r, false) }

// unblock clears a blocked state a person has acted on. A refusal for a secret
// in the text is cleared only by changing the text or by an acknowledgement
// (includeSecret), not by fixing a credential.
func unblock(r *remoteRow, includeSecret bool) bool {
	if r.LastErrorCode == CodePausedByRestore {
		return false // only switching the remote on lifts a pause (liftPause)
	}
	return clearBlock(r, includeSecret)
}

// liftPause clears the pause a restore put on a remote; the owner switching it on is what calls it.
func liftPause(r *remoteRow) bool {
	if !r.RestorePaused {
		return false
	}
	r.RestorePaused = false
	if r.LastErrorCode == CodePausedByRestore {
		clearBlock(r, true)
	}
	return true
}

func clearBlock(r *remoteRow, includeSecret bool) bool {
	if r.State != StateBlocked || (r.LastErrorCode == CodeSecretInTree && !includeSecret) {
		return false
	}
	switch {
	case r.LocalSHA != "" && r.LocalSHA != r.LastCommitSHA:
		r.State = StateBehind
	case r.LastCommitSHA != "":
		r.State = StateSynced
	default:
		r.State = StateNew
	}
	r.LastError, r.LastErrorCode, r.FailCount, r.NextAttemptAt = "", "", 0, ""
	return true
}

func updateRemote(c *ops.Ctx, in UpdateIn) (RemoteItem, error) {
	s, err := service(c)
	if err != nil {
		return RemoteItem{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return RemoteItem{}, err
	}
	orig := *r
	oldEp, _ := parseURL(r.URL, true)
	resetSync, resetTree, unblocked := false, false, false
	touchesExposure := in.Audience != nil || in.IncludeNotes != nil || in.OnlyDone != nil || in.URL != nil
	// A change that lets more reach a repository that is, or may be, public needs a person at a browser (needsPerson) and the
	// remote's name typed (confirm_name). It is decided here, from the row as it is now, before anything is written: a screen
	// that asks and an API call that does not get the same answer.
	reachesPublic := s.exposed(c, &orig)
	if in.URL != nil && !reachesPublic {
		if ep, ok := s.addrOf(strings.TrimSpace(*in.URL)); ok && strings.TrimSpace(*in.URL) != orig.URL && s.everPublic(c, orig.ProjectID, ep) {
			reachesPublic = true // a private remote pointed at an address that was ever public
		}
	}
	if touchesExposure && reachesPublic {
		if err := needsPerson(c); err != nil {
			return RemoteItem{}, err
		}
		if widens(orig, in) && strings.TrimSpace(in.ConfirmName) != orig.Name {
			return RemoteItem{}, confirmationRequired("This lets more reach a repository that is, or may be, public. Type the remote's name exactly to confirm: "+orig.Name, orig.Name)
		}
	}
	if in.Name != nil {
		n, e := cleanName(*in.Name)
		if e != nil {
			return RemoteItem{}, failure("name", e)
		}
		r.Name = n
	}
	if in.URL != nil {
		ep, e := parseURL(*in.URL, s.o.AllowFile)
		if e != nil {
			return RemoteItem{}, failure("url", e)
		}
		if (r.AuthKind == AuthSSH && ep.Proto == "https") || (r.AuthKind == AuthHTTPS && ep.Proto == "ssh") {
			return RemoteItem{}, ops.Invalid("url", "This remote uses "+map[string]string{AuthSSH: "a deploy key, so the address must be ssh://", AuthHTTPS: "a token, so the address must be https://"}[r.AuthKind]+". Remove it and add a new one to switch.")
		}
		if u := strings.TrimSpace(*in.URL); u != r.URL {
			r.URL, resetSync = u, true
			if addrIdentity(ep) != addrIdentity(oldEp) {
				// Any change of scheme, host, port or path drops everything that was
				// given for the old place: the credential (it must never be sent
				// anywhere it was not entered for), the deploy key and its public
				// half, and the accepted host key. They are entered again for the new
				// address. Nothing is ever re-sealed.
				r.SecretCipher, r.PublicKey, r.KnownHosts = "", "", ""
			}
		}
	}
	if in.Branch != nil {
		b, e := cleanBranch(*in.Branch)
		if e != nil {
			return RemoteItem{}, failure("branch", e)
		}
		if b != r.Branch {
			r.Branch, resetSync = b, true
		}
	}
	if in.PathPrefix != nil {
		p, e := cleanPrefix(*in.PathPrefix)
		if e != nil {
			return RemoteItem{}, failure("path_prefix", e)
		}
		if p != r.PathPrefix {
			r.PathPrefix, resetTree = p, true
		}
	}
	if in.Audience != nil {
		a, err := checkAudience(*in.Audience, nil)
		if err != nil {
			return RemoteItem{}, err
		}
		r.Audience = a
		if a == AudiencePublic {
			r.IncludeNotes = false
		} else if in.OnlyDone == nil {
			r.OnlyDone = false // only_done is for the public audience: it goes with it
		}
	}
	if in.OnlyDone != nil {
		r.OnlyDone = *in.OnlyDone
	}
	if r.OnlyDone && r.Audience != AudiencePublic {
		return RemoteItem{}, ops.Invalid("only_done", "only_done is for a public remote: a private remote carries the whole copy, drafts included.")
	}
	if r.Audience != orig.Audience || r.OnlyDone != orig.OnlyDone {
		resetSync = true // what the remote may carry changed: its history starts over (see the audience rules)
	}
	if in.IncludeNotes != nil && *in.IncludeNotes != r.IncludeNotes {
		if *in.IncludeNotes && r.Audience != AudiencePrivate {
			return RemoteItem{}, ops.Invalid("include_notes", "A public remote never carries notes. Make the remote private to push them.")
		}
		r.IncludeNotes, resetTree = *in.IncludeNotes, true
	}
	if in.SignCommits != nil && *in.SignCommits != r.SignCommits {
		if *in.SignCommits {
			if _, ok := s.signingKey(c, in.ProjectID); !ok {
				return RemoteItem{}, ops.Conflict("Generate the project's signing key first (git.signing.generate).")
			}
		}
		r.SignCommits = *in.SignCommits
	}
	if in.AuthorName != nil || in.AuthorEmail != nil {
		n, m := r.AuthorName, r.AuthorEmail
		if in.AuthorName != nil {
			n = *in.AuthorName
		}
		if in.AuthorEmail != nil {
			m = *in.AuthorEmail
		}
		n, m, e := cleanAuthor(n, m)
		if e != nil {
			return RemoteItem{}, failure("author_name", e)
		}
		r.AuthorName, r.AuthorEmail = n, m
	}
	if in.PushMode != nil || in.IntervalMinutes != nil {
		mode, iv := r.PushMode, r.IntervalMinutes
		if in.PushMode != nil {
			mode = *in.PushMode
		}
		if in.IntervalMinutes != nil {
			iv = *in.IntervalMinutes
		}
		var err error
		if r.PushMode, r.IntervalMinutes, err = checkMode(mode, iv); err != nil {
			return RemoteItem{}, err
		}
	}
	if in.Username != nil {
		u, err := checkUsername(*in.Username)
		if err != nil {
			return RemoteItem{}, err
		}
		r.Username = u
	}
	if in.Token != nil {
		if r.AuthKind != AuthHTTPS {
			return RemoteItem{}, ops.Invalid("token", "This remote uses a deploy key; generate a new one with git.remote.keygen.")
		}
		if !s.Enabled() {
			return RemoteItem{}, ops.Conflict(secrets.ErrNoKey.Error())
		}
		tok, err := checkToken(*in.Token)
		if err != nil {
			return RemoteItem{}, err
		}
		tokEp, e := parseURL(r.URL, s.o.AllowFile)
		if e != nil {
			return RemoteItem{}, failure("url", e)
		}
		if r.SecretCipher, err = s.o.Box.Seal([]byte(tok), sealContext(r.ID, tokEp)); err != nil {
			return RemoteItem{}, ops.Internal(err)
		}
		unblocked = resetBlockOnRow(r)
	}
	if in.Enabled != nil {
		if *in.Enabled && !r.Enabled && r.LastErrorCode == CodeCredentialsBad {
			return RemoteItem{}, ops.Conflict("Enter the credentials again before switching this remote on.")
		}
		if *in.Enabled && !r.Enabled && r.RestorePaused {
			// The owner has decided this is the server that should push: the pause is lifted.
			unblocked = liftPause(r) || unblocked
		}
		r.Enabled = *in.Enabled
	}
	if touchesExposure && r.Audience == AudiencePrivate {
		// Made to point at something that was ever public (the address check): a person's decision.
		if fep, ok := s.addrOf(r.URL); ok && s.everPublic(c, r.ProjectID, fep) {
			if err := needsPerson(c); err != nil {
				return RemoteItem{}, err
			}
		}
	}
	if resetSync {
		r.State, r.RemoteSHA, r.LastCommitSHA, r.LocalSHA, r.MirrorHash = StateNew, "", "", "", ""
		r.LastError, r.LastErrorCode, r.FailCount, r.NextAttemptAt = "", "", 0, ""
	} else if resetTree {
		r.MirrorHash = ""
	}
	if err := s.saveConfig(c, &orig, r, resetSync || resetTree || unblocked); err != nil {
		if errors.Is(err, errRemoteChanged) {
			return RemoteItem{}, ops.Conflict("This remote was changed by someone else while you were editing it. Reload it and try again.")
		}
		if db.IsUniqueViolation(err) {
			return RemoteItem{}, ops.Conflict("This project already has a remote with that name.")
		}
		return RemoteItem{}, ops.Internal(err)
	}
	if resetSync {
		// The new settings are written first, so that a push that starts from here on reads them. Then whatever was
		// already running (it may have read the old audience) is stopped and waited for, its local repository (which
		// may hold a commit built under the old settings) is removed, and its status is cleared once more, since it
		// may have written some on its way out. A push queued under the old settings finds the remote changed
		// (see the job's snapshot) and does nothing.
		s.cancelAndWait(r.ID)
		s.removeLocal(r.ProjectID, r.ID)
		clearAll := statusUpdate{State: ptr(StateNew), LocalSHA: ptr(""), LastCommitSHA: ptr(""), RemoteSHA: ptr(""), MirrorHash: ptr(""),
			LastError: ptr(""), LastErrorCode: ptr(""), FailCount: ptr(0), NextAttemptAt: ptr(""), IfURL: r.URL}
		if r.RestorePaused && !r.Enabled {
			clearAll.State, clearAll.LastError, clearAll.LastErrorCode = ptr(StateBlocked), &r.LastError, &r.LastErrorCode
		}
		_ = s.setStatus(c, r.ID, clearAll)
	}
	s.recordPublic(c, r)
	fields := changedFields(in)
	c.Audit(ops.AuditEntry{Action: "git.remote.update", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"remote_id": r.ID, "fields": fields}})
	rr, err := s.loadRemote(c, in.ProjectID, r.ID)
	if err != nil {
		return RemoteItem{}, ops.Internal(err)
	}
	return RemoteItem{Item: s.view(c, rr)}, nil
}

// changedFields names which fields an update sent, never their values.
func changedFields(in UpdateIn) []string {
	var f []string
	add := func(ok bool, n string) {
		if ok {
			f = append(f, n)
		}
	}
	add(in.Name != nil, "name")
	add(in.URL != nil, "url")
	add(in.Branch != nil, "branch")
	add(in.PathPrefix != nil, "path_prefix")
	add(in.Username != nil, "username")
	add(in.Token != nil, "token")
	add(in.PushMode != nil, "push_mode")
	add(in.IntervalMinutes != nil, "interval_minutes")
	add(in.IncludeNotes != nil, "include_notes")
	add(in.AuthorName != nil || in.AuthorEmail != nil, "author")
	add(in.Enabled != nil, "enabled")
	add(in.Audience != nil, "audience")
	add(in.OnlyDone != nil, "only_done")
	add(in.SignCommits != nil, "sign_commits")
	return f
}

func deleteRemote(c *ops.Ctx, in RemoteQuery) (struct {
	Deleted bool `json:"deleted"`
}, error) {
	type out = struct {
		Deleted bool `json:"deleted"`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	if in.RemoteID == "" {
		return out{}, ops.Invalid("remote_id", "Name the remote to remove with remote_id.")
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return out{}, err
	}
	s.cancelAndWait(r.ID)
	if _, err := s.o.DB.Writer().ExecContext(c, `DELETE FROM git_remotes WHERE id = ? AND project_id = ?`, r.ID, r.ProjectID); err != nil {
		return out{}, ops.Internal(err)
	}
	s.removeLocal(r.ProjectID, r.ID)
	c.Audit(ops.AuditEntry{Action: "git.remote.delete", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"remote_id": r.ID, "name": r.Name}})
	return out{Deleted: true}, nil
}

func keygen(c *ops.Ctx, in KeygenIn) (KeygenOut, error) {
	s, err := service(c)
	if err != nil {
		return KeygenOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return KeygenOut{}, err
	}
	if r.AuthKind != AuthSSH {
		return KeygenOut{}, ops.Invalid("remote_id", "This remote uses a token, not a deploy key.")
	}
	if !s.Enabled() {
		return KeygenOut{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	if r.SecretCipher != "" && !in.Replace {
		return KeygenOut{}, ops.Conflict("This remote already has a deploy key. Replacing it stops the old one working at the host; send replace=true to do that.")
	}
	orig := *r
	kEp, e := parseURL(r.URL, s.o.AllowFile)
	if e != nil {
		return KeygenOut{}, failure("url", e)
	}
	pem, pub, err := generateKey(keyTitle(r))
	if err != nil {
		return KeygenOut{}, ops.Internal(err)
	}
	sealed, err := s.o.Box.Seal(pem, sealContext(r.ID, kEp))
	clear(pem)
	if err != nil {
		return KeygenOut{}, ops.Internal(err)
	}
	r.SecretCipher, r.PublicKey = sealed, pub
	unblocked := resetBlockOnRow(r)
	if err := s.saveConfig(c, &orig, r, unblocked); err != nil {
		if errors.Is(err, errRemoteChanged) {
			return KeygenOut{}, ops.Conflict("This remote was changed while the key was being made. Try again.")
		}
		return KeygenOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "git.remote.keygen", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"remote_id": r.ID, "replaced": in.Replace}})
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
	fp := ""
	if k != nil {
		fp = ssh.FingerprintSHA256(k)
	}
	return KeygenOut{PublicKey: pub, Fingerprint: fp, Title: keyTitle(r), Remote: s.view(c, r)}, nil
}

func scanHostKey(c *ops.Ctx, in RemoteBody) (HostKeyOut, error) {
	s, err := service(c)
	if err != nil {
		return HostKeyOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return HostKeyOut{}, err
	}
	ep, e := parseURL(r.URL, s.o.AllowFile)
	if e != nil {
		return HostKeyOut{}, failure("url", e)
	}
	if ep.Proto != "ssh" {
		return HostKeyOut{}, ops.Invalid("remote_id", "Only ssh remotes have a host key.")
	}
	keys, err := scanHostKeys(c, ep.HostPort())
	if err != nil {
		return HostKeyOut{}, failure("", classify(err, ep.Host, r.Name))
	}
	out := HostKeyOut{Host: ep.HostPort(), Keys: keys}
	if _, pk, ok := parsePin(r.KnownHosts); ok {
		out.Pinned = &PinnedKey{Type: pk.Type(), Fingerprint: ssh.FingerprintSHA256(pk)}
		for _, k := range keys {
			if k.Fingerprint == out.Pinned.Fingerprint {
				out.PinnedFound = true
			}
		}
		out.Changed = !out.PinnedFound
	}
	return out, nil
}

func acceptHostKey(c *ops.Ctx, in HostKeyAcceptIn) (RemoteItem, error) {
	s, err := service(c)
	if err != nil {
		return RemoteItem{}, err
	}
	fp := strings.TrimSpace(in.Fingerprint)
	if !strings.HasPrefix(fp, "SHA256:") || len(fp) < 20 {
		return RemoteItem{}, ops.Invalid("fingerprint", "Send the fingerprint as scanned, like SHA256:...")
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return RemoteItem{}, err
	}
	ep, e := parseURL(r.URL, s.o.AllowFile)
	if e != nil {
		return RemoteItem{}, failure("url", e)
	}
	if ep.Proto != "ssh" {
		return RemoteItem{}, ops.Invalid("remote_id", "Only ssh remotes have a host key.")
	}
	orig := *r
	if _, pk, ok := parsePin(r.KnownHosts); ok && ssh.FingerprintSHA256(pk) != fp && !in.Replace {
		return RemoteItem{}, ops.Conflict("A different host key was accepted for this remote. If the change is expected (compare " + fp + " with what the host's owner publishes), send replace=true.")
	}
	keys, err := scanHostKeys(c, ep.HostPort())
	if err != nil {
		return RemoteItem{}, failure("", classify(err, ep.Host, r.Name))
	}
	var found *HostKey
	for i := range keys {
		if keys[i].Fingerprint == fp {
			found = &keys[i]
		}
	}
	if found == nil {
		return RemoteItem{}, ops.Conflict("The host no longer offers that key (" + fp + "). Scan again and compare the fingerprints.")
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(found.Key))
	if err != nil {
		return RemoteItem{}, ops.Internal(err)
	}
	r.KnownHosts = pinLine(ep.HostPort(), pub)
	unblocked := false
	if r.LastErrorCode == CodeHostKeyChanged || r.LastErrorCode == CodeHostKeyUnpinned {
		unblocked = resetBlockOnRow(r)
	}
	if err := s.saveConfig(c, &orig, r, unblocked); err != nil {
		if errors.Is(err, errRemoteChanged) {
			return RemoteItem{}, ops.Conflict("The remote's address changed while the host key was being checked. Scan again.")
		}
		return RemoteItem{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "git.remote.hostkey_accept", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "host": ep.HostPort(), "fingerprint": fp, "replaced": in.Replace}})
	return RemoteItem{Item: s.view(c, r)}, nil
}

func testRemote(c *ops.Ctx, in RemoteBody) (TestOut, error) {
	s, err := service(c)
	if err != nil {
		return TestOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return TestOut{}, err
	}
	orig := *r
	out := TestOut{KeyName: r.Name}
	failed := func(e *Error) (TestOut, error) {
		out.OK, out.Code, out.Message, out.Host = false, e.Code, e.Message, e.Host
		if out.Host == "" {
			if ep, pe := parseURL(r.URL, true); pe == nil {
				out.Host = ep.Host
			}
		}
		return out, nil
	}
	ep, e := parseURL(r.URL, s.o.AllowFile)
	if e != nil {
		return failed(e)
	}
	out.Host = ep.Host
	if miss := setupMissing(r); len(miss) > 0 {
		return failed(gerr(CodeCredentialsMissing, "Finish setting up %q first: %s.", r.Name, strings.Join(missingLabels(miss), " and ")))
	}
	cr, err := s.credentialsFor(c, r, ep)
	if err != nil {
		return failed(classify(err, ep.Host, r.Name))
	}
	head, exists, empty, err := remoteHead(c, cr.url, cr, r.Branch)
	if err != nil {
		return failed(classify(err, ep.Host, r.Name, cr.scrub...))
	}
	out.OK, out.Empty, out.BranchExists = true, empty, exists
	out.Message = fmt.Sprintf("Connected to %s with %q.", ep.Host, r.Name)
	if exists {
		out.RemoteHead = head.String()
		if !s.knownLocally(r, head) {
			out.Warning = "The branch " + r.Branch + " already has commits that this project did not make, so the first push would be refused. Use an empty repository or another branch."
		}
	} else if !empty {
		out.Warning = "The repository has other branches but not " + r.Branch + "; the first push creates it."
	}
	// A test that works clears a block a person has just fixed (not a refusal
	// or a secret in the text, which need their own action).
	if r.State == StateBlocked && r.LastErrorCode != CodeSecretInTree && r.LastErrorCode != CodePausedByRestore {
		if resetBlockOnRow(r) {
			_ = s.saveConfig(c, &orig, r, true)
		}
	}
	return out, nil
}

// knownLocally reports whether the commit is in the remote's local history.
func (s *Service) knownLocally(r *remoteRow, h interface{ String() string }) bool {
	repo, err := s.openLocalIfExists(r.ProjectID, r.ID)
	if err != nil || repo == nil {
		return false
	}
	return commitKnown(repo, h.String())
}

func pushNow(c *ops.Ctx, in RemoteBody) (PushOut, error) {
	s, err := service(c)
	if err != nil {
		return PushOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return PushOut{}, err
	}
	if !r.Enabled {
		return PushOut{}, ops.Conflict("This remote is switched off. Switch it on to push.")
	}
	if miss := setupMissing(r); len(miss) > 0 {
		return PushOut{}, ops.ConflictWith("Finish setting up this remote first: "+strings.Join(missingLabels(miss), " and ")+".", map[string]any{"setup": miss})
	}
	// A person pushing by hand has dealt with whatever parked the remote: a
	// rejected or blocked remote is checked again (a rejected one is rejected
	// again if the remote still has commits that are not ours).
	p, already, err := s.enqueue(c, r, TriggerManual, c.P.UserID, false)
	if err != nil {
		return PushOut{}, ops.Internal(err)
	}
	return PushOut{Item: pushView(p, false), AlreadyRunning: already}, nil
}

func pushForce(c *ops.Ctx, in ForceIn) (PushOut, error) {
	s, err := service(c)
	if err != nil {
		return PushOut{}, err
	}
	if strings.TrimSpace(in.RemoteID) == "" {
		return PushOut{}, ops.Invalid("remote_id", "Name the remote to force-push with remote_id.")
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return PushOut{}, err
	}
	if s.exposed(c, r) {
		if err := needsPerson(c); err != nil {
			return PushOut{}, err
		}
	}
	if strings.TrimSpace(in.Confirm) != r.Name {
		return PushOut{}, ops.Invalid("confirm", "A force push replaces the history on the remote. Type the remote's name exactly to confirm: "+r.Name)
	}
	if !r.Enabled {
		return PushOut{}, ops.Conflict("This remote is switched off. Switch it on to push.")
	}
	if miss := setupMissing(r); len(miss) > 0 {
		return PushOut{}, ops.ConflictWith("Finish setting up this remote first: "+strings.Join(missingLabels(miss), " and ")+".", map[string]any{"setup": miss})
	}
	p, already, err := s.enqueue(c, r, TriggerForce, c.P.UserID, true)
	if err != nil {
		return PushOut{}, ops.Internal(err)
	}
	if already {
		return PushOut{}, ops.Conflict("A push is already queued or running for this remote. Wait for it to end, then force push.")
	}
	c.Audit(ops.AuditEntry{Action: "git.push_force", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "remote": r.Name, "push_id": p.ID, "remote_head_seen": r.RemoteSHA}})
	return PushOut{Item: pushView(p, false)}, nil
}

func pushCancel(c *ops.Ctx, in RemoteBody) (struct {
	Cancelled bool `json:"cancelled" desc:"There was a push to cancel."`
}, error) {
	type out = struct {
		Cancelled bool `json:"cancelled" desc:"There was a push to cancel."`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return out{}, err
	}
	return out{Cancelled: s.Cancel(r.ID)}, nil
}

func pushHistory(c *ops.Ctx, in PushHistoryIn) (PushHistoryOut, error) {
	s, err := service(c)
	if err != nil {
		return PushHistoryOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return PushHistoryOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 100)
	rows, err := s.pushesOf(c, r.ID, in.Before, limit+1)
	if err != nil {
		return PushHistoryOut{}, ops.Internal(err)
	}
	out := PushHistoryOut{Items: make([]Push, 0, len(rows))}
	for i, p := range rows {
		if i == limit {
			out.Next = rows[limit-1].ID
			break
		}
		out.Items = append(out.Items, pushView(p, false))
	}
	return out, nil
}

func pushGet(c *ops.Ctx, in PushGetIn) (PushItem, error) {
	s, err := service(c)
	if err != nil {
		return PushItem{}, err
	}
	if in.PushID == "" {
		return PushItem{}, ops.Invalid("push_id", "Name the push with push_id.")
	}
	p, err := s.loadPush(c, in.ProjectID, in.PushID)
	if err != nil {
		if errors.Is(err, errNoPush) {
			return PushItem{}, ops.NotFound("push")
		}
		return PushItem{}, ops.Internal(err)
	}
	return PushItem{Item: pushView(p, true)}, nil
}

func secretAck(c *ops.Ctx, in SecretAckIn) (RemoteItem, error) {
	s, err := service(c)
	if err != nil {
		return RemoteItem{}, err
	}
	if in.Path == "" || len(in.SHA256) != 64 {
		return RemoteItem{}, ops.Invalid("sha256", "Name the file and the SHA-256 of its content, as listed under status.secrets.")
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return RemoteItem{}, err
	}
	orig := *r
	if in.Revoke {
		if _, err := s.o.DB.Writer().ExecContext(c, `DELETE FROM git_scan_acks WHERE remote_id = ? AND path = ? AND sha256 = ?`, r.ID, in.Path, in.SHA256); err != nil {
			return RemoteItem{}, ops.Internal(err)
		}
		c.Audit(ops.AuditEntry{Action: "git.remote.secret_unack", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"remote_id": r.ID, "path": in.Path, "sha256": in.SHA256}})
		return RemoteItem{Item: s.view(c, r)}, nil
	}
	rule := ""
	for _, f := range s.lastSecretFindings(c, r.ID) {
		if f.Path == in.Path && f.SHA == in.SHA256 {
			rule = f.Rule
		}
	}
	if rule == "" {
		return RemoteItem{}, ops.Invalid("path", "That file and content are not among the findings of the last refused push. Only a finding that was reported can be acknowledged.")
	}
	if _, err := s.o.DB.Writer().ExecContext(c, `INSERT OR IGNORE INTO git_scan_acks(remote_id, path, sha256, rule, acked_by, acked_at) VALUES (?,?,?,?,?,?)`,
		r.ID, in.Path, in.SHA256, rule, nullIfEmpty(c.P.UserID), stamp(s.o.Clock.Now())); err != nil {
		return RemoteItem{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "git.remote.secret_ack", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "path": in.Path, "sha256": in.SHA256, "rule": rule}})
	// The refusal is lifted so that the next push looks again with the
	// acknowledgement counted; it is not marked synced.
	if unblock(r, true) {
		r.MirrorHash = ""
		if err := s.saveConfig(c, &orig, r, true); err != nil && !errors.Is(err, errRemoteChanged) {
			return RemoteItem{}, ops.Internal(err)
		}
	}
	return RemoteItem{Item: s.view(c, r)}, nil
}

// widens reports whether an update lets more reach the remote's repository than the row sends now: a public remote made
// private (the whole copy), the done-only filter taken off, notes switched on, or the address changed.
func widens(orig remoteRow, in UpdateIn) bool {
	switch {
	case in.Audience != nil && *in.Audience == AudiencePrivate && orig.Audience == AudiencePublic:
		return true
	case in.OnlyDone != nil && !*in.OnlyDone && orig.OnlyDone:
		return true
	case in.IncludeNotes != nil && *in.IncludeNotes && !orig.IncludeNotes:
		return true
	case in.URL != nil && strings.TrimSpace(*in.URL) != orig.URL:
		return true
	}
	return false
}

// confirmationRequired is the refusal for a change that needs the remote's name typed: 409, with the code a client reads in
// data.git_code and the field to send in data.confirm_field.
func confirmationRequired(msg, name string) error {
	return ops.ConflictWith(msg, map[string]any{"git_code": CodeConfirmationRequired, "confirm_field": "confirm_name", "remote_name": name})
}
