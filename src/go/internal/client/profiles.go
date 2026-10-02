package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/lock"
)

// NamedProfile is the durable AnyPick route behind one native client profile.
// It deliberately stores a binding spec, not credential material: account
// secrets remain in the account snapshot/proxy owner boundary.
type NamedProfile struct {
	Client    string
	Name      string
	Spec      bindings.Spec
	CreatedAt string
	UpdatedAt string
}

// ProfileStore owns client_profiles. It is kept in internal/client because the
// table describes client-native route profiles rather than gateway credentials.
type ProfileStore struct {
	Root string
	DB   *sql.DB
}

// ProfileService is the boundary used by the CLI and desktop layers. The raw
// store stays inside internal/client and every mutation remains self-locked.
type ProfileService struct{ Store *ProfileStore }

func NewProfileService(s *ProfileStore) *ProfileService { return &ProfileService{Store: s} }

func profileNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func profileScope(clientID, name string) string {
	return "client-profile/" + clientID + "/" + name
}

func (s *ProfileStore) mutate(ctx context.Context, clientID, name string, fn func(context.Context) error) error {
	return lock.WithMutationLock(ctx, s.Root, profileScope(clientID, name), fn)
}

const namedProfileColumns = `client_id, name, spec_json, created_at, updated_at`

func scanNamedProfile(row interface{ Scan(...any) error }) (*NamedProfile, error) {
	var p NamedProfile
	var specJSON string
	if err := row.Scan(&p.Client, &p.Name, &specJSON, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(specJSON), &p.Spec); err != nil {
		return nil, anyerr.Wrapf(err, "decode client profile", "STATE_CONFLICT")
	}
	return &p, nil
}

// GetContext returns one named route, or nil when it has not been created.
func (s *ProfileStore) GetContext(ctx context.Context, clientID, name string) (*NamedProfile, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT `+namedProfileColumns+` FROM client_profiles WHERE client_id = ? AND name = ?`,
		clientID, name)
	p, err := scanNamedProfile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// ListContext returns named routes for one client in stable display order.
func (s *ProfileStore) ListContext(ctx context.Context, clientID string) ([]NamedProfile, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+namedProfileColumns+` FROM client_profiles WHERE client_id = ? ORDER BY name`,
		clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NamedProfile
	for rows.Next() {
		p, scanErr := scanNamedProfile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// UpsertContext creates or updates a named route. The source spec is the
// binding snapshot; native config and proxy lifecycle are handled by the
// activation plan before this commit step.
func (s *ProfileStore) UpsertContext(ctx context.Context, clientID, name string, spec bindings.Spec) (NamedProfile, error) {
	var out NamedProfile
	err := s.mutate(ctx, clientID, name, func(c context.Context) error {
		now := profileNow()
		existing, err := s.GetContext(c, clientID, name)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		createdAt := now
		if existing != nil {
			createdAt = existing.CreatedAt
		}
		_, err = s.DB.ExecContext(c,
			`INSERT INTO client_profiles (`+namedProfileColumns+`)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(client_id, name) DO UPDATE SET
			   spec_json = excluded.spec_json,
			   updated_at = excluded.updated_at`,
			clientID, name, string(raw), createdAt, now)
		if err != nil {
			return err
		}
		out = NamedProfile{Client: clientID, Name: name, Spec: spec,
			CreatedAt: createdAt, UpdatedAt: now}
		return nil
	})
	return out, err
}

// DeleteContext removes one named route. It does not stop a proxy: another
// profile or client may still use that account's listener.
func (s *ProfileStore) DeleteContext(ctx context.Context, clientID, name string) error {
	return s.mutate(ctx, clientID, name, func(c context.Context) error {
		_, err := s.DB.ExecContext(c,
			`DELETE FROM client_profiles WHERE client_id = ? AND name = ?`, clientID, name)
		return err
	})
}

func (s *ProfileService) GetContext(ctx context.Context, clientID, name string) (*NamedProfile, error) {
	return s.Store.GetContext(ctx, clientID, name)
}

func (s *ProfileService) ListContext(ctx context.Context, clientID string) ([]NamedProfile, error) {
	return s.Store.ListContext(ctx, clientID)
}

func (s *ProfileService) UpsertContext(ctx context.Context, clientID, name string, spec bindings.Spec) (NamedProfile, error) {
	return s.Store.UpsertContext(ctx, clientID, name, spec)
}

func (s *ProfileService) DeleteContext(ctx context.Context, clientID, name string) error {
	return s.Store.DeleteContext(ctx, clientID, name)
}
