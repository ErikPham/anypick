package app

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}
