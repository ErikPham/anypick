// Legacy data-root migration: ports migrateLegacyRootIfPristine from
// src/core/db.ts. The pre-rename `~/.hotplug` store is copied into a pristine
// AnyPick root exactly once; the original stays intact.
package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// bootstrapFiles are the artifacts an interrupted first launch leaves behind;
// their presence alone does not make a destination "dirty".
var bootstrapFiles = map[string]bool{
	"anypick.db":     true,
	"anypick.db-shm": true,
	"anypick.db-wal": true,
	".migrate.lock":  true,
	"hotplug.db":     true,
	"hotplug.db-shm": true,
	"hotplug.db-wal": true,
}

// bootstrapTables hold no user data.
var bootstrapTables = map[string]bool{"config": true, "meta": true}

func hasUserData(dbPath string) bool {
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	d, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return true // malformed/inaccessible: never unsafe to overwrite
	}
	defer d.Close()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return true
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			continue
		}
		if strings.HasPrefix(name, "sqlite_") || bootstrapTables[name] {
			continue
		}
		var count int
		if d.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, name)).Scan(&count) == nil && count > 0 {
			return true
		}
	}
	return false
}

func dirEntries(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// MigrateLegacyRootIfPristine copies the legacy ~/.hotplug root into root
// while the destination only holds bootstrap artifacts. Returns whether the
// migration ran.
func MigrateLegacyRootIfPristine(root string) error {
	parent := filepath.Dir(root)
	legacyRoot := filepath.Join(parent, ".hotplug")
	if st, err := os.Stat(legacyRoot); err != nil || !st.IsDir() {
		return nil
	}
	for _, name := range dirEntries(root) {
		if !bootstrapFiles[name] {
			return nil
		}
	}
	sourceDB := ""
	for _, candidate := range []string{"hotplug.db", "anypick.db"} {
		p := filepath.Join(legacyRoot, candidate)
		if _, err := os.Stat(p); err == nil {
			sourceDB = p
			break
		}
	}
	if sourceDB == "" || !hasUserData(sourceDB) || hasUserData(filepath.Join(root, "anypick.db")) {
		return nil
	}

	stagingRoot := fmt.Sprintf("%s.migration-%d", root, os.Getpid())
	backupRoot := fmt.Sprintf("%s.bootstrap-%d", root, os.Getpid())
	_ = os.RemoveAll(stagingRoot)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return fmt.Errorf("legacy migration staging: %w", err)
	}
	fail := func(err error) error {
		_ = os.RemoveAll(stagingRoot)
		return err
	}

	// Copy everything except the SQLite databases themselves.
	err := filepath.WalkDir(legacyRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		base := filepath.Base(path)
		rel, _ := filepath.Rel(legacyRoot, path)
		if d.IsDir() {
			if base != filepath.Base(legacyRoot) {
				return os.MkdirAll(filepath.Join(stagingRoot, rel), 0o700)
			}
			return nil
		}
		switch base {
		case "anypick.db", "anypick.db-shm", "anypick.db-wal", "hotplug.db", "hotplug.db-shm", "hotplug.db-wal":
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(stagingRoot, rel), data, 0o600)
	})
	if err != nil {
		return fail(err)
	}

	// Snapshot the authoritative database with VACUUM INTO so WAL state is
	// captured consistently (same mechanism as TS).
	src, err := sql.Open("sqlite", sourceDB+"?mode=ro")
	if err != nil {
		return fail(err)
	}
	_, verr := src.Exec("VACUUM INTO '" + strings.ReplaceAll(filepath.Join(stagingRoot, "anypick.db"), "'", "''") + "'")
	src.Close()
	if verr != nil {
		return fail(fmt.Errorf("vacuum into staging: %w", verr))
	}
	_ = os.Chmod(filepath.Join(stagingRoot, "anypick.db"), 0o600)
	lockData, lerr := os.ReadFile(filepath.Join(root, ".migrate.lock"))
	if lerr == nil {
		_ = os.WriteFile(filepath.Join(stagingRoot, ".migrate.lock"), lockData, 0o600)
	} else {
		_ = os.WriteFile(filepath.Join(stagingRoot, ".migrate.lock"), nil, 0o600)
	}

	if _, err := os.Stat(root); err == nil {
		if rerr := os.Rename(root, backupRoot); rerr != nil {
			return fail(rerr)
		}
	}
	if rerr := os.Rename(stagingRoot, root); rerr != nil {
		if _, statErr := os.Stat(backupRoot); statErr == nil {
			_ = os.Rename(backupRoot, root)
		}
		return fail(rerr)
	}
	_ = os.RemoveAll(backupRoot)
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return platformProcessAlive(pid)
}
