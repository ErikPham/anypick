package accounts

import (
	"context"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
)

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// useLocked ports AccountService.use: preflight → checkpoint → refresh
// previous → restore target → set active. Any failure restores the checkpoint.
// Proxy start/stop hooks are phase 3; NoProxy semantics are therefore
// currently identical to the default path and recorded in SwitchResult only.
func useLocked(s *Service, ctx context.Context, providerID, name string, opts UseOptions) (SwitchResult, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return SwitchResult{}, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	accountName, err := slug.AccountName(name)
	if err != nil {
		return SwitchResult{}, anyerr.Newf(err.Error(), "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	var result SwitchResult
	err = lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID),
		func(c context.Context) error {
			r, runErr := s.useUnlocked(c, pv, accountName, opts)
			if runErr == nil {
				result = r
			}
			return runErr
		})
	if err != nil {
		return SwitchResult{}, err
	}
	return result, nil
}

func (s *Service) useUnlocked(ctx context.Context, pv provider.Provider, accountName string, opts UseOptions) (SwitchResult, error) {
	target, err := s.Store.Require(pv.ID(), accountName)
	if err != nil {
		return SwitchResult{}, err
	}

	previous, err := s.Store.GetActive(pv.ID())
	if err != nil {
		return SwitchResult{}, err
	}

	if pr, ok := pv.(provider.PreflightRestorer); ok && target.Meta.CredentialKind != "proxy-only" {
		if err := pr.PreflightRestore(target.SnapshotDir); err != nil {
			return SwitchResult{}, err
		}
	}

	checkpoint, err := s.CheckpointLiveAuth(ctx, pv.ID(), false)
	if err != nil {
		return SwitchResult{}, err
	}
	defer func() { _ = s.DiscardLiveAuthCheckpoint(checkpoint) }()

	refreshedPrevious := false
	refreshedAccount := ""
	if !opts.NoRefresh && previous != "" && previous != accountName {
		live, err := pv.DetectLive()
		if err == nil && live.Present {
			previousMeta, getErr := s.Store.Get(pv.ID(), previous)
			// Activating a proxy-only account never displaced the native
			// login; writing the live login into its snapshot would overwrite
			// the credential the user typed in.
			if getErr == nil && previousMeta != nil && previousMeta.Meta.CredentialKind != "proxy-only" {
				if refreshed, saveErr := s.saveLiveIntoExistingAccount(ctx, pv, previous); saveErr == nil {
					refreshedAccount = refreshed
					refreshedPrevious = refreshed == previous
				}
			}
		}
	}

	// A proxy-only snapshot holds no native login — restoring it would at best
	// write nothing and at worst delete the real login.
	if target.Meta.CredentialKind != "proxy-only" {
		if err := pv.Restore(target.SnapshotDir); err != nil {
			restoreErr := s.restoreCheckpointQuiet(ctx, checkpoint)
			if restoreErr != nil {
				return SwitchResult{}, anyerr.Wrapf(restoreErr,
					"Account switch failed and rollback could not complete", "SWITCH_ROLLBACK_FAILED")
			}
			return SwitchResult{}, err
		}
	}
	if err := s.Store.SetActive(pv.ID(), accountName); err != nil {
		_ = s.restoreCheckpointQuiet(ctx, checkpoint)
		return SwitchResult{}, err
	}

	return SwitchResult{
		Provider:          pv.ID(),
		ProviderName:      pv.Name(),
		From:              previous,
		To:                accountName,
		RefreshedPrevious: refreshedPrevious,
		RefreshedAccount:  refreshedAccount,
	}, nil
}

func (s *Service) restoreCheckpointQuiet(ctx context.Context, cp *Checkpoint) error {
	if err := s.RestoreLiveAuthCheckpoint(ctx, cp); err != nil {
		return err
	}
	return s.DiscardLiveAuthCheckpoint(cp)
}

// saveLiveIntoExistingAccount persists the current live login into an already
// saved account snapshot (refresh before switching away from it).
func (s *Service) saveLiveIntoExistingAccount(ctx context.Context, pv provider.Provider, name string) (string, error) {
	existing, err := s.Store.Get(pv.ID(), name)
	if err != nil || existing == nil {
		if err != nil {
			return "", err
		}
		return "", anyerr.Newf("No account \""+name+"\" for provider \""+pv.ID()+".",
			"ACCOUNT_NOT_FOUND", anyerr.WithExitCode(anyerr.NotFound))
	}
	snapshotDir, _, err := s.Store.PrepareSnapshot(pv.ID(), name)
	if err != nil {
		return "", err
	}
	info, err := pv.Backup(snapshotDir)
	if err != nil {
		_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
		return "", err
	}
	meta := existing.Meta
	meta.UpdatedAt = nowISO()
	if info.Identity != "" {
		meta.Identity = info.Identity
	}
	if err := s.Store.WriteMeta(meta, snapshotDir); err != nil {
		_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
		return "", err
	}
	_ = s.Store.DiscardPreparedSnapshot(snapshotDir)
	return name, nil
}
