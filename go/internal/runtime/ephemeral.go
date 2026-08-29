package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/transport"
)

// EphemeralSpec carries what an isolated session needs to configure a client.
type EphemeralSpec struct {
	ClientID            string
	Endpoint            string // managed proxy endpoint (already started by StartProxy)
	APIKey              string // proxy token for the session
	DefaultModel        string
	ModelRoles          map[string]string
	SourceKind          string
	SourceProvider      string
	SourceName          string
	TransportCapability transport.Capability
}

// EphemeralRuntime is the shared client-owned runtime handle used by both
// built-ins and RPC-backed plugin clients.
type EphemeralRuntime = client.IsolatedRuntime

// CreateEphemeralRuntime builds an isolated home (spec §9.7.1): Claude/Codex
// never get live-patched — the child sees only this directory.
func (s *Service) CreateEphemeralRuntime(spec EphemeralSpec) (*EphemeralRuntime, error) {
	return s.CreateEphemeralRuntimeContext(context.Background(), spec)
}

func (s *Service) CreateEphemeralRuntimeContext(
	ctx context.Context,
	spec EphemeralSpec,
) (*EphemeralRuntime, error) {
	cad, err := s.Clients.Get(spec.ClientID)
	if err != nil {
		return nil, err
	}
	if adapter, ok := cad.(client.EphemeralAdapter); ok {
		return adapter.CreateEphemeralRuntime(ctx, &client.EphemeralContext{
			ClientID: spec.ClientID, Endpoint: spec.Endpoint, APIKey: spec.APIKey,
			DefaultModel: spec.DefaultModel, ModelRoles: spec.ModelRoles,
			AnyPickRoot: s.Root, Home: clientHome(s), SourceKind: spec.SourceKind,
			SourceProvider: spec.SourceProvider, SourceName: spec.SourceName,
			TransportCapability: spec.TransportCapability,
		})
	}
	root, err := os.MkdirTemp("", "anypick-"+cad.ID()+"-")
	if err != nil {
		return nil, err
	}
	out := client.NewIsolatedRuntime(
		root,
		filepath.Join(root, "home"),
		map[string]string{},
		nil,
		func() error { return os.RemoveAll(root) },
	)
	if err := os.MkdirAll(out.Home, 0o700); err != nil {
		_ = out.Cleanup()
		return nil, err
	}

	live := clientHome(s)
	switch cad.ID() {
	case "codex":
		// Carry over user config so the child keeps non-anypick settings.
		isoCodex := filepath.Join(out.Home, ".codex")
		if err := os.MkdirAll(isoCodex, 0o700); err != nil {
			_ = out.Cleanup()
			return nil, err
		}
		if err := copyIfPresent(codexConfigPath(s), filepath.Join(isoCodex, "config.toml")); err != nil {
			_ = out.Cleanup()
			return nil, err
		}
		out.Environment["CODEX_HOME"] = isoCodex
		out.Environment["ANYPICK_PROXY_API_KEY"] = spec.APIKey
	case "claude":
		isoClaude := filepath.Join(out.Home, ".claude")
		if err := os.MkdirAll(isoClaude, 0o700); err != nil {
			_ = out.Cleanup()
			return nil, err
		}
		if err := copyIfPresent(
			filepath.Join(live, ".claude", "settings.json"),
			filepath.Join(isoClaude, "settings.json"),
		); err != nil {
			_ = out.Cleanup()
			return nil, err
		}
		out.Environment["HOME"] = out.Home
	default:
		_ = out.Cleanup()
		return nil, fmt.Errorf("client %s does not support isolated homes", cad.ID())
	}

	actx := &client.ApplyContext{
		Endpoint:     spec.Endpoint,
		APIKey:       spec.APIKey,
		DefaultModel: spec.DefaultModel,
		ModelRoles:   spec.ModelRoles,
		AnyPickRoot:  s.Root,
		Home:         live,
		IsolatedHome: out.Home,
	}
	if _, err := cad.Apply(actx); err != nil {
		_ = out.Cleanup()
		return nil, err
	}
	return out, nil
}

// copyIfPresent copies src→dest when src exists; absence is not an error —
// the child simply starts without that file.
func copyIfPresent(src, dest string) error {
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read client config %s: %w", src, err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return fmt.Errorf("copy client config to %s: %w", dest, err)
	}
	return nil
}
