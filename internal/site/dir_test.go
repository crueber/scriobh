package site

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tf(kv ...string) []File {
	var out []File
	for i := 0; i < len(kv); i += 2 {
		out = append(out, File{Path: kv[i], Data: []byte(kv[i+1])})
	}
	return out
}

func readAll(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			r, _ := filepath.Rel(dir, p)
			m[filepath.ToSlash(r)] = string(b)
		}
		return nil
	})
	return m
}

func TestWriteDirFirstAndSecondPublish(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	name := DirName("My Blog", "prj_1")
	r, err := WriteDir(root, name, "prj_1", "h1", tf("index.html", "one", "assets/site.css", "css", "posts/a.html", "a"))
	if err != nil || r.Replaced || r.Files != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	pub := filepath.Join(root, name, "public")
	if got := readAll(t, pub); got["posts/a.html"] != "a" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	// An unrelated file beside the project folder and one in the root survive.
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "notes.txt"), []byte("mine too"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = WriteDir(root, name, "prj_1", "h2", tf("index.html", "two"))
	if err != nil || !r.Replaced {
		t.Fatalf("%+v %v", r, err)
	}
	if got := readAll(t, pub); len(got) != 1 || got["index.html"] != "two" {
		t.Fatalf("the old files were not replaced: %v", got)
	}
	for _, p := range []string{filepath.Join(root, "keep.txt"), filepath.Join(root, name, "notes.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted", p)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(root, name))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".public.") || e.Name() == "managed.json.new" {
			t.Errorf("leftover %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(pub, "index.html"))
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("file mode %v", fi.Mode())
		}
		fi, _ = os.Stat(pub)
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("dir mode %v", fi.Mode())
		}
		fi, _ = os.Stat(filepath.Join(pub, "..", "managed.json"))
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("manifest mode %v", fi.Mode())
		}
	}
	if n, _ := FindDir(root, "prj_1"); n != name {
		t.Errorf("FindDir = %q", n)
	}
	if n, _ := FindDir(root, "prj_other"); n != "" {
		t.Errorf("FindDir found another project's folder: %q", n)
	}
}

func TestWriteDirRefusesWhatIsNotItsOwn(t *testing.T) {
	root := t.TempDir()
	name := "blog--000001"
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "x")); err != nil {
		t.Fatal(err)
	}
	pub := filepath.Join(root, name, "public")
	// Someone adds a file to the served folder.
	if err := os.WriteFile(filepath.Join(pub, "robots-extra.txt"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WriteDir(root, name, "p1", "h", tf("index.html", "y"))
	var um *ErrUnmanaged
	if !errors.As(err, &um) || um.Paths[0] != "robots-extra.txt" {
		t.Fatalf("got %v", err)
	}
	if got := readAll(t, pub); got["index.html"] != "x" || got["robots-extra.txt"] != "theirs" {
		t.Fatalf("a refused publish changed the folder: %v", got)
	}
	// Another project may not take over the folder.
	_ = os.Remove(filepath.Join(pub, "robots-extra.txt"))
	if _, err := WriteDir(root, name, "p2", "h", tf("index.html", "y")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("another project's publish: %v", err)
	}
	// A public folder with files and no record is not ours.
	root2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root2, name, "public"), 0o755)
	_ = os.WriteFile(filepath.Join(root2, name, "public", "index.html"), []byte("hand written"), 0o644)
	if _, err := WriteDir(root2, name, "p1", "h", tf("index.html", "y")); err == nil {
		t.Fatal("a folder with someone's files and no record was replaced")
	}
	if got := readAll(t, filepath.Join(root2, name, "public")); got["index.html"] != "hand written" {
		t.Fatal("it was changed")
	}
	// An empty public folder is fine.
	root3 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root3, name, "public"), 0o755)
	if _, err := WriteDir(root3, name, "p1", "h", tf("index.html", "y")); err != nil {
		t.Fatalf("an empty folder: %v", err)
	}
}

func TestWriteDirNeverFollowsALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "precious.txt")
	_ = os.WriteFile(marker, []byte("precious"), 0o644)
	name := "blog--000001"
	// public is a link out of the tree.
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, name), 0o755)
	_ = os.Symlink(outside, filepath.Join(root, name, "public"))
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "y")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("a symlinked public folder: %v", err)
	}
	// the project folder is a link.
	root = t.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, name))
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "y")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("a symlinked project folder: %v", err)
	}
	// a link inside a published tree.
	root = t.TempDir()
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "x")); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(root, name, "public", "escape"))
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "y")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("a link inside the tree: %v", err)
	}
	if err := RemoveDir(root, name, "p1"); err == nil {
		t.Error("RemoveDir went through a link")
	}
	if b, _ := os.ReadFile(marker); string(b) != "precious" {
		t.Fatal("a file outside the tree was touched")
	}
	// A symlinked root is the administrator's choice and is followed once.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "sites")
	_ = os.Symlink(real, link)
	if _, err := WriteDir(link, name, "p1", "h", tf("index.html", "x")); err != nil {
		t.Fatalf("a symlinked root: %v", err)
	}
	if got := readAll(t, filepath.Join(real, name, "public")); got["index.html"] != "x" {
		t.Fatal("not written through the root link")
	}
}

func TestWriteDirRejectsHostilePaths(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"../escape.html", "/abs.html", "a/../../b.html", "a\\b.html", ".hidden", "a/.git/x", "", "a//b", "nul\x00.html"} {
		if _, err := WriteDir(root, "blog--000001", "p1", "h", tf(p, "x")); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	for _, n := range []string{"", "../x", "a/b", ".hid", "a\\b"} {
		if _, err := WriteDir(root, n, "p1", "h", tf("index.html", "x")); err == nil {
			t.Errorf("folder name %q accepted", n)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape.html")); err == nil {
		t.Error("a file escaped")
	}
}

func TestRemoveDirTakesOnlyWhatItWrote(t *testing.T) {
	root := t.TempDir()
	name := "blog--000001"
	if _, err := WriteDir(root, name, "p1", "h", tf("index.html", "x")); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, name, "notes.txt"), []byte("mine"), 0o644)
	if err := RemoveDir(root, name, "p2"); err != nil { // another project: nothing happens
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, name, "public", "index.html")); err != nil {
		t.Fatal("another project's removal deleted the site")
	}
	if err := RemoveDir(root, name, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, name, "public")); err == nil {
		t.Error("the site is still there")
	}
	if _, err := os.Stat(filepath.Join(root, name, "notes.txt")); err != nil {
		t.Error("an unrelated file was removed")
	}
}

func TestIDTailAndDirName(t *testing.T) {
	if IDTail("prj_01HX7ZABCDEFGH12") != "efgh12" {
		t.Errorf("%s", IDTail("prj_01HX7ZABCDEFGH12"))
	}
	if n := DirName("../../Evil/Name", "p"); strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") {
		t.Errorf("%s", n)
	}
	if n := DirName("🙂", "x"); !strings.HasPrefix(n, "site--") {
		t.Errorf("%s", n)
	}
}
