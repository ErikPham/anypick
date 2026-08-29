// Ports AccountService.login/loginMethods (src/core/service.ts): run a
// provider-owned browser sign-in, then commit its native credential as an
// AnyPick account. The prior native login and active pointer are restored
// exactly if any step after checkpointing fails.
package accounts

import (
	"context"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
	"github.com/outof0/anypick/go/internal/store"
)

const fallbackAccountName = "stash"

// LoginResult mirrors ProviderLoginResult minus protocol framing.
type LoginResult struct {
	Provider   string               `json:"provider"`
	Account    store.Meta           `json:"account"`
	Method     provider.LoginMethod `json:"method"`
	BackedUpTo *string              `json:"backedUpTo,omitempty"`
}

// LoginOptions carries the caller-supplied naming and method choice.
type LoginOptions struct {
	MethodID string
	Source   string
	Name     string
	Label    string
}

// LoginMethods lists a provider's browser sign-in methods, filtered by source
// and ordered by preference only (official first, then preferred, then label).
func (s *Service) LoginMethods(_ context.Context, providerID, source string) ([]provider.LoginMethod, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return nil, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	methodsp, isMethodAware := pv.(provider.LoginMethodsProvider)
	_, canLogin := pv.(provider.Loginer)
	if !isMethodAware || !canLogin {
		return []provider.LoginMethod{}, nil
	}
	methods := append([]provider.LoginMethod(nil), methodsp.LoginMethods()...)
	filtered := make([]provider.LoginMethod, 0, len(methods))
	for _, m := range methods {
		if source != "" && m.Source != "" && m.Source != source {
			continue
		}
		filtered = append(filtered, m)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		oi := 0
		if a.Provenance == "official" {
			oi = 1
		}
		oj := 0
		if b.Provenance == "official" {
			oj = 1
		}
		if oi != oj {
			return oi > oj
		}
		pi, pj := 0, 0
		if a.Preferred {
			pi = 1
		}
		if b.Preferred {
			pj = 1
		}
		if pi != pj {
			return pi > pj
		}
		return a.Label < b.Label
	})
	return filtered, nil
}

// Login runs the browser flow and saves the resulting live credential under
// the provider's single mutation scope (ADR 0009/0011).
func (s *Service) Login(ctx context.Context, providerID string, opts LoginOptions) (LoginResult, error) {
	var result LoginResult
	err := lock.WithMutationLock(ctx, s.Store.Root, refs.ProviderScope(providerID),
		func(c context.Context) error {
			r, runErr := s.loginLocked(c, providerID, opts)
			if runErr == nil {
				result = r
			}
			return runErr
		})
	return result, err
}

func (s *Service) loginLocked(ctx context.Context, providerID string, opts LoginOptions) (LoginResult, error) {
	pv, err := s.Providers.Get(providerID)
	if err != nil {
		return LoginResult{}, anyerr.Newf("Unknown provider "+providerID+".", "PROVIDER_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound))
	}
	_, isMethodAware := pv.(provider.LoginMethodsProvider)
	loginer, canLogin := pv.(provider.Loginer)
	if !isMethodAware || !canLogin {
		return LoginResult{}, anyerr.Newf(
			pv.Name()+" does not offer browser sign-in.",
			"PROVIDER_LOGIN_UNSUPPORTED")
	}

	sourceCount := 1
	if sp, ok := pv.(interface{ AccountSourceCount() int }); ok {
		sourceCount = sp.AccountSourceCount()
	}
	needsSource := sourceCount > 1
	if needsSource && opts.Source == "" && opts.MethodID == "" {
		return LoginResult{}, anyerr.Newf(
			"Choose which "+pv.Name()+" sign-in source to use.",
			"ACCOUNT_SOURCE_REQUIRED",
			anyerr.WithSuggestions("Pass --source <source> together with --auth-method <id>."))
	}

	methods, err := s.LoginMethods(ctx, providerID, opts.Source)
	if err != nil {
		return LoginResult{}, err
	}
	method, err := selectLoginMethod(methods, opts.MethodID)
	if err != nil {
		return LoginResult{}, err
	}
	if !method.Available {
		msg := method.UnavailableReason
		if msg == "" {
			msg = method.Label + " is unavailable."
		}
		return LoginResult{}, anyerr.Newf(
			msg,
			"PROVIDER_LOGIN_UNAVAILABLE",
			anyerr.WithSuggestions("Install the launcher required by "+method.Label+", then try again."))
	}
	source := opts.Source
	if source == "" {
		source = method.Source
	}
	if needsSource && source == "" {
		return LoginResult{}, anyerr.Newf(
			"Choose which "+pv.Name()+" sign-in source to use.",
			"ACCOUNT_SOURCE_REQUIRED",
			anyerr.WithSuggestions("Pass --source <source> together with --auth-method "+method.ID+"."))
	}
	if opts.Source != "" && method.Source != "" && opts.Source != method.Source {
		return LoginResult{}, anyerr.Newf(
			method.Label+" cannot sign in to source \""+opts.Source+"\".",
			"PROVIDER_LOGIN_METHOD_UNKNOWN")
	}

	checkpoint, err := s.CheckpointLiveAuth(ctx, providerID, false)
	if err != nil {
		return LoginResult{}, err
	}
	var backedUpTo *string
	defer func() { _ = s.DiscardLiveAuthCheckpoint(checkpoint) }()

	// rollbackOrCancel restores the checkpoint exactly once and returns the
	// cause (or the rollback failure when restoration itself broke).
	rollback := func(cause error) (LoginResult, error) {
		if rbErr := s.restoreCheckpointQuiet(ctx, checkpoint); rbErr != nil {
			return LoginResult{}, anyerr.Wrapf(rbErr,
				"Provider sign-in failed and the previous login could not be restored",
				"PROVIDER_LOGIN_ROLLBACK_FAILED")
		}
		return LoginResult{}, cause
	}
	// commitBarrier closes the late-success race: once the launcher returns,
	// a cancel that arrives before AnyPick commits must not mutate state or
	// publish success (exactly-once cancellation).
	commitBarrier := func() error {
		if ctx.Err() != nil {
			return errLoginCancelled()
		}
		return nil
	}

	if checkpoint.HadLiveAuth {
		stashName, stashErr := s.stashBackupTarget(pv)
		if stashErr == nil {
			_, stashErr = s.saveLocked(ctx, pv, stashName, nil)
		}
		if stashErr != nil {
			return rollback(stashErr)
		}
		name := stashName
		backedUpTo = &name
	}

	if err := loginer.Login(ctx, method.ID, source); err != nil {
		// Once the context is cancelled the terminal result must be
		// cancellation — however the launcher itself exited.
		if cerr := commitBarrier(); cerr != nil {
			return rollback(cerr)
		}
		return rollback(err)
	}
	// The launcher finished, but the request may already be abandoned.
	if cerr := commitBarrier(); cerr != nil {
		return rollback(cerr)
	}

	live, err := pv.DetectLive()
	if err != nil {
		return rollback(err)
	}
	if !live.Present {
		err := anyerr.Newf(
			method.Label+" completed without a usable local login.",
			"PROVIDER_LOGIN_NO_CREDENTIAL",
			anyerr.WithSuggestions("Try Sign In again and complete every step in the browser."))
		return rollback(err)
	}
	if cerr := commitBarrier(); cerr != nil {
		return rollback(cerr)
	}

	accountName, err := s.resolveCurrentSaveName(pv, opts.Name, live)
	if err != nil {
		return rollback(err)
	}
	meta, err := s.saveLockedWithLabel(ctx, pv, accountName, opts.Label)
	if err != nil {
		return rollback(err)
	}
	// Last barrier before the commit point: a cancel here must roll back the
	// snapshot write above rather than publish success.
	if cerr := commitBarrier(); cerr != nil {
		return rollback(cerr)
	}
	if err := s.Store.SetActive(providerID, meta.Name); err != nil {
		return rollback(err)
	}
	return LoginResult{Provider: providerID, Account: meta, Method: method, BackedUpTo: backedUpTo}, nil
}

// stashBackupTarget ports resolveStashBackupTarget for the no-explicit-name
// path used by login: identity match first, then active reuse, then an
// identity-derived name, then "stash".
func (s *Service) stashBackupTarget(pv provider.Provider) (string, error) {
	live, err := pv.DetectLive()
	if err != nil {
		return "", err
	}
	if live.Identity != "" {
		if byIdentity := s.findAccountNameByIdentity(pv.ID(), live.Identity); byIdentity != "" {
			return byIdentity, nil
		}
	}
	active, err := s.Store.GetActive(pv.ID())
	if err != nil {
		return "", err
	}
	if active != "" {
		activeAccount, gerr := s.Store.Get(pv.ID(), active)
		if gerr == nil && activeAccount != nil &&
			(live.Identity == "" || activeAccount.Meta.Identity == "" ||
				identitiesEqual(activeAccount.Meta.Identity, live.Identity)) {
			return active, nil
		}
	}
	if live.Identity != "" {
		return accountNameFromIdentity(live.Identity)
	}
	return fallbackAccountName, nil
}

// resolveCurrentSaveName ports resolveCurrentSaveName: existing identities win
// so a refreshed token overwrites its snapshot instead of aliasing.
func (s *Service) resolveCurrentSaveName(pv provider.Provider, requestedName string, live provider.LiveAuthStatus) (string, error) {
	if !live.Present {
		return "", anyerr.Newf(
			"No live "+pv.Name()+" login to save.",
			"NO_LIVE_AUTH",
			anyerr.WithSuggestions("Sign in with the official tool first, then try again."))
	}
	if live.Identity != "" {
		if existing := s.findAccountNameByIdentity(pv.ID(), live.Identity); existing != "" {
			return existing, nil
		}
	}
	if strings.TrimSpace(requestedName) != "" {
		return slug.AccountName(requestedName)
	}
	active, err := s.Store.GetActive(pv.ID())
	if err != nil {
		return "", err
	}
	if active != "" {
		activeAccount, gerr := s.Store.Get(pv.ID(), active)
		if gerr == nil && activeAccount != nil &&
			(live.Identity == "" || activeAccount.Meta.Identity == "" ||
				identitiesEqual(activeAccount.Meta.Identity, live.Identity)) {
			return active, nil
		}
	}
	if live.Identity != "" {
		return accountNameFromIdentity(live.Identity)
	}
	return "", anyerr.Newf(
		"Name the "+pv.Name()+" login before saving it.",
		"MISSING_ACCOUNT_NAME",
		anyerr.WithSuggestions("anypick add account "+pv.ID()+" --current --name <name>"))
}

func (s *Service) findAccountNameByIdentity(providerID, identity string) string {
	accounts, err := s.Store.ListAccounts(providerID)
	if err != nil {
		return ""
	}
	for _, acct := range accounts {
		if acct.Meta.Identity != "" && identitiesEqual(acct.Meta.Identity, identity) {
			return acct.Meta.Name
		}
	}
	return ""
}

// accountNameFromIdentity derives a stable account name from an email or id
// (ports accountNameFromIdentity); failures fall back to "stash".
func accountNameFromIdentity(identity string) (string, error) {
	trimmed := strings.TrimSpace(identity)
	local := trimmed
	if idx := strings.Index(trimmed, "@"); idx >= 0 {
		local = trimmed[:idx]
	}
	name, err := slug.AccountName(local)
	if err != nil {
		return fallbackAccountName, nil
	}
	return name, nil
}

// saveLockedWithLabel wraps saveLocked with the login-supplied label.
func (s *Service) saveLockedWithLabel(ctx context.Context, pv provider.Provider, accountName, label string) (store.Meta, error) {
	existing, err := s.Get(ctx, pv.ID(), accountName)
	if err != nil {
		return store.Meta{}, err
	}
	meta, err := s.saveLocked(ctx, pv, accountName, existing)
	if err != nil {
		return store.Meta{}, err
	}
	if label != "" && label != meta.Label {
		return s.Store.PatchMeta(pv.ID(), meta.Name, store.MetaPatch{LabelSet: true, Label: label})
	}
	return meta, nil
}

// errLoginCancelled mirrors PROVIDER_LOGIN_CANCELLED for post-launcher
// cancellation barriers.
func errLoginCancelled() error {
	return anyerr.Newf("Browser sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
}

// selectLoginMethod ports selectLoginMethod: explicit ids resolve strictly;
// otherwise the single preferred official method wins and ambiguity is an
// interactive decision we never guess.
func selectLoginMethod(methods []provider.LoginMethod, requestedMethodID string) (provider.LoginMethod, error) {
	if requestedMethodID != "" {
		for _, m := range methods {
			if m.ID == requestedMethodID {
				return m, nil
			}
		}
		suggestion := ""
		if len(methods) > 0 {
			ids := make([]string, 0, len(methods))
			for _, m := range methods {
				ids = append(ids, m.ID)
			}
			suggestion = "Choose one of: " + strings.Join(ids, ", ") + "."
		}
		opt := []anyerr.Option{}
		if suggestion != "" {
			opt = append(opt, anyerr.WithSuggestions(suggestion))
		}
		return provider.LoginMethod{}, anyerr.Newf(
			"Unknown browser sign-in method \""+requestedMethodID+"\".",
			"PROVIDER_LOGIN_METHOD_UNKNOWN", opt...)
	}

	available := make([]provider.LoginMethod, 0, len(methods))
	for _, m := range methods {
		if m.Available {
			available = append(available, m)
		}
	}
	var official, preferredOfficial, preferred []provider.LoginMethod
	for _, m := range available {
		if m.Provenance == "official" {
			official = append(official, m)
			if m.Preferred {
				preferredOfficial = append(preferredOfficial, m)
			}
		}
	}
	if len(preferredOfficial) == 1 {
		return preferredOfficial[0], nil
	}
	if len(official) == 1 {
		return official[0], nil
	}
	if len(official) > 1 {
		ids := make([]string, 0, len(official))
		for _, m := range official {
			ids = append(ids, m.ID)
		}
		return provider.LoginMethod{}, anyerr.Newf(
			"Choose an official browser sign-in method.",
			"PROVIDER_LOGIN_METHOD_REQUIRED",
			anyerr.WithSuggestions("Choose one of: "+strings.Join(ids, ", ")+"."))
	}
	for _, m := range available {
		if m.Preferred {
			preferred = append(preferred, m)
		}
	}
	if len(preferred) == 1 {
		return preferred[0], nil
	}
	if len(available) == 1 {
		return available[0], nil
	}
	if len(available) == 0 {
		for _, m := range methods {
			if m.Preferred {
				return m, nil
			}
		}
		if len(methods) > 0 {
			return methods[0], nil
		}
		return provider.LoginMethod{}, anyerr.Newf(
			"No browser sign-in method is available.",
			"PROVIDER_LOGIN_UNAVAILABLE")
	}
	ids := make([]string, 0, len(available))
	for _, m := range available {
		ids = append(ids, m.ID)
	}
	return provider.LoginMethod{}, anyerr.Newf(
		"Choose a browser sign-in method.",
		"PROVIDER_LOGIN_METHOD_REQUIRED",
		anyerr.WithSuggestions("Choose one of: "+strings.Join(ids, ", ")+"."))
}
