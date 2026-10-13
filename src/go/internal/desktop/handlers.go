// Desktop transfer (import/export/backup), gateway-test and hub source
// toggle handlers. Business logic stays in services; these only translate
// protocol payloads.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	accountsvc "github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transfer"
)

func (s *Server) handleTransferOp(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID, Status: "success"}
	fail := func(err error) Result { return errorResult(m.RequestID, err) }
	switch m.Operation {
	case "account-import":
		if m.Path == "" || m.ProviderID == "" || m.Name == "" {
			return fail(payloadErr("account-import", "providerId, name and path"))
		}
		err := lock.WithMutationLock(ctx, s.Deps.Root,
			refs.ProviderScope(m.ProviderID), func(c context.Context) error {
				_, err := s.Deps.Accounts.Import(c, accountsvc.ImportRequest{
					Paths: []string{m.Path}, ProviderID: m.ProviderID, Name: m.Name, Force: m.Overwrite,
				})
				return err
			})
		if err != nil {
			return fail(err)
		}
	case "account-export":
		outPath := m.Path
		if outPath == "" || m.ProviderID == "" || m.Name == "" {
			return fail(payloadErr("account-export", "providerId, name and path"))
		}
		if err := lock.WithMutationLock(ctx, s.Deps.Root,
			refs.ProviderScope(m.ProviderID), func(c context.Context) error {
				return s.Deps.Accounts.ExportEnvelope(c, m.ProviderID, m.Name, outPath)
			}); err != nil {
			return fail(err)
		}
	case "account-backup-all":
		dest := backupDestination(m.Path)
		var selection []transfer.BackupEntry
		for _, providerID := range s.Deps.Providers.IDs() {
			list, lerr := s.Deps.Accounts.List(ctx, providerID)
			if lerr != nil {
				continue
			}
			for _, acct := range list {
				selection = append(selection, transfer.BackupEntry{Provider: providerID, Name: acct.Meta.Name})
			}
		}
		count, err := s.Deps.Accounts.ExportSelectedBackup(ctx, selection, dest, m.Overwrite)
		if err != nil {
			return fail(err)
		}
		// The Tauri shell has no file picker and only renders Result.Message,
		// so the written path has to be in the sentence the user sees.
		result.Message = backupWrittenMessage(count, dest)
	case "account-backup-export":
		// Selective export: the native client sends the exact account list.
		if m.Path == "" {
			return fail(payloadErr("account-backup-export", "a destination path"))
		}
		if len(m.Accounts) == 0 {
			return fail(anyerr.Newf("account-backup-export needs at least one account",
				"BACKUP_EMPTY",
				anyerr.WithSuggestions("Select at least one account to export.")))
		}
		selection := make([]transfer.BackupEntry, 0, len(m.Accounts))
		for _, ref := range m.Accounts {
			selection = append(selection, transfer.BackupEntry{Provider: ref.Provider, Name: ref.Name})
		}
		if _, err := s.Deps.Accounts.ExportSelectedBackup(ctx, selection, m.Path, m.Overwrite); err != nil {
			return fail(err)
		}
	case "account-backup-import":
		// Import resolves a previously inspected package by inspectionId; the
		// protocol never sends a raw path here.
		count, err := s.inspections.Restore(ctx, m.InspectionID, m.EntryIDs, m.OverwriteConfirmed)
		if err != nil {
			return fail(err)
		}
		_ = count
	case "account-backup-forget":
		// Discard a staged review copy before its TTL (B3). Idempotent:
		// unknown ids succeed so a twice-forgotten review cannot error.
		s.inspections.Forget(m.InspectionID)
	}
	return result
}

// startEnabledProxies starts every account proxy whose stored config is
// enabled.
func (s *Server) startEnabledProxies(ctx context.Context) {
	for _, providerID := range s.Deps.Providers.IDs() {
		list, lerr := s.Deps.Accounts.List(ctx, providerID)
		if lerr != nil {
			continue
		}
		for _, acc := range list {
			cfg, _ := s.Deps.Proxy.ProxyConfig(ctx, providerID, acc.Meta.Name)
			if !cfg.Enabled {
				continue
			}
			if st, serr := s.Deps.Proxy.StatusContext(ctx, providerID, acc.Meta.Name); serr == nil && st != nil && st.Running {
				continue
			}
			_, _ = s.Deps.Proxy.StartProxy(ctx, providerID, acc.Meta.Name)
		}
	}
}

// restartRunningPools restarts pool proxies that are currently running so a
// Quota Guard toggle takes effect immediately.
func (s *Server) restartRunningPools(ctx context.Context) error {
	if s.Deps.Proxy == nil || !s.Deps.Proxy.HasPools() {
		return nil
	}
	var restartErrs []error
	for _, providerID := range s.Deps.Providers.IDs() {
		pool, perr := s.Deps.Proxy.GetPoolContext(ctx, providerID)
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
		// Attempt every pool, then surface the aggregate to the mutation
		// result and diagnostic log.
		s.logErr(joined)
	}
	return joined
}

// poolProxyIsRunning verifies shared-pool ownership from the NULL-account
// lease before consulting account-backed runtime state. Account proxies have
// similar state rows, so Status alone cannot identify a pool process.
func (s *Server) poolProxyIsRunning(ctx context.Context, providerID string) (bool, error) {
	leases, err := s.Deps.Proxy.ListPoolLeasesContext(ctx, providerID)
	if err != nil {
		return false, err
	}
	if len(leases) == 0 {
		return false, nil
	}
	accounts, err := s.Deps.Accounts.List(ctx, providerID)
	if err != nil {
		return false, err
	}
	for _, account := range accounts {
		state, serr := s.Deps.Proxy.RuntimeState(ctx, providerID, account.Meta.Name)
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

func backupWrittenMessage(count int, dest string) string {
	noun := "accounts"
	if count == 1 {
		noun = "account"
	}
	return fmt.Sprintf("Saved %d %s to %s. The file contains account credentials.", count, noun, dest)
}

// handleGatewayTest probes a gateway endpoint with the profile's key. Only a
// status code is reported — the key never appears in output. A pre-create
// test carries the endpoint, key, or account id the user just typed; those
// win over a saved gateway that happens to share the sentinel name. A saved
// gateway retest sends only the name, and the stored credential is probed.
func (s *Server) handleGatewayTest(ctx context.Context, m Mutate) Result {
	result := Result{Version: ProtocolVersion, RequestID: m.RequestID}
	endpoint, apiKey := strings.TrimSpace(m.Endpoint), m.APIKey
	supplied := endpoint != "" || apiKey != "" || strings.TrimSpace(m.Profile) != ""
	if !supplied && s.Deps.Profiles != nil {
		pr, err := s.Deps.Profiles.GetContext(ctx, m.Name)
		if err != nil {
			result = result.Fail(err)
			return result
		}
		if pr != nil {
			endpoint, apiKey = pr.Meta.Endpoint, pr.Secrets.APIKey
		}
	}
	if endpoint == "" && m.ProviderID != "" {
		// Account-derived providers (Cloudflare): the sheet probes before
		// anything is saved, so derive from the supplied profile. A saved
		// gateway already resolved above and never reaches here.
		if pv, perr := s.Deps.Providers.Get(m.ProviderID); perr == nil && pv != nil {
			if gi, ok := pv.(provider.GatewayInfoProvider); ok && gi.GatewayInfo().RequiresAccountID {
				derived, derr := deriveGatewayEndpoint(s.Deps.Providers, m.ProviderID, m.Profile)
				if derr != nil {
					result = result.Fail(derr)
					return result
				}
				endpoint = derived
			}
		}
	}
	if endpoint == "" {
		// Pre-save probe with nothing typed yet: name the missing field,
		// not a stored object the user never created.
		result = result.Fail(anyerr.Newf("Please enter an endpoint URL and API key above, then test again",
			"GATEWAY_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound),
			anyerr.WithSuggestions("Check that the Endpoint and API Key fields are filled before testing.")))
		return result
	}
	client := &http.Client{Timeout: 6 * time.Second}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpoint, "/")+"/models", nil)
	if rerr == nil && apiKey != "" {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}
	if rerr != nil {
		result = result.Fail(rerr)
		return result
	}
	resp, derr := client.Do(req)
	if derr != nil {
		result = result.Fail(anyerr.Newf("endpoint unreachable: could not connect to the gateway server", "GATEWAY_UNREACHABLE",
			anyerr.WithCause(derr),
			anyerr.WithSuggestions(
				"Verify the endpoint URL is spelled correctly and includes https://.",
				"Check that your internet connection is active and the host is online.")))
		return result
	}
	defer resp.Body.Close()
	if statusErr := evaluateGatewayResponse(resp); statusErr != nil {
		result = result.Fail(statusErr)
		return result
	}
	result.Status = "success"
	result.Message = "Gateway connection verified. Models were loaded successfully."
	return result
}

func evaluateGatewayResponse(resp *http.Response) error {
	if err := evaluateGatewayStatus(resp.StatusCode); err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return anyerr.Newf("gateway returned a response that is not a model catalog", "GATEWAY_CATALOG_INVALID",
			anyerr.WithCause(err),
			anyerr.WithSuggestions("The endpoint must answer GET /models with a JSON catalog."))
	}
	return evaluateGatewayCatalog(body)
}

func evaluateGatewayStatus(statusCode int) error {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return anyerr.Newf(
			fmt.Sprintf("endpoint rejected the API key (HTTP %d)", statusCode),
			"GATEWAY_AUTH_REJECTED",
			anyerr.WithSuggestions(
				"Check that your API key is valid and has not expired.",
				"Ensure the key has permission to list models and make requests."))
	}
	if statusCode < 200 || statusCode >= 300 {
		return anyerr.Newf(
			fmt.Sprintf("gateway returned HTTP %d", statusCode),
			"GATEWAY_HTTP_ERROR",
			anyerr.WithSuggestions(
				"The gateway service may be experiencing high load or temporary downtime.",
				"Please wait a moment and try testing the connection again."))
	}
	return nil
}

// evaluateGatewayCatalog accepts an OpenAI-style `data` list, a `models`
// list, or a bare JSON array. An empty list is a live gateway that has no
// models. HTML and other objects are not a catalog, even on HTTP 200.
func evaluateGatewayCatalog(body []byte) error {
	fail := func() error {
		return anyerr.Newf("gateway returned a response that is not a model catalog", "GATEWAY_CATALOG_INVALID",
			anyerr.WithSuggestions("The endpoint must answer GET /models with a JSON catalog."))
	}
	var probe any
	if json.Unmarshal([]byte(strings.TrimSpace(string(body))), &probe) != nil {
		return fail()
	}
	switch doc := probe.(type) {
	case []any:
		return nil
	case map[string]any:
		if _, ok := doc["data"]; ok {
			return nil
		}
		if _, ok := doc["models"]; ok {
			return nil
		}
	}
	return fail()
}
