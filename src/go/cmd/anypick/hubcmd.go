// Proxy Hub command group: status, enable, source, owner, preview, start, stop.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/refs"
)

func cmdHub(ctx context.Context, a *appView, args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr,
			"usage: anypick proxy hub <status|enable|source|owner|preview|start|stop> <name> …")
		return int(anyerr.InvalidUsage)
	}
	if len(args) == 0 {
		return usage()
	}
	sub := args[0]
	rest := args[1:]
	fs := fsFor(rest)

	switch sub {
	case "status":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 1 {
			return usage()
		}
		name := fs.Arg(0)
		cfg, gerr := a.Hub.GetConfigContext(ctx, name)
		if gerr != nil {
			return reportErr(gerr)
		}
		if cfg == nil {
			return reportErr(anyerr.Newf("Proxy Hub not found: "+name, "HUB_NOT_FOUND",
				anyerr.WithExitCode(anyerr.NotFound)))
		}
		if wantJSON(rest) {
			jsonOut(cfg)
			return int(anyerr.Success)
		}
		fmt.Printf("hub %s: enabled=%v port=%d sources=%d\n",
			cfg.Name, cfg.Enabled, cfg.Port, len(cfg.Sources))
		for _, s := range cfg.Sources {
			fmt.Printf("  source %s/%s\n", s.ProviderID, s.Account)
		}
		return int(anyerr.Success)

	case "enable":
		host := fs.String("host", "127.0.0.1", "loopback host")
		port := fs.Int("port", 0, "fixed port (default: auto)")
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 1 {
			return usage()
		}
		name := fs.Arg(0)
		if globals.DryRun {
			// Resolve the effective host/port read-only (TS: opts ?? current).
			effPort := *port
			var portVal any
			if existing, gerr := a.Hub.GetConfigContext(ctx, name); gerr == nil && existing != nil && effPort == 0 {
				effPort = existing.Port
			}
			if effPort != 0 {
				portVal = effPort
			}
			printDryRunFields("proxy.hub.enable", "Would enable the unified Proxy Hub",
				kv{Key: "host", Value: *host},
				kv{Key: "port", Value: portVal}, // Go never invents a default port (see PARITY.md)
				kv{Key: "start", Value: false})  // Go hub enable has no --start flag yet
			return int(anyerr.Success)
		}
		var saved *hub.Config
		err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.ProxyHubRef(name)), func(c context.Context) error {
			existing, gerr := a.Hub.GetConfigContext(c, name)
			if gerr != nil {
				return gerr
			}
			if existing == nil {
				existing = &hub.Config{Name: name, Sources: []hub.Source{}}
			}
			existing.Enabled = true
			if *port != 0 {
				existing.Port = *port
			}
			if *host != "" {
				existing.Host = *host
			}
			upserted, uerr := a.Hub.UpsertConfigContext(c, *existing)
			saved = &upserted
			return uerr
		})
		if err != nil {
			return reportErr(err)
		}
		fmt.Printf("enabled hub %s (port %d)\n", name, saved.Port)
		return int(anyerr.Success)

	case "source":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() < 2 || fs.NArg() > 3 {
			return usage()
		}
		name, providerID, account := fs.Arg(0), fs.Arg(1), fs.Arg(2)
		if globals.DryRun {
			// Toggle semantics resolved READ-ONLY: present → would disable.
			existing, gerr := a.Hub.GetConfigContext(ctx, name)
			if gerr != nil {
				return reportErr(gerr)
			}
			enabled := true
			found := false
			if existing != nil {
				for _, s := range existing.Sources {
					if s.ProviderID == providerID && s.Account == account {
						found, enabled = true, false
					}
				}
			}
			if !found && account == "" {
				return reportErr(anyerr.Newf("source needs an account name", "INVALID_USAGE",
					anyerr.WithExitCode(anyerr.InvalidUsage)))
			}
			printDryRunFields("proxy.hub.source",
				fmt.Sprintf("Would %s %s/%s behind Proxy Hub", map[bool]string{true: "enable", false: "disable"}[enabled], providerID, account),
				kv{Key: "provider", Value: providerID}, kv{Key: "account", Value: account},
				kv{Key: "enabled", Value: enabled})
			return int(anyerr.Success)
		}
		err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.ProxyHubRef(name)), func(c context.Context) error {
			existing, gerr := a.Hub.GetConfigContext(c, name)
			if gerr != nil {
				return gerr
			}
			if existing == nil {
				return anyerr.Newf("Proxy Hub not found: "+name, "HUB_NOT_FOUND",
					anyerr.WithExitCode(anyerr.NotFound))
			}
			// Toggle semantics: present → remove, absent → add.
			updated := existing.Sources[:0]
			found := false
			for _, s := range existing.Sources {
				if s.ProviderID == providerID && s.Account == account {
					found = true
					continue
				}
				updated = append(updated, s)
			}
			if !found {
				if account == "" {
					return anyerr.Newf("source needs an account name", "INVALID_USAGE",
						anyerr.WithExitCode(anyerr.InvalidUsage))
				}
				updated = append(updated, hub.Source{ProviderID: providerID, Account: account})
			}
			existing.Sources = updated
			_, uerr := a.Hub.UpsertConfigContext(c, *existing)
			return uerr
		})
		if err != nil {
			return reportErr(err)
		}
		fmt.Printf("sources updated for %s\n", name)
		return int(anyerr.Success)

	case "owner":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 3 {
			return usage()
		}
		name, model, owner := fs.Arg(0), fs.Arg(1), fs.Arg(2)
		if globals.DryRun {
			// TS takes separate provider/account args; Go's owner is one ref.
			p, acct, _ := strings.Cut(owner, "/")
			printDryRunFields("proxy.hub.owner",
				fmt.Sprintf("Would route %s through %s/%s", model, p, acct),
				kv{Key: "model", Value: model}, kv{Key: "provider", Value: p},
				kv{Key: "account", Value: acct})
			return int(anyerr.Success)
		}
		err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.ProxyHubRef(name)), func(c context.Context) error {
			return a.Hub.SetModelOwnerContext(c, name, model, owner)
		})
		if err != nil {
			return reportErr(err)
		}
		fmt.Printf("model %s owned by %s\n", model, owner)
		return int(anyerr.Success)

	case "preview":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 1 {
			return usage()
		}
		manifest, perr := a.Hub.PreviewManifest(ctx, fs.Arg(0))
		if perr != nil {
			return reportErr(perr)
		}
		if wantJSON(rest) {
			jsonOut(manifest)
			return int(anyerr.Success)
		}
		for _, line := range manifest.Lines {
			fmt.Println(line)
		}
		return int(anyerr.Success)

	case "start":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 1 {
			return usage()
		}
		name := fs.Arg(0)
		cfg, gerr := a.Hub.GetConfigContext(ctx, name)
		if gerr != nil {
			return reportErr(gerr)
		}
		if cfg == nil || !cfg.Enabled {
			return reportErr(anyerr.Newf("hub "+name+" is not enabled", "HUB_DISABLED"))
		}
		pidFile := hubPIDPath(a.Root, name)
		if pid := readPID(pidFile); pid != 0 {
			fmt.Printf("already running (pid %d)\n", pid)
			return int(anyerr.Success)
		}
		pid, serr := spawnDetachedService(a.Root, []string{"_serve-hub", name}, pidFile)
		if serr != nil {
			return reportErr(serr)
		}
		fmt.Printf("started (pid %d)\n", pid)
		return int(anyerr.Success)

	case "stop":
		if err := fs.Parse(reorder(rest)); err != nil || fs.NArg() != 1 {
			return usage()
		}
		name := fs.Arg(0)
		if globals.DryRun {
			printDryRunFields("proxy.hub.stop", "Would stop the unified Proxy Hub")
			return int(anyerr.Success)
		}
		pidFile := hubPIDPath(a.Root, name)
		rec := ownership.Load(pidFile)
		if rec == nil {
			fmt.Println("not running")
			return int(anyerr.Success)
		}
		// ADR 0003 §3: prove the live process is ours via the /health instance
		// echo before signalling its session group.
		cfg, gerr := a.Hub.GetConfigContext(ctx, name)
		if gerr != nil {
			return reportErr(gerr)
		}
		stopped := ownership.Stop(pidFile, ownership.StopOptions{
			Timeout: stopWait,
			Health: func(r ownership.Record) bool {
				if cfg == nil || cfg.Port == 0 {
					return false
				}
				return hubHealthEcho(fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port), r.InstanceID)
			},
		})
		if !stopped {
			return reportErr(anyerr.Newf(
				"could not verify ownership of hub `"+name+"`; not signalling it",
				"HUB_OWNERSHIP_UNVERIFIED"))
		}
		fmt.Println("stopped")
		return int(anyerr.Success)

	default:
		return usage()
	}
}

var _ = os.Stdout

// hubHealthEcho proves a hub child's identity: /health must echo the exact
// instance id recorded at spawn (ADR 0003 §3). Bounded, no retries.
func hubHealthEcho(healthURL, wantInstanceID string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var payload struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload) != nil {
		return false
	}
	return payload.OK && payload.InstanceID == wantInstanceID
}
