// Composition glue between the proxy Service and plugin providers that own
// their own compatibility-proxy lifecycle (GO-PARITY-M7). The adapter is
// stateless transport: tokens (ADR 0006), instance verification (ADR 0003),
// leases and persisted records stay inside internal/proxy. Capability comes
// only from startProxy presence on the registered adapter — never from client
// registries or protocols.
package app

import (
	"context"
	"fmt"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
)

type pluginProxyLifecycle struct {
	app *App
}

func (l pluginProxyLifecycle) providerFor(providerID string) (provider.Provider, error) {
	pv, err := l.app.Providers.Get(providerID)
	if err != nil {
		return nil, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	return pv, nil
}

func (l pluginProxyLifecycle) ForwardsLifecycle(providerID string) bool {
	pv, err := l.app.Providers.Get(providerID)
	if err != nil {
		return false
	}
	_, ok := pv.(provider.ProxyStarter)
	return ok
}

func (l pluginProxyLifecycle) StartProxy(
	ctx context.Context,
	req proxy.PluginRequest,
) (*proxy.PluginStart, error) {
	pv, err := l.providerFor(req.ProviderID)
	if err != nil {
		return nil, err
	}
	starter, ok := pv.(provider.ProxyStarter)
	if !ok {
		return nil, anyerr.Newf(
			fmt.Sprintf("Provider %q does not implement startProxy.", req.ProviderID),
			"PROXY_UNSUPPORTED")
	}
	handle, err := starter.StartProxy(ctx, pluginRequestContext(req))
	// The serving generation comes from the host pointer that actually ran
	// the call (stable even if the supervisor later advanced), never from
	// plugin JSON.
	runtime := servingRuntimeOf(pv)
	if !runtime.IsValid() {
		runtime = l.app.pluginSup.Identity()
	}
	if err != nil {
		return &proxy.PluginStart{Runtime: runtime}, err
	}
	started := &proxy.PluginStart{
		Endpoint: handle.Endpoint, Compatibility: handle.Compatibility,
		PID: handle.PID, InstanceID: handle.InstanceID, LogPath: handle.LogPath,
		Runtime: runtime,
	}
	if err := proxy.ValidatePluginStart(started); err != nil {
		return started, err
	}
	return started, nil
}

type pluginRuntimeSource interface {
	pluginRuntime() proxy.PluginRuntimeIdentity
}

func servingRuntimeOf(pv provider.Provider) proxy.PluginRuntimeIdentity {
	if src, ok := pv.(pluginRuntimeSource); ok {
		return src.pluginRuntime()
	}
	if base, ok := rpcBaseOf(pv); ok && base != nil && base.proxy != nil {
		return base.proxy.pluginRuntime()
	}
	return proxy.PluginRuntimeIdentity{}
}

// RuntimeIdentity is engine supervision metadata, not plugin output. The proxy
// service uses it only after /health also proves that this exact PID serves the
// returned instance. Callers capture it BEFORE dispatching a lifecycle call so
// the identity always names the generation that actually served the request.
func (l pluginProxyLifecycle) RuntimeIdentity() proxy.PluginRuntimeIdentity {
	return l.app.pluginSup.Identity()
}

func (l pluginProxyLifecycle) StopProxy(ctx context.Context, req proxy.PluginRequest) error {
	pv, err := l.providerFor(req.ProviderID)
	if err != nil {
		return err
	}
	stopper, ok := pv.(provider.ProxyStopper)
	if !ok {
		return proxy.ErrPluginMethodAbsent
	}
	return stopper.StopProxy(ctx, pluginRequestContext(req))
}

func (l pluginProxyLifecycle) ProxyStatus(
	ctx context.Context,
	req proxy.PluginRequest,
) (*proxy.Status, error) {
	pv, err := l.providerFor(req.ProviderID)
	if err != nil {
		return nil, err
	}
	reporter, ok := pv.(provider.ProxyStatusReporter)
	if !ok {
		return nil, proxy.ErrPluginMethodAbsent
	}
	status, err := reporter.ProxyStatus(ctx, pluginRequestContext(req))
	if err != nil {
		return nil, err
	}
	return &proxy.Status{
		Enabled: status.Enabled, Running: status.Running, Port: status.Port,
		Host: status.Host, Endpoint: status.Endpoint, Compatibility: status.Compatibility,
		PID: status.PID, LogPath: status.LogPath, Detail: status.Detail,
	}, nil
}

func (l pluginProxyLifecycle) ReadProxyLogs(
	ctx context.Context,
	req proxy.PluginRequest,
	lines int,
) (string, error) {
	pv, err := l.providerFor(req.ProviderID)
	if err != nil {
		return "", err
	}
	reader, ok := pv.(provider.ProxyLogReader)
	if !ok {
		return "", proxy.ErrPluginMethodAbsent
	}
	return reader.ReadProxyLogs(ctx, pluginRequestContext(req), lines)
}

// Terminate is the last-resort ownership lever for cancelled starts whose
// settlement could not be proven: killing the runtime guarantees nothing the
// not-yet-settled coroutine does afterwards can outlive it. The caller must
// pass the exact generation it observed BEFORE dispatching the failed call —
// a stale reference is refused instead of killing a replacement generation
// that may now be serving other accounts' proxies. Built-ins are unaffected.
func (l pluginProxyLifecycle) Terminate(_ context.Context, expected proxy.PluginRuntimeIdentity) error {
	return l.app.pluginSup.TerminateGeneration(expected)
}

// pluginRequestContext converts the Service's request into the TS-shaped
// ProxyContext the plugin RPC expects.
func pluginRequestContext(req proxy.PluginRequest) provider.ProxyContext {
	return provider.ProxyContext{
		ProviderID:  req.ProviderID,
		AccountName: req.AccountName,
		SnapshotDir: req.SnapshotDir,
		RuntimeDir:  req.RuntimeDir,
		Config:      req.Config,
		Token:       req.Token,
	}
}
