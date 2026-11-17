package gitmirror

import (
	"context"
	"encoding/json"
	"strings"

	"golang.org/x/crypto/ssh"
)

// PinnedKey describes an accepted host key.
type PinnedKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint" desc:"SHA256:... of the accepted host key."`
}

// Status is where a remote stands. Every field is data, not wording: a screen
// composes the sentence.
type Status struct {
	State         string              `json:"state" enum:"new,synced,behind,rejected,blocked" desc:"new: nothing pushed yet. synced: the remote has the newest commit. behind: a commit waits locally (the remote was unreachable or the push failed) and is retried with backoff. rejected: the remote has commits that are not ours; nothing is pushed until the writer force-pushes or imports. blocked: a person must act (credentials, host key, a secret in the text)."`
	InSync        bool                `json:"in_sync" desc:"The remote has the newest commit and nothing is waiting or running."`
	Running       bool                `json:"running" desc:"A push is queued or running now."`
	Behind        bool                `json:"behind" desc:"A local commit has not reached the remote."`
	MirrorHealthy bool                `json:"mirror_on" desc:"The Markdown mirror is running; pushes are built from it."`
	LastPushAt    string              `json:"last_push_at,omitempty" desc:"When a commit last reached the remote."`
	LastAttemptAt string              `json:"last_attempt_at,omitempty"`
	NextAttemptAt string              `json:"next_attempt_at,omitempty" desc:"When an automatic retry may run after a failure."`
	LastCommitSHA string              `json:"last_commit_sha,omitempty" desc:"The newest commit the remote is known to have."`
	LocalSHA      string              `json:"local_sha,omitempty" desc:"The newest commit in the local repository."`
	RemoteSHA     string              `json:"remote_sha,omitempty" desc:"The remote branch's head as last observed."`
	LastError     string              `json:"last_error,omitempty" desc:"Written for a person; never contains a credential."`
	LastErrorCode string              `json:"last_error_code,omitempty" desc:"A stable code: host_key_unpinned, host_key_changed, auth_failed, repo_not_found, unreachable, timeout, remote_ahead, rejected_by_remote, secret_in_tree, credentials_unreadable, no_mirror, mirror_off, bundle_failed, too_large, paused_by_restore."`
	FailCount     int                 `json:"fail_count" desc:"Consecutive failed attempts."`
	Rejected      *RejectedInfo       `json:"rejected,omitempty" desc:"Present when state is rejected: what is on the remote."`
	Secrets       []SecretFindingView `json:"secrets,omitempty" desc:"Present when the last push was refused for something that looks like a secret: the files and the SHA-256 of what matched. The owner can acknowledge one with git.remote.secret_ack."`
}

// Ack is an acknowledged finding of the pre-push check.
type Ack struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Rule    string `json:"rule"`
	AckedAt string `json:"acked_at"`
}

// RejectedInfo is what a refused push found on the remote.
type RejectedInfo struct {
	PushID      string          `json:"push_id"`
	RemoteHead  string          `json:"remote_head"`
	Commits     []ForeignCommit `json:"commits" desc:"The remote's newest commits that this project did not make, newest first (at most 20)."`
	MoreCommits bool            `json:"more_commits"`
}

// Remote is a remote as ops return it. Credentials are write-only: has_secret
// says one is stored, and nothing returns it.
type Remote struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Name            string     `json:"name"`
	Preset          string     `json:"preset,omitempty" enum:"forgejo,github"`
	URL             string     `json:"url"`
	Host            string     `json:"host" desc:"The host the address points at."`
	Branch          string     `json:"branch"`
	PathPrefix      string     `json:"path_prefix,omitempty" desc:"Subdirectory of the repository the files are placed in."`
	AuthKind        string     `json:"auth_kind" enum:"ssh_deploy_key,https_token"`
	Username        string     `json:"username,omitempty" desc:"The user name that goes with an HTTPS token."`
	HasSecret       bool       `json:"has_secret" desc:"A deploy key or token is stored. It is never returned."`
	PublicKey       string     `json:"public_key,omitempty" desc:"The deploy key's public half, an authorized_keys line to add to the repository with write access."`
	KeyFingerprint  string     `json:"key_fingerprint,omitempty" desc:"SHA256:... of the deploy key, as the host shows it."`
	HostKey         *PinnedKey `json:"host_key,omitempty" desc:"The accepted host key; absent until one is accepted."`
	PushMode        string     `json:"push_mode" enum:"on_save,interval,manual"`
	IntervalMinutes int        `json:"interval_minutes" desc:"Minutes between pushes when push_mode is interval."`
	IncludeNotes    bool       `json:"include_notes"`
	AuthorName      string     `json:"author_name,omitempty" desc:"Empty means the default identity."`
	AuthorEmail     string     `json:"author_email,omitempty"`
	Author          string     `json:"author" desc:"The identity commits carry: Name <email>."`
	Enabled         bool       `json:"enabled"`
	Audience        string     `json:"audience" enum:"public,private" desc:"public: only what a reader of the book sees. private: the full mirror."`
	OnlyDone        bool       `json:"only_done" desc:"A public remote carries only documents whose status is done."`
	SignCommits     bool       `json:"sign_commits" desc:"Commits of this remote are signed with the project's signing key."`
	CreatedVia      string     `json:"created_via,omitempty" desc:"publish: made by repo.publish."`
	RestorePaused   bool       `json:"restore_paused" desc:"A restore paused this remote. Only the owner switching it on lifts that: no check, test or restart does."`
	Setup           []string   `json:"setup" desc:"What is still needed before anything can be pushed: deploy_key, host_key, token. Empty when ready."`
	Status          Status     `json:"status"`
	Trigger         *Trigger   `json:"trigger,omitempty" desc:"The push trigger, when the remote has one: where it calls and how the last delivery went. Never its secret or the address's path."`
	Acknowledged    []Ack      `json:"acknowledged" desc:"Findings of the pre-push check the owner acknowledged, each for one file with one content."`
	CreatedAt       string     `json:"created_at"`
	UpdatedAt       string     `json:"updated_at"`
}

// view builds the Remote an op returns.
func (s *Service) view(ctx context.Context, r *remoteRow) Remote {
	v := Remote{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Preset: r.Preset, URL: r.URL, Branch: r.Branch, PathPrefix: r.PathPrefix,
		AuthKind: r.AuthKind, Username: r.Username, HasSecret: r.SecretCipher != "", PublicKey: r.PublicKey, PushMode: r.PushMode,
		IntervalMinutes: r.IntervalMinutes, IncludeNotes: r.IncludeNotes, AuthorName: r.AuthorName, AuthorEmail: r.AuthorEmail,
		Enabled: r.Enabled, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Setup: setupMissing(r),
		Audience: r.Audience, OnlyDone: r.OnlyDone, SignCommits: r.SignCommits, CreatedVia: r.CreatedVia, RestorePaused: r.RestorePaused}
	if v.Setup == nil {
		v.Setup = []string{}
	}
	if ep, e := parseURL(r.URL, true); e == nil {
		v.Host = ep.Host
	}
	if r.PublicKey != "" {
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(r.PublicKey)); err == nil {
			v.KeyFingerprint = ssh.FingerprintSHA256(k)
		}
	}
	if _, k, ok := parsePin(r.KnownHosts); ok {
		v.HostKey = &PinnedKey{Type: k.Type(), Fingerprint: ssh.FingerprintSHA256(k)}
	}
	id := s.identityFor(r)
	v.Author = id.name + " <" + id.email + ">"

	st := Status{State: r.State, LastPushAt: r.LastPushAt, LastAttemptAt: r.LastAttemptAt, NextAttemptAt: r.NextAttemptAt,
		LastCommitSHA: r.LastCommitSHA, LocalSHA: r.LocalSHA, RemoteSHA: r.RemoteSHA, LastError: r.LastError, LastErrorCode: r.LastErrorCode, FailCount: r.FailCount}
	st.Behind = r.LocalSHA != "" && r.LocalSHA != r.LastCommitSHA
	s.mu.Lock()
	_, st.Running = s.active[r.ID]
	s.mu.Unlock()
	if s.o.Source != nil {
		ms := s.o.Source.Status(r.ProjectID)
		st.MirrorHealthy = ms.Enabled
		st.InSync = r.State == StateSynced && !st.Behind && !st.Running && ms.Enabled && !ms.Pending && ms.TreeHash == r.MirrorHash
	}
	if r.State == StateRejected {
		if p := s.latestWith(ctx, r.ID, "rejected"); p != nil {
			var d struct {
				Head   string          `json:"remote_head"`
				Commit []ForeignCommit `json:"remote_commits"`
				More   bool            `json:"remote_commits_more"`
			}
			_ = json.Unmarshal([]byte(p.DetailJSON), &d)
			if d.Commit == nil {
				d.Commit = []ForeignCommit{}
			}
			st.Rejected = &RejectedInfo{PushID: p.ID, RemoteHead: d.Head, Commits: d.Commit, MoreCommits: d.More}
		}
	}
	if r.LastErrorCode == CodeSecretInTree {
		st.Secrets = s.lastSecretFindings(ctx, r.ID)
	}
	v.Status = st
	if t, err := s.loadTrigger(ctx, r.ID); err == nil && t != nil {
		tv := s.triggerView(ctx, t)
		v.Trigger = &tv
	}
	v.Acknowledged = []Ack{}
	if rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT path, sha256, rule, acked_at FROM git_scan_acks WHERE remote_id = ? ORDER BY path, sha256`, r.ID); err == nil {
		for rows.Next() {
			var a Ack
			if rows.Scan(&a.Path, &a.SHA256, &a.Rule, &a.AckedAt) == nil {
				v.Acknowledged = append(v.Acknowledged, a)
			}
		}
		rows.Close()
	}
	return v
}

// Push is one push as ops return it.
type Push struct {
	ID                string            `json:"id"`
	RemoteID          string            `json:"remote_id"`
	Trigger           string            `json:"trigger" enum:"save,interval,manual,retry,force"`
	Status            string            `json:"status" enum:"running,ok,rejected,error,cancelled"`
	Outcome           string            `json:"outcome,omitempty" desc:"pushed, up_to_date, or the error code."`
	StartedAt         string            `json:"started_at"`
	FinishedAt        string            `json:"finished_at,omitempty"`
	CommitSHA         string            `json:"commit_sha,omitempty"`
	ParentSHA         string            `json:"parent_sha,omitempty"`
	RemoteSHA         string            `json:"remote_sha,omitempty" desc:"The remote branch's head before this push."`
	Branch            string            `json:"branch,omitempty"`
	FilesChanged      int               `json:"files_changed"`
	Bytes             int64             `json:"bytes"`
	Forced            bool              `json:"forced"`
	ErrorCode         string            `json:"error_code,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	Subject           string            `json:"subject,omitempty" desc:"The commit's first line."`
	Documents         []changedFile     `json:"documents,omitempty" desc:"Documents the commit added, changed or removed, with the word change."`
	RemoteCommits     []ForeignCommit   `json:"remote_commits,omitempty" desc:"For a rejected push: the remote's commits that are not ours."`
	RemoteCommitsMore bool              `json:"remote_commits_more,omitempty"`
	Bundle            string            `json:"bundle,omitempty" desc:"For a force push: the bundle of the overwritten remote ref, in data/backups/git/."`
	Manifest          map[string]string `json:"manifest,omitempty" desc:"Only from git.push_get: path to SHA-256 of every file the commit holds."`
}

func pushView(p *pushRow, withManifest bool) Push {
	v := Push{ID: p.ID, RemoteID: p.RemoteID, Trigger: p.Trigger, Status: p.Status, Outcome: p.Outcome, StartedAt: p.StartedAt, FinishedAt: p.FinishedAt,
		CommitSHA: p.CommitSHA, ParentSHA: p.ParentSHA, RemoteSHA: p.RemoteSHA, Branch: p.Branch, FilesChanged: p.FilesChanged, Bytes: p.Bytes,
		Forced: p.Forced, ErrorCode: p.ErrorCode, ErrorMessage: p.ErrorMessage}
	var d struct {
		Subject string          `json:"subject"`
		Docs    []changedFile   `json:"documents"`
		Commits []ForeignCommit `json:"remote_commits"`
		More    bool            `json:"remote_commits_more"`
		Bundle  string          `json:"bundle"`
	}
	if json.Unmarshal([]byte(p.DetailJSON), &d) == nil {
		v.Subject, v.Documents, v.RemoteCommits, v.RemoteCommitsMore, v.Bundle = d.Subject, d.Docs, d.Commits, d.More, d.Bundle
	}
	if withManifest {
		m := map[string]string{}
		_ = json.Unmarshal([]byte(p.ManifestJSON), &m)
		v.Manifest = m
	}
	return v
}

// Preset prefills the remote form for a host. Static data: reading it makes no
// request, and it holds no host key, because a key that cannot be verified here
// must not be trusted; the host key is scanned and accepted explicitly.
type Preset struct {
	ID               string `json:"id" enum:"forgejo,github,custom"`
	Label            string `json:"label"`
	DefaultHost      string `json:"default_host,omitempty" desc:"Fixed for GitHub; empty for Forgejo, which the person hosts."`
	SSHURL           string `json:"ssh_url" desc:"Address shape, with {host}, {owner} and {repo} to fill in."`
	HTTPSURL         string `json:"https_url"`
	DefaultBranch    string `json:"default_branch"`
	KeySettingsURL   string `json:"key_settings_url,omitempty" desc:"Where the deploy key is added, with the same placeholders."`
	FingerprintsDocs string `json:"fingerprints_url,omitempty" desc:"Where the host publishes its host key fingerprints, to compare with the scan."`
	Notes            string `json:"notes"`
}

var presets = []Preset{
	{ID: "forgejo", Label: "Forgejo", SSHURL: "ssh://git@{host}/{owner}/{repo}.git", HTTPSURL: "https://{host}/{owner}/{repo}.git", DefaultBranch: "main",
		KeySettingsURL: "https://{host}/{owner}/{repo}/settings/keys",
		Notes:          "Add the public key under the repository's Settings, Deploy keys, and tick write access. If your Forgejo's SSH runs on another port, put it in the address: ssh://git@host:2222/owner/repo.git. Compare the host key fingerprint with the one your server administrator gives you."},
	{ID: "github", Label: "GitHub", DefaultHost: "github.com", SSHURL: "ssh://git@github.com/{owner}/{repo}.git", HTTPSURL: "https://github.com/{owner}/{repo}.git", DefaultBranch: "main",
		KeySettingsURL:   "https://github.com/{owner}/{repo}/settings/keys",
		FingerprintsDocs: "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints",
		Notes:            "Add the public key under the repository's Settings, Deploy keys, and tick Allow write access. A deploy key works for one repository. Compare the host key fingerprint with GitHub's published list before accepting it."},
	{ID: "custom", Label: "Any Git server", SSHURL: "ssh://git@{host}/{path}.git", HTTPSURL: "https://{host}/{path}.git", DefaultBranch: "main",
		Notes: "Any server that speaks Git over SSH or HTTPS. Use an empty repository: if the branch already has commits that this project did not make, the first push is refused."},
}

func presetID(p string) (string, bool) {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "", "custom":
		return "", true
	case "forgejo", "github":
		return p, true
	}
	return "", false
}
