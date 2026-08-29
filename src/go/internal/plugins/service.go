package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
)

const (
	// MutationScope serializes the registry as one unit (ADR-0009/0012).
	MutationScope = "plugins"
	bundledPrefix = "bundled:"
)

// Service coordinates manifest/digest work with persisted registry mutation.
type Service struct {
	store       *Store
	root        string
	bundledRoot string
}

// NewService constructs a registry service. bundledRoot is the directory
// containing shipped plugin package directories; an empty value uses the
// bundle/development resolver.
func NewService(store *Store, root, bundledRoot string) *Service {
	if bundledRoot == "" {
		bundledRoot = DefaultBundledRoot()
	}
	return &Service{store: store, root: root, bundledRoot: bundledRoot}
}

// DefaultBundledRoot resolves the package-owned plugin directory. Native and
// npm hosts may set ANYPICK_BUNDLED_PLUGINS to their signed/bundle-relative
// path; repository runs fall back to the two source layouts.
func DefaultBundledRoot() string {
	if configured := os.Getenv("ANYPICK_BUNDLED_PLUGINS"); configured != "" {
		if abs, err := filepath.Abs(configured); err == nil {
			return abs
		}
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "plugins"),
			filepath.Join(dir, "..", "plugins"),
			filepath.Join(dir, "..", "Resources", "plugins"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "plugins"), filepath.Join(cwd, "..", "plugins"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	if len(candidates) > 0 {
		abs, _ := filepath.Abs(candidates[0])
		return abs
	}
	return "plugins"
}

// ResolvePluginDirectory mirrors resolvePluginDirectory from the TypeScript
// service, including the invalid-usage boundary for bundled names.
func ResolvePluginDirectory(input, bundledRoot string) (string, error) {
	if len(input) >= len(bundledPrefix) && input[:len(bundledPrefix)] == bundledPrefix {
		name := input[len(bundledPrefix):]
		if !namePattern.MatchString(name) {
			return "", anyerr.Newf(
				fmt.Sprintf(`Invalid bundled plugin name %q.`, name), "PLUGIN_INVALID",
				anyerr.WithExitCode(anyerr.InvalidUsage),
			)
		}
		return filepath.Clean(filepath.Join(bundledRoot, name)), nil
	}
	return filepath.Abs(input)
}

func (s *Service) ResolvePluginDirectory(input string) (string, error) {
	return ResolvePluginDirectory(input, s.bundledRoot)
}

func (s *Service) List(ctx context.Context) ([]Record, error) { return s.store.List(ctx) }

func (s *Service) Get(ctx context.Context, name string) (*Record, error) {
	return s.store.Get(ctx, name)
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func notFound(name string) error {
	return anyerr.Newf(
		fmt.Sprintf(`No plugin named %q is installed.`, name), "PLUGIN_NOT_FOUND",
		anyerr.WithExitCode(anyerr.NotFound),
		anyerr.WithSuggestions("List installed plugins: anypick plugin list"),
	)
}

// Add records the current package digest but leaves a new plugin disabled.
func (s *Service) Add(ctx context.Context, input string) (Record, error) {
	path, err := s.ResolvePluginDirectory(input)
	if err != nil {
		return Record{}, err
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		return Record{}, err
	}
	digest, err := DigestPluginPackage(path)
	if err != nil {
		return Record{}, anyerr.Wrapf(err, "digest plugin package", "PLUGIN_INVALID")
	}

	var result Record
	err = lock.WithMutationLock(ctx, s.root, MutationScope, func(locked context.Context) error {
		existing, err := s.store.Get(locked, manifest.Name)
		if err != nil {
			return err
		}
		if existing != nil && existing.Path != path {
			return anyerr.Newf(
				fmt.Sprintf(`A different plugin directory is already installed as %q.`, manifest.Name),
				"PLUGIN_CONFLICT",
				anyerr.WithExitCode(anyerr.Operational),
				anyerr.WithDetails(map[string]any{"installedPath": existing.Path}),
				anyerr.WithSuggestions(
					fmt.Sprintf("Remove the existing one first: anypick plugin remove %s", manifest.Name),
				),
			)
		}
		addedAt := nowISO()
		enabled := false
		if existing != nil {
			addedAt = existing.AddedAt
			enabled = existing.Enabled
		}
		result = Record{
			Name: manifest.Name, Path: path, Version: manifest.Version,
			Enabled: enabled, Digest: digest, AddedAt: addedAt, UpdatedAt: nowISO(),
		}
		return s.store.Upsert(locked, result)
	})
	return result, err
}

func (s *Service) Remove(ctx context.Context, name string) error {
	return lock.WithMutationLock(ctx, s.root, MutationScope, func(locked context.Context) error {
		removed, err := s.store.Remove(locked, name)
		if err != nil {
			return err
		}
		if !removed {
			return notFound(name)
		}
		return nil
	})
}

func (s *Service) SetEnabled(ctx context.Context, name string, enabled bool) (Record, error) {
	var result Record
	err := lock.WithMutationLock(ctx, s.root, MutationScope, func(locked context.Context) error {
		existing, err := s.store.Get(locked, name)
		if err != nil {
			return err
		}
		if existing == nil {
			return notFound(name)
		}
		result = *existing
		result.Enabled = enabled
		result.UpdatedAt = nowISO()
		return s.store.Upsert(locked, result)
	})
	return result, err
}

// TrustResult reports both sides of the explicit code re-approval.
type TrustResult struct {
	Record         Record `json:"-"`
	PreviousDigest string `json:"previousDigest"`
}

// Trust re-pins the bytes currently on disk. Manifest/digest reads happen
// under the same coordinator scope as the row update so another plugin
// mutation cannot be overwritten by a stale pre-lock record.
func (s *Service) Trust(ctx context.Context, name string) (TrustResult, error) {
	var result TrustResult
	err := lock.WithMutationLock(ctx, s.root, MutationScope, func(locked context.Context) error {
		existing, err := s.store.Get(locked, name)
		if err != nil {
			return err
		}
		if existing == nil {
			return notFound(name)
		}
		manifest, err := ReadManifest(existing.Path)
		if err != nil {
			return err
		}
		digest, err := DigestPluginPackage(existing.Path)
		if err != nil {
			return anyerr.Wrapf(err, "digest plugin package", "PLUGIN_INVALID")
		}
		result.PreviousDigest = existing.Digest
		result.Record = *existing
		result.Record.Version = manifest.Version
		result.Record.Digest = digest
		result.Record.UpdatedAt = nowISO()
		return s.store.Upsert(locked, result.Record)
	})
	return result, err
}
