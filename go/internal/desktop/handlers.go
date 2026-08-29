// Desktop transfer (import/export/backup), gateway-test and hub source
// toggle handlers. Business logic stays in services; these only translate
// protocol payloads.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/hub"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transfer"
)

func (s *Server) handleTransferOp(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success"}
	fail := func(err error) Result {
		return Result{Version: ProtocolVersion, RequestID: m.RequestID,
			Status: "error", Message: err.Error()}
	}
	switch m.Operation {
	case "account-import":
		if m.Path == "" || m.ProviderID == "" || m.Name == "" {
			return fail(anyerr.Newf("account-import needs providerId, name and path", "INVALID_USAGE"))
		}
		err := lock.WithMutationLock(ctx, s.Deps.Root,
			refs.ProviderScope(m.ProviderID), func(c context.Context) error {
				deps := transfer.ImportDeps{
					Store:     s.Deps.Store,
					Providers: s.Deps.Providers,
					ResolveIdentityTarget: func(c context.Context, providerID, identity, accountName string, allowOverwrite bool) (string, error) {
						return s.Deps.Accounts.ResolveIdentityTarget(c, providerID, identity, accountName,
							allowOverwrite || m.Overwrite)
					},
				}
				_, err := transfer.ImportEnvelope(deps, c, m.ProviderID, m.Name, m.Path, m.Overwrite)
				return err
			})
		if err != nil {
			return fail(err)
		}
	case "account-export":
		outPath := m.Path
		if outPath == "" || m.ProviderID == "" || m.Name == "" {
			return fail(anyerr.Newf("account-export needs providerId, name and path", "INVALID_USAGE"))
		}
		if err := lock.WithMutationLock(ctx, s.Deps.Root,
			refs.ProviderScope(m.ProviderID), func(c context.Context) error {
				return transfer.ExportEnvelope(s.Deps.Store, c, m.ProviderID, m.Name, outPath)
			}); err != nil {
			return fail(err)
		}
	case "account-backup-all":
		dest := backupDestination(m.Path)
		var selection []transfer.BackupEntry
		for _, providerID := range s.Deps.Providers.IDs() {
			list, lerr := s.Deps.Store.ListAccounts(providerID)
			if lerr != nil {
				continue
			}
			for _, acct := range list {
				selection = append(selection, transfer.BackupEntry{Provider: providerID, Name: acct.Meta.Name})
			}
		}
		_, err := transfer.ExportSelectedBackup(s.Deps.Store, ctx, selection, dest, m.Overwrite)
		if err != nil {
			return fail(err)
		}
	case "account-backup-export":
		// Selective export: the native client sends the exact account list.
		if m.Path == "" {
			return fail(anyerr.Newf("account-backup-export needs a destination path", "INVALID_USAGE"))
		}
		if len(m.Accounts) == 0 {
			return fail(anyerr.Newf("account-backup-export needs at least one account", "BACKUP_EMPTY"))
		}
		selection := make([]transfer.BackupEntry, 0, len(m.Accounts))
		for _, ref := range m.Accounts {
			selection = append(selection, transfer.BackupEntry{Provider: ref.Provider, Name: ref.Name})
		}
		if _, err := transfer.ExportSelectedBackup(s.Deps.Store, ctx, selection, m.Path, m.Overwrite); err != nil {
			return fail(err)
		}
	case "account-backup-import":
		// Import resolves a previously inspected package by inspectionId; the
		// protocol never sends a raw path here.
		count, err := s.inspections.Restore(ctx, s.Deps.Store, s.Deps.Providers,
			m.InspectionID, m.EntryIDs, m.OverwriteConfirmed)
		if err != nil {
			return fail(err)
		}
		_ = count
	}
	return result
}

// startEnabledProxies starts every account proxy whose stored config is
// enabled (startEnabledProxies in src/tray/supervisor-mutations.ts).
func (s *Server) startEnabledProxies(ctx context.Context) {
	for _, providerID := range s.Deps.Providers.IDs() {
		list, lerr := s.Deps.Store.ListAccounts(providerID)
		if lerr != nil {
			continue
		}
		for _, acc := range list {
			cfg, _ := s.Deps.Store.GetProxyConfig(providerID, acc.Meta.Name)
			if !cfg.Enabled {
				continue
			}
			if st, serr := s.Deps.Proxy.Status(providerID, acc.Meta.Name); serr == nil && st != nil && st.Running {
				continue
			}
			_, _ = s.Deps.Proxy.StartProxy(ctx, providerID, acc.Meta.Name)
		}
	}
}

// restartRunningPools restarts pool proxies that are currently running so a
// Quota Guard toggle takes effect immediately (restartRunningPools in TS).
func (s *Server) restartRunningPools(ctx context.Context) error {
	if s.Deps.Proxy == nil || s.Deps.Proxy.Pools == nil {
		return nil
	}
	var restartErrs []error
	for _, providerID := range s.Deps.Providers.IDs() {
		pool, perr := s.Deps.Proxy.Pools.GetContext(ctx, providerID)
		if perr != nil {
			restartErrs = append(restartErrs,
				fmt.Errorf("inspect %s pool configuration: %w", providerID, perr))
			continue
		}
		if pool == nil || pool.Mode != "multi" || !pool.Enabled {
			continue
		}
		running, rerr := s.poolProxyIsRunning(ctx, providerID)
		if rerr != nil {
			restartErrs = append(restartErrs,
				fmt.Errorf("inspect %s pool process: %w", providerID, rerr))
			continue
		}
		if !running {
			continue
		}
		// StartPoolProxy performs a verified stop/start while holding the
		// provider and pool scopes. Calling account Stop/Start here would
		// restart the wrong process and split the provider mutation scope.
		if _, rerr := s.Deps.Proxy.StartPoolProxy(ctx, providerID); rerr != nil {
			restartErrs = append(restartErrs,
				fmt.Errorf("restart %s pool proxy: %w", providerID, rerr))
		}
	}
	joined := errors.Join(restartErrs...)
	if joined != nil {
		// Keep TypeScript's per-provider isolation by attempting every pool,
		// then surface the aggregate to the mutation result and diagnostic log.
		s.logErr(joined)
	}
	return joined
}

// poolProxyIsRunning verifies shared-pool ownership from the NULL-account
// lease before consulting account-backed runtime state. Account proxies have
// similar state rows, so Status alone cannot identify a pool process.
func (s *Server) poolProxyIsRunning(ctx context.Context, providerID string) (bool, error) {
	leases, err := s.Deps.Proxy.Jobs.ListPoolLeasesContext(ctx, providerID)
	if err != nil {
		return false, err
	}
	if len(leases) == 0 {
		return false, nil
	}
	accounts, err := s.Deps.Store.ListAccountsContext(ctx, providerID)
	if err != nil {
		return false, err
	}
	for _, account := range accounts {
		state, serr := s.Deps.Store.ReadProxyStateContext(ctx, providerID, account.Meta.Name)
		if serr != nil {
			return false, serr
		}
		if state == nil {
			continue
		}
		for _, lease := range leases {
			if lease.InstanceID == "" || lease.InstanceID != state.InstanceID {
				continue
			}
			status, serr := s.Deps.Proxy.StatusContext(ctx, providerID, account.Meta.Name)
			if serr != nil {
				return false, serr
			}
			if status != nil && status.Running &&
				(lease.Endpoint == "" || lease.Endpoint == status.Endpoint) {
				return true, nil
			}
		}
	}
	return false, nil
}

func backupDestination(p string) string {
	if p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("anypick-backup-%d.anypick-backup", time.Now().Unix()))
}

// handleGatewayTest probes a gateway endpoint with the profile's key. Only a
// status code is reported — the key never appears in output.
func (s *Server) handleGatewayTest(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID}
	pr, err := s.Deps.Profiles.GetContext(ctx, m.Name)
	if err != nil {
		result.Status, result.Message = "error", err.Error()
		return result
	}
	if pr == nil {
		result.Status, result.Message = "error", "gateway not found"
		return result
	}
	client := &http.Client{Timeout: 6 * time.Second}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(pr.Meta.Endpoint, "/")+"/models", nil)
	if rerr == nil && pr.Secrets.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+pr.Secrets.APIKey)
	}
	if rerr != nil {
		result.Status, result.Message = "error", rerr.Error()
		return result
	}
	resp, derr := client.Do(req)
	if derr != nil {
		result.Status, result.Message = "error", "endpoint unreachable"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		result.Status, result.Message = "error", fmt.Sprintf("gateway returned HTTP %d", resp.StatusCode)
		return result
	}
	result.Status = "success"
	return result
}

// handleHubSourceToggle adds or removes one source on an existing hub config.
// Presence toggles: an attached source is removed, an absent one is added.
func (s *Server) handleHubSourceToggle(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID}
	name := orString(m.Name, "default")
	account := orString(m.SourceID, m.Label)
	err := lock.WithMutationLock(ctx, s.Deps.Root, refs.MutationScopeForRef(refs.ProxyHubRef(name)), func(c context.Context) error {
		cfg, gerr := s.Deps.Hub.GetConfigContext(c, name)
		if gerr != nil {
			return gerr
		}
		if cfg == nil {
			return anyerr.Newf("Proxy Hub not found: "+name, "HUB_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound))
		}
		found := false
		kept := make([]hub.Source, 0, len(cfg.Sources))
		for _, src := range cfg.Sources {
			if src.ProviderID == m.ProviderID && src.Account == account {
				found = true
				continue
			}
			kept = append(kept, src)
		}
		if !found {
			if m.ProviderID == "" || account == "" {
				return anyerr.Newf("source needs both providerId and account", "INVALID_USAGE")
			}
			kept = append(kept, hub.Source{ProviderID: m.ProviderID, Account: account})
		}
		cfg.Sources = kept
		_, uerr := s.Deps.Hub.UpsertConfigContext(c, *cfg)
		return uerr
	})
	if err != nil {
		result.Status, result.Message = "error", err.Error()
	}
	return result
}
