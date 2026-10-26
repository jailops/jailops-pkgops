// Package fsutil holds the few filesystem helpers shared by the internal packages.
package fsutil

import (
	"io"
	"os"
	"path/filepath"
)

// AtomicWrite writes via a temporary file in the same directory and renames it
// into place so readers never observe a partial file.
func AtomicWrite(filename string, perm os.FileMode, fn func(io.Writer) error) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(filename)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := fn(tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filename); err != nil {
		return err
	}
	ok = true
	return nil
}
