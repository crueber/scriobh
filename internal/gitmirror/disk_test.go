package gitmirror_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- bundles --------------------------------------------------------------------------

func TestABundleIsVerifiedWithoutLoadingItIntoMemory(t *testing.T) {
	dir := t.TempDir()
	// A branch that is tiny on disk and large inflated: 24 MiB of zeros.
	dest := filepath.Join(dir, "zeros.bundle")
	head, err := gitmirror.WriteTestBundle(map[string][]byte{"manuscript/01.md": []byte("A chapter.\n"), "big.bin": make([]byte, 24<<20)}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Size() > 200<<10 {
		t.Fatalf("the bundle is %d bytes: not a compressible one", fi.Size())
	}
	// Within generous limits it verifies.
	if err := gitmirror.VerifyBundleLimits(dest, head, gitmirror.VerifyLimits{MaxObjects: 100, MaxInflated: 1 << 30, MaxStructural: 1 << 20}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// A disk guard would have let it pass; the declared size is what is bounded.
	if err := gitmirror.VerifyBundleLimits(dest, head, gitmirror.VerifyLimits{MaxObjects: 100, MaxInflated: 1 << 20, MaxStructural: 1 << 20}); !errors.Is(err, gitmirror.ErrBundleTooLarge) {
		t.Errorf("a 24 MiB object under a 1 MiB limit: %v", err)
	}
	if err := gitmirror.VerifyBundleLimits(dest, head, gitmirror.VerifyLimits{MaxObjects: 2, MaxInflated: 1 << 30, MaxStructural: 1 << 20}); !errors.Is(err, gitmirror.ErrBundleTooLarge) {
		t.Errorf("more objects than allowed: %v", err)
	}
	// A tree is read into memory, so one over the structural limit is refused
	// before anything walks it.
	many := map[string][]byte{}
	for i := 0; i < 4000; i++ {
		many[fmt.Sprintf("manuscript/%05d-a-chapter-with-a-longish-name.md", i)] = []byte{byte(i)}
	}
	treeBundle := filepath.Join(dir, "tree.bundle")
	th, err := gitmirror.WriteTestBundle(many, treeBundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitmirror.VerifyBundleLimits(treeBundle, th, gitmirror.VerifyLimits{MaxObjects: 1 << 20, MaxInflated: 1 << 30, MaxStructural: 16 << 10}); !errors.Is(err, gitmirror.ErrBundleTooLarge) {
		t.Errorf("a tree over the structural limit: %v", err)
	}
	if err := gitmirror.VerifyBundleLimits(treeBundle, th, gitmirror.VerifyLimits{MaxObjects: 1 << 20, MaxInflated: 1 << 30, MaxStructural: 1 << 20}); err != nil {
		t.Errorf("the same tree under a generous limit: %v", err)
	}
	// Damage is found: a flipped byte in the pack, a truncated file, the wrong tip.
	raw, _ := os.ReadFile(treeBundle)
	bad := bytes.Clone(raw)
	bad[len(bad)/2] ^= 0xff
	_ = os.WriteFile(filepath.Join(dir, "flipped.bundle"), bad, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "cut.bundle"), raw[:len(raw)-40], 0o600)
	lim := gitmirror.VerifyLimits{MaxObjects: 1 << 20, MaxInflated: 1 << 30, MaxStructural: 1 << 20}
	for _, n := range []string{"flipped.bundle", "cut.bundle"} {
		if err := gitmirror.VerifyBundleLimits(filepath.Join(dir, n), th, lim); err == nil {
			t.Errorf("%s verified", n)
		}
	}
	if err := gitmirror.VerifyBundleLimits(treeBundle, head, lim); err == nil {
		t.Error("a bundle verified for a tip it does not have")
	}
	// Nothing is left behind by verification.
	if entries, _ := filepath.Glob(filepath.Join(dir, ".verify-*")); len(entries) != 0 {
		t.Errorf("temporary stores left behind: %v", entries)
	}
}

func TestOnlyTheNewestBundlesOfARemoteAreKept(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.BundlesKeep = 2 })
	dir := filepath.Join(h.dataDir, "backups", "git", h.pid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"grm_A-20260101T000000Z-aaa.bundle", "grm_A-20260102T000000Z-bbb.bundle", "grm_A-20260103T000000Z-ccc.bundle", "grm_A-20260104T000000Z-ddd.bundle", "grm_B-20260101T000000Z-eee.bundle", "notes.txt"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	h.svc.PruneBundlesForTest(h.pid, "grm_A")
	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := "grm_A-20260103T000000Z-ccc.bundle,grm_A-20260104T000000Z-ddd.bundle,grm_B-20260101T000000Z-eee.bundle,notes.txt"
	if strings.Join(got, ",") != want {
		t.Errorf("bundles = %v", got)
	}
}

func TestForcePushesKeepOnlyTheNewestBundles(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.BundlesKeep = 2 })
	h.start()
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	h.push(h.owner)
	for i := 0; i < 4; i++ {
		// The remote moves on without us each time; the owner forces over it.
		foreign := filepath.Join(t.TempDir(), "o")
		clone, err := cloneAt(foreign, dir)
		if err != nil {
			t.Fatal(err)
		}
		commitFile(t, clone, foreign, fmt.Sprintf("N%d.md", i), "theirs\n")
		h.clk.Advance(time.Hour)
		h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 80 + i, strings.Repeat("w ", i+1) + "\n"}})
		out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: r.ID, Confirm: "Backup"}).(gitmirror.PushOut)
		if p := h.settle(out.Item.ID); p.Status != "ok" {
			t.Fatalf("force %d: %+v\n%s", i, p, h.logs.String())
		}
	}
	entries, _ := os.ReadDir(filepath.Join(h.dataDir, "backups", "git", h.pid))
	if len(entries) != 2 {
		t.Errorf("%d bundles kept, want 2", len(entries))
	}
}

func TestPurgingAProjectRemovesItsBundles(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.dataDir, "backups", "git", h.pid)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "grm_A-20260101T000000Z-aaa.bundle"), []byte("x"), 0o600)
	h.svc.ProjectPurged(context.Background(), h.pid)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the project's bundles survived a purge: %v", err)
	}
}

// ---- import quotas and races ----------------------------------------------------------

func TestImportsAreHeldToTheUsersShareAndTheInstancesSpace(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) {
		o.MaxRepoBytes = 100 << 10
		o.UserQuotaBytes = 250 << 10
		o.TotalQuotaBytes = 1 << 20
	})
	h.start()
	url := makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})
	// A clone kept beside one of the person's projects counts against them.
	kept := filepath.Join(h.dataDir, "imports", "p-"+h.pid)
	_ = os.MkdirAll(kept, 0o700)
	_ = os.WriteFile(filepath.Join(kept, "pack"), make([]byte, 200<<10), 0o600)
	if _, err := h.d.Writer().Exec(`INSERT INTO project_sources(id, project_id, kind, source_url, kept_copy_path, imported_at) VALUES ('src_q', ?, 'git', 'https://x.org/a/b.git', ?, '2026-01-01T00:00:00.000Z')`, h.pid, "imports/p-"+h.pid); err != nil {
		t.Fatal(err)
	}
	_, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: url})
	if h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "SCRIOBH_GIT_USER_QUOTA_MB") {
		t.Fatalf("over the person's share: %v", err)
	}
	// Another person has their own share.
	if _, err := h.call(h.editor, "import.git_preview", gitmirror.GitPreviewIn{URL: url}); err != nil {
		t.Errorf("another person's import: %v", err)
	}
	// Freeing the space lets the import start.
	_ = os.RemoveAll(kept)
	h.waitAll()
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: url}); err != nil {
		t.Errorf("after freeing the space: %v", err)
	}
	// The instance as a whole.
	h2 := newHarness(t, func(o *gitmirror.Options) { o.MaxRepoBytes = 100 << 10; o.TotalQuotaBytes = 150 << 10 })
	h2.start()
	_ = os.MkdirAll(filepath.Join(h2.dataDir, "git", "prj_x"), 0o700)
	_ = os.WriteFile(filepath.Join(h2.dataDir, "git", "prj_x", "f"), make([]byte, 100<<10), 0o600)
	if _, err := h2.call(h2.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: url}); h2.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "SCRIOBH_GIT_TOTAL_QUOTA_MB") {
		t.Errorf("over the instance's space: %v", err)
	}
}

// waitAll waits for every import of the harness to leave 'running'.
func (h *harness) waitAll() {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		_ = h.d.Read().QueryRow(`SELECT count(*) FROM import_jobs WHERE status = 'running'`).Scan(&n)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("imports are still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheOpenImportLimitHoldsUnderConcurrentStarts(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.UserQuotaBytes = -1; o.TotalQuotaBytes = -1 }) // the count, not the disk, is under test
	h.start()
	url := makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: url}); err == nil {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	h.waitAll()
	var open int
	_ = h.d.Read().QueryRow(`SELECT count(*) FROM import_jobs WHERE status IN ('running','preview')`).Scan(&open)
	if started != 5 || open != 5 {
		t.Errorf("%d imports started, %d open; the limit is 5", started, open)
	}
}

func TestOneImportJobCreatesOneProjectWhateverTheRequests(t *testing.T) {
	h := newHarness(t)
	h.start()
	url := makeRepo(t, map[string]string{"01-a.md": "Some text here.\n", "02-b.md": "More text.\n"})
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url}).ID)
	var before int
	_ = h.d.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&before)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.call(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: job.Summary.Token}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var after int
	_ = h.d.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&after)
	if ok != 1 || after != before+1 {
		t.Errorf("%d applies succeeded and %d projects were created, want 1 and 1", ok, after-before)
	}
	// A job that is being applied cannot be discarded.
	job2 := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url}).ID)
	if _, err := h.d.Writer().Exec(`UPDATE import_jobs SET applying = 1 WHERE id = ?`, job2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.call(h.owner, "import.git_discard", gitmirror.GitJobIn{JobID: job2.ID}); h.code(err) != ops.CodeConflict {
		t.Errorf("discarding a job being applied: %v", err)
	}
}

func TestAnInterruptedApplyIsSettledAtStart(t *testing.T) {
	h := newHarness(t)
	now := h.clk.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	ins := func(id string) {
		if _, err := h.d.Writer().Exec(`INSERT INTO import_jobs(id, owner_id, source_url, status, clone_path, created_at, applying) VALUES (?, ?, 'https://x.org/a/b.git', 'preview', 'imports/'||?, ?, 1)`, id, h.owner.UserID, id, now); err != nil {
			t.Fatal(err)
		}
	}
	ins("imp_never")
	ins("imp_made")
	if _, err := h.d.Writer().Exec(`INSERT INTO project_sources(id, project_id, kind, source_url, metadata_json, imported_at) VALUES ('src_m', ?, 'git', 'https://x.org/a/b.git', '{"job_id":"imp_made"}', ?)`, h.pid, now); err != nil {
		t.Fatal(err)
	}
	h.svc.RecoverImportsForTest(context.Background())
	var st1, st2 string
	var ap1, ap2 int
	_ = h.d.Read().QueryRow(`SELECT status, applying FROM import_jobs WHERE id = 'imp_never'`).Scan(&st1, &ap1)
	_ = h.d.Read().QueryRow(`SELECT status, applying FROM import_jobs WHERE id = 'imp_made'`).Scan(&st2, &ap2)
	if st1 != "preview" || ap1 != 0 {
		t.Errorf("a job whose project was never made: %s applying=%d, want ready again", st1, ap1)
	}
	if st2 != "done" || ap2 != 0 {
		t.Errorf("a job whose project was made: %s applying=%d, want done", st2, ap2)
	}
}

func TestKeptImportClonesExpire(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.KeepImportDays = 7; o.PollEvery = 1000 * time.Hour })
	h.start()
	url := makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url}).ID)
	out := h.must(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: job.Summary.Token}).(imports.ApplyOut)
	kept := filepath.Join(h.dataDir, "imports", "p-"+out.ProjectID)
	if _, err := os.Stat(kept); err != nil {
		t.Fatal(err)
	}
	h.stampSources()
	h.clk.Advance(6 * 24 * time.Hour)
	h.svc.ExpireKeptImportsForTest(context.Background())
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("the clone expired early")
	}
	h.clk.Advance(2 * 24 * time.Hour)
	h.svc.ExpireKeptImportsForTest(context.Background())
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("an expired clone is still kept: %v", err)
	}
	var path string
	_ = h.d.Read().QueryRow(`SELECT kept_copy_path FROM project_sources WHERE id = ?`, out.SourceID).Scan(&path)
	if path != "" {
		t.Errorf("the source still points at a removed clone: %q", path)
	}
	// The project itself is untouched.
	if rowCount(t, h, `SELECT count(*) FROM documents WHERE project_id = ?`, out.ProjectID) == 0 {
		t.Error("the imported documents are gone")
	}
}

func TestPurgeHooksRefuseIdsThatAreNotIds(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.push(h.owner)
	_ = os.MkdirAll(filepath.Join(h.dataDir, "backups", "git", h.pid), 0o700)
	_ = os.MkdirAll(filepath.Join(h.dataDir, "imports", "p-"+h.pid), 0o700)
	marker := filepath.Join(h.dataDir, "git", "marker")
	_ = os.WriteFile(marker, []byte("x"), 0o600)
	for _, bad := range []string{".", "..", "", "../..", "prj_x", "git"} {
		h.svc.ProjectPurged(context.Background(), bad)
	}
	for _, p := range []string{h.dataDir, filepath.Join(h.dataDir, "git"), marker, filepath.Join(h.dataDir, "git", h.pid, r.ID), filepath.Join(h.dataDir, "backups", "git"), filepath.Join(h.dataDir, "imports")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed by a purge of a bad id: %v", p, err)
		}
	}
}
