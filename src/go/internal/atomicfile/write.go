// Package atomicfile replaces a file by rename rather than by truncation.
//
// AnyPick writes other tools' credential files. Those are files the user owns
// and AnyPick cannot re-create: a truncated write interrupted by SIGKILL or
// power loss leaves a login empty or half-written, with nothing to fall back on.
// This package is the one way to write one, and write_test.go fails the build if
// a second hand-rolled implementation appears.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write stages data beside path, then renames it into place.
//
// The temporary file is created in the target's own directory because rename
// cannot cross a filesystem. It is removed on every failure path; a stray temp
// file is harmless, a truncated credential file is not.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".anypick-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
