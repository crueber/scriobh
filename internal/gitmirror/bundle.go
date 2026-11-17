package gitmirror

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// scratch is a throw-away directory for a fetch whose objects are only read:
// the listing of a diverged remote's commits and the bundle written before a
// force push. It lives under data/git/.scratch, never inside the mirror or a
// project's repository.
type scratch struct{ dir string }

func (s *Service) newScratch() (*scratch, error) {
	base := filepath.Join(s.o.DataDir, "git", ".scratch")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "fetch-")
	if err != nil {
		return nil, err
	}
	return &scratch{dir: dir}, nil
}

func (sc *scratch) remove() { _ = os.RemoveAll(sc.dir) }

// guardSize watches a directory while a fetch fills it and cancels the context
// when it grows past max bytes, so a huge remote cannot fill the disk. The
// returned stop function ends the watch.
func guardSize(ctx context.Context, dir string, max int64) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if dirSize(dir) > max {
					cancel(errTooBig)
					return
				}
			}
		}
	}()
	return ctx, func() { close(stop); cancel(nil) }
}

var errTooBig = errors.New("the repository is larger than the limit")

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// bundleDir is where a project's bundles of overwritten remote refs are kept
// (data/backups/git/<project id>/). They are not in the backup archive. The
// newest few of each remote are kept (SCRIOBH_GIT_BUNDLES_KEEP) and a purge
// of the project removes the folder.
func (s *Service) bundleDir(projectID string) string {
	return filepath.Join(s.o.DataDir, "backups", "git", projectID)
}

// bundleRemote writes a bundle of the remote branch before a force push
// replaces it, and verifies it. head is updated to the commit the bundle holds,
// which is what the push's lease then protects. If the bundle cannot be
// written and verified the caller must not push.
func (pr *pushRun) bundleRemote(ctx context.Context, ep endpointInfo, cr *credentials, head *plumbing.Hash, detail map[string]any) *Error {
	s, r := pr.s, pr.r
	if e := s.checkTotalQuota(s.o.MaxRepoBytes); e != nil {
		e.Host, e.KeyName = ep.Host, r.Name
		return e
	}
	fail := func(why string, err error) *Error {
		return &Error{Code: CodeBundleFailed, Message: "Nothing was pushed: a local copy of the remote's history could not be made first (" + why + ").", Host: ep.Host, KeyName: r.Name, cause: err}
	}
	sc, err := s.newScratch()
	if err != nil {
		return fail("no scratch space", err)
	}
	defer sc.remove()
	fctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	fctx, done := guardSize(fctx, sc.dir, s.o.MaxRepoBytes)
	defer done()
	srepo, err := git.PlainInit(sc.dir, true)
	if err != nil {
		return fail("no scratch repository", err)
	}
	ref := plumbing.NewBranchReferenceName(r.Branch)
	tmp := plumbing.ReferenceName("refs/scriobh/remote")
	rem := git.NewRemote(srepo.Storer, &config.RemoteConfig{Name: "origin", URLs: []string{cr.url}})
	err = rem.FetchContext(fctx, &git.FetchOptions{RemoteName: "origin", Auth: cr.auth, Tags: git.NoTags, Force: true,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + ref.String() + ":" + tmp.String())}})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if errors.Is(context.Cause(fctx), errTooBig) {
			return &Error{Code: CodeTooLarge, Message: fmt.Sprintf("Nothing was pushed: the remote repository is larger than the %d MB limit (SCRIOBH_GIT_MAX_REPO_MB), so a local copy cannot be made first.", s.o.MaxRepoBytes>>20), Host: ep.Host, KeyName: r.Name}
		}
		if ce := classify(err, ep.Host, r.Name, cr.scrub...); ce.Code != CodeInternal {
			return ce
		}
		return fail("the fetch failed", err)
	}
	if dirSize(sc.dir) > s.o.MaxRepoBytes {
		return &Error{Code: CodeTooLarge, Message: fmt.Sprintf("Nothing was pushed: the remote repository is larger than the %d MB limit (SCRIOBH_GIT_MAX_REPO_MB), so a local copy cannot be made first.", s.o.MaxRepoBytes>>20), Host: ep.Host, KeyName: r.Name}
	}
	fetched, err := srepo.Reference(tmp, true)
	if err != nil {
		return fail("the branch was not fetched", err)
	}
	h := fetched.Hash()
	if err := os.MkdirAll(s.bundleDir(r.ProjectID), 0o700); err != nil {
		return fail("the backup folder cannot be created", err)
	}
	name := fmt.Sprintf("%s-%s-%s.bundle", r.ID, s.o.Clock.Now().UTC().Format("20060102T150405Z"), h.String()[:12])
	dest := filepath.Join(s.bundleDir(r.ProjectID), name)
	lim := s.bundleLimits()
	// The scratch repository holds the fetch as one pack: the bundle is a header
	// and that pack, streamed, with nothing loaded into memory. If there is not
	// exactly one pack the objects are encoded instead, within the same caps.
	if packs, _ := filepath.Glob(filepath.Join(sc.dir, "objects", "pack", "pack-*.pack")); len(packs) == 1 {
		err = writeBundleFromPack(h, ref, packs[0], dest)
		lastBundlePath.Store("pack")
	} else {
		err = writeBundle(srepo.Storer, h, ref, dest, lim)
		lastBundlePath.Store("encode")
	}
	if err != nil {
		_ = os.Remove(dest)
		if errors.Is(err, errBundleTooLarge) {
			return &Error{Code: CodeTooLarge, Message: "Nothing was pushed: the remote branch holds an object or a total size too large to make a local copy of first.", Host: ep.Host, KeyName: r.Name, cause: err}
		}
		return fail("the bundle could not be written", err)
	}
	if err := VerifyBundleLimits(dest, h, lim); err != nil {
		_ = os.Remove(dest)
		if errors.Is(err, errBundleTooLarge) {
			return &Error{Code: CodeTooLarge, Message: "Nothing was pushed: the remote branch is too large to make a verified local copy of first.", Host: ep.Host, KeyName: r.Name, cause: err}
		}
		return fail("the bundle could not be read back", err)
	}
	s.pruneBundles(r.ProjectID, r.ID)
	*head = h
	detail["bundle"] = name
	detail["overwrote"] = h.String()
	return nil
}

// lastBundlePath records which way the newest bundle was written ("pack" or
// "encode"), for tests and for the log.
var lastBundlePath atomic.Value

// Bundle limits. A branch can be tiny on disk and enormous inflated (a few
// kilobytes of zeros are a gigabyte of file), so what is read back is bounded
// by what it says it holds, not by the size of the file.
type VerifyLimits struct {
	MaxObjects    int   // objects in the pack
	MaxInflated   int64 // the declared size of all objects together
	MaxStructural int64 // one commit, tree or tag; these are read into memory
}

var errBundleTooLarge = errors.New("the bundle is larger than the limits allow")

func (s *Service) bundleLimits() VerifyLimits {
	return VerifyLimits{MaxObjects: 2_000_000, MaxInflated: s.o.MaxRepoBytes * 20, MaxStructural: 32 << 20}
}

const bundleMaxObject = 64 << 20 // the largest single object the encode fallback will hold in memory

// writeBundleFromPack writes a version 2 Git bundle: the header, a blank line
// and the pack file as it is. The pack of a full fetch has every object
// reachable from the branch and no deltas against anything outside it, so
// `git bundle verify` and `git clone` accept it. It is written to a temporary
// file in the same folder, synced and renamed, so a bundle that exists is
// complete.
func writeBundleFromPack(head plumbing.Hash, refName plumbing.ReferenceName, packPath, dest string) (err error) {
	in, err := os.Open(packPath)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeBundleTo(dest, head, refName, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

// writeBundle encodes the objects reachable from head into a bundle. It is the
// fallback when the fetch left no single pack; objects are bounded first.
func writeBundle(st storer.EncodedObjectStorer, head plumbing.Hash, refName plumbing.ReferenceName, dest string, lim VerifyLimits) error {
	objs, err := revlist.Objects(st, []plumbing.Hash{head}, nil)
	if err != nil {
		return err
	}
	if len(objs) > lim.MaxObjects {
		return errBundleTooLarge
	}
	var total int64
	for _, h := range objs {
		n, err := st.EncodedObjectSize(h)
		if err != nil {
			return err
		}
		if n > bundleMaxObject {
			return errBundleTooLarge
		}
		if total += n; total > lim.MaxInflated {
			return errBundleTooLarge
		}
	}
	return writeBundleTo(dest, head, refName, func(w io.Writer) error {
		_, err := packfile.NewEncoder(w, st, false).Encode(objs, 10)
		return err
	})
}

func writeBundleTo(dest string, head plumbing.Hash, refName plumbing.ReferenceName, pack func(io.Writer) error) (err error) {
	f, err := os.CreateTemp(filepath.Dir(dest), ".bundle-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if _, err = fmt.Fprintf(w, "# v2 git bundle\n%s %s\n\n", head, refName); err != nil {
		return err
	}
	if err = pack(w); err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// VerifyBundle reads a bundle back with the default limits.
func VerifyBundle(path string, want plumbing.Hash) error {
	return VerifyBundleLimits(path, want, VerifyLimits{MaxObjects: 2_000_000, MaxInflated: DefaultMaxRepoMB << 20 * 20, MaxStructural: 32 << 20})
}

// VerifyBundleLimits reads a bundle back without loading it into memory: the
// header names want; a streaming pass reads every object (inflating each to
// nowhere, so memory does not depend on what the pack holds), counts them
// against the limits and checks the pack's checksum; the pack is then indexed
// into a temporary on-disk store, where every commit, tree and tag is checked
// against the structural limit before anything reachable from want is walked.
func VerifyBundleLimits(path string, want plumbing.Hash, lim VerifyLimits) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	br := bufio.NewReader(f)
	hdr := 0
	readLine := func() (string, error) {
		line, err := br.ReadString('\n')
		hdr += len(line)
		return line, err
	}
	line, err := readLine()
	if err != nil || strings.TrimSpace(line) != "# v2 git bundle" {
		return errors.New("not a version 2 bundle")
	}
	var tip plumbing.Hash
	for {
		line, err = readLine()
		if err != nil {
			return errors.New("the bundle header is cut short")
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "-") {
			return errors.New("the bundle has prerequisites; it is not self-contained")
		}
		sha, _, ok := strings.Cut(line, " ")
		if !ok || !plumbing.IsHash(sha) {
			return errors.New("the bundle header is damaged")
		}
		tip = plumbing.NewHash(sha)
	}
	if tip != want {
		return fmt.Errorf("the bundle holds %s, expected %s", tip, want)
	}
	pack := func() *io.SectionReader { return io.NewSectionReader(f, int64(hdr), fi.Size()-int64(hdr)) }

	// Pass 1: stream every object, bounded.
	sc := packfile.NewScanner(pack())
	_, count, err := sc.Header()
	if err != nil {
		return fmt.Errorf("the packfile is damaged: %w", err)
	}
	if int64(count) > int64(lim.MaxObjects) {
		return errBundleTooLarge
	}
	var inflated int64
	for i := uint32(0); i < count; i++ {
		oh, err := sc.NextObjectHeader()
		if err != nil {
			return fmt.Errorf("the packfile is damaged: %w", err)
		}
		if oh.Length < 0 {
			return errors.New("the packfile is damaged")
		}
		if inflated += oh.Length; inflated > lim.MaxInflated {
			return errBundleTooLarge
		}
		switch oh.Type {
		case plumbing.CommitObject, plumbing.TreeObject, plumbing.TagObject:
			if oh.Length > lim.MaxStructural {
				return errBundleTooLarge
			}
		}
		if _, _, err := sc.NextObject(io.Discard); err != nil {
			return fmt.Errorf("the packfile is damaged: %w", err)
		}
	}
	if _, err := sc.Checksum(); err != nil {
		return fmt.Errorf("the packfile is damaged: %w", err)
	}

	// Pass 2: index it on disk and walk what is reachable.
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	st := filesystem.NewStorage(osfs.New(tmp), cache.NewObjectLRU(16<<20))
	if err := packfile.UpdateObjectStorage(st, pack()); err != nil {
		return fmt.Errorf("the packfile is damaged: %w", err)
	}
	it, err := st.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return err
	}
	var total int64
	err = it.ForEach(func(o plumbing.EncodedObject) error {
		n := o.Size()
		if total += n; total > lim.MaxInflated {
			return errBundleTooLarge
		}
		switch o.Type() {
		case plumbing.CommitObject, plumbing.TreeObject, plumbing.TagObject:
			if n > lim.MaxStructural {
				return errBundleTooLarge
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := object.GetCommit(st, want); err != nil {
		return errors.New("the bundle does not hold its commit")
	}
	// Every object reachable from the tip is present, or revlist fails.
	if _, err := revlist.Objects(st, []plumbing.Hash{want}, nil); err != nil {
		return fmt.Errorf("the bundle is incomplete: %w", err)
	}
	return nil
}
