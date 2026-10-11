// Package safepath validates relative path keys that arrive from imports,
// backups, pasted logins and plugin manifests.
//
// Rules applied on every OS: the key must be non-empty; contain no control
// characters (0x00-0x1F, 0x7F); not be absolute or start with '/' or '\';
// carry no volume name, drive-letter prefix ("C:\x", "C:x") or UNC prefix;
// and have no ".." segment, with both '/' and '\' treated as separators.
//
// Windows-only rules, enforced through filepath.IsLocal when running on
// Windows: reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9),
// trailing dots or spaces, and ":" outside a drive-letter prefix. Those are
// not rejected off Windows in this commit.
package safepath

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrEmpty is returned for an empty key.
	ErrEmpty = errors.New("path is empty")
	// ErrBadName is returned for a key containing a control character.
	ErrBadName = errors.New("path contains a control character")
	// ErrNotLocal is returned for a key that is not a plain relative path.
	ErrNotLocal = errors.New("path is not a local relative path")
)

// ValidateKey reports whether key is a non-empty relative path that stays
// inside the directory it is joined to. Both '/' and '\' count as
// separators. It rejects absolute paths, a leading separator, any volume
// name (including drive-relative "C:x" and UNC "\\server\share"), any ".."
// segment and control characters (0x00-0x1F, 0x7F) on every OS; Windows
// reserved names are refused only on Windows (see the package comment).
// Names that merely start with dots, such as "..foo.mjs", are allowed.
func ValidateKey(key string) error {
	if key == "" {
		return ErrEmpty
	}
	if strings.IndexFunc(key, isControl) >= 0 {
		return ErrBadName
	}
	if key[0] == '/' || key[0] == '\\' || filepath.IsAbs(key) || filepath.VolumeName(key) != "" {
		return ErrNotLocal
	}
	if len(key) >= 2 && key[1] == ':' {
		return ErrNotLocal
	}
	segs := strings.FieldsFunc(key, isSep)
	for _, seg := range segs {
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

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// ImportRefusalMessage is the user-facing copy for an import or restore key
// that ValidateKey refused with err. Empty and control-character keys get a
// message that does not echo the key.
func ImportRefusalMessage(key string, err error) string {
	switch {
	case errors.Is(err, ErrEmpty) || errors.Is(err, ErrBadName) || key == "":
		// No raw value: the key may be empty or hold unprintable bytes.
		return "Can't use this file. It contains an entry with no usable name, so nothing was imported."
	default:
		return "Can't use " + strconv.Quote(key) + ". It points outside AnyPick's folder, so nothing was imported."
	}
}
