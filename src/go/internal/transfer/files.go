package transfer

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
)

var stdBase64 = base64.StdEncoding

// CollectSnapshotFiles walks a snapshot directory into a base64 file map,
// sorted by name so envelopes are byte-stable across runs (ports
// collectSnapshotFiles).
func CollectSnapshotFiles(root string) map[string]string {
	out := map[string]string{}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return out
	}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		names := make([]string, 0, len(entries))
		byName := map[string]os.DirEntry{}
		for _, e := range entries {
			names = append(names, e.Name())
			byName[e.Name()] = e
		}
		sort.Strings(names)
		for _, name := range names {
			entry := byName[name]
			full := filepath.Join(dir, name)
			rel, rerr := filepath.Rel(root, full)
			if rerr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if entry.IsDir() {
				walk(full)
				continue
			}
			if !entry.Type().IsRegular() {
				continue // symlinks and specials never enter an envelope
			}
			data, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			out[rel] = stdBase64.EncodeToString(data)
		}
	}
	walk(root)
	return out
}

// WriteSnapshotFiles stages decoded files under root, re-validating every key.
func WriteSnapshotFiles(root string, files map[string]string) error {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		dest, err := StagedFilePath(root, rel)
		if err != nil {
			return err
		}
		data, derr := stdBase64.DecodeString(files[rel])
		if derr != nil {
			return importFormat("Import file %q is not valid base64.", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
