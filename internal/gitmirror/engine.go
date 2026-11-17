package gitmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/db"
)

// setupMissing lists what a remote still needs before anything can be pushed.
// The screens show it as a checklist.
func setupMissing(r *remoteRow) []string {
	if isFileURL(r.URL) {
		return nil
	}
	var m []string
	switch r.AuthKind {
	case AuthSSH:
		if r.SecretCipher == "" {
			m = append(m, "deploy_key")
		}
		if r.KnownHosts == "" {
			m = append(m, "host_key")
		}
	case AuthHTTPS:
		if r.SecretCipher == "" {
			m = append(m, "token")
		}
	}
	return m
}

// lastSecretSignature is the signature of the findings of the newest refusal.
func (s *Service) lastSecretSignature(ctx context.Context, remoteID string) string {
	var fs []SecretFinding
	for _, v := range s.lastSecretFindings(ctx, remoteID) {
		fs = append(fs, SecretFinding{Path: v.Path, SHA: v.SHA})
	}
	return findingsSignature(fs)
}

func isFileURL(u string) bool { return strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "/") }

// pushRun is one execution of a job.
type pushRun struct {
	s    *Service
	j    *job
	r    *remoteRow
	p    *pushRow
	bg   context.Context // not cancelled by shutdown: status is always written
	auto bool
	cr   *credentials
}

// execute runs a push from the snapshot to the remote's answer. It always
// settles the push row.
func (s *Service) execute(ctx, bg context.Context, j *job) {
	p := j.push
	pr := &pushRun{s: s, j: j, p: p, bg: bg, auto: j.trigger != TriggerManual && j.trigger != TriggerForce}
	r, err := s.loadRemoteByID(bg, j.remoteID)
	if err != nil {
		p.Status, p.Outcome, p.ErrorMessage = "cancelled", "removed", "The remote was removed."
		_ = s.finishPush(bg, p)
		return
	}
	pr.r = r
	var trashed int
	_ = s.o.DB.Read().QueryRowContext(bg, `SELECT count(*) FROM projects WHERE id = ? AND (trashed_at IS NOT NULL OR deleted_at IS NOT NULL)`, r.ProjectID).Scan(&trashed)
	if trashed > 0 {
		// A trashed project is paused: nothing is pushed until it is restored.
		if pr.auto {
			s.dropPush(bg, p.ID)
			return
		}
		pr.fail(gerr(CodeInternal, "This project is in the trash. Restore it to push."))
		return
	}
	if r.URL != j.url || r.Branch != j.branch || r.Audience != j.audience || r.OnlyDone != j.onlyDone || r.IncludeCover != j.includeCover || (j.force && r.Name != j.name) {
		// Someone changed where this goes between the moment it was asked for
		// (for a force push: the moment the owner typed the confirmation) and now.
		if pr.auto {
			s.dropPush(bg, p.ID)
			return
		}
		pr.fail(gerr(CodeRemoteChanged, "The remote was changed after this push was asked for (its address, branch, name or what it may carry), so nothing was sent. Check the settings and push again."))
		return
	}
	if pr.auto && (!r.Enabled || !s.autoEligible(r)) {
		s.dropPush(bg, p.ID)
		return
	}
	if !r.Enabled {
		pr.fail(gerr(CodeInternal, "This remote is switched off. Switch it on to push."))
		return
	}
	if miss := setupMissing(r); len(miss) > 0 {
		if pr.auto {
			s.dropPush(bg, p.ID)
			return
		}
		pr.fail(gerr(CodeCredentialsMissing, "Finish setting up %q first: %s.", r.Name, strings.Join(missingLabels(miss), " and ")))
		return
	}
	ep, e := parseURL(r.URL, s.o.AllowFile)
	if e != nil {
		pr.fail(e)
		return
	}
	cr, err := s.credentialsFor(ctx, r, ep)
	if err != nil {
		pr.fail(classify(err, ep.Host, r.Name))
		return
	}
	pr.cr = cr
	if hook := execHook.Load(); hook != nil {
		(*hook)("loaded")
	}
	sn, withheld, e := s.snapshotFor(bg, r)
	if e != nil {
		pr.fail(e)
		return
	}
	if fs := withoutAcknowledged(append(scanForSecrets(sn.scannable()), scanMeta(sn)...), s.acksOf(bg, r.ID)); len(fs) > 0 {
		// Parked until the text changes (the periodic check and the save trigger
		// look again when the mirror's hash differs from this one), and an
		// automatic push that finds the same things again writes nothing more.
		views := make([]SecretFindingView, 0, len(fs))
		for _, f := range fs {
			views = append(views, SecretFindingView{Path: f.Path, Rule: f.Rule, SHA: f.SHA})
		}
		dj, _ := json.Marshal(map[string]any{"secrets": views})
		p.DetailJSON = string(dj)
		if pr.auto && r.LastErrorCode == CodeSecretInTree && s.lastSecretSignature(bg, r.ID) == findingsSignature(fs) {
			s.dropPush(bg, p.ID)
			pr.status(statusUpdate{MirrorHash: &sn.mirrorHash})
			return
		}
		pr.status(statusUpdate{MirrorHash: &sn.mirrorHash})
		pr.fail(secretError(fs))
		return
	}
	repo, err := s.openLocal(r.ProjectID, r.ID)
	if err != nil {
		pr.fail(&Error{Code: CodeInternal, Message: "The local repository could not be opened.", cause: err})
		return
	}
	if _, err := repo.Reference(localBranch, true); err != nil && r.LastCommitSHA != "" {
		// The local repository was lost (a restore, a moved data directory) but
		// the remote is known to have our last commit: take that one commit
		// back as the parent, so the history goes on instead of looking like a
		// stranger's.
		if e := s.adoptHead(ctx, repo, r, cr); e != nil && e.Code != CodeNothingToPush {
			pr.fail(e)
			return
		}
	}
	title := s.projectTitle(bg, r.ProjectID)
	var signer ssh.Signer
	if r.SignCommits {
		var e *Error
		if signer, e = s.signerFor(bg, r.ProjectID); e != nil {
			pr.fail(e)
			return
		}
	}
	res, err := s.commitSnapshot(repo, sn, s.identityFor(r), title, r.PathPrefix, s.o.Clock.Now(), signer)
	if err != nil {
		pr.fail(&Error{Code: CodeInternal, Message: "The commit could not be built.", cause: err})
		return
	}
	wasSynced := r.State == StateSynced && r.LocalSHA == r.LastCommitSHA && r.LocalSHA != ""
	headSHA := res.head.String()
	pr.status(statusUpdate{LocalSHA: &headSHA, MirrorHash: &sn.mirrorHash})
	r.LocalSHA, r.MirrorHash = headSHA, sn.mirrorHash
	p.CommitSHA, p.ParentSHA = headSHA, ""
	if res.parent != plumbing.ZeroHash && res.created {
		p.ParentSHA = res.parent.String()
	}
	if !res.created && pr.auto && wasSynced {
		s.dropPush(bg, p.ID) // nothing changed and the remote already has it
		return
	}
	manifest, _ := json.Marshal(sn.hashes)
	p.ManifestJSON = string(manifest)
	p.FilesChanged, p.Bytes = res.filesDiff, res.bytes

	if e := s.checkVisibility(ctx, r, cr); e != nil {
		pr.fail(e)
		return
	}
	head, exists, _, err := remoteHead(ctx, cr.url, cr, r.Branch)
	if err != nil {
		pr.fail(classify(err, ep.Host, r.Name, cr.scrub...))
		return
	}
	p.RemoteSHA = ""
	if exists {
		p.RemoteSHA = head.String()
	}
	pr.status(statusUpdate{RemoteSHA: ptr(p.RemoteSHA)})
	detail := map[string]any{"subject": res.subject, "documents": limitChanged(res.changed)}
	if r.Audience != AudiencePrivate {
		detail["audience"], detail["withheld"] = AudiencePublic, withheldCounts(withheld)
	}

	switch {
	case exists && head == res.head:
		p.Outcome = CodeNothingToPush
		pr.ok(res, detail, true)
		return
	case exists && !j.force && !isAncestor(repo, head, res.head):
		pr.reject(ctx, repo, ep, cr, head, detail)
		return
	case j.force && exists:
		if e := pr.bundleRemote(ctx, ep, cr, &head, detail); e != nil {
			pr.fail(e)
			return
		}
	}
	// A lease on the head we saw: the push fails rather than overwrite a
	// remote that moved since the check.
	if err := pushTo(ctx, repo, r, cr, head, exists, j.force); err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			p.Outcome = CodeNothingToPush
			pr.ok(res, detail, true)
			return
		}
		ce := classify(err, ep.Host, r.Name, cr.scrub...)
		if ce.Code == CodeRemoteAhead {
			pr.reject(ctx, repo, ep, cr, head, detail)
			return
		}
		pr.fail(ce)
		return
	}
	p.Outcome = "pushed"
	pr.ok(res, detail, false)
}

func missingLabels(m []string) []string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		switch x {
		case "deploy_key":
			out = append(out, "generate the deploy key and add it to the repository")
		case "host_key":
			out = append(out, "accept the host key")
		case "token":
			out = append(out, "enter the token")
		default:
			out = append(out, x)
		}
	}
	return out
}

func limitChanged(c []changedFile) []changedFile {
	if len(c) > 200 {
		return c[:200]
	}
	return c
}

// isAncestor reports whether remote is the head or one of its ancestors, judged
// from the local repository alone: a remote head that is not in local history is
// not ours.
func isAncestor(repo *git.Repository, remote, head plumbing.Hash) bool {
	if remote == head {
		return true
	}
	rc, err := repo.CommitObject(remote)
	if err != nil {
		return false
	}
	hc, err := repo.CommitObject(head)
	if err != nil {
		return false
	}
	ok, err := rc.IsAncestor(hc)
	return err == nil && ok
}

// pushTo pushes the local branch to the remote's branch, with a lease on the
// remote head that was seen (when there was one). force adds the + of a forced
// update; the lease still holds.
func pushTo(ctx context.Context, repo *git.Repository, r *remoteRow, cr *credentials, seen plumbing.Hash, exists, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	dst := plumbing.NewBranchReferenceName(r.Branch)
	rem := git.NewRemote(repo.Storer, &config.RemoteConfig{Name: "origin", URLs: []string{cr.url}})
	opts := &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(localBranch + ":" + dst.String())},
		Auth:       cr.auth,
		// A force is only ever a force over a head that was seen, and the push
		// is leased on it. Without a head there is nothing to overwrite: a branch
		// that appears between the check and the push is refused, not replaced
		// (no bundle of it was made).
		Force: force && exists,
	}
	if exists {
		opts.RequireRemoteRefs = []config.RefSpec{config.RefSpec(seen.String() + ":" + dst.String())}
	}
	return rem.PushContext(ctx, opts)
}

// status writes the push's result onto the remote, but only while the remote
// still has the address the push was queued for.
func (pr *pushRun) status(u statusUpdate) {
	u.IfURL = pr.j.url
	_ = pr.s.setStatus(pr.bg, pr.r.ID, u)
}

// ok settles a push that reached the remote (or found it already current).
func (pr *pushRun) ok(res *commitResult, detail map[string]any, upToDate bool) {
	s, r, p := pr.s, pr.r, pr.p
	now := stamp(s.o.Clock.Now())
	head := res.head.String()
	p.Status = "ok"
	if upToDate {
		p.FilesChanged, p.Bytes = 0, 0
	}
	dj, _ := json.Marshal(detail)
	p.DetailJSON = string(dj)
	upd := statusUpdate{State: ptr(StateSynced), LastAttemptAt: &now, NextAttemptAt: ptr(""), LastCommitSHA: &head, RemoteSHA: &head,
		LastError: ptr(""), LastErrorCode: ptr(""), FailCount: ptr(0)}
	if !upToDate {
		upd.LastPushAt = &now
	}
	if upToDate && pr.auto {
		s.dropPush(pr.bg, p.ID)
	} else {
		_ = s.finishPush(pr.bg, p)
	}
	firstPush := r.LastCommitSHA == ""
	pr.status(upd)
	if !upToDate {
		s.afterPushed(pr, head, firstPush)
		s.log.Info("git: pushed", "project", r.ProjectID, "remote", r.ID, "commit", head[:12], "files", p.FilesChanged, "forced", p.Forced)
	}
	if p.Forced {
		s.auditRaw(pr.bg, pr.j.by, "git.push_force.done", r.ProjectID, map[string]any{"remote_id": r.ID, "remote": r.Name, "commit": head, "overwrote": p.RemoteSHA, "bundle": detail["bundle"]})
	}
}

// afterPushed does what follows a push that reached the remote: a remote made by repo.publish gets its default
// branch pointed at the branch that now exists (a new empty repository's default may be another name), and the
// remote's trigger, if it has one, is queued. Neither can fail the push: it has happened.
func (s *Service) afterPushed(pr *pushRun, head string, firstPush bool) {
	r := pr.r
	if firstPush && r.CreatedVia == "publish" && pr.cr != nil && pr.cr.token != "" {
		s.setDefaultBranchAfterPush(pr, r)
	}
	s.enqueueTrigger(pr.bg, r, pr.p.ID, head)
}

func (s *Service) setDefaultBranchAfterPush(pr *pushRun, r *remoteRow) {
	ep, e := parseURL(r.URL, false)
	if e != nil {
		return
	}
	host := ep.Host
	if ep.Port != 443 {
		host = ep.HostPort()
	}
	t, e := newTarget(r.Preset, host)
	parts := strings.Split(strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git"), "/")
	if e != nil || len(parts) != 2 {
		return
	}
	ctx, cancel := context.WithTimeout(pr.bg, 30*time.Second)
	defer cancel()
	if he := newProviderClient(t, pr.cr.token).setDefaultBranch(ctx, parts[0], parts[1], r.Branch); he != nil {
		s.log.Info("git: could not set the new repository's default branch", "remote", r.ID, "code", he.Code)
	}
}

// reject settles a push the remote's history forbids. The remote's commits
// that are not ours are listed from a scratch fetch, only to be shown.
func (pr *pushRun) reject(ctx context.Context, repo *git.Repository, ep endpointInfo, cr *credentials, remote plumbing.Hash, detail map[string]any) {
	s, r, p := pr.s, pr.r, pr.p
	foreign, more := s.listForeign(ctx, repo, r, cr, remote)
	detail["remote_head"] = remote.String()
	detail["remote_commits"] = foreign
	detail["remote_commits_more"] = more
	dj, _ := json.Marshal(detail)
	p.Status, p.Outcome, p.ErrorCode = "rejected", CodeRemoteAhead, CodeRemoteAhead
	p.ErrorMessage = fmt.Sprintf("%s has commits that this project did not make, so nothing was pushed and nothing on the remote was changed. Force push replaces the remote's history (a bundle of it is kept first); Import brings it into a new project.", ep.Host)
	p.DetailJSON = string(dj)
	p.RemoteSHA = remote.String()
	_ = s.finishPush(pr.bg, p)
	now := stamp(s.o.Clock.Now())
	pr.status(statusUpdate{State: ptr(StateRejected), LastAttemptAt: &now, NextAttemptAt: ptr(""), RemoteSHA: ptr(remote.String()),
		LastError: &p.ErrorMessage, LastErrorCode: ptr(CodeRemoteAhead)})
	s.log.Warn("git: remote has commits that are not ours; push refused", "project", r.ProjectID, "remote", r.ID)
}

// fail settles a push that did not happen and decides what happens next.
func (pr *pushRun) fail(e *Error) {
	s, r, p := pr.s, pr.r, pr.p
	now := s.o.Clock.Now()
	p.Status, p.Outcome, p.ErrorCode, p.ErrorMessage = "error", e.Code, e.Code, e.Message
	upd := statusUpdate{LastAttemptAt: ptr(stamp(now)), LastError: &e.Message, LastErrorCode: &e.Code}
	switch e.Code {
	case CodeInterrupted:
		p.Status = "cancelled"
		upd.LastError, upd.LastErrorCode = nil, nil
	case CodeRemoteChanged:
		upd = statusUpdate{} // the remote is not the one this push was for: leave its status alone
	case CodeUnreachable, CodeTimeout, CodeNoMirror, CodeMirrorOff, CodeMirrorBehind, CodeInternal, CodeAuthFailed, CodeRepoNotFound:
		fc := r.FailCount + 1
		upd.FailCount = &fc
		if r.LocalSHA != "" && r.LocalSHA != r.LastCommitSHA {
			upd.State = ptr(StateBehind)
		}
		upd.NextAttemptAt = ptr(stamp(now.Add(s.backoffFor(fc))))
		if e.Code == CodeInternal && r.State == StateNew {
			upd.State = nil
		}
	default:
		// A host key that is not or no longer the pinned one, credentials that
		// cannot be read, a secret in the text, a refusal by the remote: retrying
		// cannot help. A person acts, then pushes.
		fc := r.FailCount + 1
		upd.FailCount = &fc
		upd.State = ptr(StateBlocked)
		upd.NextAttemptAt = ptr("")
	}
	if pr.auto && p.Status == "cancelled" {
		s.dropPush(pr.bg, p.ID)
	} else {
		_ = s.finishPush(pr.bg, p)
	}
	pr.status(upd)
	if p.Status != "cancelled" {
		s.log.Warn("git: push did not complete", "project", r.ProjectID, "remote", r.ID, "code", e.Code, "trigger", pr.j.trigger)
	}
}

// ---- the remote's commits that are not ours ---------------------------------------

// ForeignCommit is a commit on the remote that this project did not make.
type ForeignCommit struct {
	SHA     string `json:"sha" desc:"Full commit id."`
	Author  string `json:"author" desc:"The commit's author name."`
	When    string `json:"when" desc:"The commit's time, UTC."`
	Subject string `json:"subject" desc:"First line of the commit message."`
}

const maxForeign = 20

// listForeign fetches the remote branch into a scratch repository, only to list
// the commits it holds that our history does not. Nothing is merged, no working
// tree exists, and the scratch repository is removed afterwards; the mirror and
// the local repository are not touched. A failure gives an empty list: the
// refusal stands without it.
func (s *Service) listForeign(ctx context.Context, repo *git.Repository, r *remoteRow, cr *credentials, remote plumbing.Hash) (out []ForeignCommit, more bool) {
	ours := map[plumbing.Hash]bool{}
	if ref, err := repo.Reference(localBranch, true); err == nil {
		if it, err := repo.Log(&git.LogOptions{From: ref.Hash()}); err == nil {
			n := 0
			_ = it.ForEach(func(c *object.Commit) error {
				ours[c.Hash] = true
				if n++; n >= 20000 {
					return errStop
				}
				return nil
			})
		}
	}
	sc, err := s.newScratch()
	if err != nil {
		return nil, false
	}
	defer sc.remove()
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fctx, done := guardSize(fctx, sc.dir, s.o.MaxRepoBytes)
	defer done()
	ref := plumbing.ReferenceName("refs/scriobh/remote")
	srepo, err := git.PlainInit(sc.dir, true)
	if err != nil {
		return nil, false
	}
	rem := git.NewRemote(srepo.Storer, &config.RemoteConfig{Name: "origin", URLs: []string{cr.url}})
	spec := []config.RefSpec{config.RefSpec("+" + plumbing.NewBranchReferenceName(r.Branch).String() + ":" + ref.String())}
	// A shallow fetch keeps this small on a long history; a server that does
	// not offer it gets a full fetch, still bounded by the size guard.
	for _, depth := range []int{50, 0} {
		err = rem.FetchContext(fctx, &git.FetchOptions{RemoteName: "origin", Auth: cr.auth, Tags: git.NoTags, Force: true, Depth: depth, RefSpecs: spec})
		if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
			err = nil
			break
		}
		if fctx.Err() != nil {
			break
		}
	}
	if err != nil {
		s.log.Debug("git: could not fetch the remote's commits to list them", "remote", r.ID, "code", classify(err, "", r.Name, cr.scrub...).Code)
		return nil, false
	}
	it, err := srepo.Log(&git.LogOptions{From: remote, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, false
	}
	_ = it.ForEach(func(c *object.Commit) error {
		if ours[c.Hash] {
			return nil
		}
		if len(out) >= maxForeign {
			more = true
			return errStop
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
		out = append(out, ForeignCommit{SHA: c.Hash.String(), Author: shorten(c.Author.Name), When: stamp(c.Committer.When), Subject: shorten(subject)})
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].When > out[j].When })
	return out, more
}

var errStop = errors.New("stop")

// execHook is a test seam: called at named points of a push run.
var execHook atomic.Pointer[func(stage string)]

// auditRaw writes an audit line from outside an op (the worker finishing a
// force push that an op started).
func (s *Service) auditRaw(ctx context.Context, actor, action, projectID string, detail map[string]any) {
	dj, _ := json.Marshal(detail)
	_, err := s.o.DB.Writer().ExecContext(ctx, `INSERT INTO audit_log(id, at, actor_id, action, target_type, target_id, detail_json) VALUES (?,?,?,?,?,?,?)`,
		db.NewID("aud"), db.Now(), nullIfEmpty(actor), action, "project", projectID, string(dj))
	if err != nil {
		s.log.Warn("git: could not write an audit line", "action", action, "err", cause(err))
	}
}

var _ = transport.ErrRepositoryNotFound

// adoptHead restores the local history after the local repository was lost. It
// asks the remote for its branch head, and only if that is exactly the commit
// recorded as the last one pushed does it fetch it (shallow when the server
// allows, else in full, under the size guard) as the local branch. Anything
// else, a remote that moved on or a record older than the remote, adopts
// nothing and the push is judged as usual: refused when the remote has
// commits that are not ours. This fetches our own commit; it merges nothing.
func (s *Service) adoptHead(ctx context.Context, repo *git.Repository, r *remoteRow, cr *credentials) *Error {
	head, exists, _, err := remoteHead(ctx, cr.url, cr, r.Branch)
	if err != nil {
		return classify(err, cr.host, r.Name, cr.scrub...)
	}
	if !exists || head.String() != r.LastCommitSHA {
		return gerr(CodeNothingToPush, "nothing to adopt")
	}
	dir := s.repoDir(r.ProjectID, r.ID)
	fctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	fctx, done := guardSize(fctx, dir, s.o.MaxRepoBytes)
	defer done()
	rem := git.NewRemote(repo.Storer, &config.RemoteConfig{Name: "origin", URLs: []string{cr.url}})
	spec := []config.RefSpec{config.RefSpec("+" + plumbing.NewBranchReferenceName(r.Branch).String() + ":" + localBranch)}
	for _, depth := range []int{1, 0} {
		err = rem.FetchContext(fctx, &git.FetchOptions{RemoteName: "origin", Auth: cr.auth, Tags: git.NoTags, Force: true, Depth: depth, RefSpecs: spec})
		if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		if fctx.Err() != nil {
			break
		}
	}
	if errors.Is(context.Cause(fctx), errTooBig) {
		return gerr(CodeTooLarge, "The remote's history is larger than the %d MB limit (SCRIOBH_GIT_MAX_REPO_MB).", s.o.MaxRepoBytes>>20)
	}
	return classify(err, cr.host, r.Name, cr.scrub...)
}
