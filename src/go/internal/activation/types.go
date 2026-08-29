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

// StepKind enumerates PlanStepKind — all 23 literals from src/types/bindings.ts.
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
)

// PlanStep mirrors PlanStep.
type PlanStep struct {
	Kind   StepKind               `json:"kind"`
	Detail string                 `json:"detail,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// RollbackStep mirrors RollbackStep.
type RollbackStep struct {
	Kind   string                 `json:"kind"`
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
	Token    string `json:"token,omitempty"`
}

// ResolvedTransport mirrors ResolvedTransport.
type ResolvedTransport struct {
	Capability   transport.Capability `json:"capability"`
	Protocol     transport.Protocol   `json:"protocol"`
	Endpoint     string               `json:"endpoint,omitempty"`
	ManagedProxy *ManagedProxyRef     `json:"managedProxy,omitempty"`
}

// ResolvedSource mirrors ResolvedSource.
type ResolvedSource struct {
	Ref     refs.Ref
	Kind    string // 'account' | 'gateway' | 'proxy-hub'
	Adapter transport.Adapter
	Display string
}

// Request mirrors ActivationRequest.
type Request struct {
	Mode            Mode
	Client          string
	Source          *refs.Ref // account | gateway | proxy-hub
	Preset          string
	Model           string
	ModelRoles      map[string]string
	ProjectRoot     string
	TransportPolicy transport.Policy
}

// Plan mirrors ActivationPlan.
type Plan struct {
	Mode           Mode                    `json:"mode"`
	Client         string                  `json:"client"`
	ResolvedSource *ResolvedSource         `json:"-"`
	SourceRefJSON  map[string]interface{}  `json:"source,omitempty"`
	Transport      ResolvedTransport       `json:"transport"`
	Model          bindings.ModelSelection `json:"model"`
	Steps          []PlanStep              `json:"steps"`
	Rollback       []RollbackStep          `json:"rollback"`
	Warnings       []Warning               `json:"warnings"`
	BindingSpec    *bindings.Spec          `json:"-"`
	Provenance     *bindings.Provenance    `json:"-"`
}
