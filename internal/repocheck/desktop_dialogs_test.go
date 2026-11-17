package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// On Linux and the BSDs rfd draws dialogs with GTK 3, and its blocking calls (MessageDialog::show,
// FileDialog::save_file) post the dialog to GLib's default main context and wait. tao's event loop owns that context
// on the main thread, so a blocking call made there waits for itself for ever: the window stops and no quit gets
// through (a headless Arch run showed it: the main thread on a futex in the shell's dialog code, rfd's thread in
// g_main_context_iteration). The shell's dialogs therefore use the asynchronous API wherever GTK is the backend, and
// keep the blocking calls only on the branch for macOS and Windows. The blocking call also used to serialise the
// dialogs, so the asynchronous ones go through one queue (dialog_queue().run) that shows one at a time, in request
// order. This test keeps that shape: each of the three entry points (info, ask, save) opens with a GTK_BACKEND
// branch that hands the asynchronous dialog to the queue and returns, before its blocking construction; nothing in
// that branch spawns a dialog task of its own (it would bypass the queue); and no other file in the shell touches
// rfd (a second use would bypass the guard).
func TestShellDialogsDoNotBlockTheMainThreadOnGtk(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "desktop", "src-tauri", "src")
	b, err := os.ReadFile(filepath.Join(src, "dialogs.rs"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)

	// Spacing and line breaks may change under rustfmt; the tokens may not.
	guard := regexp.MustCompile(`if\s+GTK_BACKEND\s*&&\s*auto\(\)\.is_none\(\)\s*\{`)
	blockingCall := regexp.MustCompile(`rfd::(MessageDialog|FileDialog)::new\(\)`)
	asyncCall := regexp.MustCompile(`rfd::Async(MessageDialog|FileDialog)::new\(\)`)
	queued := regexp.MustCompile(`dialog_queue\(\)\s*\.run\(`)
	returns := regexp.MustCompile(`return;\s*\}`)
	blockingBranch := regexp.MustCompile(`later\(\s*app\s*,\s*move\s*\|\|\s*\{`)

	if n, bl, as := len(guard.FindAllString(text, -1)), len(blockingCall.FindAllString(text, -1)), len(asyncCall.FindAllString(text, -1)); n != 3 || bl != 3 || as != 3 {
		t.Errorf("dialogs.rs has %d GTK guards, %d blocking dialogs and %d asynchronous ones; there must be three of each (info, ask, save)", n, bl, as)
	}
	// Each function: guard, then the asynchronous dialog handed to the queue, then a return, then the blocking one.
	for _, fn := range []string{"pub fn info_then(", "pub fn ask(", "pub fn save("} {
		i := strings.Index(text, fn)
		if i < 0 {
			t.Fatalf("dialogs.rs has no %s", fn)
		}
		body := text[i+len(fn):]
		if j := strings.Index(body, "\npub fn "); j >= 0 {
			body = body[:j]
		}
		g := guard.FindStringIndex(body)
		blk := blockingBranch.FindStringIndex(body)
		if g == nil || blk == nil {
			t.Errorf("%s must open with a GTK_BACKEND branch, and keep the blocking dialog in a later(app, ...) branch", fn)
			continue
		}
		branch := body[g[1]:blk[0]]
		a, q, r := asyncCall.FindStringIndex(branch), queued.FindStringIndex(branch), returns.FindStringIndex(branch)
		if a == nil || q == nil || r == nil || q[1] > r[0] {
			t.Errorf("%s: the GTK_BACKEND branch must give the asynchronous dialog to dialog_queue().run(...) and return, before the blocking one", fn)
		}
		if strings.Contains(branch, "async_runtime::spawn") {
			t.Errorf("%s: the GTK_BACKEND branch spawns its own task; dialogs must go through dialog_queue() so that only one is open at a time", fn)
		}
		if blockingCall.FindStringIndex(branch) != nil || !blockingCall.MatchString(body[blk[1]:]) {
			t.Errorf("%s: a blocking rfd dialog sits in the GTK_BACKEND branch, or the later(app, ...) branch has none", fn)
		}
	}
	if !strings.Contains(text, "const GTK_BACKEND: bool = cfg!(all(unix, not(target_os = \"macos\")));") {
		t.Error("GTK_BACKEND is not the Linux-and-BSD condition")
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "dialogs.rs" || !strings.HasSuffix(e.Name(), ".rs") {
			continue
		}
		o, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(o), "rfd::") {
			t.Errorf("%s uses rfd directly: dialogs go through dialogs.rs, whose GTK branch is what keeps the main thread from blocking", e.Name())
		}
	}
}
