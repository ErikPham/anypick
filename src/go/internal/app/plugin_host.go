// Plugin activation and diagnostics after the executable host was removed.
//
// AnyPick used to run plugin packages by starting a private Node process
// (internal/pluginhost) and talking to it over stdio RPC. ADR-0020 §5 removes
// that: a native release ships one Go engine and no Node runtime, and
// executable ESM plugins are not a native-release requirement.
//
// What survives here is the seam, deliberately:
//
//   - internal/plugins still owns the registry: manifests, whole-package
//     digests, enable/disable, add/remove. `anypick plugin …` keeps working.
//   - Nothing executes the package. Every candidate degrades to a LoadFailure
//     with a reason that names this removal, so `doctor` and the desktop
//     protocol stay authoritative instead of silently reporting a healthy
//     empty plugin set.
//   - proxy.PluginLifecycle and internal/proxy/service_plugin.go are kept even
//     though no provider currently implements provider.ProxyStarter. They are
//     the contract a future Go-native declarative executor would implement.
//
// When that executor lands it replaces activatePlugins and this file's header;
// the registry, the CLI and the proxy contract do not change.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/proxy"
)

// ErrPluginHostRemoved reports that a package cannot run because this build
// has no plugin host. It is the reason recorded on every candidate and is
// surfaced unchanged by doctor, `plugin list`, and the desktop protocol.
var ErrPluginHostRemoved = errors.New(
	"this build of AnyPick has no plugin host: executable plugin packages are not supported",
)

// pluginSupervisor is the bookkeeping side of plugin state. It used to own a
// supervised Node process across generations; with the host gone it records
// startup outcomes and reports an inert runtime to every diagnostic surface.
type pluginSupervisor struct {
	app *App

	mu              sync.Mutex
	startupFailures []plugins.LoadFailure
	loaded          []plugins.LoadedPlugin
	closing         bool
}

func newPluginSupervisor(a *App) *pluginSupervisor {
	return &pluginSupervisor{app: a}
}

func (s *pluginSupervisor) recordStartupFailure(failure plugins.LoadFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupFailures = append(s.startupFailures, failure)
}

// runtimeSnapshot is the startup view consumed by PluginDiagnostics. It is
// frozen at startup because no candidate can ever load.
func (s *pluginSupervisor) runtimeSnapshot() ([]plugins.LoadedPlugin, []plugins.LoadFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]plugins.LoadedPlugin(nil), s.loaded...),
		append([]plugins.LoadFailure(nil), s.startupFailures...)
}

// Identity is the zero value: there is no supervised generation. An invalid
// identity makes every ownership check in internal/proxy fail closed, which is
// the correct answer when no plugin-owned child can exist.
func (s *pluginSupervisor) Identity() proxy.PluginRuntimeIdentity {
	return proxy.PluginRuntimeIdentity{}
}

// TerminateGeneration has no generation to kill. A zero expectation is a
// no-op; anything else names a generation this build can never have produced,
// so it is refused rather than silently accepted.
func (s *pluginSupervisor) TerminateGeneration(expected proxy.PluginRuntimeIdentity) error {
	if !expected.IsValid() {
		return nil
	}
	return fmt.Errorf("%w: cannot terminate %+v", ErrPluginHostRemoved, expected)
}

func (s *pluginSupervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closing = true
}

// activatePlugins records one non-fatal failure per digest-verified candidate.
// Candidates are preserved so `plugin list` still shows what the registry
// knows about; Loaded stays empty because nothing can commit registrations.
func (a *App) activatePlugins(_ context.Context, discovered plugins.Runtime) plugins.Runtime {
	runtime := plugins.Runtime{
		Candidates: append([]plugins.Candidate(nil), discovered.Candidates...),
		Loaded:     []plugins.LoadedPlugin{},
		Failures:   append([]plugins.LoadFailure(nil), discovered.Failures...),
	}
	// Inspect-stage failures (manifest/digest refusals) happened before any
	// host question and are owned here so doctor/desktop stay authoritative.
	for _, failure := range discovered.Failures {
		a.pluginSup.recordStartupFailure(failure)
	}
	for _, candidate := range discovered.Candidates {
		failure := plugins.LoadFailure{
			Name:      candidate.Record.Name,
			Path:      candidate.Record.Path,
			Reason:    ErrPluginHostRemoved.Error(),
			Untrusted: false,
		}
		runtime.Failures = append(runtime.Failures, failure)
		a.pluginSup.recordStartupFailure(failure)
	}
	return runtime
}

// PluginDiagnostics returns the immutable startup view. The host is always
// "inactive" because there is no host; failures carry the real reasons,
// including the per-candidate removal notice.
func (a *App) PluginDiagnostics(_ context.Context) plugins.Diagnostics {
	loaded, failures := a.pluginSup.runtimeSnapshot()
	return plugins.Diagnostics{
		Host:     plugins.HostDiagnostic{State: "inactive", LoadedPlugins: len(loaded)},
		Failures: failures,
	}
}
