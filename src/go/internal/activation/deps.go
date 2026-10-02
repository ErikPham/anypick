package activation

import (
	"context"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/presets"
	"github.com/outof0/anypick/go/internal/refs"
)

// The view types below keep the activation package decoupled from concrete
// services while preserving the exact data the TS ResolveSourceDeps provided.

// NewAccountsView / NewPresetsView / NewPoolsView are composition helpers.
func NewAccountsView(svc *accounts.Service) *accountsView {
	return &accountsView{Svc: svc}
}

func NewPresetsView(svc *presets.Service) *presetsView {
	return &presetsView{Svc: svc}
}

func NewPoolsView(svc *pools.Service) *poolsView {
	return &poolsView{Svc: svc}
}

type accountLike struct {
	providerID   string
	name         string
	snapshotDir  string
	proxyEnabled bool
	proxyPort    int
}

type accountsView struct{ Svc *accounts.Service }

func (v *accountsView) Get(ctx context.Context, providerID, name string) (*accountLike, error) {
	a, err := v.Svc.Get(ctx, providerID, name)
	if err != nil || a == nil {
		return nil, err
	}
	return &accountLike{
		providerID:   a.Meta.Provider,
		name:         a.Meta.Name,
		snapshotDir:  a.SnapshotDir,
		proxyEnabled: a.Proxy.Enabled,
		proxyPort:    a.Proxy.Port,
	}, nil
}

func (v *accountsView) loadForRef(ctx context.Context, ref refs.Ref) (*accountLike, error) {
	if ref.Kind != refs.KindAccount {
		return nil, nil
	}
	return v.Get(ctx, ref.Provider, ref.Name)
}

type presetsView struct{ Svc *presets.Service }

type presetExpansion struct {
	presetID       string
	presetName     string
	presetRevision int
	source         *ResolvedSource
	model          bindings.ModelSelection
	bindingSpec    bindings.Spec
}

func (v *presetsView) GetByName(name string) (*presets.Saved, error) {
	return v.Svc.GetByNameContext(context.Background(), name)
}

func (v *presetsView) GetContext(ctx context.Context, name string) (*presets.Saved, error) {
	return v.Svc.GetByNameContext(ctx, name)
}

type poolsView struct{ Svc *pools.Service }

func (v *poolsView) Get(providerID string) (*pools.Pool, error) {
	return v.Svc.GetContext(context.Background(), providerID)
}

func (v *poolsView) GetContext(ctx context.Context, providerID string) (*pools.Pool, error) {
	return v.Svc.GetContext(ctx, providerID)
}

// restoreCheckpoint / discardCheckpoint adapt a journal-decoded checkpoint
// onto the real AccountService.
func (v *accountsView) restoreCheckpoint(ctx context.Context, cp *checkpointLike) error {
	converted := &accounts.Checkpoint{
		ProviderID:  cp.ProviderID,
		HadLiveAuth: cp.HadLiveAuth,
		Directory:   cp.Directory,
	}
	if cp.ActiveAccount != "" {
		a := cp.ActiveAccount
		converted.ActiveAccount = &a
	}
	return v.Svc.RestoreLiveAuthCheckpoint(ctx, converted)
}

func (v *accountsView) discardCheckpoint(cp *checkpointLike) error {
	return v.Svc.DiscardLiveAuthCheckpoint(&accounts.Checkpoint{Directory: cp.Directory})
}
