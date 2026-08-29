package db

import (
	"database/sql"
	"os"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// OpenForeign opens a third-party SQLite database (e.g. kiro-cli's
// data.sqlite3) without applying AnyPick schema or migrations. Read-only mode
// refuses writes at the driver level so probes can never corrupt another
// tool's data.
func OpenForeign(path string, readOnly bool) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, anyerr.Wrapf(err, "stat "+path, "FS_PATH")
	}
	dsn := "file:" + path
	if readOnly {
		dsn += "?mode=ro&_pragma=query_only(1)"
	}
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, anyerr.Wrapf(err, "open "+path, "DB_OPEN")
	}
	d.SetMaxOpenConns(1)
	return d, nil
}
