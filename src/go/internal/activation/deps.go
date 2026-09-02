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

func NewPresetsView(s *presets.Store) *presetsView {
	return &presetsView{Store: s}
}

func NewPoolsView(s *pools.Store) *poolsView {
	return &poolsView{Store: s}
}

type accountLike struct {
	providerID   string
	name         string
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

type bindingsStore = bindings.Store

type presetsView struct{ Store *presets.Store }

type presetExpansion struct {
	presetID       string
	presetName     string
	presetRevision int
	source         *ResolvedSource
	model          bindings.ModelSelection
	bindingSpec    bindings.Spec
}

func (v *presetsView) GetByName(name string) (*presets.Saved, error) {
	return v.Store.GetByName(name)
}

func (v *presetsView) GetContext(ctx context.Context, name string) (*presets.Saved, error) {
	return v.Store.GetByNameContext(ctx, name)
}

type poolsView struct{ Store *pools.Store }

func (v *poolsView) Get(providerID string) (*pools.Pool, error) { return v.Store.Get(providerID) }

func (v *poolsView) GetContext(ctx context.Context, providerID string) (*pools.Pool, error) {
	return v.Store.GetContext(ctx, providerID)
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
