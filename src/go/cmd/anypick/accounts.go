// Subcommand groups for the Go CLI. Handlers stay thin: every behavior lives
// on a service; these files only parse flags, call services, and print.
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
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transfer"
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

// ---- account group ------------------------------------------------------

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
	if err := a.Accounts.Delete(ctx, providerID, name); err != nil {
		return reportErr(err)
	}
	fmt.Printf("removed %s/%s\n", providerID, name)
	return int(anyerr.Success)
}

// isTTY reports whether f is a terminal that a human can answer on.
//
// It must NOT be `Mode()&os.ModeCharDevice != 0`: /dev/null is a character
// device too, so that check calls it a terminal. confirmDelete and
// confirmPluginTrust then print a prompt and read stdin under cron and CI
// instead of refusing outright, which breaks invariant #6 (never prompt
// without a TTY) and gives scripts the wrong exit code.
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func cmdAccountRefresh(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() > 2 {
		return usageError("usage: anypick account refresh [<provider> [<name>]]")
	}
	type result struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Status   string `json:"status"`
	}
	var results []result
	refreshOne := func(providerID, name string) result {
		meta, err := a.Accounts.RefreshSnapshot(ctx, providerID, name)
		if err != nil {
			return result{Provider: providerID, Name: name, Status: "error: " + err.Error()}
		}
		return result{Provider: providerID, Name: meta.Name, Status: "refreshed"}
	}
	switch fs.NArg() {
	case 2:
		results = append(results, refreshOne(fs.Arg(0), fs.Arg(1)))
	default:
		for _, providerID := range a.Providers.IDs() {
			list, lerr := a.Accounts.List(ctx, providerID)
			if lerr != nil {
				continue
			}
			for _, acct := range list {
				results = append(results, refreshOne(providerID, acct.Meta.Name))
			}
		}
	}
	if wantJSON(args) {
		jsonOut(results)
	} else {
		for _, r := range results {
			fmt.Printf("%s/%s: %s\n", r.Provider, r.Name, r.Status)
		}
	}
	return int(anyerr.Success)
}

func cmdAccountExport(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	out := fs.String("out", "", "output file")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 2 || *out == "" {
		return usageError("usage: anypick account export <provider> <name> --out <file>")
	}
	if globals.DryRun {
		printDryRunFields("account.export",
			fmt.Sprintf("Would export %s/%s → %s", fs.Arg(0), fs.Arg(1), *out),
			kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)},
			kv{Key: "out", Value: *out})
		return int(anyerr.Success)
	}
	if err := transfer.ExportEnvelope(a.Store, ctx, fs.Arg(0), fs.Arg(1), *out); err != nil {
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
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 3 {
		return usageError("usage: anypick account import <provider> <name> <file> [--force]")
	}
	if globals.DryRun {
		printDryRunFields("account.import",
			fmt.Sprintf("Would import %s/%s from %s", fs.Arg(0), fs.Arg(1), fs.Arg(2)),
			kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)},
			kv{Key: "input", Value: fs.Arg(2)}, kv{Key: "force", Value: *force})
		return int(anyerr.Success)
	}
	deps := transfer.ImportDeps{
		Store:     a.Store,
		Providers: a.Providers,
		ResolveIdentityTarget: func(c context.Context, providerID, identity, accountName string, allowOverwrite bool) (string, error) {
			return a.Accounts.ResolveIdentityTarget(c, providerID, identity, accountName, allowOverwrite)
		},
	}
	meta, err := transfer.ImportEnvelope(deps, ctx, fs.Arg(0), fs.Arg(1), fs.Arg(2), *force)
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

// ---- gateway extras -----------------------------------------------------

func cmdGatewayShow(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway show <name>")
	}
	pr, err := a.Runtime.Profiles.GetContext(ctx, fs.Arg(0))
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
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick gateway edit <name> [--endpoint url] [--model id]")
	}
	name := fs.Arg(0)
	if globals.DryRun {
		printDryRunFields("gateway.edit", "Would update gateway "+name,
			kv{Key: "name", Value: name},
			kv{Key: "endpoint", Value: strOrNil(*endpoint)},
			kv{Key: "model", Value: strOrNil(*model)},
			kv{Key: "models", Value: nil}, // Go edit has no --models flag yet
			kv{Key: "hasApiKey", Value: false})
		return int(anyerr.Success)
	}
	err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.GatewayRef(name)), func(c context.Context) error {
		existing, gerr := a.Runtime.Profiles.GetContext(c, name)
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
		_, uerr := a.Runtime.Profiles.UpsertContext(c, *existing)
		return uerr
	})
	if err != nil {
		return reportErr(err)
	}
	fmt.Printf("updated %s\n", name)
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
		return a.Runtime.Profiles.DeleteContext(c, name)
	})
	if err != nil {
		return reportErr(err)
	}
	fmt.Printf("removed %s\n", name)
	return int(anyerr.Success)
}

func cmdGatewayReset(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	clientID := fs.String("client", "", "client to reset from the gateway profile")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 || *clientID == "" {
		return usageError("usage: anypick gateway reset <gateway> --client <clientId>")
	}
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
	fmt.Printf("reset %s\n", *clientID)
	return int(anyerr.Success)
}

var _ = profile.Runtime{}
var _ = store.Meta{}
var _ = filepath.Join
var _ = strings.TrimSpace
