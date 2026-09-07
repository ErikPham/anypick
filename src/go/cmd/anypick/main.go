// Command anypick is the Go CLI over the ported core. Command handlers stay
// thin; behavior lives on services composed by internal/app.
//
// Invariant #2: proxy secrets never cross an output boundary — commands print
// endpoints, never tokens. Invariant #6: exit codes come from anyerr.ExitCode.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/cli"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/desktop"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/modelcache"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transport"
	"github.com/outof0/anypick/go/internal/tui"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Root identity/help paths never need the service graph. Keeping them before
	// app.Ready is both a latency contract and a safety property: asking how to
	// use AnyPick must not create or migrate the user's data directory.
	for _, arg := range args {
		if arg == "--version" || arg == "-v" {
			fmt.Printf("anypick-go %s\n", packageVersion())
			return int(anyerr.Success)
		}
	}
	args = extractGlobals(args)
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		usage()
		return int(anyerr.Success)
	}

	ctx := context.Background()
	// Hidden detached helpers skip startup maintenance: they are spawned by a
	// parent that may hold mutation locks and must boot instantly.
	maintenance := true
	if len(args) > 0 && (strings.HasPrefix(args[0], "_") || args[0] == "desktop-service") {
		maintenance = false // hidden helper children must boot instantly
	} else {
		// Root-level global flags are accepted anywhere before pass-through
		// payload; hidden helpers keep their argv byte-exact.
		args = normalizeGlobalFlags(args)
	}
	// The TTY guard runs here, before any service is touched (AGENTS.md).
	// app.Ready's startup maintenance runs RecoverIncompleteOperations — which
	// compensates half-applied activation steps, i.e. it rewrites real
	// credential files — and reaps detached proxy leases. A scheduled
	// `anypick tui < /dev/null` must not do any of that only to be refused
	// afterwards, and it must never kill proxies the desktop app is using.
	//
	// Only the explicitly named surface is knowable this early. A bare
	// invocation resolves ui.defaultSurface from config, which cannot be read
	// until startup has opened the database; that path still reaches the guard
	// inside tui.Run, one startup later.
	if isExplicitTuiSurface(args) && !isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprintln(os.Stderr, "error:", tui.ErrNoTTY)
		return int(anyerr.InvalidUsage)
	}

	a, closeFn, err := app.Ready(ctx, app.Options{SkipMaintenance: !maintenance})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return int(anyerr.Operational)
	}
	defer closeFn()

	if len(args) == 0 {
		return bareInvocation(ctx, a)
	}

	var code int
	switch args[0] {
	case "add":
		code = cmdAdd(ctx, a, args[1:])
	case "use":
		code = cmdUse(ctx, a, args[1:])
	case "current":
		code = cmdCurrent(ctx, a, args[1:])
	case "list":
		code = cmdList(ctx, a, args[1:])
	case "tui", "--tui":
		return runTuiSurface(ctx, a)
	case "app":
		code = cmdAppRun(ctx, a, args[1:])
	case "desktop-service":
		code = cmdDesktopService(ctx, a, args[1:])
	case "tray":
		code = cmdTray(ctx, a, args[1:])
	case "start":
		code = cmdStart(ctx, a, args[1:])
	case "status":
		code = cmdStatus(a, args[1:])
	case "stop":
		code = cmdStop(a, args[1:])
	case "serve":
		code = cmdServe(ctx, a, args[1:])
	case "proxy":
		code = cmdProxy(ctx, a, args[1:])
	case "_serve-hub":
		return serveHubChild(ctx, a, args[1:])
	case "_supervise":
		return runSuperviseChild(ctx, a)
	case "_serve-pool":
		return servePoolChild(ctx, a, args[1:])
	case "_serve-proxy":
		// Hidden detached child: serve the persisted proxy state until
		// SIGTERM/SIGINT. Never printed in help.
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "_serve-proxy requires provider and account")
			return int(anyerr.InvalidUsage)
		}
		if err := a.Proxy.ServeExisting(ctx, args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			return int(anyerr.Operational)
		}
		return int(anyerr.Success)
	case "stop-proxy":
		code = cmdStopProxy(ctx, a, args[1:])
	case "gateway":
		code = cmdGateway(ctx, a, args[1:])
	case "plugin":
		code = cmdPlugin(ctx, a, args[1:])
	case "account":
		code = cmdAccount(ctx, a, args[1:])
	case "preset":
		code = cmdPreset(ctx, a, args[1:])
	case "link":
		code = cmdLink(ctx, a, args[1:])
	case "unlink":
		code = cmdUnlink(ctx, a, args[1:])
	case "reset":
		code = cmdReset(ctx, a, args[1:])
	case "run":
		code = cmdRun(ctx, a, args[1:])
	case "providers":
		code = cmdProviders(ctx, a, args[1:])
	case "clients":
		code = cmdClients(ctx, a, args[1:])
	case "__complete":
		code = cmdComplete(ctx, a, args[1:])
	case "doctor":
		code = cmdDoctor(ctx, a, args[1:])
	case "update":
		code = cmdUpdate(ctx, a, args[1:])
	case "completion":
		code = cmdCompletion(a, args[1:])
	case "-h", "--help", "help":
		usage()
		return int(anyerr.Success)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return int(anyerr.InvalidUsage)
	}
	return int(code)
}

// runTuiSurface opens the terminal UI and maps its failure to an exit code.
//
// The UI is a bubbletea program over the same services the CLI commands use
// (ADR 0020 §5, NATIVE-M7 row 3): native releases ship no Node runtime, so the
// Ink/React tree in src/tui cannot be the one users get.
// isExplicitTuiSurface reports whether argv names the terminal UI. It must not
// consult config: it runs before the app is built, so nothing is touched.
func isExplicitTuiSurface(args []string) bool {
	return len(args) > 0 && (args[0] == "tui" || args[0] == "--tui")
}

func runTuiSurface(ctx context.Context, a *app.App) int {
	if err := tui.Run(ctx, a, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		// Invariant #6: refusing to run without a TTY is a usage problem,
		// not a failure of the user's data.
		if errors.Is(err, tui.ErrNoTTY) {
			return int(anyerr.InvalidUsage)
		}
		return int(anyerr.Operational)
	}
	return int(anyerr.Success)
}

// bareInvocation mirrors the TS default: open the configured desktop surface.
// The configured surface comes from ui.defaultSurface in global config — an
// explicit, persisted policy (plan §Scope: never silently add a Go TUI).
func bareInvocation(ctx context.Context, a *app.App) int {
	switch configuredLaunchSurface(ctx, a) {
	case "tray":
		return cmdAppRun(ctx, a, nil)
	case "tui":
		// The user asked for the terminal surface, so honour it. This is not
		// the "silent" launch the policy forbids: the policy is about bare
		// invocation with *no* configuration, which stays on help below.
		return runTuiSurface(ctx, a)
	default:
		// Unconfigured keeps today's explicit policy: CLI help, not an
		// accidental app launch. Native cutover may change this default, but
		// only as a tested decision (ADR 0020 §6).
		usage()
		fmt.Println("\nRun `anypick tui` for the interactive terminal UI.")
		return int(anyerr.Success)
	}
}

func usage() {
	fmt.Print(`anypick-go — point AI coding clients at accounts you already have

Usage:
  anypick add account <provider> --name <name> [--current|--new] [--force]
  anypick add gateway <name> --provider <id> [--endpoint url]
  anypick add <provider> --name <name> [--force]   (deprecated alias for add account --current)
  anypick use <client> --with <source> [--model <id>] [--transport auto|direct|proxy]
  anypick run <client> --with <source> -- <args…>   ephemeral isolated launch
  anypick current <provider>
  anypick list providers | accounts [<provider>]
  anypick link|unlink <client> --with <source> [--project]
  anypick reset <client>
  anypick account list|remove|refresh|export|import …
  anypick gateway add|list|show|edit|remove|reset …
	anypick plugin list|add|remove|enable|disable|trust …
  anypick preset list|show|remove …
  anypick proxy enable|disable|config|start|stop|status|restart|logs …
  anypick proxy pool status|enable|disable|member|start|stop …
  anypick proxy hub status|enable|source|owner|preview|start|stop …
  anypick serve [--detach|--stop]   core server (status API)
  anypick doctor | update | completion <bash|zsh|fish>

Sources:  provider/account · gateway-name · @preset · pool:<provider>
`)
}

func fsFor(_ []string) *flag.FlagSet { return cli.FsFor() }

func reorder(args []string) []string { return cli.Reorder(args) }

func cmdAdd(ctx context.Context, a *app.App, args []string) int {
	// Canonical documented grammar (README, getting-started):
	//   anypick add account <provider> --name <name> [--current|--new] [--force]
	//   anypick add gateway <name> --provider <id> [--endpoint url]
	// The bare `add <provider>` form predates the docs and stays as a
	// deprecated alias for `add account --current`.
	if len(args) > 0 && args[0] == "account" {
		return cmdAddAccount(ctx, a, args[1:])
	}
	if len(args) > 0 && args[0] == "gateway" {
		return cmdGatewayAdd(ctx, a, args[1:])
	}
	return cmdAddAccountCurrent(ctx, a, args, true)
}

func cmdAddAccount(ctx context.Context, a *app.App, args []string) int {
	// A bare `--secret` / `--api-key` (no =value, next token is a flag or
	// absent) means "prompt on the TTY". The flag package would otherwise
	// swallow the following token as the value, so detect it first.
	args, promptSecretFlag, promptAPIKey := splitBareSecretFlags(args)
	usage := "usage: anypick add account <provider> --name <name> [--current|--new] [--force]\n" +
		"       anypick add account <provider> --new --name <name> [--auth-method <id>] [--source <id>]\n" +
		"       anypick add account <provider> --name <name> --credential <kind> --secret [--option profile=<id>]"
	fs := fsFor(args)
	name := fs.String("name", "", "account name")
	force := fs.Bool("force", false, "overwrite an existing account")
	current := fs.Bool("current", false, "snapshot the current live login (default)")
	newLogin := fs.Bool("new", false, "start a fresh provider login, then save it")
	kind := fs.String("kind", "", "credential kind for compatible vendors (e.g. api-key)")
	credential := fs.String("credential", "", "credential kind for compatible vendors (e.g. api-key, subscription-key)")
	secret := fs.String("secret", "", "credential secret; prefer ANYPICK_SECRET (value via --secret is visible in process listing)")
	apiKey := fs.String("api-key", "", "API key value; bare flag prompts on the TTY (implies --credential api-key)")
	profileID := fs.String("profile", "", "endpoint profile id (default: first)")
	authMethod := fs.String("auth-method", "", "browser sign-in method id (with --new)")
	source := fs.String("source", "", "sign-in source (with --new; gemini-cli|antigravity also selects which sign-in --current reads)")
	region := fs.String("region", "", "cloud region (kiro API-key accounts)")
	var options optionFlags
	fs.Var(&options, "option", "provider qualifier as key=value (repeatable; e.g. --option profile=singapore)")
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 || *name == "" {
		fmt.Fprintln(os.Stderr, usage)
		return int(anyerr.InvalidUsage)
	}
	if *current && *newLogin {
		fmt.Fprintln(os.Stderr, "usage: anypick add account <provider> --name <name> [--current|--new]: --current and --new are mutually exclusive")
		return int(anyerr.InvalidUsage)
	}
	providerID := fs.Arg(0)

	if *newLogin {
		// Browser sign-in: credential-input flags belong to the typed-input
		// flow, not to a fresh login.
		if *kind != "" || *credential != "" || *secret != "" || *apiKey != "" ||
			promptSecretFlag || promptAPIKey || *profileID != "" || len(options) > 0 || *region != "" {
			fmt.Fprintln(os.Stderr, "usage: --new runs a fresh browser sign-in and takes no credential flags (--credential/--secret/--api-key/--option/--region)")
			return int(anyerr.InvalidUsage)
		}
		res, err := a.Accounts.Login(ctx, providerID, accounts.LoginOptions{
			Name: *name, MethodID: *authMethod, Source: *source,
		})
		if err != nil {
			return reportErr(err)
		}
		if !globals.Quiet {
			if res.Account.Identity != "" {
				fmt.Printf("saved %s/%s (%s)\n", res.Account.Provider, res.Account.Name, res.Account.Identity)
			} else {
				fmt.Printf("saved %s/%s\n", res.Account.Provider, res.Account.Name)
			}
		}
		return int(anyerr.Success)
	}

	// Snapshot flows below: --auth-method only selects a browser sign-in
	// method, which --current never runs.
	if *authMethod != "" {
		fmt.Fprintln(os.Stderr, "usage: --auth-method selects a browser sign-in method and requires --new")
		return int(anyerr.InvalidUsage)
	}
	// Gemini is the only provider whose snapshot read is source-scoped:
	// --source picks which sign-in --current reads when both exist.
	if *source != "" {
		if providerID != "gemini" || (*source != "gemini-cli" && *source != "antigravity") {
			fmt.Fprintln(os.Stderr, "usage: --source gemini-cli|antigravity is only supported for gemini (with --current), or use --new --source <id>")
			return int(anyerr.InvalidUsage)
		}
		if err := os.Setenv("GEMINI_OAUTH_SOURCE", *source); err != nil {
			return reportErr(err)
		}
	}
	// --region today only exists for kiro API-key accounts, whose save path
	// is not implemented yet (see the kiro note in the typed-input branch).
	if *region != "" {
		fmt.Fprintln(os.Stderr, "usage: --region is only supported for kiro API-key accounts, which are not implemented yet")
		return int(anyerr.InvalidUsage)
	}

	resolvedKind := *kind
	if *credential != "" {
		if resolvedKind != "" && resolvedKind != *credential {
			fmt.Fprintln(os.Stderr, "usage: --kind and --credential disagree; pass one credential kind")
			return int(anyerr.InvalidUsage)
		}
		resolvedKind = *credential
	}
	if *apiKey != "" || promptAPIKey {
		if resolvedKind != "" && resolvedKind != "api-key" {
			fmt.Fprintln(os.Stderr, "usage: --api-key implies credential kind api-key and disagrees with --credential/--kind")
			return int(anyerr.InvalidUsage)
		}
		resolvedKind = "api-key"
	}
	profile := *profileID
	if len(options) > 0 {
		prof, err := options.profile()
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage:", err)
			return int(anyerr.InvalidUsage)
		}
		if prof != "" {
			if profile != "" && profile != prof {
				fmt.Fprintln(os.Stderr, "usage: --profile and --option profile= disagree; pass one endpoint profile")
				return int(anyerr.InvalidUsage)
			}
			profile = prof
		}
	}

	rest := []string{"--name", *name}
	if *force {
		rest = append(rest, "--force")
	}
	if resolvedKind != "" {
		rest = append(rest, "--kind", resolvedKind)
		secretVal := *secret
		if secretVal == "" {
			secretVal = *apiKey
		}
		if secretVal == "" {
			secretVal = os.Getenv("ANYPICK_SECRET")
		}
		if secretVal == "" && (promptSecretFlag || promptAPIKey) {
			var perr error
			secretVal, perr = promptSecret("credential secret")
			if perr != nil {
				return reportErr(perr)
			}
		}
		if secretVal != "" {
			rest = append(rest, "--secret", secretVal)
		}
		if profile != "" {
			rest = append(rest, "--profile", profile)
		}
	}
	rest = append(rest, providerID)
	_ = current
	return cmdAddAccountCurrent(ctx, a, rest, false)
}

// splitBareSecretFlags pulls a valueless --secret / --api-key out of argv
// before flag parsing: bare means "prompt on the TTY". A following token that
// does not look like a flag is the value, not a prompt request.
func splitBareSecretFlags(args []string) (rest []string, promptSecret, promptAPIKey bool) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		tok := args[i]
		name := ""
		if tok == "--secret" || tok == "--api-key" {
			name = tok
		}
		if name == "" {
			rest = append(rest, tok)
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
			if name == "--secret" {
				promptSecret = true
			} else {
				promptAPIKey = true
			}
			continue
		}
		rest = append(rest, tok)
	}
	return rest, promptSecret, promptAPIKey
}

// optionFlags collects repeatable --option key=value qualifiers. Only the
// `profile` qualifier exists in the Go credential-input contract; anything
// else fails closed with the supported set instead of being silently dropped.
type optionFlags []string

func (o *optionFlags) String() string { return strings.Join(*o, ",") }

func (o *optionFlags) Set(v string) error {
	*o = append(*o, v)
	return nil
}

func (o optionFlags) profile() (string, error) {
	profile := ""
	for _, item := range o {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" || value == "" {
			return "", fmt.Errorf("--option must be key=value, got %q (supported: profile=<id>)", item)
		}
		if key != "profile" {
			return "", fmt.Errorf("--option %q is not a qualifier this provider contract supports (supported: profile=<id>)", key)
		}
		profile = value
	}
	return profile, nil
}

func cmdAddAccountCurrent(ctx context.Context, a *app.App, args []string, deprecated bool) int {
	fs := fsFor(args)
	name := fs.String("name", "", "account name")
	force := fs.Bool("force", false, "overwrite an existing account")
	kind := fs.String("kind", "", "credential kind for compatible vendors (e.g. api-key)")
	secret := fs.String("secret", "", "credential secret; prefer ANYPICK_SECRET (value via --secret is visible in process listing)")
	profileID := fs.String("profile", "", "endpoint profile id (default: first)")
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 || *name == "" {
		if deprecated {
			fmt.Fprintln(os.Stderr, "usage: anypick add <provider> --name <name> [--kind <k> --profile <p>] [--force]")
		} else {
			fmt.Fprintln(os.Stderr, "usage: anypick add account <provider> --name <name> [--current|--new] [--force]")
		}
		fmt.Fprintln(os.Stderr, "  compatible vendors take --kind/--secret (env ANYPICK_SECRET recommended)")
		return int(anyerr.InvalidUsage)
	}
	var (
		meta store.Meta
		err  error
	)
	if envSecret := os.Getenv("ANYPICK_SECRET"); *kind != "" && envSecret != "" && *secret == "" {
		*secret = envSecret
	}
	if *kind != "" {
		meta, err = a.Accounts.SaveInput(ctx, fs.Arg(0), *name, accounts.CredentialInput{
			Kind:    *kind,
			Secret:  *secret,
			Profile: orDefaultStr(*profileID, "global"),
		}, *force)
	} else {
		meta, err = a.Accounts.Save(ctx, fs.Arg(0), *name, *force)
	}
	if err != nil {
		return reportErr(err)
	}
	if meta.Identity != "" {
		if !globals.Quiet { // --quiet suppresses human chatter, not the mutation
			fmt.Printf("saved %s/%s (%s)\n", meta.Provider, meta.Name, meta.Identity)
		}
	} else if !globals.Quiet {
		fmt.Printf("saved %s/%s\n", meta.Provider, meta.Name)
	}
	return int(anyerr.Success)
}

func orDefaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func cmdUse(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	with := fs.String("with", "", "source reference")
	model := fs.String("model", "", "explicit model id")
	transportPolicy := fs.String("transport", "auto", "transport policy")
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 || *with == "" {
		fmt.Fprintln(os.Stderr, "usage: anypick use <client> --with <source>")
		return int(anyerr.InvalidUsage)
	}
	// Known account providers = every registered provider (file-snapshot AND
	// compatible vendors), mirroring parseAndResolveSourceInput.
	providerSet := map[string]bool{}
	for _, id := range a.Providers.IDs() {
		providerSet[id] = true
	}
	ref, err := refs.ParseRef(*with, &refs.ParseContext{AccountProviders: providerSet})
	if err != nil {
		return reportErr(err)
	}
	tracef("resolving source %q for client %q", *with, fs.Arg(0))
	plan, err := activation.PlanActivation(ctx, activation.Request{
		Mode:            activation.ModePersistent,
		Client:          fs.Arg(0),
		Source:          &ref,
		Model:           *model,
		TransportPolicy: refToPolicy(*transportPolicy),
	}, a.Deps, activation.Options{})
	if err != nil {
		return reportErr(err)
	}
	tracef("planned %d step(s) via %s", len(plan.Steps), plan.Transport.Capability)
	if globals.DryRun {
		// Plan only: no files, no DB rows, no processes (invariant #3).
		display := refs.DisplayRef(plan.ResolvedSource.Ref)
		if plan.ResolvedSource.Ref.Kind == refs.KindPreset {
			display = refs.DisplayRef(plan.ResolvedSource.Ref)
		}
		transportInfo := map[string]any{"capability": plan.Transport.Capability}
		if plan.Transport.Endpoint != "" {
			transportInfo["endpoint"] = plan.Transport.Endpoint
		}
		if globals.JSON {
			jsonOut(map[string]any{
				"client":        plan.Client,
				"source":        display,
				"model":         plan.Model.ID,
				"transport":     transportInfo,
				"alreadyActive": false,
				"dryRun":        true,
			})
		} else {
			fmt.Println("Dry run — plan only:")
			fmt.Printf("  · would activate %s × %s via %s\n", plan.Client, display, plan.Transport.Capability)
		}
		return int(anyerr.Success)
	}
	result, err := activation.Execute(ctx, plan, a.Deps, activation.ExecuteOptions{})
	if err != nil {
		return reportErr(err)
	}
	// Record a successful source resume for persistent activations. This is a
	// best-effort DX cache: binding + native config already succeeded.
	if plan.BindingSpec != nil {
		_ = a.Bindings.RecordSourceResumeContext(ctx, *plan.BindingSpec)
	}
	// --quiet silences human chatter only; JSON mode never printed these.
	if !globals.Quiet {
		fmt.Printf("activated %s × %s via %s\n",
			plan.Client, plan.ResolvedSource.Display, plan.Transport.Capability)
		if result.ProxyEndpoint != "" {
			// Endpoint only — the token never crosses an output boundary.
			label := "endpoint"
			if strings.HasPrefix(result.ProxyEndpoint, "http://127.0.0.1:") {
				label = "proxy"
			}
			fmt.Printf("%s: %s\n", label, result.ProxyEndpoint)
		}
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return int(anyerr.Success)
}

func cmdCurrent(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: anypick current <provider>")
		return int(anyerr.InvalidUsage)
	}
	cur, err := a.Accounts.Current(ctx, fs.Arg(0))
	if err != nil {
		return reportErr(err)
	}
	// TS prints a human table by default and JSON only under --json; keep the
	// same contract here (machine stream stays exactly one JSON value).
	if !globals.JSON {
		if cur.Active == "" {
			fmt.Printf("%s: no active account\n", fs.Arg(0))
			return int(anyerr.Success)
		}
		line := fmt.Sprintf("%s: %s", fs.Arg(0), cur.Active)
		if cur.Account != nil {
			line += fmt.Sprintf(" (%s)", cur.Account.Meta.Name)
		}
		fmt.Println(line)
		if !cur.IsLiveMatch {
			fmt.Println("  live credentials differ from the saved snapshot")
		}
		return int(anyerr.Success)
	}
	out := struct {
		Active      string `json:"active"`
		Present     bool   `json:"livePresent"`
		LiveMatch   bool   `json:"isLiveMatch"`
		AccountName string `json:"accountName,omitempty"`
	}{
		Active: cur.Active, Present: cur.Live.Present, LiveMatch: cur.IsLiveMatch,
	}
	if cur.Account != nil {
		out.AccountName = cur.Account.Meta.Name
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return int(anyerr.Success)
}

func cmdList(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	switch {
	case fs.NArg() == 0 || fs.Arg(0) == "providers":
		for _, id := range a.Providers.IDs() {
			pv, _ := a.Providers.Get(id)
			fmt.Printf("%s\t%s\n", id, pv.Description())
		}
		return int(anyerr.Success)
	case fs.Arg(0) == "accounts":
		providerIDs := []string{fs.Arg(1)}
		if fs.Arg(1) == "" {
			providerIDs = a.Providers.IDs()
		}
		for _, providerID := range providerIDs {
			all, err := a.Store.ListAccounts(ctx, providerID)
			if err != nil {
				return reportErr(err)
			}
			active, _ := a.Store.GetActive(ctx, providerID)
			for _, acct := range all {
				marker := ""
				if active == acct.Meta.Name {
					marker = " *"
				}
				fmt.Printf("%s/%s%s\t%s\n", acct.Meta.Provider, acct.Meta.Name,
					marker, acct.Meta.Identity)
			}
		}
		return int(anyerr.Success)
	default:
		fmt.Fprintln(os.Stderr, "usage: anypick list providers|accounts [<provider>]")
		return int(anyerr.InvalidUsage)
	}
}

func cmdStopProxy(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: anypick stop-proxy <provider> <account>")
		return int(anyerr.InvalidUsage)
	}
	if err := a.Proxy.Stop(ctx, fs.Arg(0), fs.Arg(1)); err != nil {
		return reportErr(err)
	}
	fmt.Println("stopped")
	return int(anyerr.Success)
}

func reportErr(err error) int {
	exit := int(anyerr.ExitCodeOf(err))
	fmt.Fprintln(os.Stderr, "error:", err)
	if ae := asAnyPick(err); ae != nil && len(ae.Suggestions) > 0 {
		for _, s := range ae.Suggestions {
			fmt.Fprintln(os.Stderr, "  →", s)
		}
	}
	return exit
}

func asAnyPick(err error) *anyerr.Error {
	var target *anyerr.Error
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func refToPolicy(s string) transport.Policy {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "direct":
		return transport.PolicyDirect
	case "proxy":
		return transport.PolicyProxy
	default:
		return transport.PolicyAuto
	}
}

// cmdGateway manages gateway profiles (vendor API keys saved under a name).
func cmdGateway(ctx context.Context, a *app.App, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: anypick gateway add <name> --provider <id> [--endpoint url] | anypick gateway list")
		return int(anyerr.InvalidUsage)
	}
	switch args[0] {
	case "add":
		return cmdGatewayAdd(ctx, a, args[1:])
	case "list", "ls":
		return cmdGatewayList(ctx, a)
	case "show":
		return cmdGatewayShow(ctx, a, args[1:])
	case "edit":
		return cmdGatewayEdit(ctx, a, args[1:])
	case "remove", "rm":
		return cmdGatewayRemove(ctx, a, args[1:])
	case "reset":
		return cmdGatewayReset(ctx, a, args[1:])
	default:
		fmt.Fprintln(os.Stderr, "unknown gateway subcommand:", args[0])
		return int(anyerr.InvalidUsage)
	}
}

func cmdGatewayAdd(ctx context.Context, a *app.App, args []string) int {
	// A bare --api-key (no =value, next token is a flag or absent) means
	// "prompt on the TTY" — see splitBareSecretFlags.
	args, _, promptAPIKey := splitBareSecretFlags(args)
	fs := fsFor(args)
	providerID := fs.String("provider", "", "catalog provider id (e.g. openrouter)")
	endpoint := fs.String("endpoint", "", "OpenAI-style base URL (defaults to the vendor's)")
	secret := fs.String("secret", "", "API key; prefer ANYPICK_SECRET (value via --secret is visible in process listing)")
	apiKey := fs.String("api-key", "", "API key value; bare flag prompts on the TTY")
	apiKeyEnv := fs.String("api-key-env", "", "read the API key from this environment variable")
	model := fs.String("model", "", "default model id")
	var models roleModelFlags
	fs.Var(&models, "models", "model role override as role=id (repeatable)")
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}
	if fs.NArg() != 1 || *providerID == "" {
		fmt.Fprintln(os.Stderr, "usage: anypick gateway add <name> --provider <id> [--endpoint url] [--api-key [key]|--api-key-env <var>|--secret <key>] [--model <id>] [--models <role=id>]")
		return int(anyerr.InvalidUsage)
	}
	cat := catalog.NewIndex(catalog.Builtin())
	entry, known := cat.Lookup(*providerID)
	if !known {
		return reportErr(anyerr.Newf("Unknown catalog provider \""+*providerID+".",
			"INVALID_USAGE", anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithSuggestions("Known: "+strings.Join(catalog.IDs(), ", "))))
	}
	if envSecret := os.Getenv("ANYPICK_SECRET"); *secret == "" && *apiKey == "" && *apiKeyEnv == "" && envSecret != "" {
		*secret = envSecret
	}
	if *apiKeyEnv != "" {
		if *secret != "" || *apiKey != "" {
			fmt.Fprintln(os.Stderr, "usage: pass one API key source (--secret, --api-key or --api-key-env)")
			return int(anyerr.InvalidUsage)
		}
		envVal, ok := os.LookupEnv(*apiKeyEnv)
		if !ok || envVal == "" {
			fmt.Fprintf(os.Stderr, "error: environment variable %s is not set or empty\n", *apiKeyEnv)
			return int(anyerr.InvalidUsage)
		}
		*secret = envVal
	}
	if *apiKey != "" {
		if *secret != "" {
			fmt.Fprintln(os.Stderr, "usage: pass one API key source (--secret, --api-key or --api-key-env)")
			return int(anyerr.InvalidUsage)
		}
		*secret = *apiKey
	}
	if *secret == "" && promptAPIKey {
		var perr error
		*secret, perr = promptSecret("API key")
		if perr != nil {
			return reportErr(perr)
		}
	}
	if *secret == "" {
		fmt.Fprintln(os.Stderr, "error: an API key is required (--secret, --api-key, --api-key-env or ANYPICK_SECRET)")
		return int(anyerr.InvalidUsage)
	}
	resolvedEndpoint := *endpoint
	if resolvedEndpoint == "" {
		resolvedEndpoint = entry.DefaultEndpoint
	}
	if resolvedEndpoint == "" {
		fmt.Fprintln(os.Stderr, "error: this vendor needs an explicit --endpoint")
		return int(anyerr.InvalidUsage)
	}
	gatewayName := fs.Arg(0)
	err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.GatewayRef(gatewayName)), func(c context.Context) error {
		_, err := a.Runtime.Profiles.UpsertContext(c, profile.Runtime{
			Meta: profile.Meta{
				Name: gatewayName, Provider: *providerID, Endpoint: resolvedEndpoint,
				DefaultModel: *model, Models: map[string]string(models),
			},
			Secrets: profile.Secrets{APIKey: *secret},
		})
		return err
	})
	if err != nil {
		return reportErr(err)
	}
	fmt.Printf("gateway %s (%s) → %s\n", gatewayName, *providerID, resolvedEndpoint)
	fmt.Printf("anthropic surface: %s\n",
		runtime.SmartEndpointFor(&client.Capabilities{
			ProtocolPreference: transport.Anthropic,
		}, *providerID, resolvedEndpoint, cat))
	return int(anyerr.Success)
}

func cmdGatewayList(ctx context.Context, a *app.App) int {
	names, err := a.Runtime.Profiles.ListContext(ctx)
	if err != nil {
		return reportErr(err)
	}
	for _, name := range names {
		pr, gerr := a.Runtime.Profiles.GetContext(ctx, name)
		if gerr != nil || pr == nil {
			continue
		}
		fmt.Printf("%s\t%s\t%s\n", pr.Meta.Name, pr.Meta.Provider, pr.Meta.Endpoint)
	}
	return int(anyerr.Success)
}

// roleModelFlags collects repeatable --models role=id overrides for gateway
// model roles. The profile stores roles as a role→model-id map, so anything
// else fails closed at parse time instead of being silently dropped.
type roleModelFlags map[string]string

func (r *roleModelFlags) String() string {
	parts := make([]string, 0, len(*r))
	for role, id := range *r {
		parts = append(parts, role+"="+id)
	}
	return strings.Join(parts, ",")
}

func (r *roleModelFlags) Set(v string) error {
	role, id, ok := strings.Cut(v, "=")
	if !ok || role == "" || id == "" {
		return fmt.Errorf("--models must be role=model-id, got %q", v)
	}
	if *r == nil {
		*r = roleModelFlags{}
	}
	(*r)[role] = id
	return nil
}

// cmdProxy manages per-account builtin proxies across invocations.
func cmdProxy(ctx context.Context, a *app.App, args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr,
			"usage: anypick proxy enable|disable <provider> <account> [--port N]\n"+
				"       anypick proxy start|stop|status <provider> <account>")
		return int(anyerr.InvalidUsage)
	}
	if len(args) < 2 {
		return usage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "enable", "disable":
		fs := fsFor(rest)
		port := fs.Int("port", 0, "fixed port (default: auto)")
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 2 {
			return usage()
		}
		if globals.DryRun {
			// TS extras: enable {provider,name,port,host,start}; disable
			// {provider,name}. Unset flags stay omitted like JSON.stringify.
			if sub == "enable" {
				printDryRunFields("proxy.enable",
					fmt.Sprintf("Would enable %s/%s", fs.Arg(0), fs.Arg(1)),
					kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)},
					kv{Key: "port", Value: intOrNil(*port)},
					kv{Key: "host", Value: "127.0.0.1"}, // TS option default
					kv{Key: "start", Value: true})
			} else {
				printDryRunFields("proxy.disable",
					fmt.Sprintf("Would disable %s/%s", fs.Arg(0), fs.Arg(1)),
					kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)})
			}
			return int(anyerr.Success)
		}
		cfg, err := a.Proxy.Configure(ctx, fs.Arg(0), fs.Arg(1), sub == "enable", *port)
		if err != nil {
			return reportErr(err)
		}
		fmt.Printf("%s %s/%s (port %d)\n", sub, fs.Arg(0), fs.Arg(1), cfg.Port)
		return int(anyerr.Success)

	case "start":
		fs := fsFor(rest)
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 2 {
			return usage()
		}
		if globals.DryRun {
			printDryRunFields("proxy.start",
				fmt.Sprintf("Would start %s/%s", fs.Arg(0), fs.Arg(1)),
				kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)})
			return int(anyerr.Success)
		}
		res, err := a.Proxy.StartProxy(ctx, fs.Arg(0), fs.Arg(1))
		if err != nil {
			return reportErr(err)
		}
		state := "started"
		if !res.StartedNow {
			state = "already running"
		}
		fmt.Printf("proxy %s (%s)\nendpoint: %s\n", state, res.InstanceID[:8], res.Endpoint)
		return int(anyerr.Success)

	case "stop":
		fs := fsFor(rest)
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 2 {
			return usage()
		}
		if globals.DryRun {
			printDryRunFields("proxy.stop",
				fmt.Sprintf("Would stop %s/%s", fs.Arg(0), fs.Arg(1)),
				kv{Key: "provider", Value: fs.Arg(0)}, kv{Key: "name", Value: fs.Arg(1)})
			return int(anyerr.Success)
		}
		if err := a.Proxy.Stop(ctx, fs.Arg(0), fs.Arg(1)); err != nil {
			return reportErr(err)
		}
		fmt.Println("stopped")
		return int(anyerr.Success)

	case "status":
		fs := fsFor(rest)
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 2 {
			return usage()
		}
		st, err := a.Proxy.StatusContext(ctx, fs.Arg(0), fs.Arg(1))
		if err != nil {
			return reportErr(err)
		}
		out := struct {
			Enabled  bool   `json:"enabled"`
			Running  bool   `json:"running"`
			Port     int    `json:"port,omitempty"`
			Endpoint string `json:"endpoint,omitempty"`
			PID      int    `json:"pid,omitempty"`
		}{Enabled: st.Enabled, Running: st.Running, Port: st.Port,
			Endpoint: st.Endpoint, PID: st.PID}
		// Human by default like TS printProxyStatus; --json stays machine-only.
		if globals.JSON {
			data, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(data))
			return int(anyerr.Success)
		}
		fmt.Printf("%s/%s\n", fs.Arg(0), fs.Arg(1))
		fmt.Printf("  Enabled:        %v\n", yesNo(st.Enabled))
		fmt.Printf("  Running:        %v\n", yesNo(st.Running))
		if st.Endpoint != "" {
			fmt.Printf("  Endpoint:       %s\n", st.Endpoint)
		}
		if st.PID > 0 {
			fmt.Printf("  PID:            %d\n", st.PID)
		}
		return int(anyerr.Success)

	case "config":
		return cmdProxyConfig(ctx, a, rest)

	case "restart":
		return cmdProxyRestart(ctx, a, rest)

	case "logs":
		return cmdProxyLogs(ctx, a, rest)

	case "pool":
		return cmdPool(ctx, a, rest)

	case "hub":
		return cmdHub(ctx, a, rest)

	default:
		return usage()
	}
}

// cmdAppRun is the desktop-app core (supervisor): pushes snapshots over
// stdout and executes commands from stdin — the Go counterpart of
// `tray run` in src/tray/supervisor.ts. Set ANYPICK_APP_BIN to have this
// process spawn and pipe the native app exactly like the TS supervisor does.
func cmdAppRun(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil {
		return int(anyerr.InvalidUsage)
	}

	var in io.Reader = os.Stdin
	var out io.Writer = os.Stdout
	if bin := os.Getenv("ANYPICK_APP_BIN"); bin != "" {
		proxyCount := "0"
		iconDir := filepath.Join(a.Root, "runtime", "tray", "icons")
		cmd := exec.Command(bin, proxyCount)
		cmd.Env = append(os.Environ(), "ANYPICK_TRAY_ICON_DIR="+iconDir)
		stdin, perr1 := cmd.StdinPipe()
		stdout, perr2 := cmd.StdoutPipe()
		if perr1 != nil || perr2 != nil {
			return reportErr(anyerr.Newf("pipe native app", "OPERATIONAL"))
		}
		cmd.Stderr = os.Stderr
		if err := startPlatformProcess(cmd); err != nil {
			return reportErr(anyerr.Wrapf(err, "spawn ANYPICK_APP_BIN", "OPERATIONAL"))
		}
		// The child is its own session leader (Setsid): signal the whole group
		// so helpers it spawns die with it.
		defer func() {
			_ = terminatePlatformPID(cmd.Process.Pid)
			_, _ = cmd.Process.Wait()
		}()
		in, out = stdout, stdin
		fmt.Fprintf(os.Stderr, "[app] native: %s\n", bin)
	}

	return runDesktopServer(ctx, a, in, out, false)
}

// cmdDesktopService is the app-owned service entrypoint. It never launches a
// UI process: the native host owns this child and supplies the dedicated
// protocol pipes (ADR 0020 §6). `app` remains a compatibility alias during the
// ownership inversion.
func cmdDesktopService(ctx context.Context, a *app.App, args []string) int {
	fs := fsFor(args)
	stdio := fs.Bool("stdio", false, "serve the desktop protocol over stdin/stdout")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 0 || !*stdio {
		return usageError("usage: anypick desktop-service --stdio")
	}
	return runDesktopServer(ctx, a, os.Stdin, os.Stdout, true)
}

func runDesktopServer(ctx context.Context, a *app.App, in io.Reader, out io.Writer, appOwned bool) int {
	deps := desktop.Deps{
		Root:              a.Root,
		Store:             a.Store,
		Accounts:          a.Accounts,
		Discovery:         &discovery.Service{Root: a.Root, Cache: &modelcache.Store{DB: a.DB}},
		Proxy:             a.Proxy,
		Providers:         a.Providers,
		Clients:           a.Clients,
		Profiles:          a.Runtime.Profiles,
		Bindings:          a.Bindings,
		Journal:           a.Journal,
		Activation:        a.Deps,
		Hub:               a.Hub,
		Config:            a.Config,
		PluginDiagnostics: a.PluginDiagnostics,
		ShutdownOwned: func(shutdownCtx context.Context) error {
			return stopDesktopOwnedServices(shutdownCtx, a)
		},
	}
	var srv *desktop.Server
	if appOwned {
		srv = desktop.NewService(deps, in, out)
	} else {
		srv = desktop.New(deps, in, out)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	if err := srv.Run(ctx); err != nil {
		return reportErr(err)
	}
	return int(anyerr.Success)
}

// configuredLaunchSurface resolves ui.defaultSurface through the config
// service; unconfigured roots report "" (explicit CLI-help policy).
func configuredLaunchSurface(ctx context.Context, a *app.App) string {
	if a.Config == nil {
		return ""
	}
	cfg, err := a.Config.Read(ctx)
	if err != nil {
		return ""
	}
	return config.LaunchSurfaceOf(cfg)
}
