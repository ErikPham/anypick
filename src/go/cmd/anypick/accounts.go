package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// jsonOut prints one machine-readable result on stdout (invariant #6).
func jsonOut(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	fmt.Println(string(data))
}

func wantJSON(args []string) bool {
	if globalJSON {
		return true
	}
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

func usageError(format string, args ...interface{}) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return int(anyerr.InvalidUsage)
}

// appView aliases the composed application for command handlers.
type appView = app.App

func cmdAccount(ctx context.Context, a *appView, args []string) int {
	if len(args) == 0 {
		return usageError("usage: anypick account <list|remove|refresh|export|import> …")
	}
	switch args[0] {
	case "list":
		return cmdList(ctx, a, []string{"accounts"})
	case "remove":
		return cmdAccountRemove(ctx, a, args[1:])
	case "refresh":
		return cmdAccountRefresh(ctx, a, args[1:])
	case "export":
		return cmdAccountExport(ctx, a, args[1:])
	case "import":
		return cmdAccountImport(ctx, a, args[1:])
	default:
		return usageError("unknown account subcommand %q", args[0])
	}
}

func cmdAccountRemove(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 2 {
		return usageError("usage: anypick account remove <provider> <name> [--yes]")
	}
	providerID, name := fs.Arg(0), fs.Arg(1)
	if globals.DryRun {
		// Planner-purity analog (invariant #3): print and exit before any
		// service call or lock acquisition.
		printDryRunFields("account.remove", fmt.Sprintf("Would remove %s/%s", providerID, name),
			kv{Key: "provider", Value: providerID}, kv{Key: "name", Value: name})
		return int(anyerr.Success)
	}
	acct, err := a.Accounts.Get(ctx, providerID, name)
	if err != nil {
		return reportErr(err)
	}
	if acct == nil {
		return reportErr(anyerr.Newf("Account not found: "+providerID+"/"+name, "ACCOUNT_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound)))
	}
	// confirmDelete port (commands.ts): --yes short-circuits; non-TTY refuses
	// with CONFIRMATION_REQUIRED exit 2 instead of consuming stdin (#6).
	switch err := confirmDelete(providerID+"/"+name, globals.Yes); {
	case errors.Is(err, errDeclined):
		return int(anyerr.Success)
	case err != nil:
		return reportErr(err)
	}
	deleted, err := a.Accounts.Delete(ctx, providerID, name)
	if err != nil {
		return reportErr(err)
	}
	// Hub sources and route pins name accounts by value; without a prune the
	// deleted row rots into ghosts that fail requests. Best-effort after a
	// successful delete: the account is gone either way.
	prunedMsg := ""
	if a.ProxyHub != nil {
		if pruned, perr := a.ProxyHub.PruneAccount(ctx, providerID, name); perr != nil {
			fmt.Fprintf(os.Stderr, "warning: hub cleanup for %s/%s failed: %s\n", providerID, name, perr)
		} else if len(pruned) > 0 {
			prunedMsg = fmt.Sprintf(" Hub references dropped: %s.", strings.Join(pruned, ", "))
		}
		refreshUnsyncedHubs(ctx, a, providerID, name)
	}
	// Named profiles are removed with the account inside Delete, so the
	// cleanup is reported rather than left invisible: the user asked to
	// remove one account and lost a native Codex profile too.
	profileMsg := ""
	if len(deleted.Profiles) > 0 {
		profileMsg = fmt.Sprintf(" Removed named profile(s): %s.", strings.Join(deleted.Profiles, ", "))
	}
	emitOK(fmt.Sprintf("removed %s/%s%s%s", providerID, name, profileMsg, prunedMsg),
		kv{Key: "ok", Value: true},
		kv{Key: "provider", Value: providerID},
		kv{Key: "name", Value: name},
		kv{Key: "profiles", Value: deleted.Profiles})
	return int(anyerr.Success)
}

// isTTY reports whether f is a terminal that a human can answer on.
//
// It must NOT be `Mode()&os.ModeCharDevice != 0`: /dev/null is a character
// device too, so that check calls it a terminal. confirmDelete and
// confirmPluginTrust then print a prompt and read stdin under cron and CI
// instead of refusing outright, which breaks invariant #6 (never prompt
// without a TTY) and gives scripts the wrong exit code.
// refreshUnsyncedHubs restarts children that are still serving the deleted
// account. In-sync hubs and port-0 hubs are left alone: the former already
// match, and the latter cannot be echoed (ADR-0030).
func refreshUnsyncedHubs(ctx context.Context, a *appView, providerID, name string) {
	if a.ProxyHub == nil || a.Deps == nil || a.Deps.SpawnHubChild == nil {
		return
	}
	configs, err := a.ProxyHub.ListConfigsContext(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: hub refresh for %s/%s failed: %s\n", providerID, name, err)
		return
	}
	if err := hub.ReloadUnsynced(configs, a.Deps.SpawnHubChild); err != nil {
		fmt.Fprintf(os.Stderr, "warning: hub refresh for %s/%s failed: %s\n", providerID, name, err)
	}
}

func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// accountRefreshResult is one row of `account refresh` output.
type accountRefreshResult struct {
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// refreshProviderAccounts refreshes every saved account of one provider.
func refreshProviderAccounts(ctx context.Context, a *appView, providerID string,
	refreshOne func(provider, name string) accountRefreshResult) ([]accountRefreshResult, error) {
	list, lerr := a.Accounts.List(ctx, providerID)
	if lerr != nil {
		return nil, lerr
	}
	var out []accountRefreshResult
	for _, acct := range list {
		out = append(out, refreshOne(providerID, acct.Meta.Name))
	}
	return out, nil
}

func cmdAccountRefresh(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	// --all is the documented spelling of "refresh every saved account". That
	// is already what a provider with no account name does (and what no
	// arguments does across providers), so the flag only has to be accepted
	// and to refuse the contradictory `--all <provider> <name>` form.
	all := fs.Bool("all", false, "refresh every saved account of the named provider")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() > 2 {
		return usageError("usage: anypick account refresh [<provider> [<name>]] [--all]")
	}
	if *all && fs.NArg() == 2 {
		return usageError("usage: anypick account refresh [<provider>] --all (not both --all and an account name)")
	}
	var results []accountRefreshResult
	refreshOne := func(providerID, name string) accountRefreshResult {
		meta, err := a.Accounts.RefreshSnapshot(ctx, providerID, name)
		if err != nil {
			row := accountRefreshResult{Provider: providerID, Name: name, Status: "error: " + err.Error()}
			var ae *anyerr.Error
			if errors.As(err, &ae) {
				row.Suggestions = append([]string(nil), ae.Suggestions...)
			}
			return row
		}
		return accountRefreshResult{Provider: providerID, Name: meta.Name, Status: "refreshed"}
	}
	switch fs.NArg() {
	case 2:
		results = append(results, refreshOne(fs.Arg(0), fs.Arg(1)))
	case 1:
		// A provider alone filters: the documented usage says
		// `[<provider> [<name>]]`, and silently refreshing every provider when
		// one was named is the kind of surprise this CLI refuses elsewhere.
		providerID := fs.Arg(0)
		rows, lerr := refreshProviderAccounts(ctx, a, providerID, refreshOne)
		if lerr != nil {
			return reportErr(anyerr.Wrapf(lerr, "list "+providerID, "ACCOUNT_LIST_FAILED"))
		}
		results = rows
	default:
		for _, providerID := range a.Providers.IDs() {
			// A provider whose list fails is skipped, not fatal: a bulk
			// inventory should still refresh the ones it can read.
			rows, lerr := refreshProviderAccounts(ctx, a, providerID, refreshOne)
			if lerr == nil {
				results = append(results, rows...)
			}
		}
	}
	failed := 0
	if wantJSON(args) {
		jsonOut(results)
	} else {
		for _, r := range results {
			fmt.Printf("%s/%s: %s\n", r.Provider, r.Name, r.Status)
			for _, suggestion := range r.Suggestions {
				fmt.Printf("  %s\n", suggestion)
			}
		}
	}
	for _, r := range results {
		if strings.HasPrefix(r.Status, "error:") {
			failed++
		}
	}
	// A named account is a single contract: failure must be visible as the
	// process exit. Bulk refresh keeps per-row status and exits 0 so a
	// proxy-only account that cannot re-detect live auth does not fail the
	// whole inventory.
	if fs.NArg() == 2 && failed > 0 {
		return int(anyerr.Operational)
	}
	return int(anyerr.Success)
}

func cmdAccountExport(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	out := fs.String("out", "", "output file")
	// -o is the documented short form of --out; the stdlib parser has no alias
	// concept, so the same target is registered twice.
	outShort := fs.String("o", "", "alias of --out")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 2 {
		return usageError("usage: anypick account export <provider> <name> --out <file>")
	}
	if *outShort != "" {
		if *out != "" && *out != *outShort {
			return usageError("pass one output path (--out or -o)")
		}
		*out = *outShort
	}
	if *out == "" {
		return usageError("usage: anypick account export <provider> <name> --out <file>")
	}
	if globals.DryRun {
		printDryRunFields("account.export",
			fmt.Sprintf("Would export %s/%s → %s", fs.Arg(0), fs.Arg(1), *out),
			kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)},
			kv{Key: "out", Value: *out})
		return int(anyerr.Success)
	}
	if err := a.Accounts.ExportEnvelope(ctx, fs.Arg(0), fs.Arg(1), *out); err != nil {
		return reportErr(err)
	}
	if !wantJSON(args) {
		return int(anyerr.Success)
	}
	jsonOut(map[string]string{"file": *out})
	return int(anyerr.Success)
}

func cmdAccountImport(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	force := fs.Bool("force", false, "overwrite existing account / identity conflict")
	// The guides write `account import <provider> <name> -i ./work.json`; -i is
	// the documented short form of the input file, which the reference also
	// accepts positionally.
	input := fs.String("i", "", "input file (or pass it positionally)")
	if err := fs.Parse(reorder(args)); err != nil {
		return usageError("usage: anypick account import <provider> <name> <file> [--force]")
	}
	// The file is positional in the reference form and a flag value in the
	// guide form; accept either, but exactly one.
	var providerID, name, file string
	switch {
	case fs.NArg() == 3 && *input == "":
		providerID, name, file = fs.Arg(0), fs.Arg(1), fs.Arg(2)
	case fs.NArg() == 2 && *input != "":
		providerID, name, file = fs.Arg(0), fs.Arg(1), *input
	default:
		return usageError("usage: anypick account import <provider> <name> <file> [--force]")
	}
	if globals.DryRun {
		printDryRunFields("account.import",
			fmt.Sprintf("Would import %s/%s from %s", providerID, name, file),
			kv{Key: "provider", Value: providerID}, kv{Key: "name", Value: name},
			kv{Key: "input", Value: file}, kv{Key: "force", Value: *force})
		return int(anyerr.Success)
	}
	meta, err := a.Accounts.ImportEnvelope(ctx, providerID, name, file, *force)
	if err != nil {
		return reportErr(err)
	}
	if wantJSON(args) {
		jsonOut(meta)
		return int(anyerr.Success)
	}
	fmt.Printf("imported %s/%s\n", meta.Provider, meta.Name)
	return int(anyerr.Success)
}

func cmdGatewayShow(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway show <name>")
	}
	pr, err := a.Runtime.ProfileSvc.GetContext(ctx, fs.Arg(0))
	if err != nil {
		return reportErr(err)
	}
	if pr == nil {
		return reportErr(anyerr.Newf("gateway not found", "GATEWAY_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound)))
	}
	// Secrets are masked unless --reveal (mirrors profiles.redact).
	shown := pr.Secrets.APIKey
	if !globals.Reveal && shown != "" {
		shown = maskSecret(shown)
	}
	if wantJSON(args) {
		jsonOut(struct {
			Meta    map[string]string `json:"meta"`
			Secrets map[string]string `json:"secrets"`
		}{
			Meta: map[string]string{
				"name":         pr.Meta.Name,
				"provider":     pr.Meta.Provider,
				"endpoint":     pr.Meta.Endpoint,
				"defaultModel": pr.Meta.DefaultModel,
			},
			Secrets: map[string]string{"apiKey": shown},
		})
		return int(anyerr.Success)
	}
	fmt.Println(pr.Meta.Name)
	fmt.Printf("  provider  %s\n", pr.Meta.Provider)
	fmt.Printf("  endpoint  %s\n", pr.Meta.Endpoint)
	fmt.Printf("  apiKey    %s\n", shown)
	fmt.Printf("  model     %s\n", pr.Meta.DefaultModel)
	return int(anyerr.Success)
}

// maskSecret ports maskSecret from src/core/profile-service.ts.
func maskSecret(value string) string {
	if len(value) == 0 {
		return "(empty)"
	}
	if len(value) <= 8 {
		return "••••••••"
	}
	return value[:4] + "…" + value[len(value)-4:]
}

func cmdGatewayEdit(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	endpoint := fs.String("endpoint", "", "new base URL")
	model := fs.String("model", "", "default model")
	// The docs write `gateway edit <name> -m <id>`; -m is the short form of
	// --model. Registered as a separate variable rather than an alias so the
	// stdlib parser (which has no alias concept) accepts either spelling.
	modelShort := fs.String("m", "", "alias of --model")
	// Editing the stored key is what the docs promise, and the desktop
	// mutateGatewayEdit already writes Meta/Secrets this way; only the CLI
	// lacked the flag. Bare `--api-key` means "prompt on the TTY", like
	// `add gateway`.
	args, _, promptAPIKey := splitBareSecretFlags(args)
	apiKey := fs.String("api-key", "", "new API key value; bare flag prompts on the TTY")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway edit <name> [--endpoint url] [--model id] [--api-key key]")
	}
	name := fs.Arg(0)
	if *modelShort != "" {
		if *model != "" && *model != *modelShort {
			return usageError("pass one model id (--model or -m)")
		}
		*model = *modelShort
	}
	if *apiKey == "" && promptAPIKey {
		var perr error
		*apiKey, perr = promptSecret("API key")
		if perr != nil {
			return reportErr(perr)
		}
	}
	if globals.DryRun {
		printDryRunFields("gateway.edit", "Would update gateway "+name,
			kv{Key: "name", Value: name},
			kv{Key: "endpoint", Value: strOrNil(*endpoint)},
			kv{Key: "model", Value: strOrNil(*model)},
			kv{Key: "models", Value: nil}, // Go edit has no --models flag yet
			kv{Key: "hasApiKey", Value: *apiKey != ""})
		return int(anyerr.Success)
	}
	err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.GatewayRef(name)), func(c context.Context) error {
		existing, gerr := a.Runtime.ProfileSvc.GetContext(c, name)
		if gerr != nil {
			return gerr
		}
		if existing == nil {
			return anyerr.Newf("gateway not found", "GATEWAY_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound))
		}
		if *endpoint != "" {
			existing.Meta.Endpoint = *endpoint
		}
		if *model != "" {
			existing.Meta.DefaultModel = *model
		}
		if *apiKey != "" {
			existing.Secrets.APIKey = *apiKey
		}
		_, uerr := a.Runtime.ProfileSvc.UpsertContext(c, *existing)
		return uerr
	})
	if err != nil {
		return reportErr(err)
	}
	emitOK(fmt.Sprintf("updated %s", name),
		kv{Key: "ok", Value: true},
		kv{Key: "name", Value: name})
	return int(anyerr.Success)
}

func cmdGatewayRemove(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway remove <name>")
	}
	name := fs.Arg(0)
	if globals.DryRun {
		printDryRunFields("gateway.remove", "Would remove gateway "+name,
			kv{Key: "name", Value: name})
		return int(anyerr.Success)
	}
	switch err := confirmDelete("gateway "+name, globals.Yes); {
	case errors.Is(err, errDeclined):
		return int(anyerr.Success)
	case err != nil:
		return reportErr(err)
	}
	err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.GatewayRef(name)), func(c context.Context) error {
		return a.Runtime.ProfileSvc.DeleteContext(c, name)
	})
	if err != nil {
		return reportErr(err)
	}
	emitOK(fmt.Sprintf("removed %s", name),
		kv{Key: "ok", Value: true}, kv{Key: "name", Value: name})
	return int(anyerr.Success)
}

func cmdGatewayReset(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	clientID := fs.String("client", "", "client to reset from the gateway profile")
	// -c is the short form the golden tree advertises; the stdlib parser has no
	// alias concept, so the same target is registered twice.
	clientShort := fs.String("c", "", "alias of --client")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway reset <gateway> --client <clientId> [--yes]")
	}
	if *clientShort != "" {
		if *clientID != "" && *clientID != *clientShort {
			return usageError("pass one client (--client or -c)")
		}
		*clientID = *clientShort
	}
	if *clientID == "" {
		return usageError("usage: anypick gateway reset <gateway> --client <clientId> [--yes]")
	}
	gateway := fs.Arg(0)
	if _, err := a.Clients.Get(*clientID); err != nil {
		return reportErr(err)
	}
	if globals.DryRun {
		g, gerr := a.BindingSvc.GetGlobalContext(ctx, *clientID)
		if gerr != nil {
			return reportErr(gerr)
		}
		emitResetDryRun(*clientID, g != nil, true)
		return int(anyerr.Success)
	}
	// Same gate as reset: the mutation below rewrites the client's own login
	// file, and a headless stdin gets no prompt (invariant #7).
	switch err := confirmDelete("client "+*clientID+" of gateway "+gateway, globals.Yes); {
	case errors.Is(err, errDeclined):
		return int(anyerr.Success)
	case err != nil:
		return reportErr(err)
	}
	err := lock.WithMutationLock(ctx, a.Root, "client/"+*clientID, func(context.Context) error {
		cad, gerr := a.Clients.Get(*clientID)
		if gerr != nil {
			return gerr
		}
		return cad.Reset()
	})
	if err != nil {
		return reportErr(err)
	}
	emitOK(fmt.Sprintf("reset %s", *clientID),
		kv{Key: "ok", Value: true}, kv{Key: "client", Value: *clientID})
	return int(anyerr.Success)
}

var _ = profile.Runtime{}
var _ = store.Meta{}
var _ = filepath.Join
var _ = strings.TrimSpace
