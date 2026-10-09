package activation

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/compensation"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// RecoveryResult summarizes what RecoverIncompleteOperations completed, failed, or refused.
type RecoveryResult struct {
	Recovered int
	Failed    []string
	Refused   []string
}

// RecoverIncompleteOperations performs exact file rollback from recorded
// backups and durable checkpoint restore, never forward execution when the
// source cannot be re-resolved exactly (spec §23.4).
func RecoverIncompleteOperations(ctx context.Context, deps *Deps) (*RecoveryResult, error) {
	incomplete, err := deps.Journal.ListIncompleteContext(ctx)
	if err != nil {
		return nil, err
	}
	result := &RecoveryResult{}
	for _, listed := range incomplete {
		// doctor:fix rows are NOT activations: their affected resources are
		// raw filesystem paths, which no branch below recognises, so an
		// executing row fell through to markFailed and made every startup
		// print "incomplete operations could not be rolled back" for a
		// self-referential non-operation. complete_journal_rollback consumes
		// these rows deliberately (it knows what a doctor repair means);
		// activation recovery must leave them alone.
		if listed.Type == "doctor:fix" {
			continue
		}
		// recoverOne takes the context explicitly because it runs either bare
		// or inside the lock below. Passing the outer ctx into the locked call
		// would re-enter with a context that does not carry the held scopes and
		// block on our own lock (ADR 0009).
		recoverOne := func(ctx context.Context) error {
			entry, err := deps.Journal.GetContext(ctx, listed.ID)
			if err != nil || entry == nil || !isIncompleteState(string(entry.State)) {
				return err
			}
			if entry.Corrupt {
				// The recorded resources are unknown, so there is nothing to
				// compensate exactly. Report it as unrecovered rather than
				// "nothing to do": claiming a clean recovery over a
				// half-applied mutation is worse than admitting the gap, and
				// the other entries still get their chance.
				result.Failed = append(result.Failed, entry.ID)
				return nil
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
	// Reverse mutation order: client-config files first, then the native-auth
	// checkpoint. Each compensation is independent: a failed client-config
	// restore must not skip the native-auth restore (that would abandon the
	// live login on the switched identity), and a failed item must not skip
	// the independent items behind it. Aggregate and finalize once.
	var restoreErrs []string

	// markJournal records the terminal transition. A refused transition is
	// itself a failure — the entry stays incomplete and will be retried next
	// startup — so success is only counted when the journal accepts it.
	markJournal := func(state journal.State) bool {
		_, err := deps.Journal.UpdateContext(ctx, entry.ID, journal.Entry{State: state})
		return err == nil
	}
	markFailed := func() {
		markJournal(journal.StateFailed)
		result.Failed = append(result.Failed, entry.ID)
	}

	// Exact file rollback from compensation records.
	//
	// The parse and the direction live in internal/compensation, shared with
	// runtime.RestoreClientConfigRecovery. Two readers of one wire format is
	// how the direction got inverted once already: this path used to copy the
	// live file over its own backup, destroying the only copy (ADR 0004).
	// record_test.go fails the build if a second parser appears.
	if len(entry.BackupPaths) > 0 {
		restored := false
		for _, backup := range entry.BackupPaths {
			rec, ok := compensation.Parse(backup)
			if !ok {
				// Not a record: a bare backup path, which must still be there.
				// A missing one fails this item but not the items behind it.
				if _, err := os.Stat(backup); err != nil {
					restoreErrs = append(restoreErrs, "client-config")
				} else {
					restored = true
				}
				continue
			}
			if err := rec.Apply(); err != nil {
				restoreErrs = append(restoreErrs, "client-config")
				continue
			}
			restored = true
		}
		if restored {
			restoredDurableState = true
		}
	}

	// Durable native-auth checkpoints restore AFTER client config files
	// (reverse mutation order), then the journal is finalized.
	if entry.Params != nil {
		if raw, ok := entry.Params["activationUndo"]; ok && raw != nil {
			// Hub route, hub port, and binding writes are journalled before
			// they land. The in-memory undo stack is gone after a kill.
			restored, undoErrs := applyActivationUndo(ctx, deps, raw)
			restoreErrs = append(restoreErrs, undoErrs...)
			if restored {
				restoredDurableState = true
			}
		}
	}
	if cpRaw, ok := entry.Params["nativeAuthCheckpoint"]; ok {
		cpMap, isObj := cpRaw.(map[string]interface{})
		providerID, _ := cpMap["providerId"].(string)
		directory, _ := cpMap["directory"].(string)
		hadLiveAuth, _ := cpMap["hadLiveAuth"].(bool)
		if !isObj || providerID == "" || directory == "" {
			restoreErrs = append(restoreErrs, "native-auth-checkpoint")
		} else {
			activeAccount, _ := cpMap["activeAccount"].(string)
			cp := &checkpointLike{
				ProviderID:    providerID,
				Directory:     directory,
				HadLiveAuth:   hadLiveAuth,
				ActiveAccount: activeAccount,
			}
			if err := deps.Accounts.restoreCheckpoint(ctx, cp); err != nil {
				restoreErrs = append(restoreErrs, "native-auth")
			} else if derr := deps.Accounts.discardCheckpoint(cp); derr != nil {
				restoreErrs = append(restoreErrs, "native-auth-discard")
			} else {
				restoredDurableState = true
			}
		}
	}

	if len(restoreErrs) > 0 {
		markFailed()
		return
	}

	if restoredDurableState {
		if markJournal(journal.StateRolledBack) {
			result.Recovered++
		} else {
			result.Failed = append(result.Failed, entry.ID)
		}
		return
	}

	if sourceRef != nil && !reResolvedOk {
		if markJournal(journal.StateFailed) {
			result.Refused = append(result.Refused, entry.ID)
		}
		result.Failed = append(result.Failed, entry.ID)
		return
	}

	if entry.State == journal.StatePlanned {
		// Never started — no mutation to compensate.
		if markJournal(journal.StateFailed) {
			result.Recovered++
		} else {
			result.Failed = append(result.Failed, entry.ID)
		}
		return
	}

	// executing / verifying / rolling_back without backups cannot continue.
	markFailed()
}

// recoveryScopes maps journal refs back onto lock scopes: account/pool refs
// map onto their provider scope — the same lock an activation would have held.
func recoveryScopes(entry journal.Entry) []string {
	set := map[string]bool{}
	for _, resource := range entry.AffectedResources {
		switch {
		case strings.HasPrefix(resource, "client/") ||
			strings.HasPrefix(resource, "client-profile/") ||
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
	// client-profile/ sorts before client/. Acquiring both up front, in
	// this order, is what WithMutationLocks also does. Taking only client/
	// and locking client-profile/ later deadlocks with a profile activation
	// that already holds client-profile/ and is waiting for client/.
	sort.Strings(out)
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
