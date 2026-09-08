// Presets, project bindings, ephemeral run, and client reset commands.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// ---- preset group -------------------------------------------------------

func cmdPreset(ctx context.Context, a *appView, args []string) int {
	if len(args) == 0 {
		return usageError("usage: anypick preset <list|show|remove> …")
	}
	switch args[0] {
	case "list":
		saved, err := a.Presets.ListContext(ctx)
		if err != nil {
			return reportErr(err)
		}
		if wantJSON(args) {
			jsonOut(saved)
			return int(anyerr.Success)
		}
		for _, p := range saved {
			fmt.Printf("%s\t%s\n", p.Name, refs.DisplayRef(p.Spec.Source))
		}
		return int(anyerr.Success)
	case "show":
		return cmdPresetShow(ctx, a, args[1:])
	case "remove":
		return cmdPresetRemove(ctx, a, args[1:])
	default:
		return usageError("unknown preset subcommand %q", args[0])
	}
}

func cmdPresetShow(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick preset show <name>")
	}
	p, err := a.Presets.GetByNameContext(ctx, fs.Arg(0))
	if err != nil || p == nil {
		return reportErr(anyerr.Newf("preset not found: "+fs.Arg(0), "PRESET_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound)))
	}
	out := struct {
		Name      string `json:"name"`
		Client    string `json:"client"`
		Model     string `json:"model,omitempty"`
		CreatedAt string `json:"createdAt"`
		LastUsed  string `json:"lastUsedAt,omitempty"`
		UseCount  int    `json:"useCount"`
	}{p.Name, p.Spec.Client, p.Spec.Model.ID, p.CreatedAt, p.LastUsedAt, p.UseCount}
	if wantJSON(args) {
		jsonOut(out)
	} else {
		fmt.Printf("%s: %s × %s (used %d×)\n", p.Name, p.Spec.Client, refs.DisplayRef(p.Spec.Source), p.UseCount)
	}
	return int(anyerr.Success)
}

func cmdPresetRemove(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick preset remove <name>")
	}
	if globals.DryRun {
		printDryRunFields("preset.remove", "Would remove preset @"+fs.Arg(0),
			kv{Key: "name", Value: fs.Arg(0)})
		return int(anyerr.Success)
	}
	err := lock.WithMutationLock(ctx, a.Root, refs.MutationScopeForRef(refs.PresetRef(fs.Arg(0))), func(c context.Context) error {
		return a.Presets.DeleteByNameContext(c, fs.Arg(0))
	})
	if err != nil {
		return reportErr(err)
	}
	fmt.Printf("removed %s\n", fs.Arg(0))
	return int(anyerr.Success)
}

// ---- link / unlink / reset ----------------------------------------------
//
// TS semantics (src/core/binding-service.ts): `link` writes a PROJECT
// binding, `unlink` removes project binding(s), and `reset` clears the
// client's managed config AND its global binding. None of them touch global
// bindings implicitly.

func cmdLink(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	dir := fs.String("dir", "", "project directory (default: cwd)")
	model := fs.String("model", "", "explicit model for the binding")
	withFlag := fs.String("with", "", "source reference")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick link <client> --with <source> [--model id]")
	}
	clientID := fs.Arg(0)
	root, rerr := projectRoot(*dir)
	if rerr != nil {
		return reportErr(rerr)
	}

	// Without --with: re-apply the ENTIRE global spec into this project —
	// model, client options (model roles) and provenance travel with it, like
	// BindingService.link in TypeScript. Planning a fresh source-only request
	// would silently drop them.
	var planOpts activation.Options
	if *withFlag == "" {
		global, gerr := a.BindingSvc.GetGlobalContext(ctx, clientID)
		if gerr != nil {
			return reportErr(gerr)
		}
		if global == nil {
			return reportErr(anyerr.Newf(
				fmt.Sprintf("no --with source and no global binding for %s", clientID),
				"INVALID_USAGE", anyerr.WithExitCode(anyerr.InvalidUsage)))
		}
		specCopy := global.Spec
		planOpts.ReapplySpec = &specCopy
		// TS authority records globalBindingUpdatedAt from the source global
		// binding — snapshot provenance must survive link-without---with.
		planOpts.ReapplyProv = &bindings.Provenance{
			Kind:                   "global_binding_snapshot",
			GlobalBindingUpdatedAt: global.UpdatedAt,
		}
	}
	var ref refs.Ref
	if *withFlag != "" {
		parsed, err := parseSourceRef(a, *withFlag)
		if err != nil {
			return reportErr(err)
		}
		ref = parsed
	}
	req := activation.Request{
		Mode:        activation.ModeProject,
		Client:      clientID,
		Model:       *model,
		ProjectRoot: root,
	}
	if *withFlag != "" {
		req.Source = &ref
	}
	plan, err := activation.PlanActivation(ctx, req, a.Deps, planOpts)
	if err != nil {
		return reportErr(err)
	}
	if globals.DryRun {
		// Plan-steps output per primary.ts: planning is pure, so nothing was
		// persisted and nothing will be executed.
		if globals.JSON {
			display := ""
			if plan.ResolvedSource != nil {
				display = plan.ResolvedSource.Display
			}
			fmt.Println(orderedJSON([]kv{
				{Key: "client", Value: clientID},
				{Key: "source", Value: display},
				{Key: "dryRun", Value: true},
				{Key: "scope", Value: "project"},
			}))
		} else {
			fmt.Println("Dry run — project link plan:")
			for _, s := range plan.Steps {
				fmt.Printf("  · %s\n", s.Kind)
			}
		}
		return int(anyerr.Success)
	}
	if _, err := activation.Execute(ctx, plan, a.Deps, activation.ExecuteOptions{}); err != nil {
		return reportErr(err)
	}
	display := refs.DisplayRef(ref)
	if *withFlag == "" {
		display = refs.DisplayRef(plan.ResolvedSource.Ref)
	}
	fmt.Printf("linked %s → %s (project)\n", clientID, display)
	return int(anyerr.Success)
}

func cmdUnlink(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	dir := fs.String("dir", "", "project directory (default: cwd)")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() > 1 {
		return usageError("usage: anypick unlink [<client>]")
	}
	root, rerr := projectRoot(*dir)
	if rerr != nil {
		return reportErr(rerr)
	}
	clientID := fs.Arg(0)
	if globals.DryRun {
		// Read-only count (commands.ts unlink): no lock, no mutation.
		var removed int64
		if clientID == "" {
			n, cerr := a.BindingSvc.CountProjectsContext(ctx, root)
			if cerr != nil {
				return reportErr(cerr)
			}
			removed = n
		} else {
			existing, gerr := a.BindingSvc.GetProjectContext(ctx, root, clientID)
			if gerr != nil {
				return reportErr(gerr)
			}
			if existing != nil {
				removed = 1
			}
		}
		printDryRunFields("binding.unlink",
			fmt.Sprintf("Would remove %d project binding(s)", removed),
			kv{Key: "client", Value: strOrJSONNull(clientID)}, kv{Key: "removed", Value: removed})
		return int(anyerr.Success)
	}
	var removed int64
	err := lock.WithMutationLock(ctx, a.Root, "project/"+root, func(c context.Context) error {
		if clientID == "" {
			n, derr := a.BindingSvc.DeleteAllProjectsContext(c, root)
			removed = n
			return derr
		}
		existed, gerr := a.BindingSvc.GetProjectContext(c, root, clientID)
		if gerr != nil {
			return gerr
		}
		if existed == nil {
			return nil
		}
		removed = 1
		return a.BindingSvc.DeleteProjectContext(c, root, clientID)
	})
	if err != nil {
		return reportErr(err)
	}
	if removed == 0 {
		fmt.Println("Nothing to unlink")
	} else {
		fmt.Printf("Removed %d project binding(s)\n", removed)
	}
	return int(anyerr.Success)
}

// cmdReset clears AnyPick-managed client configuration and the global
// binding under the client lock (mirrors bindingService.reset).
func cmdReset(ctx context.Context, a *appView, args []string) int {
	fs := fsFor(args)
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return usageError("usage: anypick reset <client>")
	}
	clientID := fs.Arg(0)
	cad, gerr := a.Clients.Get(clientID)
	if gerr != nil {
		return reportErr(gerr)
	}
	if globals.DryRun {
		g, rerr := a.BindingSvc.GetGlobalContext(ctx, clientID)
		if rerr != nil {
			return reportErr(rerr)
		}
		emitResetDryRun(clientID, g != nil, false)
		return int(anyerr.Success)
	}
	// `c`, not the outer ctx: re-entrancy is carried in the context (ADR 0009),
	// and the delete error must be returned — dropping it reported "reset" for
	// a binding that was still on disk.
	err := lock.WithMutationLock(ctx, a.Root, "client/"+cad.ID(), func(c context.Context) error {
		if err := cad.Reset(); err != nil {
			return err
		}
		return a.BindingSvc.DeleteGlobalContext(c, clientID)
	})
	if err != nil {
		return reportErr(err)
	}
	fmt.Printf("reset %s\n", clientID)
	return int(anyerr.Success)
}

// ---- run (ephemeral launch) ----------------------------------------------

func cmdRun(ctx context.Context, a *appView, args []string) int {
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	head, passThrough := args, []string(nil)
	if sep >= 0 {
		head, passThrough = args[:sep], args[sep+1:]
	}
	fs := fsFor(head)
	with := fs.String("with", "", "source reference")
	model := fs.String("model", "", "explicit model id")
	transportPolicy := fs.String("transport", "auto", "transport policy")
	if err := fs.Parse(reorder(head)); err != nil || fs.NArg() < 1 || *with == "" {
		return usageError("usage: anypick run <client> --with <source> [--model id] [--transport p] -- <args…>")
	}
	ref, err := parseSourceRef(a, *with)
	if err != nil {
		return reportErr(err)
	}
	plan, err := activation.PlanActivation(ctx, activation.Request{
		Mode:            activation.ModeEphemeral,
		Client:          fs.Arg(0),
		Source:          &ref,
		Model:           *model,
		TransportPolicy: refToPolicy(*transportPolicy),
	}, a.Deps, activation.Options{})
	if err != nil {
		return reportErr(err)
	}
	result, err := activation.Execute(ctx, plan, a.Deps, activation.ExecuteOptions{})
	if err != nil {
		return reportErr(err)
	}
	binary := plan.Client
	env := map[string]string(nil)
	if result.Isolated != nil {
		env = result.Isolated.Environment
		defer func() { _ = result.Isolated.Cleanup() }()
	}
	if len(passThrough) == 0 && result.Isolated == nil {
		fmt.Fprintln(os.Stderr, "run: nothing to launch (pass the command after --)")
		return int(anyerr.InvalidUsage)
	}
	if len(passThrough) == 0 {
		passThrough = []string{binary}
	}
	cmd := exec.Command(binary, passThrough...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return reportErr(err)
	}
	return int(anyerr.Success)
}

func projectRoot(dir string) (string, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Clean(cwd), nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		return "", anyerr.Newf("not a directory: "+abs, "INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	return abs, nil
}

func parseSourceRef(a *appView, with string) (refs.Ref, error) {
	providerSet := map[string]bool{}
	for _, id := range a.Providers.IDs() {
		providerSet[id] = true
	}
	return refs.ParseRef(strings.TrimSpace(with), &refs.ParseContext{AccountProviders: providerSet})
}
