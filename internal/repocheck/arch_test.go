package repocheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Arch package recipes (desktop/arch/PKGBUILD, which the release makes, and desktop/arch/aur/PKGBUILD with its
// .SRCINFO, which is staged for a future AUR package) are shell and text that nothing else reads until a person on
// Arch builds them. These tests keep what can be kept without Arch: the version agrees with the app's, the two
// recipes install the same way and depend on the same libraries, the .SRCINFO says what the PKGBUILD says, and a
// tray icon cannot be added to the shell without the recipe being made to depend on its library.

func archFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pkgVar returns the value of a one-line NAME=value assignment of a PKGBUILD, quotes removed.
func pkgVar(t *testing.T, text, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(.*?)\s*(?:#.*)?$`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no %s= line in the PKGBUILD", name)
	}
	return strings.Trim(m[1], `'"`)
}

// pkgArray returns the words of a one-line NAME=(...) array.
func pkgArray(t *testing.T, text, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=\((.*)\)`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no %s=(...) line in the PKGBUILD", name)
	}
	var out []string
	for _, w := range regexp.MustCompile(`'[^']*'|"[^"]*"|\S+`).FindAllString(m[1], -1) {
		out = append(out, strings.Trim(w, `'"`))
	}
	return out
}

// packageBody is the package() function, to its closing brace in column 0.
func packageBody(t *testing.T, text string) string {
	t.Helper()
	i := strings.Index(text, "package() {")
	if i < 0 {
		t.Fatal("no package() in the PKGBUILD")
	}
	j := strings.Index(text[i:], "\n}\n")
	if j < 0 {
		t.Fatal("package() is not closed in column 0")
	}
	return text[i : i+j+3]
}

func tauriVersion(t *testing.T) string {
	t.Helper()
	var conf struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(archFile(t, "desktop/src-tauri/tauri.conf.json")), &conf); err != nil {
		t.Fatal(err)
	}
	return conf.Version
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestArchRecipesTrackTheAppVersion(t *testing.T) {
	ver := tauriVersion(t)
	// pkgver may not hold a hyphen, so a prerelease 2.3.0-rc.1 is the pkgver 2.3.0_rc.1; the .deb's file name keeps it.
	wantPkgver := strings.ReplaceAll(ver, "-", "_")
	for _, rel := range []string{"desktop/arch/PKGBUILD", "desktop/arch/aur/PKGBUILD"} {
		text := archFile(t, rel)
		if got := pkgVar(t, text, "pkgver"); got != wantPkgver {
			t.Errorf("%s: pkgver is %q, but desktop/src-tauri/tauri.conf.json says %q (pkgver %q; the version is in four places that must agree)", rel, got, ver, wantPkgver)
		}
	}
	// The AUR recipe builds the .deb's file name from _debver, the original, never from pkgver: for a prerelease the two
	// differ (2.3.0-rc.1 and 2.3.0_rc.1) and the file is Scriobh_2.3.0-rc.1_amd64.deb.
	aur := archFile(t, "desktop/arch/aur/PKGBUILD")
	if got := pkgVar(t, aur, "_debver"); got != ver {
		t.Errorf("desktop/arch/aur/PKGBUILD: _debver is %q, want %q (the version as the .deb's name has it)", got, ver)
	}
	if got, want := pkgVar(t, aur, "_deb"), "Scriobh_${_debver}_amd64.deb"; got != want {
		t.Errorf("desktop/arch/aur/PKGBUILD: _deb is %q, want %q (not built from pkgver, which maps - to _)", got, want)
	}
	if got, want := pkgVar(t, aur, "_tag"), "v"+ver; got != want {
		t.Errorf("desktop/arch/aur/PKGBUILD: _tag is %q, want %q", got, want)
	}
	// The name the Tauri bundler gives the .deb: <product name>_<version>_<arch>.deb.
	if got, want := pkgVar(t, archFile(t, "desktop/arch/PKGBUILD"), "_deb"), "Scriobh_"+ver+"_amd64.deb"; got != want {
		t.Errorf("desktop/arch/PKGBUILD: _deb is %q, want %q", got, want)
	}
}

func TestArchRecipesInstallTheSameWay(t *testing.T) {
	a := archFile(t, "desktop/arch/PKGBUILD")
	b := archFile(t, "desktop/arch/aur/PKGBUILD")
	if packageBody(t, a) != packageBody(t, b) {
		t.Error("the package() functions of desktop/arch/PKGBUILD and desktop/arch/aur/PKGBUILD differ: one recipe was changed and the other not")
	}
	for _, name := range []string{"depends", "optdepends", "options"} {
		if x, y := strings.Join(pkgArray(t, a, name), "\n"), strings.Join(pkgArray(t, b, name), "\n"); x != y {
			t.Errorf("%s differs between the two recipes:\n%s\nvs\n%s", name, x, y)
		}
	}
	if pkgVar(t, a, "pkgname") != "scriobh-desktop" || pkgVar(t, b, "pkgname") != "scriobh-desktop-bin" {
		t.Error("the release recipe is scriobh-desktop and the AUR one scriobh-desktop-bin")
	}
}

// A tray icon makes the shell load libayatana-appindicator (or libappindicator) at run time; the recipe says it is not
// needed because the shell has none. Adding the tray feature must change the recipe in the same commit.
func TestArchDependsFollowTheTrayFeature(t *testing.T) {
	cargo := archFile(t, "desktop/src-tauri/Cargo.toml")
	deps := strings.Join(pkgArray(t, archFile(t, "desktop/arch/PKGBUILD"), "depends"), " ")
	hasIndicator := strings.Contains(deps, "appindicator")
	if strings.Contains(cargo, "tray-icon") && !hasIndicator {
		t.Error("the shell enables a tray icon, so the Arch recipes must depend on libayatana-appindicator")
	}
	for _, need := range []string{"webkit2gtk-4.1", "gtk3"} {
		if !strings.Contains(" "+deps+" ", " "+need+" ") {
			t.Errorf("depends lacks %s", need)
		}
	}
}

func TestArchSrcinfoSaysWhatThePKGBUILDSays(t *testing.T) {
	pk := archFile(t, "desktop/arch/aur/PKGBUILD")
	info := archFile(t, "desktop/arch/aur/.SRCINFO")
	field := func(name string) []string {
		var out []string
		for _, l := range strings.Split(info, "\n") {
			l = strings.TrimSpace(l)
			if v, ok := strings.CutPrefix(l, name+" = "); ok {
				out = append(out, v)
			}
		}
		return out
	}
	one := func(name string) string {
		v := field(name)
		if len(v) != 1 {
			t.Fatalf(".SRCINFO has %d %s lines, want 1", len(v), name)
		}
		return v[0]
	}
	if got := one("pkgver"); got != pkgVar(t, pk, "pkgver") {
		t.Errorf(".SRCINFO pkgver %q, PKGBUILD %q", got, pkgVar(t, pk, "pkgver"))
	}
	if got := one("pkgrel"); got != pkgVar(t, pk, "pkgrel") {
		t.Errorf(".SRCINFO pkgrel %q, PKGBUILD %q", got, pkgVar(t, pk, "pkgrel"))
	}
	if got := one("url"); got != pkgVar(t, pk, "url") {
		t.Errorf(".SRCINFO url %q, PKGBUILD %q", got, pkgVar(t, pk, "url"))
	}
	if got, want := strings.Join(field("depends"), " "), strings.Join(pkgArray(t, pk, "depends"), " "); got != want {
		t.Errorf(".SRCINFO depends %q, PKGBUILD %q", got, want)
	}
	if got := field("pkgname"); len(got) != 1 || got[0] != pkgVar(t, pk, "pkgname") {
		t.Errorf(".SRCINFO pkgname %v, PKGBUILD %q", got, pkgVar(t, pk, "pkgname"))
	}
	ver, tag := pkgVar(t, pk, "_debver"), pkgVar(t, pk, "_tag") // the file name's version, not pkgver
	src := field("source")
	wantSrc := "Scriobh_" + ver + "_amd64.deb::" + pkgVar(t, pk, "url") + "/releases/download/" + tag + "/Scriobh_" + ver + "_amd64.deb"
	if len(src) != 3 || src[0] != wantSrc || src[1] != "LICENSE" || src[2] != "NOTICE" {
		t.Errorf(".SRCINFO sources %v, want [%s LICENSE NOTICE]", src, wantSrc)
	}
	sums := field("sha256sums")
	if len(sums) != 3 {
		t.Fatalf(".SRCINFO has %d sha256sums, want 3", len(sums))
	}
	if sp := pkgArray(t, pk, "sha256sums"); strings.Join(sp, " ") != strings.Join(sums, " ") {
		t.Errorf("sha256sums differ: PKGBUILD %v, .SRCINFO %v", sp, sums)
	}
	// The two local files are the repository's own, unedited: a changed LICENSE or NOTICE needs the sums renewed.
	if want := sha(archFile(t, "LICENSE")); sums[1] != want {
		t.Errorf("the LICENSE sum is %s, but the repository's LICENSE is %s: update desktop/arch/aur/PKGBUILD and .SRCINFO", sums[1], want)
	}
	if want := sha(archFile(t, "NOTICE")); sums[2] != want {
		t.Errorf("the NOTICE sum is %s, but the repository's NOTICE is %s: update desktop/arch/aur/PKGBUILD and .SRCINFO", sums[2], want)
	}
	// The deb's sum is a placeholder until a public release has the file; makepkg refuses it, which is the point.
	// Once a real one is written, it must look like one.
	if s := sums[0]; s != "REPLACE_WITH_SHA256_OF_THE_DEB" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) {
		t.Errorf("the .deb's sum %q is neither the placeholder nor a SHA-256", s)
	}
}
