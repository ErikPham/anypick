package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/ownership"
)

// stopDesktopOwnedServices mirrors the native app lifecycle contract in ADR
// 0019: explicit Quit stops the Hub first, then every pool and account proxy.
// Each concrete stop still performs its own instance/health verification, so
// a shutdown sweep never weakens ADR 0003 ownership checks.
func stopDesktopOwnedServices(ctx context.Context, a *app.App) error {
	var errs []error
	errs = append(errs, stopAllHubChildren(ctx, a))
	for _, providerID := range a.Providers.IDs() {
		// Pool runtime state is stored against its primary account for TS schema
		// compatibility. Stop it before the per-account sweep so that state is
		// not mistaken for a single-account child and its NULL-account lease is
		// released by the correct lifecycle path.
		if err := a.Proxy.StopPoolProxy(ctx, providerID); err != nil {
			errs = append(errs, fmt.Errorf("stop pool %s: %w", providerID, err))
		}
		accounts, err := a.Store.ListAccounts(ctx, providerID)
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s proxies: %w", providerID, err))
		} else {
			// Do not key shutdown off Enabled. A crash or settings change may
			// leave a verified child running after its persisted toggle changed.
			for _, account := range accounts {
				if err := a.Proxy.Stop(ctx, providerID, account.Meta.Name); err != nil {
					errs = append(errs, fmt.Errorf(
						"stop proxy %s/%s: %w", providerID, account.Meta.Name, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func stopAllHubChildren(ctx context.Context, a *app.App) error {
	root := filepath.Join(a.Root, "runtime", "hub")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list Hub runtime: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		pidFile := hubPIDPath(a.Root, name)
		record := ownership.Load(pidFile)
		if record == nil {
			continue
		}
		cfg, cfgErr := a.Hub.GetConfigContext(ctx, name)
		if cfgErr != nil {
			errs = append(errs, fmt.Errorf("read Hub %s: %w", name, cfgErr))
			continue
		}
		stopped := ownership.Stop(pidFile, ownership.StopOptions{
			Timeout: stopWait,
			Health: func(candidate ownership.Record) bool {
				if cfg == nil || cfg.Port == 0 {
					return false
				}
				return hubHealthEcho(
					fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port),
					candidate.InstanceID,
				)
			},
		})
		if !stopped {
			errs = append(errs, fmt.Errorf(
				"could not verify ownership of Hub %q; it was not signalled", name))
		}
	}
	return errors.Join(errs...)
}
