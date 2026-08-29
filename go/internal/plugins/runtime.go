package plugins

import (
	"context"
	"fmt"
	"os"
)

// Candidate is a digest-verified enabled package ready to be handed to the
// isolated Node host. It does not mean JavaScript has been imported or loaded.
type Candidate struct {
	Record   Record   `json:"record"`
	Manifest Manifest `json:"manifest"`
	Entry    string   `json:"entry"`
}

// LoadFailure is non-fatal startup degradation for one enabled package.
type LoadFailure struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Untrusted bool   `json:"untrusted"`
}

// LoadedPlugin is one package whose activation committed to the native
// registries. The executable objects remain owned by the isolated host.
type LoadedPlugin struct {
	Record       Record `json:"record"`
	ActivationID string `json:"activationId"`
}

// Runtime is the startup view used by plugin list, doctor, and desktop
// degraded-state reporting. Candidates only survive when activation has not
// been attempted (for example, the caller requested inspection only).
type Runtime struct {
	Candidates []Candidate    `json:"candidates"`
	Loaded     []LoadedPlugin `json:"loaded"`
	Failures   []LoadFailure  `json:"failures"`
}

// HostDiagnostic is a redacted health view for doctor and desktop protocol
// surfaces. Counts are safe; plugin values and invocation arguments never
// leave the private host.
type HostDiagnostic struct {
	State           string `json:"state"` // inactive | running | degraded | unavailable
	LoadedPlugins   int    `json:"loadedPlugins"`
	ActivationCount int    `json:"activationCount,omitempty"`
	ObjectCount     int    `json:"objectCount,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Diagnostics combines dynamic host health with startup load failures.
type Diagnostics struct {
	Host     HostDiagnostic `json:"host"`
	Failures []LoadFailure  `json:"failures"`
}

// Inspect validates every enabled record without executing JavaScript. A
// refused package is collected, never allowed to abort the core app.
func (s *Service) Inspect(ctx context.Context) (Runtime, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return Runtime{}, err
	}
	runtime := Runtime{Candidates: []Candidate{}, Loaded: []LoadedPlugin{}, Failures: []LoadFailure{}}
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		manifest, err := ReadManifest(record.Path)
		if err == nil && manifest.Name != record.Name {
			err = invalid(
				fmt.Sprintf(
					`Plugin at %s now calls itself %q but is installed as %q.`,
					record.Path, manifest.Name, record.Name,
				),
				"Remove and re-add the plugin.",
			)
		}
		var entry string
		if err == nil {
			entry, err = ResolveEntry(record.Path, manifest.Main)
		}
		var digest string
		if err == nil {
			digest, err = DigestPluginPackage(record.Path)
		}
		untrusted := false
		if err == nil && digest != record.Digest {
			untrusted = true
			err = fmt.Errorf("Plugin %s has changed since you trusted it.", record.Name)
		}
		if err != nil {
			runtime.Failures = append(runtime.Failures, LoadFailure{
				Name: record.Name, Path: record.Path, Reason: err.Error(), Untrusted: untrusted,
			})
			continue
		}
		runtime.Candidates = append(runtime.Candidates, Candidate{
			Record: record, Manifest: manifest, Entry: entry,
		})
	}
	return runtime, nil
}

// DisabledByEnv mirrors ANYPICK_NO_PLUGINS=1|true for safe startup bisects.
func DisabledByEnv() bool {
	value := os.Getenv("ANYPICK_NO_PLUGINS")
	return value == "1" || value == "true"
}
