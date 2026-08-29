// Package paths ports src/core/paths.ts: every filesystem path under the data
// root. The data directory is ~/.anypick (ANYPICK_HOME overrides) — invariant #7.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// Root resolves the AnyPick data root: explicit override > ANYPICK_HOME >
// ~/.anypick.
func Root(override string) string {
	if override != "" {
		return expandHome(override)
	}
	if env := os.Getenv("ANYPICK_HOME"); env != "" {
		return expandHome(env)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".anypick")
}

func ProviderDir(root, providerID string) string {
	return filepath.Join(root, "providers", providerID)
}

func AccountsDir(root, providerID string) string {
	return filepath.Join(ProviderDir(root, providerID), "accounts")
}

func AccountDir(root, providerID, accountName string) string {
	return filepath.Join(AccountsDir(root, providerID), accountName)
}

func AccountSnapshotDir(root, providerID, accountName string) string {
	return filepath.Join(AccountDir(root, providerID, accountName), "snapshot")
}

// SnapshotStagingDir is a private per-operation work area for backup/import.
// It must never share the materialized snapshot cache: readers may rebuild
// that cache from SQLite at any time (ADR 0004).
func SnapshotStagingDir(root string) string {
	return filepath.Join(root, "staging", "snapshots")
}

func AccountProxyRuntimeDir(root, providerID, accountName string) string {
	return filepath.Join(AccountDir(root, providerID, accountName), "runtime")
}

func ProxyPidPath(root, providerID, accountName string) string {
	return filepath.Join(AccountProxyRuntimeDir(root, providerID, accountName), "proxy.pid")
}

func ProxyLogPath(root, providerID, accountName string) string {
	return filepath.Join(AccountProxyRuntimeDir(root, providerID, accountName), "proxy.log")
}

// PoolRuntimeDir mirrors poolRuntimeDir (src/core/pool-store.ts): shared
// runtime area for a provider's multi-account pool (pid/log/quota-guard).
func PoolRuntimeDir(root, providerID string) string {
	return filepath.Join(ProviderDir(root, providerID), "pool-runtime")
}

// PoolQuotaGuardStatePath is the secret-free Quota Guard audit file TS reads
// back for `quotaGuardEvents` — the layout is cross-runtime.
func PoolQuotaGuardStatePath(root, providerID string) string {
	return filepath.Join(PoolRuntimeDir(root, providerID), "quota-guard.json")
}

func PoolLogPath(root, providerID string) string {
	return filepath.Join(PoolRuntimeDir(root, providerID), "pool.log")
}

func DBPath(root string) string { return filepath.Join(root, "anypick.db") }

func LocksDir(root string) string { return filepath.Join(root, "locks") }
