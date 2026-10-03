package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/lock"
)

// Lease mirrors ProxyLease (src/types/bindings.ts). Leases record which
// activation owns a running proxy so cleanup can be scoped and reaped.
type Lease struct {
	LeaseID     string   `json:"leaseId"`
	Provider    string   `json:"provider"`
	Account     string   `json:"account,omitempty"`
	Port        int      `json:"port"`
	Host        string   `json:"host,omitempty"`
	Endpoint    string   `json:"endpoint,omitempty"`
	OwnerPID    int      `json:"ownerPid"`
	InstanceID  string   `json:"instanceId,omitempty"`
	BindingRefs []string `json:"bindingRefs,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// LeaseStore owns the proxy_leases table (src/core/lease-store.ts).
type LeaseStore struct {
	Root string
	DB   *sql.DB
}

// mutate serialises a structural lease change under the provider/account slot
// lock so correctness does not depend on the caller holding it (ADR 0009). The
// cleanup path at service.go already takes this scope alongside the provider
// scope, so a caller that holds it re-enters instead of deadlocking.
//
// HeartbeatContext, HeartbeatInstanceContext and ReapStaleContext stay outside
// the coordinator on purpose. The heartbeat goroutine's context is a
// long-lived cancel context, so it can never carry a provider lock, and giving
// it one per tick would serialise every 15s liveness write behind proxy
// cleanup and drop ticks on the 5s lock budget. Reaping is a cross-provider
// sweep with no single scope; the coordinator would only serialise reaps
// against other reaps, which SQLite's single writer already does. Neither
// changes which rows exist in a way a concurrent structural change can observe,
// because both filter on identity and age the writer re-checks.
func (s *LeaseStore) mutate(ctx context.Context, providerID, accountName string, fn func(context.Context) error) error {
	return lock.WithMutationLock(ctx, s.Root, proxyScope(providerID, accountName), fn)
}

// HeartbeatInterval is deliberately shorter than the expiry window. A missed
// tick must not immediately make a healthy detached proxy reclaimable.
const HeartbeatInterval = 15 * time.Second

// LeaseExpiry is the maximum age of a lease heartbeat before maintenance may
// consider it stale, subject to the existing PID and instance-health checks.
const LeaseExpiry = 2 * time.Minute

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

const leaseColumns = `lease_id, provider, account, port, host, endpoint, owner_pid, instance_id, binding_refs_json, created_at, updated_at`

func scanLease(row rowScanner) (*Lease, error) {
	var l Lease
	var account, endpoint, instanceID sql.NullString
	var host sql.NullString
	var bindingRefs string
	if err := row.Scan(&l.LeaseID, &l.Provider, &account, &l.Port, &host, &endpoint,
		&l.OwnerPID, &instanceID, &bindingRefs, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.Account = account.String
	l.Endpoint = endpoint.String
	l.Host = host.String
	l.InstanceID = instanceID.String
	if l.Host == "" {
		l.Host = "127.0.0.1"
	}
	if err := json.Unmarshal([]byte(bindingRefs), &l.BindingRefs); err != nil {
		l.BindingRefs = nil
	}
	return &l, nil
}

func (s *LeaseStore) CreateContext(ctx context.Context, l Lease) error {
	return s.mutate(ctx, l.Provider, l.Account, func(c context.Context) error {
		now := nowISO()
		if l.Host == "" {
			l.Host = "127.0.0.1"
		}
		bindingRefs, _ := json.Marshal(l.BindingRefs)
		_, err := s.DB.ExecContext(c,
			`INSERT INTO proxy_leases (`+leaseColumns+`)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			l.LeaseID, l.Provider, nullIfEmpty(l.Account), l.Port,
			nullIfEmpty(l.Host), nullIfEmpty(l.Endpoint), l.OwnerPID,
			nullIfEmpty(l.InstanceID), string(bindingRefs), now, now)
		return err
	})
}

// Get returns a lease by id.
//
// Deprecated: prefer GetContext when the caller has a context.
func (s *LeaseStore) Get(leaseID string) (*Lease, error) {
	return s.GetContext(context.Background(), leaseID)
}

func (s *LeaseStore) GetContext(ctx context.Context, leaseID string) (*Lease, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM proxy_leases WHERE lease_id = ?`, leaseID)
	l, err := scanLease(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return l, err
}

func (s *LeaseStore) DeleteContext(ctx context.Context, leaseID string) error {
	// The scope is the lease's provider/account, which only the row knows, so
	// it is read before the lock is taken. A lease that is already gone needs
	// no lock: the delete was a no-op and still is.
	l, err := s.GetContext(ctx, leaseID)
	if err != nil || l == nil {
		return err
	}
	return s.mutate(ctx, l.Provider, l.Account, func(c context.Context) error {
		_, err := s.DB.ExecContext(c, `DELETE FROM proxy_leases WHERE lease_id = ?`, leaseID)
		return err
	})
}

// HeartbeatContext refreshes a lease only when the exact serving instance still
// owns it. A replacement process cannot accidentally keep the old lease alive.
func (s *LeaseStore) HeartbeatContext(ctx context.Context, leaseID, instanceID string, ownerPID int) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE proxy_leases SET updated_at = ?
		 WHERE lease_id = ? AND instance_id = ? AND owner_pid = ?`,
		nowISO(), leaseID, instanceID, ownerPID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// HeartbeatInstanceContext refreshes every lease held by one serving
// instance. The detached child owns the lease the parent created but never
// learns the lease id, so it refreshes by the instance id it echoes on
// /health — the same identity proof maintenance trusts.
func (s *LeaseStore) HeartbeatInstanceContext(ctx context.Context, instanceID string, ownerPID int) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE proxy_leases SET updated_at = ?
		 WHERE instance_id = ? AND owner_pid = ?`,
		nowISO(), instanceID, ownerPID)
	return err
}

// ListByAccount returns live leases for an account.
//
// Deprecated: prefer ListByAccountContext when the caller has a context.
func (s *LeaseStore) ListByAccount(providerID, accountName string) ([]*Lease, error) {
	return s.ListByAccountContext(context.Background(), providerID, accountName)
}

// ListPoolLeases returns live pool leases — keyed (provider, ""), persisted
// with a NULL account column, so the per-account query cannot see them.
//
// Deprecated: prefer ListPoolLeasesContext when the caller has a context.
func (s *LeaseStore) ListPoolLeases(providerID string) ([]*Lease, error) {
	return s.ListPoolLeasesContext(context.Background(), providerID)
}

func (s *LeaseStore) ListPoolLeasesContext(ctx context.Context, providerID string) ([]*Lease, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+leaseColumns+` FROM proxy_leases
		 WHERE provider = ? AND account IS NULL ORDER BY created_at`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *LeaseStore) ListByAccountContext(ctx context.Context, providerID, accountName string) ([]*Lease, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+leaseColumns+` FROM proxy_leases
		 WHERE provider = ? AND account = ? ORDER BY created_at`, providerID, accountName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ReapStale removes leases whose owner process is gone (owner_pid = this pid
// marks in-process servers; after a crash the pid no longer exists).
func (s *LeaseStore) ReapStaleContext(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT lease_id, owner_pid, instance_id, endpoint, updated_at
		 FROM proxy_leases WHERE owner_pid <> ?`, ownPID())
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id, instanceID, endpoint, updatedAt string
		pid                                 int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		// instance_id and endpoint are nullable: leases written before the
		// heartbeat work have neither, and scanning NULL into a plain string
		// fails, which would drop exactly the rows reaping exists to remove.
		var instanceID, endpoint sql.NullString
		if err := rows.Scan(&c.id, &c.pid, &instanceID, &endpoint, &c.updatedAt); err == nil {
			c.instanceID = instanceID.String
			c.endpoint = endpoint.String
			candidates = append(candidates, c)
		}
	}
	rows.Close()
	reaped := 0
	now := time.Now()
	for _, c := range candidates {
		// Rows written before heartbeats existed carry no identity proof at
		// all, so they keep the PID-only rule they were created under.
		legacy := c.instanceID == "" && c.endpoint == ""
		if legacy {
			if processAlive(c.pid) {
				continue
			}
		} else {
			// A fresh beat proves the owner is still refreshing, and it
			// outranks the PID: a PID can be reused by an unrelated process.
			if leaseExpired(c.updatedAt, now) {
				// The instance echo is the authoritative proof and outranks
				// even an expired beat, so a healthy proxy that merely lost
				// write access to the database is never reclaimed.
				if c.endpoint != "" && healthInstance(c.endpoint) == c.instanceID {
					continue
				}
				// Nothing to echo against: fall back to the PID, but only as
				// a veto — it can keep a lease, never condemn one.
				if c.endpoint == "" && processAlive(c.pid) {
					continue
				}
			} else {
				continue
			}
		}
		res, err := s.DB.ExecContext(ctx, `DELETE FROM proxy_leases WHERE lease_id = ?`, c.id)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				reaped += int(n)
			}
		}
	}
	return reaped, nil
}

// leaseExpired reports whether the last heartbeat is older than LeaseExpiry.
// An unparseable timestamp cannot prove freshness, so it counts as expired —
// the caller's echo check is what protects a live proxy from that.
func leaseExpired(updatedAt string, now time.Time) bool {
	updated, err := time.Parse("2006-01-02T15:04:05.000Z07:00", updatedAt)
	if err != nil {
		return true
	}
	return now.Sub(updated) > LeaseExpiry
}

type rowScanner interface{ Scan(dest ...any) error }

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// proxyScope locks one provider/account proxy slot (proxy/<p>/<a>).
func proxyScope(providerID, accountName string) string {
	return "proxy/" + providerID + "/" + accountName
}
