package activation

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// RecoveryResult mirrors RecoveryResult.
type RecoveryResult struct {
	Recovered int
	Failed    []string
	Refused   []string
}

// RecoverIncompleteOperations ports recoverIncompleteOperations: exact file
// rollback from recorded backups, durable checkpoint restore, never forward
// execution when the source cannot be re-resolved exactly (spec §23.4).
func RecoverIncompleteOperations(ctx context.Context, deps *Deps) (*RecoveryResult, error) {
	incomplete, err := deps.Journal.ListIncomplete()
	if err != nil {
		return nil, err
	}
	result := &RecoveryResult{}
	for _, listed := range incomplete {
		// recoverOne takes the context explicitly because it runs either bare
		// or inside the lock below. Passing the outer ctx into the locked call
		// would re-enter with a context that does not carry the held scopes and
		// block on our own lock (ADR 0009).
		recoverOne := func(ctx context.Context) error {
			entry, err := deps.Journal.Get(listed.ID)
			if err != nil || entry == nil || !isIncompleteState(string(entry.State)) {
				return err
			}
			recover(ctx, deps, entry, result)
			return nil
		}
		scopes := recoveryScopes(listed)
		if deps.Root != "" && len(scopes) > 0 {
			if err := lock.WithMutationLocks(ctx, deps.Root, scopes, func(c context.Context) error {
				return recoverOne(c)
			}, 0); err != nil {
				result.Failed = append(result.Failed, listed.ID)
			}
		} else {
			if err := recoverOne(ctx); err != nil {
				result.Failed = append(result.Failed, listed.ID)
			}
		}
	}
	return result, nil
}

func isIncompleteState(state string) bool {
	switch journal.State(state) {
	case journal.StatePlanned, journal.StateExecuting,
		journal.StateVerifying, journal.StateRollingBack:
		return true
	}
	return false
}

// sourceRefFromJournal parses the canonical ref from params/affectedResources.
func sourceRefFromJournal(entry *journal.Entry) *refs.Ref {
	raw := ""
	if s, ok := entry.Params["source"].(string); ok {
		raw = s
	} else {
		for _, r := range entry.AffectedResources {
			if strings.HasPrefix(r, "account/") || strings.HasPrefix(r, "gateway/") ||
				strings.HasPrefix(r, "hub/") || strings.HasPrefix(r, "preset/") {
				raw = r
				break
			}
		}
	}
	if raw == "" {
		return nil
	}
	ref, err := refs.ParseRef(raw, nil)
	if err != nil {
		return nil
	}
	return &ref
}

func recover(ctx context.Context, deps *Deps, entry *journal.Entry, result *RecoveryResult) {
	sourceRef := sourceRefFromJournal(entry)
	reResolvedOk := true
	expectedSource := ""
	if s, ok := entry.Params["source"].(string); ok {
		expectedSource = s
	}

	if sourceRef != nil {
		if sourceRef.Kind == refs.KindPreset {
			// Presets are never forward-executed from journal entries.
			reResolvedOk = false
		} else {
			resolved, err := MaterializeResolvedSource(ctx, *sourceRef, deps)
			if err != nil {
				reResolvedOk = false
			} else if refs.SerializeRef(resolved.Ref) != refs.SerializeRef(*sourceRef) ||
				(expectedSource != "" && refs.SerializeRef(*sourceRef) != expectedSource) {
				reResolvedOk = false
			}
		}
	}

	restoredDurableState := false

	// Exact file rollback from "src=>dest" / "delete=>dest" records.
	if len(entry.BackupPaths) > 0 {
		allRestored := true
		for _, backup := range entry.BackupPaths {
			if strings.Contains(backup, "=>") {
				parts := strings.SplitN(backup, "=>", 2)
				src := strings.TrimSpace(parts[0])
				dest := strings.TrimSpace(parts[1])
				if src == "" || dest == "" {
					allRestored = false
					break
				}
				if src == "delete" {
					_ = os.RemoveAll(dest)
					continue
				}
				if _, err := os.Stat(src); err != nil {
					allRestored = false
					break
				}
				data, err := os.ReadFile(src)
				if err != nil {
					allRestored = false
					break
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil ||
					os.WriteFile(dest, data, 0o600) != nil {
					allRestored = false
					break
				}
			} else if _, err := os.Stat(backup); err != nil {
				allRestored = false
				break
			}
		}
		if allRestored {
			restoredDurableState = true
		} else {
			_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
			result.Failed = append(result.Failed, entry.ID)
			return
		}
	}

	// Durable native-auth checkpoints restore AFTER client config files
	// (reverse mutation order), then the journal is finalized.
	if cpRaw, ok := entry.Params["nativeAuthCheckpoint"]; ok {
		cpMap, isObj := cpRaw.(map[string]interface{})
		providerID, _ := cpMap["providerId"].(string)
		directory, _ := cpMap["directory"].(string)
		hadLiveAuth, _ := cpMap["hadLiveAuth"].(bool)
		if !isObj || providerID == "" || directory == "" {
			_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
			result.Failed = append(result.Failed, entry.ID)
			return
		}
		activeAccount, _ := cpMap["activeAccount"].(string)
		cp := &checkpointLike{
			ProviderID:    providerID,
			Directory:     directory,
			HadLiveAuth:   hadLiveAuth,
			ActiveAccount: activeAccount,
		}
		if err := deps.Accounts.restoreCheckpoint(ctx, cp); err != nil {
			_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
			result.Failed = append(result.Failed, entry.ID)
			return
		}
		_ = deps.Accounts.discardCheckpoint(cp)
		restoredDurableState = true
	}

	if restoredDurableState {
		_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateRolledBack})
		result.Recovered++
		return
	}

	if sourceRef != nil && !reResolvedOk {
		_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
		result.Refused = append(result.Refused, entry.ID)
		result.Failed = append(result.Failed, entry.ID)
		return
	}

	if entry.State == journal.StatePlanned {
		// Never started — no mutation to compensate.
		_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
		result.Recovered++
		return
	}

	// executing / verifying / rolling_back without backups cannot continue.
	_, _ = deps.Journal.Update(entry.ID, journal.Entry{State: journal.StateFailed})
	result.Failed = append(result.Failed, entry.ID)
}

// recoveryScopes ports recoveryScopes: journal refs map account/pool back onto
// their provider scope — the same lock an activation would have held.
func recoveryScopes(entry journal.Entry) []string {
	set := map[string]bool{}
	for _, resource := range entry.AffectedResources {
		switch {
		case strings.HasPrefix(resource, "client/") ||
			strings.HasPrefix(resource, "gateway/") ||
			strings.HasPrefix(resource, "hub/"):
			set[resource] = true
		case accountRefPattern.MatchString(resource):
			m := accountRefPattern.FindStringSubmatch(resource)
			set[refs.ProviderScope(m[1])] = true
		case poolRefPattern.MatchString(resource):
			m := poolRefPattern.FindStringSubmatch(resource)
			set[refs.ProviderScope(m[1])] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// checkpointLike is a decoded Checkpoint from journal params. The accounts
// view converts it for restore.
type checkpointLike struct {
	ProviderID    string
	ActiveAccount string
	HadLiveAuth   bool
	Directory     string
}
