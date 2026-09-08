// Package accounts ports the essential surface of src/core/service.ts:
// save, use (switch), current, and the live-auth checkpoints that make
// switches compensable. Proxy side effects stay behind an optional interface —
// a service that was never handed proxy collaborators must not fake them.
package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
	"github.com/outof0/anypick/go/internal/store"
)

// Checkpoint mirrors LiveAuthCheckpoint: the exact local auth captured before
// a switch, used to compensate failures and crash recovery.
type Checkpoint struct {
	ProviderID    string  `json:"providerId"`
	Source        *string `json:"source,omitempty"`
	ActiveAccount *string `json:"activeAccount"`
	HadLiveAuth   bool    `json:"hadLiveAuth"`
	Directory     string  `json:"directory"`
}

// SwitchResult mirrors SwitchResult (proxy fields omitted until phase 3).
type SwitchResult struct {
	Provider          string `json:"provider"`
	ProviderName      string `json:"providerName"`
	From              string `json:"from"`
	To                string `json:"to"`
	RefreshedPrevious bool   `json:"refreshedPrevious"`
	RefreshedAccount  string `json:"refreshedAccount,omitempty"`
}

// CurrentResult mirrors current() minus proxy status.
type CurrentResult struct {
	Active         string
	Live           provider.LiveAuthStatus
	Account        *store.Account
	IsLiveMatch    bool
	LiveMatchKnown bool
}

// Service is the Go AccountService.
type Service struct {
	Store     *store.AccountStore
	Providers *provider.Registry
	Proxy     ProxyStopper
}

// ProxyStopper is the narrow proxy lifecycle surface account deletion needs.
// It stays optional so account services used by import/tests do not need a
// running proxy composition.
type ProxyStopper interface {
	Stop(ctx context.Context, providerID, accountName string) error
}

func New(st *store.AccountStore, reg *provider.Registry) *Service {
	return &Service{Store: st, Providers: reg}
}

// BackupInputer is implemented by providers that accept explicit credential
// input (compatible vendors) instead of reading a native login.
type BackupInputer interface {
	BackupInput(kind, secret, profile, destDir string) (provider.SnapshotInfo, error)
}

// CredentialInput mirrors CredentialInput from src/types/account.ts.
type CredentialInput struct {
	Kind    string
	Secret  string
	Profile string // endpoint profile id (e.g. 'global', 'china')
}

// SaveInput saves an explicitly supplied credential (API key / plan key) as an
// account snapshot — the compatible-vendor path.
func (s *Service) SaveInput(ctx context.Context, providerID, name string, in CredentialInput, force bool) (store.Meta, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return store.Meta{}, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	inputer, ok := pv.(BackupInputer)
	if !ok {
		return store.Meta{}, anyerr.Newf(
			"Provider "+providerID+" does not accept credential inputs. Sign in natively and use save.",
			"UNSUPPORTED_CREDENTIAL_INPUT")
	}
	accountName, err := slug.AccountName(name)
	if err != nil {
		return store.Meta{}, anyerr.Newf(err.Error(), "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	var result store.Meta
	err = lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID), func(c context.Context) error {
		existing, getErr := s.Get(c, providerID, accountName)
		if getErr != nil {
			return getErr
		}
		if existing != nil && !force {
			return anyerr.Newf(
				"Account \""+accountName+"\" already exists for "+providerID+".",
				"ACCOUNT_EXISTS",
				anyerr.WithSuggestions("Pick another --name, or pass --force to overwrite."))
		}
		snapshotDir, _, err := s.Store.PrepareSnapshot(providerID, accountName)
		if err != nil {
			return err
		}
		info, err := inputer.BackupInput(in.Kind, in.Secret, in.Profile, snapshotDir)
		if err != nil {
			_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
			return err
		}
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		meta := store.Meta{
			Name: accountName, Provider: providerID,
			CreatedAt: now, UpdatedAt: now,
			CredentialKind:  info.CredentialKind,
			CredentialClass: store.CredentialClass(info.CredentialClass),
			Label:           info.Label,
		}
		if existing != nil {
			meta.CreatedAt = existing.Meta.CreatedAt
		}
		if err := s.Store.WriteMetaContext(c, meta, snapshotDir); err != nil {
			_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
			return err
		}
		_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
		result = meta
		return nil
	})
	if err != nil {
		return store.Meta{}, err
	}
	return result, nil
}

// List returns saved accounts for one provider. The service owns this read
// surface so outer layers do not reach through to the table-owning store.
func (s *Service) List(ctx context.Context, providerID string) ([]store.Account, error) {
	return s.Store.ListAccounts(ctx, providerID)
}

// Active is the cheap read behind "which account is selected for this
// provider". It deliberately does not do the live-auth probing that Current
// does: call sites that only want the pointer must not pay for, or be
// affected by, a live credential check.
func (s *Service) Active(ctx context.Context, providerID string) (string, error) {
	return s.Store.GetActive(ctx, providerID)
}

func (s *Service) Get(ctx context.Context, providerID, name string) (*store.Account, error) {
	normalized, err := slug.AccountName(name)
	if err != nil {
		return nil, err
	}
	return s.Store.Get(ctx, providerID, normalized)
}

// Save backs up live auth into a named account. writeMeta stays the atomic
// commit point: staging first, backup second, single transaction last.
func (s *Service) Save(ctx context.Context, providerID, name string, force bool) (store.Meta, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return store.Meta{}, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	accountName, err := slug.AccountName(name)
	if err != nil {
		return store.Meta{}, anyerr.Newf(err.Error(), "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	var result store.Meta
	err = lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID), func(c context.Context) error {
		existing, getErr := s.Get(c, providerID, accountName)
		if getErr != nil {
			return getErr
		}
		if existing != nil && !force {
			return anyerr.Newf(
				"Account \""+accountName+"\" already exists for "+providerID+".",
				"ACCOUNT_EXISTS",
				anyerr.WithSuggestions("Pick another --name, or pass --force to overwrite."))
		}
		meta, saveErr := s.saveLocked(c, pv, accountName, existing)
		if saveErr == nil {
			result = meta
		}
		return saveErr
	})
	if err != nil {
		return store.Meta{}, err
	}
	return result, nil
}

// RefreshSnapshot replaces one saved account with the provider's current live
// credential. Requiring the existing row prevents a UI refresh from silently
// creating a different account after a concurrent delete.
func (s *Service) RefreshSnapshot(ctx context.Context, providerID, name string) (store.Meta, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return store.Meta{}, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	accountName, err := slug.AccountName(name)
	if err != nil {
		return store.Meta{}, anyerr.Newf(err.Error(), "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	var result store.Meta
	err = lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID), func(c context.Context) error {
		existing, getErr := s.Store.Get(c, providerID, accountName)
		if getErr != nil {
			return getErr
		}
		if existing == nil {
			return anyerr.Newf(
				`No account "`+accountName+`" for provider "`+providerID+`".`,
				"ACCOUNT_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound),
			)
		}
		refreshed, refreshErr := s.saveLocked(c, pv, accountName, existing)
		if refreshErr == nil {
			result = refreshed
		}
		return refreshErr
	})
	return result, err
}

// Delete stops the exact account proxy, then removes the account and active
// pointer under the provider's single mutation scope (ADR 0011).
func (s *Service) Delete(ctx context.Context, providerID, name string) error {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	accountName, err := slug.AccountName(name)
	if err != nil {
		return anyerr.Newf(err.Error(), "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	return lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID), func(c context.Context) error {
		existing, err := s.Store.Get(c, providerID, accountName)
		if err != nil {
			return err
		}
		if existing == nil {
			return anyerr.Newf(
				`No account "`+accountName+`" for provider "`+providerID+`".`,
				"ACCOUNT_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound),
			)
		}
		if s.Proxy != nil && pv.CanProxy() {
			if err := s.Proxy.Stop(c, providerID, accountName); err != nil {
				return err
			}
		}
		// `c` carries the coordinator lock this closure runs under; `ctx` does
		// not, and re-entering with it would block on our own lock (ADR 0009).
		return s.Store.DeleteContext(c, providerID, accountName)
	})
}

func (s *Service) saveLocked(ctx context.Context, pv provider.Provider, accountName string, existing *store.Account) (store.Meta, error) {
	snapshotDir, _, err := s.Store.PrepareSnapshot(pv.ID(), accountName)
	if err != nil {
		return store.Meta{}, err
	}
	info, err := pv.Backup(snapshotDir)
	if err != nil {
		_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
		return store.Meta{}, err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	meta := store.Meta{
		Name:           accountName,
		Provider:       pv.ID(),
		CreatedAt:      now,
		UpdatedAt:      now,
		Identity:       info.Identity,
		Label:          info.Label,
		Notes:          info.Notes,
		CredentialKind: info.CredentialKind,
	}
	if existing != nil {
		meta.CreatedAt = existing.Meta.CreatedAt
	}
	if err := s.Store.WriteMetaContext(ctx, meta, snapshotDir); err != nil {
		_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
		return store.Meta{}, err
	}
	_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
	return meta, nil
}

// Use switches live auth to a saved account (useLocked in TS). The previous
// login is checkpointed before anything mutates; any failure restores it.
func (s *Service) Use(ctx context.Context, providerID, name string, opts UseOptions) (SwitchResult, error) {
	return useLocked(s, ctx, providerID, name, opts)
}

type UseOptions struct {
	NoRefresh bool
	NoProxy   bool
}

// CheckpointLiveAuth captures the real local login before a switch.
func (s *Service) CheckpointLiveAuth(ctx context.Context, providerID string, durable bool) (*Checkpoint, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return nil, err
	}
	checkpointRoot := os.TempDir()
	if durable {
		checkpointRoot = filepath.Join(s.Store.Root, "recovery", "live-auth")
	}
	if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(checkpointRoot, "anypick-live-checkpoint-")
	if err != nil {
		return nil, err
	}
	active, err := s.Store.GetActive(ctx, providerID)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	live, err := pv.DetectLive()
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	if live.Present {
		if _, err := pv.Backup(directory); err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
	}
	var activePtr *string
	if active != "" {
		copyActive := active
		activePtr = &copyActive
	}
	return &Checkpoint{
		ProviderID:    providerID,
		ActiveAccount: activePtr,
		HadLiveAuth:   live.Present,
		Directory:     directory,
	}, nil
}

// RestoreLiveAuthCheckpoint restores the captured login AND the active pointer.
func (s *Service) RestoreLiveAuthCheckpoint(ctx context.Context, cp *Checkpoint) error {
	pv, err := s.Providers.Get(cp.ProviderID)
	if err != nil {
		return err
	}
	if cp.HadLiveAuth {
		if err := pv.Restore(cp.Directory); err != nil {
			return err
		}
	} else if clearer, ok := pv.(provider.LiveClearer); ok {
		if err := clearer.ClearLive(); err != nil {
			return err
		}
	} else {
		return anyerr.Newf(
			"Cannot restore an empty "+pv.Name()+" login state safely.",
			"SWITCH_ROLLBACK_UNAVAILABLE")
	}
	if cp.ActiveAccount != nil && *cp.ActiveAccount != "" {
		return s.Store.SetActiveContext(ctx, cp.ProviderID, *cp.ActiveAccount)
	}
	return s.Store.ClearActiveContext(ctx, cp.ProviderID)
}

func (s *Service) DiscardLiveAuthCheckpoint(cp *Checkpoint) error {
	return os.RemoveAll(cp.Directory)
}

// ResolveIdentityTarget resolves a duplicate identity to its existing local
// account after confirmation (ports resolveIdentityTarget in src/core/service.ts).
func (s *Service) ResolveIdentityTarget(ctx context.Context, providerID string, identity, accountName string, allowOverwrite bool) (string, error) {
	if identity == "" || strings.TrimSpace(identity) == "" {
		return accountName, nil
	}
	accounts, err := s.Store.ListAccounts(ctx, providerID)
	if err != nil {
		return "", err
	}
	norm := normalizeIdentity(identity)
	var existingName string
	for _, acct := range accounts {
		if acct.Meta.Identity != "" && normalizeIdentity(acct.Meta.Identity) == norm {
			existingName = acct.Meta.Name
			break
		}
	}
	if existingName == "" || existingName == accountName {
		return accountName, nil
	}
	if allowOverwrite {
		return existingName, nil
	}
	return "", anyerr.Newf(
		fmt.Sprintf("Login %s is already saved as %s/%s.", identity, providerID, existingName),
		"ACCOUNT_IDENTITY_EXISTS",
		anyerr.WithSuggestions(fmt.Sprintf("Use %s/%s instead of saving a duplicate.", providerID, existingName)))
}

// Current reports the active pointer, live detection, and whether they agree.
func (s *Service) Current(ctx context.Context, providerID string) (*CurrentResult, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return nil, err
	}
	active, err := s.Store.GetActive(ctx, providerID)
	if err != nil {
		return nil, err
	}
	live, err := pv.DetectLive()
	if err != nil {
		return nil, err
	}
	out := &CurrentResult{Active: active, Live: live}
	if active == "" {
		return out, nil
	}
	account, err := s.Store.Get(ctx, providerID, active)
	if err != nil || account == nil {
		return out, err
	}
	out.Account = account
	match, known, matchErr := computeLiveMatch(pv, account.SnapshotDir, live)
	if matchErr == nil {
		out.LiveMatchKnown = known
		out.IsLiveMatch = match || (!known && active != "" && live.Present)
	} else {
		out.IsLiveMatch = live.Present
	}
	// Identity fallback: refreshed tokens can change material while keeping
	// the same upstream identity.
	if !out.IsLiveMatch && account.Meta.CredentialKind != "proxy-only" &&
		account.Meta.Identity != "" && live.Identity != "" &&
		identitiesEqual(account.Meta.Identity, live.Identity) {
		out.IsLiveMatch = true
	}
	return out, nil
}

func computeLiveMatch(pv provider.Provider, snapshotDir string, live provider.LiveAuthStatus) (match, known bool, err error) {
	matcher, ok := pv.(provider.LiveMatcher)
	if !ok {
		return false, false, nil
	}
	match, known, err = matcher.SnapshotMatchesLive(snapshotDir)
	if err != nil {
		return false, true, err
	}
	return match, known, nil
}

func identitiesEqual(a, b string) bool { return normalizeIdentity(a) == normalizeIdentity(b) }

// normalizeIdentity compares emails case-insensitively (mirrors
// identitiesMatch in TS without its full alias table).
func normalizeIdentity(identity string) string {
	var parsed struct {
		Email string `json:"email"`
		Raw   string
	}
	if err := json.Unmarshal([]byte(identity), &parsed); err == nil && parsed.Email != "" {
		return lower(parsed.Email)
	}
	return lower(identity)
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
