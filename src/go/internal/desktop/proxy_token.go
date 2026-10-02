package desktop

import (
	"context"
	"encoding/json"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// mutateProxyToken returns one proxy or hub bearer in the result payload.
// The snapshot never carries it (ADR 0006); this command is the desktop
// counterpart of `proxy token` and `proxy hub token`. The message stays free
// of the secret because the host renders Message and not Payload.
func (s *Server) mutateProxyToken(ctx context.Context, m Mutate, result Result) Result {
	if m.ProviderID == "proxy-hub" {
		return s.mutateHubProxyToken(ctx, m, result)
	}
	return s.mutateAccountProxyToken(ctx, m, result)
}

func (s *Server) mutateHubProxyToken(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Hub == nil {
		return result.Fail(serviceUnavailableErr("proxy hub"))
	}
	token, err := s.Deps.Hub.EnsureHubTokenContext(ctx, m.Name)
	if err != nil {
		return result.Fail(err)
	}
	return proxyTokenResult(result, token)
}

func (s *Server) mutateAccountProxyToken(ctx context.Context, m Mutate, result Result) Result {
	if s.Deps.Proxy == nil {
		return result.Fail(serviceUnavailableErr("proxy"))
	}
	rst, err := s.Deps.Proxy.RuntimeState(ctx, m.ProviderID, m.Name)
	if err != nil {
		return result.Fail(err)
	}
	if rst == nil || rst.Token == "" {
		return result.Fail(anyerr.Newf(
			"no active token for proxy (is it running?)",
			"PROXY_NOT_RUNNING", anyerr.WithExitCode(anyerr.NotFound)))
	}
	return proxyTokenResult(result, rst.Token)
}

func proxyTokenResult(result Result, token string) Result {
	payload, err := json.Marshal(struct {
		APIKey string `json:"apiKey"`
	}{APIKey: token})
	if err != nil {
		return result.Fail(err)
	}
	result.Payload = payload
	result.Message = "API key ready."
	return result
}
