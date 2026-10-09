package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// closeGuard makes App.Close idempotent.
type closeGuard struct{ once sync.Once }

func (g *closeGuard) Do(fn func()) { g.once.Do(fn) }

// ensureBootstrapState creates directories later milestones rely on and
// bootstraps the global config row. Keeping the hook here preserves the
// startup ordering.
func ensureBootstrapState(ctx context.Context, a *App) error {
	for _, dir := range []string{
		filepath.Join(a.Root, "runtime", "tray"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("bootstrap %s: %w", dir, err)
		}
	}
	if a.Config != nil {
		if _, err := a.Config.Ensure(ctx); err != nil {
			return fmt.Errorf("bootstrap config: %w", err)
		}
	}
	return nil
}

// RunningTrayPid reports the PID recorded in runtime/tray/tray.pid when
// that process is still alive.
func RunningTrayPid(root string) *int {
	raw, err := os.ReadFile(filepath.Join(root, "runtime", "tray", "tray.pid"))
	if err != nil {
		return nil
	}
	pid, perr := strconv.Atoi(trimSpace(string(raw)))
	if perr != nil || pid <= 0 || !processAlive(pid) {
		return nil
	}
	return &pid
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
