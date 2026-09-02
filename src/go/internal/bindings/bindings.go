// Package bindings ports src/core/binding-store.ts and the binding types from
// src/types/bindings.ts. JSON shapes match TypeScript so both runtimes read
// the same rows.
package bindings

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/refs"
)

// ModelSelection mirrors the TS tagged union:
//
//	{mode:'explicit', id} | {mode:'omitted'} | {mode:'unknown', reason}
type ModelSelection struct {
	Mode   string `json:"mode"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func ExplicitModel(id string) ModelSelection { return ModelSelection{Mode: "explicit", ID: id} }
func OmittedModel() ModelSelection           { return ModelSelection{Mode: "omitted"} }

// Spec mirrors BindingSpec.
type Spec struct {
	Client          string                 `json:"client"`
	Source          refs.Ref               `json:"source"`
	Model           ModelSelection         `json:"model"`
	TransportPolicy string                 `json:"transportPolicy"`
	ClientOptions   map[string]interface{} `json:"clientOptions,omitempty"`
}

// Provenance mirrors BindingProvenance. Kind is one of
// direct | preset_snapshot | global_binding_snapshot | legacy_migration.
type Provenance struct {
	Kind                     string `json:"kind"`
	PresetID                 string `json:"presetId,omitempty"`
	PresetNameAtSnapshot     string `json:"presetNameAtSnapshot,omitempty"`
	PresetRevisionAtSnapshot int    `json:"presetRevisionAtSnapshot,omitempty"`
	GlobalBindingUpdatedAt   string `json:"globalBindingUpdatedAt,omitempty"`
	SourceConfidence         string `json:"sourceConfidence,omitempty"`
	ModelConfidence          string `json:"modelConfidence,omitempty"`
	ImportedAt               string `json:"importedAt,omitempty"`
}

// Global mirrors GlobalBinding.
type Global struct {
	Client                string     `json:"client"`
	Spec                  Spec       `json:"spec"`
	Provenance            Provenance `json:"provenance"`
	ManagedConfigRevision string     `json:"managedConfigRevision,omitempty"`
	CreatedAt             string     `json:"createdAt"`
	UpdatedAt             string     `json:"updatedAt"`
}

// Project mirrors ProjectBinding.
type Project struct {
	ProjectRoot string     `json:"projectRoot"`
	Client      string     `json:"client"`
	Spec        Spec       `json:"spec"`
	Provenance  Provenance `json:"provenance"`
	CreatedAt   string     `json:"createdAt"`
	UpdatedAt   string     `json:"updatedAt"`
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Store owns all SQL for global/project bindings (stores own SQL; services
// never embed it).
type Store struct{ DB *sql.DB }

const globalColumns = `client_id, spec_json, provenance_json, managed_config_revision, created_at, updated_at`

func scanGlobal(row rowScanner) (*Global, error) {
	var clientID, specJSON, provJSON string
	var revision sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&clientID, &specJSON, &provJSON, &revision, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	g := &Global{Client: clientID, ManagedConfigRevision: revision.String, CreatedAt: createdAt, UpdatedAt: updatedAt}
	if err := json.Unmarshal([]byte(specJSON), &g.Spec); err != nil {
		return nil, anyerr.Wrapf(err, "decode binding spec", "STATE_CONFLICT")
	}
	if err := json.Unmarshal([]byte(provJSON), &g.Provenance); err != nil {
		return nil, anyerr.Wrapf(err, "decode binding provenance", "STATE_CONFLICT")
	}
	return g, nil
}

func (s *Store) GetGlobalContext(ctx context.Context, clientID string) (*Global, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT `+globalColumns+` FROM global_bindings WHERE client_id = ?`, clientID)
	g, err := scanGlobal(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

// UpsertGlobal inserts or replaces the global binding for a client.
//
// There is deliberately no ctx-less form — and neither does any other method
// on Store. A caller that starts from context.Background() cannot see the lock
// its caller holds, so a mutator reached that way re-enters the coordinator and
// hangs rather than erroring (ADR 0009). internal/store deleted its ctx-less
// *mutators* for exactly this reason; bindings goes further and deletes the
// ctx-less reads too, so no method on this Store can be reached through a fresh
// context. internal/bindings/locking_test.go fails the build if one comes back.
func (s *Store) UpsertGlobalContext(ctx context.Context, g Global) (Global, error) {
	now := nowISO()
	existing, err := s.GetGlobalContext(ctx, g.Client)
	if err != nil {
		return Global{}, err
	}
	createdAt := now
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	specJSON, err := json.Marshal(g.Spec)
	if err != nil {
		return Global{}, err
	}
	provJSON, err := json.Marshal(g.Provenance)
	if err != nil {
		return Global{}, err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO global_bindings (`+globalColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(client_id) DO UPDATE SET
		   spec_json = excluded.spec_json,
		   provenance_json = excluded.provenance_json,
		   managed_config_revision = excluded.managed_config_revision,
		   updated_at = excluded.updated_at`,
		g.Client, string(specJSON), string(provJSON), g.ManagedConfigRevision, createdAt, now)
	if err != nil {
		return Global{}, err
	}
	g.CreatedAt = createdAt
	g.UpdatedAt = now
	return g, nil
}

// DeleteGlobal removes the global binding for a client. No ctx-less form — see
// the note on UpsertGlobalContext.
func (s *Store) DeleteGlobalContext(ctx context.Context, clientID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM global_bindings WHERE client_id = ?`, clientID)
	return err
}

const projectColumns = `project_root, client_id, spec_json, provenance_json, created_at, updated_at`

func scanProject(row rowScanner) (*Project, error) {
	var root, clientID, specJSON, provJSON, createdAt, updatedAt string
	if err := row.Scan(&root, &clientID, &specJSON, &provJSON, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p := &Project{ProjectRoot: root, Client: clientID, CreatedAt: createdAt, UpdatedAt: updatedAt}
	if err := json.Unmarshal([]byte(specJSON), &p.Spec); err != nil {
		return nil, anyerr.Wrapf(err, "decode binding spec", "STATE_CONFLICT")
	}
	if err := json.Unmarshal([]byte(provJSON), &p.Provenance); err != nil {
		return nil, anyerr.Wrapf(err, "decode binding provenance", "STATE_CONFLICT")
	}
	return p, nil
}

func (s *Store) GetProjectContext(ctx context.Context, root, clientID string) (*Project, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM project_bindings WHERE project_root = ? AND client_id = ?`,
		root, clientID)
	p, err := scanProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// UpsertProject inserts or replaces one project binding. No ctx-less form —
// see the note on UpsertGlobalContext.
func (s *Store) UpsertProjectContext(ctx context.Context, p Project) (Project, error) {
	now := nowISO()
	existing, err := s.GetProjectContext(ctx, p.ProjectRoot, p.Client)
	if err != nil {
		return Project{}, err
	}
	createdAt := now
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	specJSON, err := json.Marshal(p.Spec)
	if err != nil {
		return Project{}, err
	}
	provJSON, err := json.Marshal(p.Provenance)
	if err != nil {
		return Project{}, err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO project_bindings (`+projectColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_root, client_id) DO UPDATE SET
		   spec_json = excluded.spec_json,
		   provenance_json = excluded.provenance_json,
		   updated_at = excluded.updated_at`,
		p.ProjectRoot, p.Client, string(specJSON), string(provJSON), createdAt, now)
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt = createdAt
	p.UpdatedAt = now
	return p, nil
}

// DeleteProject removes one project binding. No ctx-less form — see the note on
// UpsertGlobalContext.
func (s *Store) DeleteProjectContext(ctx context.Context, root, clientID string) error {
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM project_bindings WHERE project_root = ? AND client_id = ?`, root, clientID)
	return err
}

// CountProjects reports how many project bindings exist under a root without
// mutating anything — `unlink --dry-run` needs TS's read-only removal count.
func (s *Store) CountProjectsContext(ctx context.Context, root string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_bindings WHERE project_root = ?`, root).Scan(&n)
	return n, err
}

// DeleteAllProjects removes every project binding under a project root and
// reports how many were removed (TS deleteProject(root)).
// DeleteAllProjectsContext removes every project binding under a project root
// and reports how many were removed (TS deleteProject(root)). No ctx-less form
// — see the note on UpsertGlobalContext.
func (s *Store) DeleteAllProjectsContext(ctx context.Context, root string) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM project_bindings WHERE project_root = ?`, root)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type rowScanner interface{ Scan(dest ...any) error }

// SourceResume mirrors the source_resumes table: a successful, source-specific
// setup kept as a DX cache so clients can resume recent compatible setups.
type SourceResume struct {
	SourceRef string `json:"sourceRef"`
	Client    string `json:"clientId"`
	Spec      Spec   `json:"spec"`
	UpdatedAt string `json:"updatedAt"`
}

// RecordSourceResumeContext ports recordSourceResume. Presets are expanded by
// the planner before activation, so a symbolic preset pointer is never retained
// — it could be edited or deleted independently of this history row. No
// ctx-less form — see the note on UpsertGlobalContext.
func (s *Store) RecordSourceResumeContext(ctx context.Context, spec Spec) error {
	if spec.Source.Kind == refs.KindPreset {
		return nil
	}
	sourceRef := refs.SerializeRef(spec.Source)
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, werr := s.DB.ExecContext(ctx,
		`INSERT INTO source_resumes (source_ref, client_id, spec_json, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(source_ref, client_id) DO UPDATE SET
		   spec_json = excluded.spec_json,
		   updated_at = excluded.updated_at`,
		sourceRef, spec.Client, string(specJSON), nowISO())
	return werr
}

// ListSourceResumesContext returns the most recently successful compatible
// setups for a concrete source (newest first). Preset sources have no history
// rows.
func (s *Store) ListSourceResumesContext(ctx context.Context, source refs.Ref) ([]SourceResume, error) {
	if source.Kind == refs.KindPreset {
		return []SourceResume{}, nil
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT source_ref, client_id, spec_json, updated_at
		 FROM source_resumes WHERE source_ref = ?
		 ORDER BY updated_at DESC, client_id ASC`,
		refs.SerializeRef(source))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceResume{}
	for rows.Next() {
		var r SourceResume
		var specJSON string
		if err := rows.Scan(&r.SourceRef, &r.Client, &specJSON, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(specJSON), &r.Spec) != nil {
			continue // corrupt rows are skipped, matching parseSourceResume
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
