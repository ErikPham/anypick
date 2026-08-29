package db

// CurrentSchemaVersion is the newest PRAGMA user_version this build can read
// and write. Desktop handshakes publish it before any mutation is accepted.
func CurrentSchemaVersion() int { return BaselineVersion + len(migrations) }
