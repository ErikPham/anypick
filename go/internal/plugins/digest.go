package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

var digestSkipNames = map[string]bool{
	".git":        true,
	".hg":         true,
	".svn":        true,
	".DS_Store":   true,
	"__pycache__": true,
}

type packageFile struct {
	full string
	rel  string
}

// DigestEntry returns the SHA-256 of one file. Trust decisions must use
// DigestPluginPackage; this helper exists only for diagnostics and parity
// vectors.
func DigestEntry(entry string) (string, error) {
	data, err := os.ReadFile(entry)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// DigestPluginPackage computes the ADR-0014 trust pin using the exact Node
// framing: for every locale-sorted relative POSIX path, hash
// path\0byteLength\0fileBytes. Runtime dependencies under node_modules are
// included; only the same VCS/dev names as plugin-loader.ts are skipped.
func DigestPluginPackage(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	files, err := listPluginFiles(absRoot)
	if err != nil {
		return "", err
	}

	// String.localeCompare uses the root CLDR collation for the path shapes
	// accepted here. Stable sorting plus native directory enumeration also
	// preserves Node's tie behavior (for example paths differing only by case).
	collator := collate.New(language.Und)
	sort.SliceStable(files, func(i, j int) bool {
		return collator.CompareString(files[i].rel, files[j].rel) < 0
	})

	hash := sha256.New()
	for _, file := range files {
		data, err := os.ReadFile(file.full)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(file.rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(fmt.Sprintf("%d", len(data))))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func listPluginFiles(root string) ([]packageFile, error) {
	var out []packageFile
	var walk func(string) error
	walk = func(dir string) error {
		// os.File.Readdir retains filesystem enumeration order, matching
		// fs.promises.readdir. os.ReadDir would pre-sort and change stable
		// localeCompare ties.
		handle, err := os.Open(dir)
		if err != nil {
			return err
		}
		entries, readErr := handle.Readdir(-1)
		closeErr := handle.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		for _, entry := range entries {
			if digestSkipNames[entry.Name()] {
				continue
			}
			full := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			if !entry.Mode().IsRegular() && entry.Mode()&os.ModeSymlink == 0 {
				continue
			}
			// Node stat() follows a symlink, skips broken links and includes only
			// regular-file targets. Symlinked directories are never traversed.
			info, err := os.Stat(full)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			rel, err := filepath.Rel(root, full)
			if err != nil {
				return err
			}
			out = append(out, packageFile{
				full: full,
				rel:  strings.ReplaceAll(rel, string(filepath.Separator), "/"),
			})
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return out, nil
}
