package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
)

// AccountStore is the SQLite-backed account store (src/core/store.ts).
// Structured data lives in <root>/anypick.db; snapshot bytes are BLOBs
// materialized to a cache dir when providers need a real filesystem path.
type AccountStore struct {
	Root string
	DB   *db.DB

	mu       sync.Mutex
	prepared map[string]map[string]bool // key provider\x00name → staging dirs
}

func New(root string, database *db.DB) *AccountStore {
	return &AccountStore{
		Root:     paths.Root(root),
		DB:       database,
		prepared: map[string]map[string]bool{},
	}
}

// mutate runs fn under the provider-scoped coordinator lock (ADR 0011),
// acquiring it when ctx does not already hold it. Re-entrancy means a caller
// that already locked the same provider simply re-enters, so services can keep
// wrapping broader sequences without deadlocking, while a caller that forgot
// to lock is still serialized. This is what makes ADR 0009's "correctness
// never depends on the caller locking" true rather than aspirational.
func (s *AccountStore) mutate(ctx context.Context, providerID string, fn func(context.Context) error) error {
	return lock.WithMutationLock(ctx, s.Root, refs.ProviderScope(providerID), fn)
}

func (s *AccountStore) rowToAccount(providerID, name, metaJSON, proxyJSON string) Account {
	storedName := normalizeAccountName(name)
	var meta Meta
	_ = json.Unmarshal([]byte(metaJSON), &meta)
	// The SQL primary key is the authority for identity and filesystem paths.
	// Never let legacy/corrupt metadata redirect a snapshot outside its own
	// provider/account directory.
	meta.Name = storedName
	meta.Provider = providerID
	if meta.CreatedAt == "" {
		meta.CreatedAt = nowISO()
	}
	if meta.UpdatedAt == "" {
		meta.UpdatedAt = nowISO()
	}
	proxy := DefaultProxyConfig()
	_ = json.Unmarshal([]byte(proxyJSON), &proxy)
	return Account{
		Meta:        meta,
		SnapshotDir: paths.AccountSnapshotDir(s.Root, providerID, storedName),
		AccountDir:  paths.AccountDir(s.Root, providerID, storedName),
		Proxy:       proxy,
	}
}

const listAccountsSQL = `SELECT name, meta_json, proxy_json FROM accounts
 WHERE provider = ?
   AND EXISTS (
     SELECT 1 FROM account_snapshot_files
     WHERE account_snapshot_files.provider = accounts.provider
       AND account_snapshot_files.account = accounts.name
   )`

// getAccountSQL filters by exact identity, unlike the listing variant above.
const getAccountSQL = `SELECT name, meta_json, proxy_json FROM accounts
 WHERE provider = ? AND name = ?
   AND EXISTS (
     SELECT 1 FROM account_snapshot_files
     WHERE account_snapshot_files.provider = accounts.provider
       AND account_snapshot_files.account = accounts.name
   )`

func (s *AccountStore) ListAccounts(ctx context.Context, providerID string) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx, listAccountsSQL+` ORDER BY name`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var name, metaJSON, proxyJSON string
		if err := rows.Scan(&name, &metaJSON, &proxyJSON); err != nil {
			return nil, err
		}
		out = append(out, s.rowToAccount(providerID, name, metaJSON, proxyJSON))
	}
	return out, rows.Err()
}

// Get returns the account and materializes its snapshot cache for
// provider.Restore / export. Returns nil when absent.
//
// Materialize deletes the cache directory and rewrites it. That window is
// inside the provider lock (ADR 0011): Gemini Restore treats a missing
// snapshot file as "delete the live credential", and a desktop snapshot
// read can call Get while Use is restoring. The caller's ctx is what makes
// the lock re-entrant, so a Get already inside mutate does not deadlock.
func (s *AccountStore) Get(ctx context.Context, providerID, name string) (*Account, error) {
	var account *Account
	err := s.mutate(ctx, providerID, func(ctx context.Context) error {
		got, gerr := s.get(ctx, providerID, name)
		account = got
		return gerr
	})
	return account, err
}

func (s *AccountStore) get(ctx context.Context, providerID, name string) (*Account, error) {
	account, err := s.CachedAccount(ctx, providerID, name)
	if err != nil || account == nil {
		return account, err
	}
	if err := MaterializeSnapshot(s.Root, s.DB.DB, providerID, account.Meta.Name); err != nil {
		return nil, err
	}
	return account, nil
}

// CachedAccount returns the saved account and its on-disk snapshot directory
// without rebuilding that directory and without the provider lock.
//
// The detached proxy child resolves upstream while its parent still holds the
// provider lock and is blocked in the /health wait. Get from the child waits
// on that lock, and the parent waits for the child. Reading the cache the
// parent just materialized is the path that does not deadlock.
func (s *AccountStore) CachedAccount(ctx context.Context, providerID, name string) (*Account, error) {
	var metaJSON, proxyJSON string
	err := s.DB.QueryRowContext(ctx, getAccountSQL, providerID, name).Scan(&name, &metaJSON, &proxyJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	account := s.rowToAccount(providerID, name, metaJSON, proxyJSON)
	return &account, nil
}

func (s *AccountStore) Require(ctx context.Context, providerID, name string) (Account, error) {
	account, err := s.Get(ctx, providerID, name)
	if err != nil {
		return Account{}, err
	}
	if account == nil {
		return Account{}, anyerr.Newf(
			`No account "`+name+`" for provider "`+providerID+`".`, "ACCOUNT_NOT_FOUND")
	}
	return *account, nil
}

// PatchMetaContext is mutation-guarded: it takes the provider lock itself, so
// a caller that forgets to is still serialized (ADR 0009).
func (s *AccountStore) PatchMetaContext(ctx context.Context, providerID, name string, patch MetaPatch) (Meta, error) {
	var out Meta
	err := s.mutate(ctx, providerID, func(ctx context.Context) error {
		meta, err := s.patchMetaContext(ctx, providerID, name, patch)
		out = meta
		return err
	})
	return out, err
}

func (s *AccountStore) patchMetaContext(ctx context.Context, providerID, name string, patch MetaPatch) (Meta, error) {
	account, err := s.Get(ctx, providerID, name)
	if err != nil {
		return Meta{}, err
	}
	if account == nil {
		return Meta{}, anyerr.Newf(
			`No account "`+name+`" for provider "`+providerID+`".`, "ACCOUNT_NOT_FOUND")
	}
	now := nowISO()
	next := account.Meta
	next.UpdatedAt = now
	if patch.IdentitySet {
		next.Identity = patch.Identity
	}
	if patch.LabelSet {
		next.Label = patch.Label
	}
	if patch.NotesSet {
		next.Notes = patch.Notes
	}
	if patch.CredentialClass != "" {
		next.CredentialClass = patch.CredentialClass
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE accounts SET meta_json = ?, updated_at = ? WHERE provider = ? AND name = ?`,
		marshalJSON(next), now, providerID, name)
	if err != nil {
		return Meta{}, err
	}
	return next, nil
}

// MetaPatch uses explicit set-flags so clearing a field is distinguishable
// from leaving it alone (the TS patch object has the same property).
type MetaPatch struct {
	IdentitySet     bool
	Identity        string
	LabelSet        bool
	Label           string
	NotesSet        bool
	Notes           string
	CredentialClass CredentialClass
}

// PrepareSnapshot creates an isolated staging directory for backup/import.
// It must NOT touch existing snapshot rows — doing so before backup succeeds
// would destroy the previous snapshot on a fault (DATA-01).
func (s *AccountStore) PrepareSnapshot(providerID, name string) (snapshotDir, accountDir string, err error) {
	stagingRoot := paths.SnapshotStagingDir(s.Root)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", "", anyerr.Wrapf(err, "create staging root", "FS_PATH")
	}
	snap, err := os.MkdirTemp(stagingRoot, "snapshot-")
	if err != nil {
		return "", "", anyerr.Wrapf(err, "create staging dir", "FS_PATH")
	}
	key := providerID + "\x00" + name
	s.mu.Lock()
	if s.prepared[key] == nil {
		s.prepared[key] = map[string]bool{}
	}
	s.prepared[key][snap] = true
	s.mu.Unlock()
	return snap, paths.AccountDir(s.Root, providerID, name), nil
}

// WriteMetaContext atomically replaces an account's metadata + snapshot files
// in one transaction. Callers MUST fill snapshotDir first. Any error leaves the
// previous rows + metadata exactly as they were (DATA-01): old snapshot files
// are deleted and re-ingested together, never partially. This is the atomic
// commit point — invariant #4 (ADR 0004), and the single worst place to lose
// serialization (ADR 0009): two concurrent saves of one provider could
// interleave the delete+re-ingest and leave the account with neither the old
// nor the new snapshot.
func (s *AccountStore) WriteMetaContext(ctx context.Context, meta Meta, snapshotDir string) error {
	return s.mutate(ctx, meta.Provider, func(ctx context.Context) error {
		return s.writeMeta(ctx, meta, snapshotDir)
	})
}

func (s *AccountStore) writeMeta(ctx context.Context, meta Meta, snapshotDir string) error {
	now := meta.UpdatedAt
	if now == "" {
		now = nowISO()
	}
	key := meta.Provider + "\x00" + meta.Name
	s.mu.Lock()
	pendingLen := len(s.prepared[key])
	if snapshotDir == "" && pendingLen > 1 {
		s.mu.Unlock()
		return anyerr.Newf(
			"Multiple prepared snapshots exist for this account; pass the staging path explicitly.",
			"SNAPSHOT_AMBIGUOUS")
	}
	snap := snapshotDir
	if snap == "" {
		for p := range s.prepared[key] {
			snap = p
			break
		}
		if snap == "" {
			snap = paths.AccountSnapshotDir(s.Root, meta.Provider, meta.Name)
		}
	}
	s.mu.Unlock()

	stored := meta
	stored.UpdatedAt = now
	err := s.DB.InTransactionContext(ctx, func(tx *sql.Tx) error {
		var proxyJSON sql.NullString
		var createdAt sql.NullString
		err := tx.QueryRow(
			`SELECT proxy_json, created_at FROM accounts WHERE provider = ? AND name = ?`,
			meta.Provider, meta.Name).Scan(&proxyJSON, &createdAt)
		switch {
		case err == sql.ErrNoRows:
			proxyJSON = sql.NullString{String: marshalJSON(DefaultProxyConfig()), Valid: true}
			createdAt = sql.NullString{String: orDefault(meta.CreatedAt, now), Valid: true}
		case err != nil:
			return err
		}

		if _, err := tx.Exec(
			`INSERT INTO accounts (provider, name, meta_json, proxy_json, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(provider, name) DO UPDATE SET
			   meta_json = excluded.meta_json,
			   updated_at = excluded.updated_at`,
			meta.Provider, meta.Name, marshalJSON(stored), proxyJSON.String,
			orDefault(createdAt.String, now), now); err != nil {
			return err
		}

		// Delete + re-ingest snapshot files atomically so a partial write
		// cannot strand old rows.
		return IngestSnapshotDir(tx, meta.Provider, meta.Name, snap)
	})
	if err != nil {
		return err
	}

	// Snapshot paths are a cache only. Failing to refresh one must not turn a
	// durable SQLite commit into a failed save; the next read rebuilds it.
	_ = MaterializeSnapshot(s.Root, s.DB.DB, meta.Provider, meta.Name)

	s.mu.Lock()
	if pending := s.prepared[key]; pending != nil && pending[snap] {
		delete(pending, snap)
		if len(pending) == 0 {
			delete(s.prepared, key)
		}
	}
	s.mu.Unlock()
	return nil
}

// BatchWrite carries one account for WriteAccountsBatch (restore paths).
type BatchWrite struct {
	Meta        Meta
	Proxy       ProxyConfig
	SnapshotDir string
}

// WriteAccountsBatch atomically writes many accounts in one transaction —
// the equivalent of writeAccountsBatch in src/core/store.ts. A failure leaves
// every prior row untouched, so a batch restore cannot half-land.
// WriteAccountsBatch is mutation-guarded (ADR 0009). A batch can span
// providers, so it takes every affected provider scope at once: acquiring them
// in sorted order is what keeps a mixed batch from deadlocking against a
// single-provider mutation touching the same provider.
func (s *AccountStore) WriteAccountsBatchContext(ctx context.Context, entries []BatchWrite) error {
	scopes := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		p := entry.Meta.Provider
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		scopes = append(scopes, refs.ProviderScope(p))
	}
	run := func(c context.Context) error { return s.writeAccountsBatch(c, entries) }
	if len(scopes) == 0 {
		return run(ctx)
	}
	return lock.WithMutationLocks(ctx, s.Root, scopes, run, 0)
}

func (s *AccountStore) writeAccountsBatch(ctx context.Context, entries []BatchWrite) error {
	err := s.DB.InTransactionContext(ctx, func(tx *sql.Tx) error {
		for _, entry := range entries {
			meta := entry.Meta
			now := meta.UpdatedAt
			if now == "" {
				now = nowISO()
			}
			var createdAt sql.NullString
			err := tx.QueryRow(
				`SELECT created_at FROM accounts WHERE provider = ? AND name = ?`,
				meta.Provider, meta.Name).Scan(&createdAt)
			switch {
			case err == sql.ErrNoRows:
				createdAt = sql.NullString{String: orDefault(meta.CreatedAt, now), Valid: true}
			case err != nil:
				return err
			}
			stored := meta
			stored.CreatedAt = createdAt.String
			stored.UpdatedAt = now
			if _, err := tx.Exec(
				`INSERT INTO accounts (provider, name, meta_json, proxy_json, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT(provider, name) DO UPDATE SET
				   meta_json = excluded.meta_json,
				   proxy_json = excluded.proxy_json,
				   updated_at = excluded.updated_at`,
				meta.Provider, meta.Name, marshalJSON(stored), marshalJSON(entry.Proxy),
				createdAt.String, now); err != nil {
				return err
			}
			if err := IngestSnapshotDir(tx, meta.Provider, meta.Name, entry.SnapshotDir); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Snapshot directories are a cache; the transaction is authoritative.
	for _, entry := range entries {
		_ = MaterializeSnapshot(s.Root, s.DB.DB, entry.Meta.Provider, entry.Meta.Name)
	}
	return nil
}

// DiscardPreparedSnapshot removes a staging dir we created. A provider
// receives this path, so only delete below our dedicated staging root.
func (s *AccountStore) DiscardPreparedSnapshot(snapshotDir string) error {
	root := paths.SnapshotStagingDir(s.Root)
	if snapshotDir == root || !hasPathPrefix(snapshotDir, root) {
		return anyerr.Newf("Refusing to remove a snapshot outside the staging area.", "FS_PATH")
	}
	s.mu.Lock()
	for key, pending := range s.prepared {
		delete(pending, snapshotDir)
		if len(pending) == 0 {
			delete(s.prepared, key)
		}
	}
	s.mu.Unlock()
	return os.RemoveAll(snapshotDir)
}

func hasPathPrefix(p, prefix string) bool {
	return p != prefix && filepath.Dir(p) != p && len(p) > len(prefix)+1 &&
		p[:len(prefix)] == prefix && p[len(prefix)] == filepath.Separator
}

// WriteProxyStateContext persists owner-only runtime state (PROXY-01 token
// included; never surfaced in status output). It is mutation-guarded (ADR
// 0009): proxy_state carries the per-instance proxy secret, so it must never be
// written outside the coordinator.
func (s *AccountStore) WriteProxyStateContext(ctx context.Context, providerID, name string, state ProxyRuntimeState) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.writeProxyStateContext(ctx, providerID, name, state)
	})
}

func (s *AccountStore) writeProxyStateContext(ctx context.Context, providerID, name string, state ProxyRuntimeState) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO proxy_state (provider, account, state_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(provider, account) DO UPDATE SET state_json = excluded.state_json`,
		providerID, name, marshalJSON(state))
	return err
}

func (s *AccountStore) ReadProxyState(ctx context.Context, providerID, name string) (*ProxyRuntimeState, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx,
		`SELECT state_json FROM proxy_state WHERE provider = ? AND account = ?`,
		providerID, name).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state ProxyRuntimeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, nil // corrupt state degrades to "not running", never crashes
	}
	return &state, nil
}

// ProxyStateRef is one persisted proxy runtime state row.
type ProxyStateRef struct {
	Provider string
	Account  string
	State    *ProxyRuntimeState
}

// ListProxyStates returns every account's persisted proxy runtime state. It
// powers host-loss reconciliation: after the shared plugin host is terminated,
// every in-host or non-detached forwarded proxy it served is gone, so their
// state rows and leases must be reconciled. Corrupt rows are skipped rather
// than failing the whole sweep.
func (s *AccountStore) ListProxyStates(ctx context.Context) ([]ProxyStateRef, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT provider, account, state_json FROM proxy_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyStateRef
	for rows.Next() {
		var provider, account, raw string
		if err := rows.Scan(&provider, &account, &raw); err != nil {
			return nil, err
		}
		var state ProxyRuntimeState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			continue
		}
		out = append(out, ProxyStateRef{Provider: provider, Account: account, State: &state})
	}
	return out, rows.Err()
}

// ClearProxyStateContext is mutation-guarded (ADR 0009).
func (s *AccountStore) ClearProxyStateContext(ctx context.Context, providerID, name string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.clearProxyStateContext(ctx, providerID, name)
	})
}

func (s *AccountStore) clearProxyStateContext(ctx context.Context, providerID, name string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM proxy_state WHERE provider = ? AND account = ?`, providerID, name)
	return err
}

// ClearProxyStateAndLeasesContext drops an account's proxy_state row and ALL
// of its account-scoped proxy_leases rows in one SQLite transaction. This is
// the atomic commit point for verified-dead cleanup (ADR 0004 discipline):
// host-loss reconciliation enumerates proxy_state, so clearing the row before
// the leases — or stranding either half through a partial failure — means no
// later pass ever revisits the account. Either both halves land or neither. The
// pair of deletes is mutation-guarded (ADR 0009): it must not interleave with
// another mutation of the same provider.
func (s *AccountStore) ClearProxyStateAndLeasesContext(ctx context.Context, providerID, name string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.clearProxyStateAndLeasesContext(ctx, providerID, name)
	})
}

func (s *AccountStore) clearProxyStateAndLeasesContext(ctx context.Context, providerID, name string) error {
	return s.DB.InTransactionContext(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM proxy_state WHERE provider = ? AND account = ?`, providerID, name); err != nil {
			return err
		}
		_, err := tx.Exec(
			`DELETE FROM proxy_leases WHERE provider = ? AND account = ?`, providerID, name)
		return err
	})
}

// ReleaseProxyRuntime releases an account's proxy liveness (endpoint, pid,
// instance, started-at) and ALL of its leases in one transaction, but KEEPS
// the persisted bearer token. Client configs are written once at activation
// holding that token; rotating it on every stop would 401 every configured
// app until the next activation rewrote it, so stopping must not rotate.
// Dropping the secret stays explicit via ClearProxyStateAndLeasesContext.
// There is deliberately no ctx-less form: AGENTS.md invariant 5 forbids one
// for a mutating method, and nothing needs it — every caller already holds a
// context from the proxy lifecycle it is cleaning up after.
//
// ReleaseProxyRuntimeContext is mutation-guarded (ADR 0009).
func (s *AccountStore) ReleaseProxyRuntimeContext(ctx context.Context, providerID, name string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.DB.InTransactionContext(ctx, func(tx *sql.Tx) error {
			var raw sql.NullString
			if err := tx.QueryRow(
				`SELECT state_json FROM proxy_state WHERE provider = ? AND account = ?`,
				providerID, name).Scan(&raw); err != nil && err != sql.ErrNoRows {
				return err
			}
			token := ""
			if raw.Valid && raw.String != "" {
				var state ProxyRuntimeState
				if json.Unmarshal([]byte(raw.String), &state) == nil {
					token = state.Token
				}
			}
			if _, err := tx.Exec(
				`DELETE FROM proxy_leases WHERE provider = ? AND account = ?`, providerID, name); err != nil {
				return err
			}
			if token == "" {
				_, err := tx.Exec(
					`DELETE FROM proxy_state WHERE provider = ? AND account = ?`, providerID, name)
				return err
			}
			_, err := tx.Exec(
				`INSERT INTO proxy_state (provider, account, state_json)
				 VALUES (?, ?, ?)
				 ON CONFLICT(provider, account) DO UPDATE SET state_json = excluded.state_json`,
				providerID, name, marshalJSON(ProxyRuntimeState{AccountName: name, Token: token}))
			return err
		})
	})
}

// Delete removes the account, its proxy state, and a stale active pointer in
// ONE durable commit — clearing the pointer after cleanup could strand it
// after a crash.
// Delete is mutation-guarded (ADR 0009): it removes the account, its proxy
// state and a stale active pointer in one durable commit.
func (s *AccountStore) DeleteContext(ctx context.Context, providerID, name string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.deleteAccount(ctx, providerID, name)
	})
}

func (s *AccountStore) deleteAccount(ctx context.Context, providerID, name string) error {
	deleted := false
	err := s.DB.InTransactionContext(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`DELETE FROM accounts WHERE provider = ? AND name = ?`, providerID, name)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		deleted = true
		if _, err := tx.Exec(
			`DELETE FROM proxy_state WHERE provider = ? AND account = ?`,
			providerID, name); err != nil {
			return err
		}
		var active string
		err = tx.QueryRow(
			`SELECT account_name FROM provider_active WHERE provider = ?`,
			providerID).Scan(&active)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return err
		case active == name:
			if _, err := tx.Exec(
				`DELETE FROM provider_active WHERE provider = ?`, providerID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !deleted {
		return anyerr.Newf(
			`No account "`+name+`" for provider "`+providerID+`".`, "ACCOUNT_NOT_FOUND")
	}
	// Best-effort cache cleanup; DB state already consistent.
	_ = os.RemoveAll(paths.AccountSnapshotDir(s.Root, providerID, name))
	_ = os.RemoveAll(paths.AccountDir(s.Root, providerID, name))
	return nil
}

func (s *AccountStore) GetActive(ctx context.Context, providerID string) (string, error) {
	var name string
	err := s.DB.QueryRowContext(ctx,
		`SELECT account_name FROM provider_active WHERE provider = ?`,
		providerID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

// SetActiveContext is mutation-guarded (ADR 0009): the active pointer decides
// which credential file every read resolves, so it must not interleave.
func (s *AccountStore) SetActiveContext(ctx context.Context, providerID, name string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.setActiveContext(ctx, providerID, name)
	})
}

func (s *AccountStore) setActiveContext(ctx context.Context, providerID, name string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO provider_active (provider, account_name)
		 VALUES (?, ?)
		 ON CONFLICT(provider) DO UPDATE SET account_name = excluded.account_name`,
		providerID, name)
	return err
}

func (s *AccountStore) ClearActiveContext(ctx context.Context, providerID string) error {
	return s.mutate(ctx, providerID, func(ctx context.Context) error {
		return s.clearActiveContext(ctx, providerID)
	})
}

func (s *AccountStore) clearActiveContext(ctx context.Context, providerID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM provider_active WHERE provider = ?`, providerID)
	return err
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// normalizeAccountName ports utils/slug normalizeAccountName.
func normalizeAccountName(name string) string {
	s, err := slug.AccountName(name)
	if err != nil {
		// The SQL primary key is the authority; a stored name that no longer
		// slugifies (legacy rows) must not break reads. Keep it verbatim.
		return name
	}
	return s
}
