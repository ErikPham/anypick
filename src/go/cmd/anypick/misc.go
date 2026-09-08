// providers / clients / doctor / update / completion plus the hidden pool and
// hub serve children.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/outof0/anypick/go/internal/proxy"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/buildinfo"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/ownership"
)

func cmdProviders(ctx context.Context, a *appView, args []string) int {
	if wantJSON(args) {
		type providerInfo struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		}
		var out []providerInfo
		for _, id := range a.Providers.IDs() {
			pv, _ := a.Providers.Get(id)
			out = append(out, providerInfo{ID: id, Description: pv.Description()})
		}
		jsonOut(out)
		return int(anyerr.Success)
	}
	return cmdList(ctx, a, []string{"providers"})
}

func cmdClients(ctx context.Context, a *appView, args []string) int {
	type clientInfo struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Present bool   `json:"present"`
		Issues  string `json:"issues,omitempty"`
	}
	var out []clientInfo
	for _, id := range a.Clients.IDs() {
		cad, err := a.Clients.Get(id)
		if err != nil {
			continue
		}
		info := clientInfo{ID: cad.ID(), Name: cad.Name()}
		if insp, ierr := cad.Inspect(); ierr == nil {
			info.Present = insp.Present
			info.Issues = strings.Join(insp.Issues, "; ")
		}
		out = append(out, info)
	}
	if wantJSON(args) {
		jsonOut(out)
		return int(anyerr.Success)
	}
	for _, c := range out {
		state := "not installed"
		if c.Present {
			state = "present"
		}
		fmt.Printf("%s\t%s\t%s\n", c.ID, c.Name, state)
	}
	return int(anyerr.Success)
}

// cmdDoctor runs read-only health checks over the local installation.
func cmdDoctor(ctx context.Context, a *appView, args []string) int {
	type finding struct {
		Check  string `json:"check"`
		Status string `json:"status"` // ok | warn | fail
		Detail string `json:"detail,omitempty"`
	}
	findings := []finding{}
	pluginDiagnostics := a.PluginDiagnostics(ctx)

	if _, err := os.Stat(filepath.Join(a.Root, "anypick.db")); err != nil {
		findings = append(findings, finding{Check: "database", Status: "fail",
			Detail: "anypick.db missing under data root"})
	} else {
		findings = append(findings, finding{Check: "database", Status: "ok"})
	}

	// Active pointers must reference existing accounts.
	for _, providerID := range a.Providers.IDs() {
		active, _ := a.Accounts.Active(ctx, providerID)
		if active == "" {
			continue
		}
		acct, gerr := a.Accounts.Get(ctx, providerID, active)
		switch {
		case gerr != nil || acct == nil:
			findings = append(findings, finding{Check: "active-binding", Status: "warn",
				Detail: fmt.Sprintf("%s points at missing account %q", providerID, active)})
		default:
			findings = append(findings, finding{Check: "active-binding", Status: "ok",
				Detail: providerID + "/" + active})
		}
	}

	// Enabled proxies should have persisted state or be restartable.
	for _, providerID := range a.Providers.IDs() {
		list, lerr := a.Accounts.List(ctx, providerID)
		if lerr != nil {
			continue
		}
		for _, acct := range list {
			cfg, _ := a.Proxy.ProxyConfig(ctx, providerID, acct.Meta.Name)
			if !cfg.Enabled {
				continue
			}
			st, serr := a.Proxy.StatusContext(ctx, providerID, acct.Meta.Name)
			if serr == nil && st.Running {
				findings = append(findings, finding{Check: "proxy", Status: "ok",
					Detail: fmt.Sprintf("%s/%s running", providerID, acct.Meta.Name)})
			} else {
				findings = append(findings, finding{Check: "proxy", Status: "warn",
					Detail: fmt.Sprintf("%s/%s enabled but not running", providerID, acct.Meta.Name)})
			}
		}
	}

	if pluginDiagnostics.Host.LoadedPlugins > 0 || pluginDiagnostics.Host.State == "degraded" {
		pluginStatus := "ok"
		if pluginDiagnostics.Host.State != "running" {
			pluginStatus = "warn"
		}
		detail := fmt.Sprintf(
			"%d loaded; host %s",
			pluginDiagnostics.Host.LoadedPlugins,
			pluginDiagnostics.Host.State,
		)
		if pluginDiagnostics.Host.Error != "" {
			detail += ": " + pluginDiagnostics.Host.Error
		}
		findings = append(findings, finding{
			Check: "plugin-host", Status: pluginStatus, Detail: detail,
		})
	}
	for _, failure := range pluginDiagnostics.Failures {
		findings = append(findings, finding{
			Check: "plugin:" + failure.Name, Status: "warn", Detail: failure.Reason,
		})
	}

	status := "ok"
	for _, f := range findings {
		if f.Status == "warn" && status == "ok" {
			status = "degraded"
		}
		if f.Status == "fail" {
			status = "unhealthy"
		}
	}
	if wantJSON(args) {
		jsonOut(map[string]interface{}{
			"status": status, "findings": findings, "plugins": pluginDiagnostics,
		})
		return int(anyerr.Success)
	}
	fmt.Printf("anypick doctor — %s\n", status)
	for _, f := range findings {
		// TS prints check detail only in verbose human mode.
		detail := ""
		if globals.Verbose {
			detail = f.Detail
		}
		fmt.Printf("  [%s] %s %s\n", f.Status, f.Check, detail)
	}
	return int(anyerr.Success)
}

// cmdUpdate checks GitHub Releases for a newer published version (advisory
// only). There is no npm package: the real channels are GitHub Release assets
// and source checkouts (ADR-0020), so the check queries the releases API and
// the guidance points at those two channels.
func cmdUpdate(_ context.Context, a *appView, args []string) int {
	const releasesAPI = "https://api.github.com/repos/outof0/anypick/releases/latest"
	const releasesPage = "https://github.com/outof0/anypick/releases/latest"
	current := packageVersion()
	req, err := http.NewRequest(http.MethodGet, releasesAPI, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update check failed (current version %s)\n", current)
		return int(anyerr.HealthFailure)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "anypick/"+current)
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update check failed (current version %s)\n", current)
		return int(anyerr.HealthFailure)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// No release has been published yet; the source workflow is the only
		// channel that exists today, so say that instead of failing.
		if wantJSON(args) {
			jsonOut(map[string]any{"current": current, "latest": nil, "detail": "no published release yet"})
			return int(anyerr.Success)
		}
		fmt.Printf("anypick %s: no published release yet\n", current)
		fmt.Println("update a source checkout with: git pull && make build")
		return int(anyerr.Success)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "update check failed (current version %s)\n", current)
		return int(anyerr.HealthFailure)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || body.TagName == "" {
		fmt.Fprintf(os.Stderr, "update check returned an unreadable response\n")
		return int(anyerr.HealthFailure)
	}
	latest := strings.TrimPrefix(body.TagName, "v")
	if wantJSON(args) {
		jsonOut(map[string]string{"current": current, "latest": latest})
		return int(anyerr.Success)
	}
	if latest == strings.TrimPrefix(current, "v") {
		fmt.Printf("anypick %s is up to date\n", current)
		return int(anyerr.Success)
	}
	if globals.DryRun {
		printDryRun("update", "Would upgrade anypick "+current+" → "+latest,
			map[string]any{"latest": latest})
		return int(anyerr.Success)
	}
	fmt.Printf("anypick %s → %s available\n", current, latest)
	fmt.Println("upgrade: download " + releasesPage + " or update a source checkout with: git pull && make build")
	return int(anyerr.Success)
}

// packageVersion reports the linked release version; "(devel)" for source
// builds (internal/buildinfo — injected via -ldflags by the release pipeline).
func packageVersion() string { return buildinfo.Version }

func cmdCompletion(a *appView, args []string) int {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	}
	// Static word lists reflect the dispatched tree minus hidden helpers;
	// positional depth resolves live ids through the hidden `__complete`
	// helper (mirrors src/cli/completion.ts).
	const topLevel = "add use run current list link unlink reset account gateway preset proxy plugin tray serve app start status stop stop-proxy update completion doctor providers clients"
	switch shell {
	case "bash":
		fmt.Printf(`# bash completion for anypick
_anypick_completions() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"
  cmd="${COMP_WORDS[1]}"
  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=( $(compgen -W '%[1]s' -- "$cur") )
    return
  fi
  case "$cmd" in
    use|run|link|unlink|reset)
      [[ ${COMP_CWORD} -eq 2 ]] && COMPREPLY=( $(compgen -W "$(anypick __complete clients 2>/dev/null)" -- "$cur") )
      ;;
    list)
      COMPREPLY=( $(compgen -W 'accounts gateways clients presets' -- "$cur") )
      ;;
    add)
      if [[ ${COMP_CWORD} -eq 2 ]]; then COMPREPLY=( $(compgen -W 'account gateway' -- "$cur") )
      elif [[ ${COMP_CWORD} -eq 3 && ${COMP_WORDS[2]} == account ]]; then COMPREPLY=( $(compgen -W "$(anypick __complete providers 2>/dev/null)" -- "$cur") ); fi
      ;;
    account)
      if [[ ${COMP_CWORD} -eq 2 ]]; then COMPREPLY=( $(compgen -W 'list remove refresh export import' -- "$cur") )
      elif [[ ${COMP_CWORD} -eq 3 ]]; then COMPREPLY=( $(compgen -W "$(anypick __complete providers 2>/dev/null)" -- "$cur") )
      elif [[ ${COMP_CWORD} -eq 4 ]]; then COMPREPLY=( $(compgen -W "$(anypick __complete accounts ${COMP_WORDS[2]} 2>/dev/null)" -- "$cur") ); fi
      ;;
    gateway)
      if [[ ${COMP_CWORD} -eq 2 ]]; then COMPREPLY=( $(compgen -W 'add list show edit remove reset' -- "$cur") )
      else COMPREPLY=( $(compgen -W "$(anypick __complete gateways 2>/dev/null)" -- "$cur") ); fi
      ;;
    preset)
      [[ ${COMP_CWORD} -eq 2 ]] && COMPREPLY=( $(compgen -W 'list show remove' -- "$cur") )
      ;;
	plugin)
	  [[ ${COMP_CWORD} -eq 2 ]] && COMPREPLY=( $(compgen -W 'list add remove enable disable trust' -- "$cur") )
	  ;;
    proxy)
      if [[ ${COMP_CWORD} -eq 2 ]]; then COMPREPLY=( $(compgen -W 'status start stop enable disable config logs pool hub' -- "$cur") )
      elif [[ ${COMP_CWORD} -eq 3 && ${COMP_WORDS[2]} != pool && ${COMP_WORDS[2]} != hub ]]; then COMPREPLY=( $(compgen -W "$(anypick __complete providers 2>/dev/null)" -- "$cur") )
      elif [[ ${COMP_CWORD} -eq 4 && ${COMP_WORDS[2]} != pool && ${COMP_WORDS[2]} != hub ]]; then COMPREPLY=( $(compgen -W "$(anypick __complete accounts ${COMP_WORDS[2]} 2>/dev/null)" -- "$cur") ); fi
      ;;
  esac
}
complete -F _anypick_completions anypick
`, topLevel)
	case "zsh":
		fmt.Printf(`#compdef anypick
_anypick() {
  if (( CURRENT == 2 )); then
    _values 'command' %[1]s
    return
  fi
  case $words[2] in
    (use|run|link|unlink|reset)
      (( CURRENT == 3 )) && _values 'client' ${(f)"$(anypick __complete clients 2>/dev/null)"}
      ;;
    (list)
      _values 'kind' accounts gateways clients presets
      ;;
    (add)
      if (( CURRENT == 2 )); then _values 'kind' account gateway
      elif (( CURRENT == 3 )) && [[ $words[3] == account ]]; then _values 'provider' ${(f)"$(anypick __complete providers 2>/dev/null)"}; fi
      ;;
    (account)
      if (( CURRENT == 2 )); then _values 'action' list remove refresh export import
      elif (( CURRENT == 3 )); then _values 'provider' ${(f)"$(anypick __complete providers 2>/dev/null)"}
      elif (( CURRENT == 4 )); then _values 'account' ${(f)"$(anypick __complete accounts $words[3] 2>/dev/null)"}; fi
      ;;
    (gateway)
      if (( CURRENT == 2 )); then _values 'action' add list show edit remove reset
      else _values 'gateway' ${(f)"$(anypick __complete gateways 2>/dev/null)"}; fi
      ;;
    (preset)
      (( CURRENT == 2 )) && _values 'action' list show remove
      ;;
	(plugin)
	  (( CURRENT == 2 )) && _values 'action' list add remove enable disable trust
	  ;;
    (proxy)
      if (( CURRENT == 2 )); then _values 'action' status start stop enable disable config logs pool hub
      elif (( CURRENT == 3 )); then _values 'provider' ${(f)"$(anypick __complete providers 2>/dev/null)"}
      elif (( CURRENT == 4 )); then _values 'account' ${(f)"$(anypick __complete accounts $words[3] 2>/dev/null)"}; fi
      ;;
  esac
}
compdef _anypick anypick
`, topLevel)
	case "fish":
		fmt.Println("# fish completion for anypick")
		fmt.Println("complete -c anypick -f")
		fmt.Printf("complete -c anypick -n __fish_use_subcommand -a '%s'\n", topLevel)
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from use run link unlink reset' -a '(anypick __complete clients 2>/dev/null)'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from list' -a 'accounts gateways clients presets'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from add' -a 'account gateway'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from plugin' -a 'list add remove enable disable trust'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from account' -a '(anypick __complete providers 2>/dev/null)'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from gateway' -a '(anypick __complete gateways 2>/dev/null)'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from account; and test (count (commandline -opc)) -eq 3' -a '(anypick __complete accounts (commandline -opc)[3] 2>/dev/null)'")
		fmt.Println("complete -c anypick -n '__fish_seen_subcommand_from proxy; and test (count (commandline -opc)) -eq 4' -a '(anypick __complete accounts (commandline -opc)[3] 2>/dev/null)'")
	default:
		return usageError("usage: anypick completion <bash|zsh|fish>")
	}
	return int(anyerr.Success)
}

// cmdComplete backs shell completion: hidden `__complete <kind> [arg]` prints
// live ids one per line from the current data root (TS parity: hidden
// `__complete` command in src/cli/commands.ts). Never listed in help.
func cmdComplete(ctx context.Context, a *appView, args []string) int {
	if len(args) == 0 {
		return usageError("usage: anypick __complete <providers|clients|gateways|accounts> [<provider>]")
	}
	arg := ""
	if len(args) > 1 {
		arg = args[1]
	}
	var out []string
	switch args[0] {
	case "providers":
		out = append(out, a.Providers.IDs()...)
	case "clients":
		out = append(out, a.Clients.IDs()...)
	case "gateways", "profiles":
		names, err := a.Runtime.ProfileSvc.ListContext(ctx)
		if err != nil {
			return reportErr(err)
		}
		out = append(out, names...)
	case "accounts":
		providers := []string{arg}
		if arg == "" {
			providers = a.Providers.IDs()
		}
		for _, providerID := range providers {
			list, err := a.Accounts.List(ctx, providerID)
			if err != nil {
				continue
			}
			for _, acct := range list {
				out = append(out, acct.Meta.Name)
			}
		}
	default:
		return usageError("unknown completion kind %q", args[0])
	}
	for _, id := range out {
		fmt.Println(id)
	}
	return int(anyerr.Success)
}

// serveHubChild serves one hub until terminated (hidden `_serve-hub <name>`).
func serveHubChild(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("_serve-hub requires a hub name")
	}
	name := args[0]
	cfg, err := a.ProxyHub.GetConfig(ctx, name)
	if err != nil || cfg == nil || !cfg.Enabled {
		fmt.Fprintln(os.Stderr, "hub is not enabled:", name)
		return int(anyerr.Operational)
	}
	srv := &hub.Server{
		Config: *cfg,
		// Echo the parent-recorded instance so `proxy hub stop` can prove the
		// process it is about to signal is the one it spawned (ADR 0003 §3).
		Instance:  os.Getenv(ownership.EnvInstanceID),
		Store:     a.ProxyHub,
		Resolver:  hubUpstreamResolver{a},
		PoolRings: hubPoolRings{a},
	}
	if err := srv.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "hub start failed:", err)
		return int(anyerr.Operational)
	}
	awaitSignal(ctx)
	_ = srv.Stop()
	return int(anyerr.Success)
}

// servePoolChild keeps an enabled pool's proxies alive until terminated
// (hidden `_serve-pool <provider>`). All pool business logic — member ring,
// failover, quota guard, ownership echo — lives in the proxy service; this
// wrapper only dispatches, awaits a signal, and maps failures to exit codes.
func servePoolChild(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("_serve-pool requires a provider id")
	}
	if err := a.Proxy.ServePoolExisting(ctx, args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "pool serve failed:", err)
		return int(anyerr.Operational)
	}
	return int(anyerr.Success)
}

// awaitSignal blocks until SIGINT/SIGTERM.
func awaitSignal(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-ctx.Done():
	}
}

// hubUpstreamResolver adapts the app's snapshot resolver for the hub server.
type hubUpstreamResolver struct{ a *appView }

func (h hubUpstreamResolver) ResolveUpstream(ctx context.Context, providerID, account string) (*proxy.Upstream, error) {
	resolver := h.a.Proxy.Resolver
	if resolver == nil {
		return nil, fmt.Errorf("no upstream resolver configured")
	}
	return resolver.ResolveUpstream(ctx, providerID, account)
}

// hubPoolRings adapts the proxy service's pool ring builder for hub sources
// whose account value references a multi-account pool.
type hubPoolRings struct{ a *appView }

func (h hubPoolRings) ResolvePoolRing(ctx context.Context, providerID string) (*proxy.PoolServer, error) {
	return h.a.Proxy.HubPoolRing(ctx, providerID)
}
