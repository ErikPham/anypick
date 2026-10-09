// Package journal implements the operation journal state
// machine over the operation_journal table.
package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

type State string

const (
	StatePlanned     State = "planned"
	StateExecuting   State = "executing"
	StateVerifying   State = "verifying"
	StateCommitted   State = "committed"
	StateRollingBack State = "rolling_back"
	StateRolledBack  State = "rolled_back"
	StateFailed      State = "failed"
)

// Entry is one operation journal row. Params may carry credential-adjacent
// metadata; keep it out of error messages.
type Entry struct {
	ID                string                 `json:"id"`
	Type              string                 `json:"type"`
	State             State                  `json:"state"`
	AffectedResources []string               `json:"affectedResources"`
	BackupPaths       []string               `json:"backupPaths"`
	Params            map[string]interface{} `json:"params,omitempty"`
	StartedAt         string                 `json:"startedAt"`
	UpdatedAt         string                 `json:"updatedAt"`

	// Corrupt is set when a stored column could not be parsed. The affected
	// resources are unknown in that case, which is NOT the same as empty: a
	// recovery that read them as empty would leave a half-applied mutation in
	// place while reporting that it had nothing to undo. It is not serialized —
	// it describes this read, not the row.
	Corrupt bool `json:"-"`
}

// OperationJournal is the CRUD surface over operation_journal.
type OperationJournal struct {
	DB *sql.DB
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptorandRead(b); err != nil {
		return "", err
	}
	// Format as UUID v4.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b), nil
}

func formatUUID(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out)
}

type CreateOptions struct {
	ID                string
	AffectedResources []string
	BackupPaths       []string
	Params            map[string]interface{}
	State             State
}

func (j *OperationJournal) CreateContext(ctx context.Context, entryType string, opts CreateOptions) (Entry, error) {
	ts := nowISO()
	state := opts.State
	if state == "" {
		state = StatePlanned
	}
	id := opts.ID
	if id == "" {
		var err error
		id, err = newID()
		if err != nil {
			return Entry{}, anyerr.Wrapf(err, "generate journal id", "DB_WRITE")
		}
	}
	affected := opts.AffectedResources
	if affected == nil {
		affected = []string{}
	}
	backupPaths := opts.BackupPaths
	if backupPaths == nil {
		backupPaths = []string{}
	}
	entry := Entry{
		ID:                id,
		Type:              entryType,
		State:             state,
		AffectedResources: affected,
		BackupPaths:       backupPaths,
		Params:            opts.Params,
		StartedAt:         ts,
		UpdatedAt:         ts,
	}
	var paramsJSON interface{}
	if entry.Params != nil {
		paramsJSON = marshalParams(entry.Params)
	}
	_, err := j.DB.ExecContext(ctx,
		`INSERT INTO operation_journal
		   (id, type, state, affected_resources_json, backup_paths_json, params_json, started_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Type, string(entry.State),
		mustJSON(entry.AffectedResources), mustJSON(entry.BackupPaths),
		paramsJSON, entry.StartedAt, entry.UpdatedAt)
	if err != nil {
		return Entry{}, anyerr.Wrapf(err, "journal insert", "DB_WRITE")
	}
	return entry, nil
}

func (j *OperationJournal) Get(id string) (*Entry, error) {
	return j.GetContext(context.Background(), id)
}

func (j *OperationJournal) GetContext(ctx context.Context, id string) (*Entry, error) {
	row := j.DB.QueryRowContext(ctx,
		`SELECT id, type, state, affected_resources_json, backup_paths_json,
		        params_json, started_at, updated_at
		 FROM operation_journal WHERE id = ?`, id)
	return scanEntry(row)
}

type rowScanner interface{ Scan(dest ...any) error }

func scanEntry(row rowScanner) (*Entry, error) {
	var e Entry
	var state string
	var affectedRaw, backupRaw string
	var paramsRaw sql.NullString
	err := row.Scan(&e.ID, &e.Type, &state, &affectedRaw, &backupRaw, &paramsRaw,
		&e.StartedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.State = State(state)
	// A column that parses fails the whole entry's trustworthiness. Unparsable
	// JSON is corruption, not an empty list: the two mean opposite things to
	// recovery (ADR 0004).
	if !decodeStoredList(affectedRaw, &e.AffectedResources) {
		e.Corrupt = true
	}
	if !decodeStoredList(backupRaw, &e.BackupPaths) {
		e.Corrupt = true
	}
	if paramsRaw.Valid && paramsRaw.String != "" {
		if err := json.Unmarshal([]byte(paramsRaw.String), &e.Params); err != nil {
			e.Corrupt = true
		}
	}
	if e.AffectedResources == nil {
		e.AffectedResources = []string{}
	}
	if e.BackupPaths == nil {
		e.BackupPaths = []string{}
	}
	return &e, nil
}

// decodeStoredList reports whether a stored JSON array column parsed. An empty
// column is a row written before that field existed, which is not corruption.
func decodeStoredList(raw string, into *[]string) bool {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return true
	}
	return json.Unmarshal([]byte(raw), into) == nil
}

// UpdateContext applies a patch: zero-value fields keep the stored value.
func (j *OperationJournal) UpdateContext(ctx context.Context, id string, patch Entry) (Entry, error) {
	existing, err := j.GetContext(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if existing == nil {
		return Entry{}, anyerr.Newf("Journal entry not found: "+id, "NOT_FOUND")
	}
	next := *existing
	if patch.State != "" {
		next.State = patch.State
	}
	if patch.BackupPaths != nil {
		next.BackupPaths = patch.BackupPaths
	}
	if patch.AffectedResources != nil {
		next.AffectedResources = patch.AffectedResources
	}
	if patch.Params != nil {
		next.Params = patch.Params
	}
	next.UpdatedAt = nowISO()
	var paramsJSON interface{}
	if next.Params != nil {
		paramsJSON = marshalParams(next.Params)
	}
	_, err = j.DB.ExecContext(ctx,
		`UPDATE operation_journal SET
		   state = ?, affected_resources_json = ?, backup_paths_json = ?,
		   params_json = ?, updated_at = ?
		 WHERE id = ?`,
		string(next.State), mustJSON(next.AffectedResources), mustJSON(next.BackupPaths),
		paramsJSON, next.UpdatedAt, id)
	if err != nil {
		return Entry{}, err
	}
	return next, nil
}

// ListIncomplete returns entries that may need recovery on startup.
func (j *OperationJournal) ListIncomplete() ([]Entry, error) {
	return j.ListIncompleteContext(context.Background())
}

func (j *OperationJournal) ListIncompleteContext(ctx context.Context) ([]Entry, error) {
	return j.queryContext(ctx,
		`SELECT id, type, state, affected_resources_json, backup_paths_json,
		        params_json, started_at, updated_at
		 FROM operation_journal
		 WHERE state IN ('planned', 'executing', 'verifying', 'rolling_back')
		 ORDER BY started_at`)
}

func (j *OperationJournal) ListRecent(limit int) ([]Entry, error) {
	return j.ListRecentContext(context.Background(), limit)
}

func (j *OperationJournal) ListRecentContext(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	return j.queryContext(ctx,
		`SELECT id, type, state, affected_resources_json, backup_paths_json,
		        params_json, started_at, updated_at
		 FROM operation_journal
		 ORDER BY started_at DESC LIMIT ?`, limit)
}

// No ctx-less query wrapper: a fresh context.Background() here cannot see a
// lock the caller holds, which is the re-entrancy trap ADR 0009 documents.
// Callers pass their context to the exported methods.

func (j *OperationJournal) queryContext(ctx context.Context, q string, args ...any) ([]Entry, error) {
	rows, err := j.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Inputs are controlled ([]string / JSON-safe maps); marshaling
		// cannot fail in practice. Return a safe fallback instead of
		// crashing the process on corrupt in-memory state.
		return "[]"
	}
	return string(b)
}

func marshalParams(p map[string]interface{}) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}
