// Package runtime ports the RuntimeService essentials
// (src/core/runtime-service.ts): applying proxy endpoints and gateway
// profiles to client configurations, with exact file compensation for
// rollback.
package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/compensation"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/transport"
)

// Service applies configuration to clients.
type Service struct {
	Root     string
	Home     string // live client home (~)
	Clients  *client.Registry
	Profiles *profile.Store
	// Catalog informs protocol-aware endpoint routing (nil-safe). Synchronized
	// because plugin-host recovery rewrites it while resolutions run.
	Catalog *catalog.Index
}

// SmartEndpointRouting: an Anthropic-protocol client must never receive an
// OpenAI-style trailing-/v1 base — the SDK appends /v1/messages itself, so a
// /v1 base would double the segment and 404. Resolution order:
//
//  1. catalog-declared AnthropicEndpoint for the vendor (authoritative);
//  2. heuristic: strip one trailing "/v1" from the gateway endpoint;
//  3. otherwise leave the configured endpoint untouched.
func SmartEndpointFor(caps *client.Capabilities, vendorID, endpoint string, cat *catalog.Index) string {
	if caps == nil || caps.ProtocolPreference != transport.Anthropic {
		return endpoint
	}
	if entry, ok := cat.Lookup(vendorID); ok && entry.AnthropicEndpoint != "" {
		return entry.AnthropicEndpoint
	}
	trimmed := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1")
	}
	return endpoint
}

func clientHome(s *Service) string {
	if s.Home != "" {
		return s.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

func codexConfigPath(s *Service) string {
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return filepath.Join(env, "config.toml")
	}
	return filepath.Join(clientHome(s), ".codex", "config.toml")
}

// ProxyEndpointOptions mirrors applyProxyEndpoint's payload.
type ProxyEndpointOptions struct {
	Endpoint            string
	APIKey              string // proxy token; 'anypick-proxy' placeholder is never used implicitly
	DefaultModel        string
	ModelRoles          map[string]string
	Label               string
	SourceKind          string
	SourceProvider      string
	SourceName          string
	TransportCapability transport.Capability
}

// ApplyProxyEndpoint injects the managed proxy endpoint into a client's
// native config (account + managed proxy branch of WriteClientConfig).
func (s *Service) ApplyProxyEndpoint(ctx context.Context, clientID string, opts ProxyEndpointOptions) (*client.ApplyResult, error) {
	cad, err := s.Clients.Get(clientID)
	if err != nil {
		return nil, err
	}
	actx := &client.ApplyContext{
		Endpoint:            opts.Endpoint,
		APIKey:              opts.APIKey,
		DefaultModel:        opts.DefaultModel,
		ModelRoles:          opts.ModelRoles,
		AnyPickRoot:         s.Root,
		Home:                clientHome(s),
		SourceKind:          opts.SourceKind,
		SourceProvider:      opts.SourceProvider,
		SourceName:          opts.SourceName,
		TransportCapability: opts.TransportCapability,
		Mode:                "persistent",
	}
	if persistent, ok := cad.(client.PersistentAdapter); ok {
		return persistent.ApplyPersistent(ctx, actx)
	}
	return cad.Apply(actx)
}

// GatewayApplyInput is what a saved gateway contributes to a client's config.
//
// Two execution paths need exactly these fields: the persistent write
// (ApplyGatewayProfile) and the ephemeral isolated home (activation's
// createEphemeralHome). Resolving them separately is how `use <client> --with
// <gateway>` and `run <client> --with <gateway>` drift apart — the second one
// shipped without the model, the credential or the protocol-aware endpoint
// until a client that requires a model made the difference visible.
type GatewayApplyInput struct {
	// Endpoint is the gateway's URL mapped onto the surface that speaks the
	// client's protocol (SmartEndpointFor).
	Endpoint string
	APIKey   string
	Model    string
	Provider string
	Models   map[string]string // role → model override
}

// GatewayApplyInputFor resolves a gateway profile for one client.
func (s *Service) GatewayApplyInputFor(ctx context.Context, profileName, clientID string) (GatewayApplyInput, error) {
	cad, err := s.Clients.Get(clientID)
	if err != nil {
		return GatewayApplyInput{}, err
	}
	pr, err := s.Profiles.GetContext(ctx, profileName)
	if err != nil {
		return GatewayApplyInput{}, err
	}
	if pr == nil {
		return GatewayApplyInput{}, anyerr.Newf("Gateway `"+profileName+"` was not found.", "GATEWAY_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	if pr.Meta.Endpoint == "" {
		return GatewayApplyInput{}, anyerr.Newf(
			fmt.Sprintf("Gateway %s has no endpoint. Set one before use: anypick profile edit %s --endpoint …",
				profileName, profileName),
			"CLIENT_CONFIG_INVALID")
	}
	// Smart routing: pick the surface that speaks THIS client's protocol.
	endpoint := SmartEndpointFor(cad.Capabilities(), pr.Meta.Provider, pr.Meta.Endpoint, s.Catalog)

	apiKey := pr.Secrets.APIKey
	if apiKey == "" {
		apiKey = "anypick-proxy"
	}
	in := GatewayApplyInput{
		Endpoint: endpoint,
		APIKey:   apiKey,
		Model:    pr.Meta.DefaultModel,
		Provider: pr.Meta.Provider,
	}
	if pr.Meta.Models != nil {
		in.Models = map[string]string{}
		for role, model := range pr.Meta.Models {
			in.Models[role] = model
		}
	}
	return in, nil
}

// ApplyGatewayProfile resolves a saved gateway profile and applies it to the
// client (gateway branch of WriteClientConfig).
func (s *Service) ApplyGatewayProfile(ctx context.Context, profileName, clientID string) (*client.ApplyResult, error) {
	in, err := s.GatewayApplyInputFor(ctx, profileName, clientID)
	if err != nil {
		return nil, err
	}
	cad, err := s.Clients.Get(clientID)
	if err != nil {
		return nil, err
	}
	actx := &client.ApplyContext{
		Endpoint:            in.Endpoint,
		APIKey:              in.APIKey,
		DefaultModel:        in.Model,
		AnyPickRoot:         s.Root,
		Home:                clientHome(s),
		SourceKind:          "gateway",
		SourceProvider:      in.Provider,
		SourceName:          profileName,
		TransportCapability: transport.Direct,
		Mode:                "persistent",
	}
	if actx.ModelRoles == nil && in.Models != nil {
		actx.ModelRoles = map[string]string{}
		for role, model := range in.Models {
			actx.ModelRoles[role] = model
		}
	}
	if persistent, ok := cad.(client.PersistentAdapter); ok {
		return persistent.ApplyPersistent(ctx, actx)
	}
	return cad.Apply(actx)
}

// PrepareClientConfigRecovery persists exact file compensation BEFORE an
// adapter may touch disk: "src=>dest" records the executor can restore.
func (s *Service) PrepareClientConfigRecovery(clientID, journalID string) ([]string, error) {
	cad, err := s.Clients.Get(clientID)
	if err != nil {
		return nil, err
	}
	var sources []string
	switch cad.ID() {
	case "claude":
		sources = append(sources, filepath.Join(clientHome(s), ".claude", "settings.json"))
	case "codex":
		sources = append(sources, codexConfigPath(s))
	case "zcode":
		sources = append(sources, client.ZCodeConfigPaths(clientHome(s))...)
	default:
		return nil, nil
	}
	recoveryDir := filepath.Join(s.Root, "recovery", "clients", clientID)
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		return nil, anyerr.Wrapf(err, "create client config recovery directory", "CLIENT_CONFIG_BACKUP_FAILED")
	}
	// Two sources can share a basename — ZCode keeps a config.json under both
	// ~/.zcode/cli and ~/.zcode/v2. Restoring one into the other's path is far
	// worse than a slightly uglier backup name, so disambiguate with the
	// parent directory.
	names := recoveryBackupNames(sources)
	var records []string
	for i, src := range sources {
		data, readErr := os.ReadFile(src)
		dest := filepath.Join(recoveryDir, names[i]+"."+sanitize(journalID)+".bak")
		if readErr == nil {
			if writeErr := os.WriteFile(dest, data, 0o600); writeErr != nil {
				return nil, anyerr.Wrapf(
					writeErr,
					"back up client config "+src,
					"CLIENT_CONFIG_BACKUP_FAILED",
				)
			}
			records = append(records, src+"=>"+dest)
		} else if os.IsNotExist(readErr) {
			// File did not exist pre-activation; recovery must delete it back.
			records = append(records, "delete=>"+src)
		} else {
			return nil, anyerr.Wrapf(
				readErr,
				"read client config "+src,
				"CLIENT_CONFIG_BACKUP_FAILED",
			)
		}
	}
	return records, nil
}

// recoveryBackupNames derives a distinct backup filename for each source path.
func recoveryBackupNames(sources []string) []string {
	counts := map[string]int{}
	for _, src := range sources {
		counts[filepath.Base(src)]++
	}
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		base := filepath.Base(src)
		if counts[base] > 1 {
			out = append(out, filepath.Base(filepath.Dir(src))+"-"+base)
			continue
		}
		out = append(out, base)
	}
	return out
}

// RestoreClientConfigRecovery replays compensation records.
//
// Both the parse and the direction live in internal/compensation so the
// in-process undo and the crash-recovery path in internal/activation cannot
// disagree about which way a record reads (ADR 0004).
func RestoreClientConfigRecovery(records []string) error {
	for _, record := range records {
		rec, ok := compensation.Parse(record)
		if !ok {
			continue
		}
		if err := rec.Apply(); err != nil {
			return err
		}
	}
	return nil
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return string(out)
}
