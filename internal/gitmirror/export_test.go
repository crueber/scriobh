package gitmirror

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Test seams for the external test package.

// Check runs the periodic pass once.
func (s *Service) Check(ctx context.Context) { s.check(ctx) }

// ScanHostKeys is scanHostKeys.
var ScanHostKeys = scanHostKeys

// ScanForSecrets reports the paths of files that look like secrets, with the rule.
func ScanForSecrets(files map[string][]byte) []string {
	var out []string
	for _, f := range scanForSecrets(files) {
		out = append(out, f.Path+"|"+f.Rule)
	}
	return out
}

// TreeHashOf is treeHashOf.
func TreeHashOf(files map[string][]byte) (string, error) {
	h, err := treeHashOf(files)
	return h.String(), err
}

// LocalRepo opens the remote's local repository.
func (s *Service) LocalRepo(projectID, remoteID string) (*git.Repository, error) {
	return git.PlainOpen(s.repoDir(projectID, remoteID))
}

// RepoDir is where a remote's local repository lives.
func (s *Service) RepoDir(projectID, remoteID string) string { return s.repoDir(projectID, remoteID) }

// ParseURL is parseURL; it reports only whether the address is accepted and its host.
func ParseURL(raw string, allowFile bool) (host string, port int, err error) {
	ep, e := parseURL(raw, allowFile)
	if e != nil {
		return "", 0, e
	}
	return ep.Host, ep.Port, nil
}

// Hash is the zero-value helper for tests.
var ZeroHash = plumbing.ZeroHash

// SealContext is what a remote's credential is sealed for.
func SealContext(remoteID, rawURL string) string {
	ep, e := parseURL(rawURL, true)
	if e != nil {
		panic(e)
	}
	return sealContext(remoteID, ep)
}

// LegacySealContext is the id-only context rows used before the address was bound.
func LegacySealContext(remoteID string) string { return legacySealContext(remoteID) }

// PushToForTest runs the push step alone against a remote's local repository,
// as the engine would after its checks: seen is the remote head that was seen
// (when exists), force the owner's decision.
func (s *Service) PushToForTest(ctx context.Context, projectID, remoteID string, seen plumbing.Hash, exists, force bool) error {
	r, err := s.loadRemote(ctx, projectID, remoteID)
	if err != nil {
		return err
	}
	repo, err := s.openLocal(projectID, remoteID)
	if err != nil {
		return err
	}
	ep, e := parseURL(r.URL, true)
	if e != nil {
		return e
	}
	cr, err := s.credentialsFor(ctx, r, ep)
	if err != nil {
		return err
	}
	return pushTo(ctx, repo, r, cr, seen, exists, force)
}

// BlockedIP is blockedIP.
func BlockedIP(ip string) bool { return blockedIP(net.ParseIP(ip)) }

// SetRootCAsForTest makes the HTTPS client trust the given roots (a test
// server's certificate) and drops pooled connections.
func SetRootCAsForTest(p *x509.CertPool) {
	httpTransport.TLSClientConfig.RootCAs = p
	httpTransport.CloseIdleConnections()
}

// HTTPClientForTest is the client go-git is given for HTTPS: the guarded
// dialer, no proxy, bounded bodies and redirects.
func HTTPClientForTest() *http.Client {
	return &http.Client{Transport: limitTransport{httpTransport}, CheckRedirect: checkRedirect}
}

// ErrBundleTooLarge is the error a bundle over the limits is refused with.
var ErrBundleTooLarge = errBundleTooLarge

// WriteTestBundle writes a bundle of one commit holding files, by the encode
// path with generous limits, and returns the commit.
func WriteTestBundle(files map[string][]byte, dest string) (plumbing.Hash, error) {
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	tree, err := writeTree(repo.Storer, files)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sig := object.Signature{Name: "t", Email: "t@e", When: time.Unix(1700000000, 0).UTC()}
	c := &object.Commit{Author: sig, Committer: sig, Message: "m\n", TreeHash: tree}
	o := repo.Storer.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	h, err := repo.Storer.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return h, writeBundle(repo.Storer, h, "refs/heads/main", dest, VerifyLimits{MaxObjects: 1 << 20, MaxInflated: 1 << 30, MaxStructural: 1 << 30})
}

// PruneBundlesForTest runs the bundle pruning for one remote.
func (s *Service) PruneBundlesForTest(projectID, remoteID string) {
	s.pruneBundles(projectID, remoteID)
}

// ExpireKeptImportsForTest runs the expiry of kept import clones.
func (s *Service) ExpireKeptImportsForTest(ctx context.Context) { s.expireKeptImports(ctx) }

// RecoverImportsForTest runs the start-up recovery of import jobs.
func (s *Service) RecoverImportsForTest(ctx context.Context) { s.recoverImports(ctx) }

// LastBundlePath says which way the newest bundle was written.
func LastBundlePath() string {
	v, _ := lastBundlePath.Load().(string)
	return v
}

// ---- Phase 7 seams ------------------------------------------------------------------

// SetProviderRootCAs makes the host API client trust the given roots.
func SetProviderRootCAs(p *x509.CertPool) { providerRootCAs.Store(p) }

// SetProviderTimeout sets the API client's per-request deadline (zero: the default).
func SetProviderTimeout(d time.Duration) { providerTimeout.Store(int64(d)) }

// SetAfterCreateHook makes repo.publish fail after it created a repository.
func SetAfterCreateHook(f func() error) {
	if f == nil {
		afterCreateHook.Store(nil)
		return
	}
	afterCreateHook.Store(&f)
}

// WaitReleases blocks until no release job is running.
func (s *Service) WaitReleases() { s.relWG.Wait() }

// DeliverTriggers sends the trigger deliveries that are due.
func (s *Service) DeliverTriggers(ctx context.Context) { s.deliverDue(ctx) }

// TargetFor says where a host's API is.
func TargetFor(kind, host string) (api, uploads, web string, err error) {
	t, e := newTarget(kind, host)
	if e != nil {
		return "", "", "", e
	}
	return t.api, t.uploads, t.host, nil
}

// SnapshotFor is the reader's view of a snapshot read from the service's source, for a remote row described by
// its audience settings: the file paths and contents it carries and the documents it withholds.
func (s *Service) SnapshotFor(ctx context.Context, projectID, audience string, onlyDone, includeNotes bool, prefix string) (files map[string][]byte, withheld []Withheld, err error) {
	sn, w, e := s.snapshotFor(ctx, &remoteRow{ProjectID: projectID, Audience: audience, OnlyDone: onlyDone, IncludeNotes: includeNotes, PathPrefix: prefix})
	if e != nil {
		return nil, nil, e
	}
	return sn.files, w, nil
}

// SignSSH signs data in the SSHSIG format with the namespace git.
var SignSSH = signSSH

// SigningPEM opens the project's signing key as the engine would, for a test that searches for it.
func (s *Service) SigningPEM(projectID string) (string, error) {
	var sealed string
	if err := s.o.DB.Read().QueryRow(`SELECT key_sealed FROM git_signing_keys WHERE project_id = ?`, projectID).Scan(&sealed); err != nil {
		return "", err
	}
	b, err := s.o.Box.Open(sealed, signingContext(projectID))
	return string(b), err
}

// SetExecHook sets a function called at named points of a push run (nil clears it).
func SetExecHook(f func(stage string)) {
	if f == nil {
		execHook.Store(nil)
		return
	}
	execHook.Store(&f)
}
