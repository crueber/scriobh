package mirror

import (
	"os"
	"path/filepath"
)

// FS is the small slice of the file system the mirror uses. It is a seam so a
// test can fail a write between the temporary file and the rename and check
// that the visible file is never partial.
type FS interface {
	MkdirAll(path string) error
	// WriteTemp writes data to a new file in dir (created if needed), syncs it,
	// and returns its path. The file is private (0600).
	WriteTemp(dir string, data []byte) (string, error)
	Rename(oldpath, newpath string) error
	Remove(path string) error
	RemoveAll(path string) error
	ReadFile(path string) ([]byte, error)
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.DirEntry, error)
}

// OSFS is the real file system.
type OSFS struct{}

// MkdirAll creates a directory tree with mode 0700.
func (OSFS) MkdirAll(path string) error { return os.MkdirAll(path, 0o700) }

// WriteTemp writes and syncs a private temporary file.
func (OSFS) WriteTemp(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "w-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// Rename renames a file or directory.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Remove removes one file or empty directory.
func (OSFS) Remove(path string) error { return os.Remove(path) }

// RemoveAll removes a tree.
func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

// ReadFile reads a file.
func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Stat reports on a path without following a final symlink.
func (OSFS) Stat(path string) (os.FileInfo, error) { return os.Lstat(path) }

// ReadDir lists a directory.
func (OSFS) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }

// writeFileAtomic puts data at path: a temporary file in tmpDir (same file
// system, outside every project directory, so a snapshot of a project never
// sees it), then a rename. A reader sees the old file or the new one.
func writeFileAtomic(fs FS, tmpDir, path string, data []byte) error {
	if err := fs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := fs.WriteTemp(tmpDir, data)
	if err != nil {
		return err
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return err
	}
	return nil
}
