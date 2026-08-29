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

// ApplyGatewayProfile resolves a saved gateway profile and applies it to the
// client (gateway branch of WriteClientConfig).
func (s *Service) ApplyGatewayProfile(ctx context.Context, profileName, clientID string) (*client.ApplyResult, error) {
	cad, err := s.Clients.Get(clientID)
	if err != nil {
		return nil, err
	}
	pr, err := s.Profiles.GetContext(ctx, profileName)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, anyerr.Newf("Gateway `"+profileName+"` was not found.", "GATEWAY_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	endpoint := pr.Meta.Endpoint
	if endpoint == "" {
		return nil, anyerr.Newf(
			fmt.Sprintf("Gateway %s has no endpoint. Set one before use: anypick profile edit %s --endpoint …",
				profileName, profileName),
			"CLIENT_CONFIG_INVALID")
	}
	// Smart routing: pick the surface that speaks THIS client's protocol.
	endpoint = SmartEndpointFor(cad.Capabilities(), pr.Meta.Provider, endpoint, s.Catalog)

	apiKey := pr.Secrets.APIKey
	if apiKey == "" {
		apiKey = "anypick-proxy"
	}
	actx := &client.ApplyContext{
		Endpoint:            endpoint,
		APIKey:              apiKey,
		DefaultModel:        pr.Meta.DefaultModel,
		AnyPickRoot:         s.Root,
		Home:                clientHome(s),
		SourceKind:          "gateway",
		SourceProvider:      pr.Meta.Provider,
		SourceName:          profileName,
		TransportCapability: transport.Direct,
		Mode:                "persistent",
	}
	if actx.ModelRoles == nil && pr.Meta.Models != nil {
		actx.ModelRoles = map[string]string{}
		for role, model := range pr.Meta.Models {
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
	default:
		return nil, nil
	}
	recoveryDir := filepath.Join(s.Root, "recovery", "clients", clientID)
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		return nil, anyerr.Wrapf(err, "create client config recovery directory", "CLIENT_CONFIG_BACKUP_FAILED")
	}
	var records []string
	for _, src := range sources {
		data, readErr := os.ReadFile(src)
		dest := filepath.Join(recoveryDir, filepath.Base(src)+"."+sanitize(journalID)+".bak")
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

// RestoreClientConfigRecovery replays compensation records ("src=>dest" or
// "delete=>dest").
func RestoreClientConfigRecovery(records []string) error {
	for _, record := range records {
		src, dest, ok := splitRecord(record)
		if !ok {
			continue
		}
		if src == "delete" {
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(src, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func splitRecord(record string) (src, dest string, ok bool) {
	for i := 0; i+1 < len(record); i++ {
		if record[i] == '=' && record[i+1] == '>' {
			return trimSpace(record[:i]), trimSpace(record[i+2:]), true
		}
	}
	return "", "", false
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
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
