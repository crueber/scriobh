package pandoc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pandoc"
)

// FAILS by design (TestReviewVersionFloorIsNotBelowTheIncompleteFix): the version floor admits builds with a known arbitrary-file-write. The import always runs
// with --extract-media on untrusted files. CVE-2023-35936 (fixed 3.1.4) was fixed incompletely: CVE-2023-38745,
// "Pandoc before 3.1.6 allows arbitrary file write ... a crafted image element ... --extract-media ... an incomplete
// fix for CVE-2023-35936 (double encoded path names)". The advisory states that --sandbox, "which only affects IO
// done by readers and writers themselves, does not block this vulnerability".
func TestReviewVersionFloorIsNotBelowTheIncompleteFix(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	for _, v := range []string{"3.1.4", "3.1.5"} {
		_, bin := fakeDir(t)
		s, _ := os.ReadFile(bin)
		if err := os.WriteFile(bin, []byte(strings.ReplaceAll(string(s), "pandoc 3.1.9", "pandoc "+v)), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Locate(context.Background(), bin); err == nil {
			t.Errorf("a Pandoc %s is accepted; it has CVE-2023-38745 (arbitrary file write through --extract-media)", v)
		}
	}
}

// FAILS by design (TestReviewACallFolderIsBoundedWhileTheConverterRuns): nothing caps what a converter writes into its call folder while it runs. RLIMIT_FSIZE is per
// file (the three caps added up, 160 MiB by default), the media caps apply when the files are read afterwards, and
// the folder is under the data directory, on the database's volume. Here a converter writes 120 MB of media with
// limits of 1 MiB each and is not stopped; the folder is read while it still runs.
const diskScript = `#!/bin/sh
D="$(dirname "$0")"
case "$1" in --version) printf 'pandoc 3.1.9\n'; exit 0;; esac
mkdir -p media
i=0
while [ $i -lt 30 ]; do head -c 4000000 /dev/zero > media/f$i.png; i=$((i+1)); done
touch "$D/written"
sleep 3
echo '# ok'
`

func TestReviewACallFolderIsBoundedWhileTheConverterRuns(t *testing.T) {
	root := t.TempDir()
	dir, bin := fakeDir(t)
	if err := os.WriteFile(bin, []byte(diskScript), 0o755); err != nil {
		t.Fatal(err)
	}
	r := pandoc.NewRunner(pandoc.Limits{MaxInput: 1 << 20, MaxOutput: 1 << 20, MaxMediaFile: 1 << 20, MaxMediaTotal: 1 << 20}, root, "")
	b := locate(t, r, bin)
	done := make(chan error, 1)
	go func() { _, err := r.Import(context.Background(), b, "rst", []byte("x")); done <- err }()
	// The converter is stopped by the folder cap, either before it has finished writing (the call ends with
	// too_large) or, if it got to the end first, the folder must not hold more than the cap allows. The first
	// version of this test required "written" to appear, which fails when the process is killed in time: that is the
	// behaviour the test is for, so a call that ends in too_large is a pass.
	var size int64
	deadline := time.After(15 * time.Second)
	for {
		select {
		case err := <-done:
			if kindOf(err) != pandoc.KindTooLarge {
				t.Fatalf("the call ended with %v; the converter was not stopped for filling its folder", err)
			}
			return
		case <-deadline:
			t.Fatal("the converter was neither stopped nor finished")
		case <-time.After(50 * time.Millisecond):
		}
		if _, err := os.Stat(filepath.Join(dir, "written")); err == nil {
			break
		}
	}
	filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			size += fi.Size()
		}
		return nil
	})
	<-done
	if size > 16<<20 {
		t.Errorf("the call folder held %d MB while the converter ran, with every cap at 1 MiB", size>>20)
	}
}

// GUARD (passes today): at the runner, the same hostile names are a map miss and nothing is started.
func TestReviewRunnerRefusesHostileFormatNames(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	for _, name := range []string{"-", "--", "=", " odt", "odt ", "odt\x00", "\x00odt", "оdt", "Odt", "rst+raw_html", "latex+raw_tex", "gfm+lua", "typst+lua", "custom.lua", "odt\r\n--lua-filter=x", "gfm", "markdown", "json"} {
		if _, err := r.Import(context.Background(), b, name, []byte("x")); kindOf(err) != pandoc.KindBadFormat {
			t.Errorf("import %q: %v", name, err)
		}
		if _, err := r.Export(context.Background(), b, name, []byte("x")); kindOf(err) != pandoc.KindBadFormat {
			t.Errorf("export %q: %v", name, err)
		}
	}
	if readLog(t, dir, "args.log") != "" {
		t.Fatal("Pandoc was started for a name that is not on the list")
	}
}
