// Package safepath validates relative path keys that arrive from imports,
// backups, pasted logins and plugin manifests. The rules are the same on
// every OS, so a key accepted on Linux or macOS is also safe on Windows.
package safepath

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrNotLocal is returned for any key that is not a plain relative path.
var ErrNotLocal = errors.New("path is not a local relative path")

// ValidateKey reports whether key is a non-empty relative path that stays
// inside the directory it is joined to. Both '/' and '\' count as
// separators. It rejects absolute paths, a leading separator, any volume
// name (including drive-relative "C:x" and UNC "\\server\share"), NUL bytes,
// and any ".." segment. Names that merely start with dots, such as
// "..foo.mjs", are allowed.
func ValidateKey(key string) error {
	if key == "" || strings.ContainsRune(key, 0) {
		return ErrNotLocal
	}
	if key[0] == '/' || key[0] == '\\' || filepath.IsAbs(key) || filepath.VolumeName(key) != "" {
		return ErrNotLocal
	}
	// A colon in the first segment is a drive or stream prefix on Windows.
	if len(key) >= 2 && key[1] == ':' {
		return ErrNotLocal
	}
	for _, seg := range strings.FieldsFunc(key, isSep) {
		if seg == ".." {
			return ErrNotLocal
		}
	}
	if !filepath.IsLocal(key) || !filepath.IsLocal(strings.ReplaceAll(key, `\`, "/")) {
		return ErrNotLocal
	}
	return nil
}

// ValidateName is ValidateKey for a single file name: no separators at all.
func ValidateName(name string) error {
	if err := ValidateKey(name); err != nil {
		return err
	}
	if strings.ContainsAny(name, `/\`) || name == "." {
		return ErrNotLocal
	}
	return nil
}

func isSep(r rune) bool { return r == '/' || r == '\\' }
