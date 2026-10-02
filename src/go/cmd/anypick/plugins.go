package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/plugins"
)

func cmdPlugin(ctx context.Context, a *appView, args []string) int {
	if len(args) == 0 {
		return usageError("usage: anypick plugin <list|add|remove|enable|disable|trust> …")
	}
	switch args[0] {
	case "list", "ls":
		return cmdPluginList(ctx, a, args[1:])
	case "add":
		return cmdPluginAdd(ctx, a, args[1:])
	case "remove", "rm":
		return cmdPluginRemove(ctx, a, args[1:])
	case "enable":
		return cmdPluginEnable(ctx, a, args[1:])
	case "disable":
		return cmdPluginDisable(ctx, a, args[1:])
	case "trust":
		return cmdPluginTrust(ctx, a, args[1:])
	default:
		return usageError("unknown plugin subcommand %q", args[0])
	}
}

type pluginListFailure struct {
	Reason    string `json:"reason"`
	Untrusted bool   `json:"untrusted"`
}

type pluginListRecord struct {
	Name      string             `json:"name"`
	Version   string             `json:"version"`
	Path      string             `json:"path"`
	Enabled   bool               `json:"enabled"`
	Digest    string             `json:"digest"`
	Loaded    bool               `json:"loaded"`
	Failure   *pluginListFailure `json:"failure"`
	AddedAt   string             `json:"addedAt"`
	UpdatedAt string             `json:"updatedAt"`
}

func cmdPluginList(ctx context.Context, a *appView, args []string) int {
	if len(args) != 0 {
		return usageError("usage: anypick plugin list")
	}
	records, err := a.Plugins.List(ctx)
	if err != nil {
		return reportErr(err)
	}
	failures := make(map[string]plugins.LoadFailure, len(a.PluginRuntime.Failures))
	for _, failure := range a.PluginRuntime.Failures {
		failures[failure.Name] = failure
	}
	loaded := make(map[string]bool, len(a.PluginRuntime.Loaded))
	for _, entry := range a.PluginRuntime.Loaded {
		loaded[entry.Record.Name] = true
	}
	if globals.JSON {
		out := make([]pluginListRecord, 0, len(records))
		for _, record := range records {
			var failure *pluginListFailure
			if current, ok := failures[record.Name]; ok {
				failure = &pluginListFailure{Reason: current.Reason, Untrusted: current.Untrusted}
			}
			out = append(out, pluginListRecord{
				Name: record.Name, Version: record.Version, Path: record.Path,
				Enabled: record.Enabled, Digest: record.Digest,
				Loaded: loaded[record.Name], Failure: failure,
				AddedAt: record.AddedAt, UpdatedAt: record.UpdatedAt,
			})
		}
		jsonOut(out)
		return int(anyerr.Success)
	}
	if len(records) == 0 {
		fmt.Println("No plugins installed.")
		fmt.Println("  next install a plugin directory  anypick plugin add <dir>")
		return int(anyerr.Success)
	}
	for _, record := range records {
		state := "?"
		failure, failed := failures[record.Name]
		if !record.Enabled {
			state = "off"
		} else if failed {
			state = "fail"
		} else if loaded[record.Name] {
			state = "on"
		}
		fmt.Printf("  %-4s %-20s %-10s %s\n", state, record.Name, record.Version, shortPluginDigest(record.Digest))
		fmt.Printf("    %s\n", record.Path)
		if failed {
			fmt.Printf("    %s\n", failure.Reason)
			if failure.Untrusted {
				fmt.Printf("    → anypick plugin trust %s\n", record.Name)
			}
		}
	}
	return int(anyerr.Success)
}

func cmdPluginAdd(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("usage: anypick plugin add <dir>")
	}
	record, err := a.Plugins.Add(ctx, args[0])
	if err != nil {
		return reportErr(err)
	}
	if globals.JSON {
		jsonOut(record)
		return int(anyerr.Success)
	}
	if !globals.Quiet {
		fmt.Printf("Added %s %s (%s)\n", record.Name, record.Version, shortPluginDigest(record.Digest))
		if !record.Enabled {
			fmt.Printf("  next plugins do not run until enabled  anypick plugin enable %s\n", record.Name)
		}
	}
	return int(anyerr.Success)
}

func cmdPluginRemove(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("usage: anypick plugin remove <name>")
	}
	if err := a.Plugins.Remove(ctx, args[0]); err != nil {
		return reportErr(err)
	}
	if globals.JSON {
		jsonOut(map[string]any{"name": args[0], "removed": true})
	} else if !globals.Quiet {
		fmt.Printf("Removed %s\n", args[0])
	}
	return int(anyerr.Success)
}

func cmdPluginEnable(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("usage: anypick plugin enable <name>")
	}
	name := args[0]
	existing, err := a.Plugins.Get(ctx, name)
	if err != nil {
		return reportErr(err)
	}
	if existing != nil && existing.Enabled {
		if globals.JSON {
			jsonOut(existing)
		} else if !globals.Quiet {
			fmt.Printf("%s is already enabled.\n", name)
		}
		return int(anyerr.Success)
	}
	if !globals.JSON && !globals.Quiet && existing != nil {
		// ADR-0021: a native build never loads plugin code, so the legacy
		// "runs as executable JavaScript" warning would describe a boundary
		// that does not exist. Keep the trust framing without the execution claim.
		fmt.Printf("Warning: enabling %s records a trust decision.\n", name)
		fmt.Printf("  %s\n", existing.Path)
		fmt.Println("  A native build does not load or run plugin code (ADR-0021);")
		fmt.Println("  if a Go-native executor lands, enabling will mean trusting its author.")
		fmt.Println("  Package digest is re-checked before every import; changes require trust.")
	}
	if err := confirmPluginTrust(
		"Enabling "+name,
		"Enable "+name+" and allow its code to run in the isolated plugin host?",
		globals.Yes,
	); err != nil {
		if errors.Is(err, errDeclined) {
			return int(anyerr.Cancelled)
		}
		return reportErr(err)
	}
	record, err := a.Plugins.SetEnabled(ctx, name, true)
	if err != nil {
		return reportErr(err)
	}
	if globals.JSON {
		jsonOut(record)
	} else if !globals.Quiet {
		fmt.Printf("Enabled %s\n", name)
		fmt.Println("  next confirm it loads on the next run  anypick plugin list")
	}
	return int(anyerr.Success)
}

func cmdPluginDisable(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("usage: anypick plugin disable <name>")
	}
	record, err := a.Plugins.SetEnabled(ctx, args[0], false)
	if err != nil {
		return reportErr(err)
	}
	if globals.JSON {
		jsonOut(record)
	} else if !globals.Quiet {
		fmt.Printf("Disabled %s\n", args[0])
	}
	return int(anyerr.Success)
}

type pluginTrustJSON struct {
	plugins.Record
	PreviousDigest string `json:"previousDigest"`
}

func cmdPluginTrust(ctx context.Context, a *appView, args []string) int {
	if len(args) != 1 {
		return usageError("usage: anypick plugin trust <name>")
	}
	name := args[0]
	existing, err := a.Plugins.Get(ctx, name)
	if err != nil {
		return reportErr(err)
	}
	if !globals.JSON && !globals.Quiet && existing != nil {
		fmt.Printf("Warning: This approves the code currently at %s.\n", existing.Path)
	}
	if err := confirmPluginTrust(
		"Trusting "+name, "Trust the current code of "+name+"?", globals.Yes,
	); err != nil {
		if errors.Is(err, errDeclined) {
			return int(anyerr.Cancelled)
		}
		return reportErr(err)
	}
	result, err := a.Plugins.Trust(ctx, name)
	if err != nil {
		return reportErr(err)
	}
	if globals.JSON {
		jsonOut(pluginTrustJSON{Record: result.Record, PreviousDigest: result.PreviousDigest})
	} else if !globals.Quiet {
		fmt.Printf("Trusted %s %s\n", name, result.Record.Version)
		fmt.Printf("  %s → %s\n", shortPluginDigest(result.PreviousDigest), shortPluginDigest(result.Record.Digest))
	}
	return int(anyerr.Success)
}

func shortPluginDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// confirmPluginTrust is intentionally a constructive confirmation gate: the
// dangerous operation is granting code execution, not deleting a registry row.
func confirmPluginTrust(gerund, question string, yes bool) error {
	if yes {
		return nil
	}
	if globals.NoInput || !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		return anyerr.Newf(
			gerund+" requires --yes in non-interactive mode.",
			"CONFIRMATION_REQUIRED",
			anyerr.WithExitCode(anyerr.InvalidUsage),
			anyerr.WithMutated(false),
		)
	}
	// JSON stdout must remain parseable even when the user chose to confirm in
	// an interactive terminal. Human prompts belong on stderr in that mode.
	var output io.Writer = os.Stdout
	if globals.JSON {
		output = os.Stderr
	}
	fmt.Fprintf(output, "%s [y/N] ", question)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		if !globals.JSON && !globals.Quiet {
			fmt.Println("Cancelled.")
		}
		return errDeclined
	}
	return nil
}
