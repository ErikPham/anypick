package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
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
type LeaseStore struct{ DB *sql.DB }

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

// Create inserts a new lease.
func (s *LeaseStore) Create(l Lease) error {
	return s.CreateContext(context.Background(), l)
}

func (s *LeaseStore) CreateContext(ctx context.Context, l Lease) error {
	now := nowISO()
	if l.Host == "" {
		l.Host = "127.0.0.1"
	}
	bindingRefs, _ := json.Marshal(l.BindingRefs)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO proxy_leases (`+leaseColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.LeaseID, l.Provider, nullIfEmpty(l.Account), l.Port,
		nullIfEmpty(l.Host), nullIfEmpty(l.Endpoint), l.OwnerPID,
		nullIfEmpty(l.InstanceID), string(bindingRefs), now, now)
	return err
}

// Get returns a lease by id.
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

// Delete removes a released lease.
func (s *LeaseStore) Delete(leaseID string) error {
	return s.DeleteContext(context.Background(), leaseID)
}

func (s *LeaseStore) DeleteContext(ctx context.Context, leaseID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM proxy_leases WHERE lease_id = ?`, leaseID)
	return err
}

// ListByAccount returns live leases for an account.
func (s *LeaseStore) ListByAccount(providerID, accountName string) ([]*Lease, error) {
	return s.ListByAccountContext(context.Background(), providerID, accountName)
}

// ListPoolLeases returns live pool leases — keyed (provider, ""), persisted
// with a NULL account column, so the per-account query cannot see them.
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
func (s *LeaseStore) ReapStale() (int, error) {
	return s.ReapStaleContext(context.Background())
}

func (s *LeaseStore) ReapStaleContext(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT DISTINCT owner_pid FROM proxy_leases WHERE owner_pid <> ?`, ownPID())
	if err != nil {
		return 0, err
	}
	var pids []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err == nil {
			pids = append(pids, pid)
		}
	}
	rows.Close()
	reaped := 0
	for _, pid := range pids {
		if !processAlive(pid) {
			res, err := s.DB.ExecContext(ctx, `DELETE FROM proxy_leases WHERE owner_pid = ?`, pid)
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					reaped += int(n)
				}
			}
		}
	}
	return reaped, nil
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
