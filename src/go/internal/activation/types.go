// Package activation ports src/core/activation-planner.ts,
// activation-executor.ts, and their helper modules.
//
// The plan is the single source of truth (invariant #3): the executor
// interprets planned steps and never re-derives behavior from transport
// capability, so planner and executor cannot silently diverge.
package activation

import (
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

// StepKind enumerates PlanStepKind — the activation steps shared by normal
// bindings and named client profiles.
type StepKind string

const (
	StepResolveSource            StepKind = "ResolveSource"
	StepExpandPreset             StepKind = "ExpandPreset"
	StepValidateCompatibility    StepKind = "ValidateCompatibility"
	StepValidateCredential       StepKind = "ValidateCredential"
	StepInspectClientState       StepKind = "InspectClientState"
	StepResolveTransport         StepKind = "ResolveTransport"
	StepValidateExternalDep      StepKind = "ValidateExternalDependency"
	StepAllocateProxyLease       StepKind = "AllocateProxyLease"
	StepStartProxy               StepKind = "StartProxy"
	StepWaitForHealth            StepKind = "WaitForHealth"
	StepEnsureProxyHub           StepKind = "EnsureProxyHub"
	StepAttachProxyHubRoute      StepKind = "AttachProxyHubRoute"
	StepWaitForHubHealth         StepKind = "WaitForHubHealth"
	StepValidateProxyHubRoute    StepKind = "ValidateProxyHubRoute"
	StepWriteNativeAuth          StepKind = "WriteNativeAuth"
	StepWriteClientConfig        StepKind = "WriteClientConfig"
	StepCreateEnvironmentOverlay StepKind = "CreateEnvironmentOverlay"
	StepCreateTempClientHome     StepKind = "CreateTemporaryClientHome"
	StepVerifyEffectiveState     StepKind = "VerifyEffectiveState"
	StepCommitGlobalBinding      StepKind = "CommitGlobalBinding"
	StepCommitProjectBinding     StepKind = "CommitProjectBinding"
	StepCommitClientProfile      StepKind = "CommitClientProfile"
	StepSpawnChild               StepKind = "SpawnChild"
	StepReleaseLease             StepKind = "ReleaseLease"
	StepRestoreTemporaryState    StepKind = "RestoreTemporaryState"
)

// Mode mirrors the ActivationRequest mode union.
type Mode string

const (
	ModePersistent Mode = "persistent"
	ModeEphemeral  Mode = "ephemeral"
	ModeProject    Mode = "project"
	ModeProfile    Mode = "profile"
)

type PlanStep struct {
	Kind   StepKind               `json:"kind"`
	Detail string                 `json:"detail,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// Warning mirrors PlanWarning.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ManagedProxyRef mirrors ResolvedTransport.managedProxy. Token is owner-only:
// it must never reach an output boundary (invariant #2).
type ManagedProxyRef struct {
	Provider string `json:"provider"`
	Account  string `json:"account,omitempty"`
	Port     int    `json:"port"`
	LeaseID  string `json:"leaseId"`
	Token    string `json:"-"`
}

type ResolvedTransport struct {
	Capability   transport.Capability `json:"capability"`
	Protocol     transport.Protocol   `json:"protocol"`
	Endpoint     string               `json:"endpoint,omitempty"`
	ManagedProxy *ManagedProxyRef     `json:"managedProxy,omitempty"`
}

// ResolvedSource is a ref after materialization, ready for planning.
type ResolvedSource struct {
	Ref     refs.Ref
	Kind    string // 'account' | 'gateway' | 'proxy-hub'
	Adapter transport.Adapter
	Display string
}

// Request mirrors ActivationRequest.
type Request struct {
	Mode        Mode
	Client      string
	Source      *refs.Ref // account | gateway | proxy-hub
	ProfileName string    // native named profile (currently Codex)
	Preset      string
	Model       string
	ModelRoles  map[string]string
	// ModelCatalog is the source-scoped model list used by client adapters
	// whose native configuration has its own model registry.
	ModelCatalog []string
	// ModelCatalogErr carries the discovery failure behind an empty
	// ModelCatalog, if any, so planning can explain a missing default model
	// actionably (stale login vs unsupported catalog). Nil when discovery
	// succeeded or was never attempted.
	ModelCatalogErr error
	ProjectRoot     string
	TransportPolicy transport.Policy
}

// Plan mirrors ActivationPlan.
type Plan struct {
	Mode        Mode   `json:"mode"`
	Client      string `json:"client"`
	ProfileName string `json:"profileName,omitempty"`
	ProjectRoot string `json:"-"`
	// NativeAuthSnapshotDir is kept out of serialized plans and output. It is
	// only the source path used to copy an account's Codex auth.json into the
	// private profile CODEX_HOME.
	NativeAuthSnapshotDir string                  `json:"-"`
	ResolvedSource        *ResolvedSource         `json:"-"`
	SourceRefJSON         map[string]interface{}  `json:"source,omitempty"`
	Transport             ResolvedTransport       `json:"transport"`
	Model                 bindings.ModelSelection `json:"model"`
	ModelCatalog          []string                `json:"-"`
	Steps                 []PlanStep              `json:"steps"`
	Warnings              []Warning               `json:"warnings"`
	BindingSpec           *bindings.Spec          `json:"-"`
	Provenance            *bindings.Provenance    `json:"-"`
}
