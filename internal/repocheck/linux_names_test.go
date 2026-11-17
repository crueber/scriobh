package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// desktop/scripts/linux-deb.sh names the published .deb and AppImage for the release tag (Scriobh_<tag without v>_amd64.*)
// when VERSION is a tag, as the Windows and macOS scripts name theirs, and leaves the bundler's names alone otherwise.
// Its `name` step runs here against a fake tree and a fake bundle folder: no Rust, no Tauri, only the naming.

func fakeLinuxBundle(t *testing.T, appVersion string) (src, deb, img string) {
	t.Helper()
	src = t.TempDir()
	conf := filepath.Join(src, "desktop", "src-tauri")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "{\n  \"productName\": \"Scriobh\",\n  \"version\": \"" + appVersion + "\"\n}\n"
	if err := os.WriteFile(filepath.Join(conf, "tauri.conf.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(conf, "target", "release", "bundle")
	for _, d := range []string{"deb", "appimage"} {
		if err := os.MkdirAll(filepath.Join(bundle, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deb = filepath.Join(bundle, "deb", "Scriobh_"+appVersion+"_amd64.deb")
	img = filepath.Join(bundle, "appimage", "Scriobh_"+appVersion+"_amd64.AppImage")
	for _, f := range []string{deb, img, img + ".sig"} {
		if err := os.WriteFile(f, []byte(filepath.Base(f)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src, deb, img
}

func runLinuxName(t *testing.T, src, version string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	cmd := exec.Command(bash, filepath.Join(repoRoot(t), "desktop", "scripts", "linux-deb.sh"), "name")
	cmd.Env = append(os.Environ(), "SRC_DIR="+src, "VERSION="+version)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestLinuxPackagesAreNamedForTheTag(t *testing.T) {
	src, deb, img := fakeLinuxBundle(t, "2.2.0")
	out, err := runLinuxName(t, src, "v2.2.1")
	if err != nil {
		t.Fatalf("linux-deb.sh name: %v\n%s", err, out)
	}
	dir, adir := filepath.Dir(deb), filepath.Dir(img)
	for _, want := range []string{
		filepath.Join(dir, "Scriobh_2.2.1_amd64.deb"),
		filepath.Join(adir, "Scriobh_2.2.1_amd64.AppImage"),
		filepath.Join(adir, "Scriobh_2.2.1_amd64.AppImage.sig"),
	} {
		if !exists(want) {
			t.Errorf("%s was not made\n%s", want, out)
		}
	}
	for _, gone := range []string{deb, img, img + ".sig"} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	if !strings.Contains(out, "WARNING: the release is v2.2.1 but the app inside says 2.2.0") {
		t.Errorf("a tag that is not the app's version did not warn:\n%s", out)
	}
	// A second run changes nothing and does not fail.
	if out, err := runLinuxName(t, src, "v2.2.1"); err != nil {
		t.Errorf("a second run failed: %v\n%s", err, out)
	}
}

func TestLinuxPackagesNamedForTheTagWithoutWarningWhenTheyAgree(t *testing.T) {
	src, deb, _ := fakeLinuxBundle(t, "2.2.0")
	out, err := runLinuxName(t, src, "v2.2.0")
	if err != nil {
		t.Fatalf("linux-deb.sh name: %v\n%s", err, out)
	}
	if strings.Contains(out, "WARNING") || !exists(deb) {
		t.Errorf("a tag equal to the app's version warned or renamed:\n%s", out)
	}
}

func TestLinuxPackagesKeepTheBundlersNamesWithoutATag(t *testing.T) {
	for _, v := range []string{"", "dev-abc1234", "main"} {
		src, deb, img := fakeLinuxBundle(t, "2.2.0")
		if out, err := runLinuxName(t, src, v); err != nil || strings.Contains(out, "WARNING") {
			t.Fatalf("VERSION %q: %v\n%s", v, err, out)
		}
		if !exists(deb) || !exists(img) || !exists(img+".sig") {
			t.Errorf("VERSION %q renamed the bundler's files", v)
		}
	}
}

func TestLinuxNamingRefusesAFileItDoesNotKnow(t *testing.T) {
	src, deb, _ := fakeLinuxBundle(t, "2.2.0")
	odd := filepath.Join(filepath.Dir(deb), "something-else.deb")
	if err := os.Rename(deb, odd); err != nil {
		t.Fatal(err)
	}
	if out, err := runLinuxName(t, src, "v2.2.1"); err == nil {
		t.Errorf("a .deb named by a bundler this script does not know was renamed or passed over:\n%s", out)
	}
}
