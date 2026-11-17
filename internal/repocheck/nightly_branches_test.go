package repocheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/nightly-branches.sh picks the branches the nightly run tests. The nightly race stage ran three phase
// branches that were already merged into main, each for half an hour, because each had a commit in the last 24
// hours; a branch whose tip is an ancestor of the default branch's tip was tested there. This builds a scratch
// repository (the script reads it as a remote) and checks what the script lists.
func TestNightlyBranchesSkipsWhatMainAlreadyHolds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "nightly-branches.sh")
	repo := t.TempDir()
	now := int64(2_000_000_000)
	day := int64(86400)
	clock := now - 6*3600 // every commit is within the window unless a test says otherwise

	git := func(args ...string) string {
		t.Helper()
		clock++
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		stamp := fmt.Sprintf("%d +0000", clock)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file, text string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, file), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", file)
		git("commit", "-q", "-m", file+" "+text)
		return git("rev-parse", "HEAD")
	}

	git("init", "-q", "-b", "main")
	commit("a", "1")
	// phase-1 and phase-2 branch from here. phase-1 is merged into main (a merge commit), phase-2 is not.
	git("branch", "phase-1")
	git("branch", "phase-2")
	git("checkout", "-q", "phase-1")
	commit("p1", "merged")
	git("checkout", "-q", "phase-2")
	p2 := commit("p2", "not merged")
	// A branch that main reached by a fast-forward earlier, so its tip is an ancestor of main and not main's tip.
	git("checkout", "-q", "main")
	git("branch", "ff")
	git("checkout", "-q", "ff")
	commit("f", "ff")
	git("checkout", "-q", "main")
	git("merge", "-q", "--ff-only", "ff")
	git("merge", "-q", "--no-ff", "-m", "merge phase-1", "phase-1")
	mainTip := commit("m", "after the merges")
	// A squash merge: not an ancestor, but the same tree as main, so the tree dedupe still drops it.
	git("checkout", "-q", "-b", "squashed", "main~1")
	git("checkout", "-q", "main")
	sq := git("commit-tree", "-m", "squash", "main^{tree}")
	git("branch", "squashed2", sq)
	// An unrelated branch with its own work, and one older than the window.
	git("checkout", "-q", "-b", "feature-x", "main")
	fx := commit("x", "own work")
	git("checkout", "-q", "main")

	run := func(args ...string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script, "--remote", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		var so, se strings.Builder
		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			t.Fatalf("nightly-branches %v: %v\n%s", args, err, se.String())
		}
		return so.String(), se.String()
	}
	names := func(out string) []string {
		var ns []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if f := strings.Fields(l); len(f) == 2 {
				ns = append(ns, f[1])
			}
		}
		return ns
	}
	since := fmt.Sprint(now - day)

	out, errOut := run("--since", since)
	got := names(out)
	// main first, then phase-2 (unmerged work), then the rest. phase-1 and ff are in main; squashed and squashed2
	// share main's tree.
	want := []string{"main", "phase-2", "feature-x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("listed %v, want %v\n%s\nstderr:\n%s", got, want, out, errOut)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		switch f[1] {
		case "main":
			if f[0] != mainTip {
				t.Errorf("main listed at %s, tip is %s", f[0], mainTip)
			}
		case "phase-2":
			if f[0] != p2 {
				t.Errorf("phase-2 listed at %s, tip is %s", f[0], p2)
			}
		case "feature-x":
			if f[0] != fx {
				t.Errorf("feature-x listed at %s, tip is %s", f[0], fx)
			}
		}
	}
	if !strings.Contains(errOut, "already merged into main") || !strings.Contains(errOut, "phase-1") || !strings.Contains(errOut, "ff") {
		t.Errorf("stderr does not say what was left out as merged:\n%s", errOut)
	}

	// The cap counts only branches that will run: two slots hold main and phase-2, feature-x is over the cap, and
	// a merged branch is not named as over the cap.
	out, errOut = run("--since", since, "--max", "2")
	if got := names(out); strings.Join(got, ",") != "main,phase-2" {
		t.Errorf("with --max 2 listed %v", got)
	}
	if !strings.Contains(errOut, "over the cap of 2, not tested tonight: feature-x") {
		t.Errorf("stderr: %s", errOut)
	}

	// A manual run lists the branch it names even when it is merged.
	out, _ = run("--branch", "phase-1")
	if got := names(out); strings.Join(got, ",") != "phase-1" {
		t.Errorf("--branch phase-1 listed %v", got)
	}

	// A window that holds only a merged branch lists nothing but main when main is new, and nothing at all when
	// nothing in the window is new work.
	out, _ = run("--since", fmt.Sprint(clock+1))
	if got := names(out); len(got) != 0 {
		t.Errorf("an empty window listed %v", got)
	}
}
