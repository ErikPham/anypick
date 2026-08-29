// Package lock ports src/utils/lock.ts (advisory flock with in-process
// serialization) and src/core/mutation-lock.ts (scoped, sorted, re-entrant
// coordinator locks — ADR 0009).
package lock

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/paths"
)

const (
	defaultTimeout = 5 * time.Second
	pollInterval   = 25 * time.Millisecond
)

// Per-path in-process serialization so concurrent goroutines queue like the
// TS promise chain (utils/lock.ts localTails); flock then arbitrates across
// processes. Different paths never block each other.
var pathMutexes sync.Map

func pathMutex(path string) *sync.Mutex {
	m, _ := pathMutexes.LoadOrStore(path, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// FileLock is an advisory exclusive lock on one file. The lock file persists
// after release (no unlink ⇒ no ABA split), matching utils/lock.ts.
type FileLock struct {
	path     string
	platform platformFileLock
	mu       *sync.Mutex
	released bool
}

type platformFileLock interface {
	release() error
}

// AcquireFileLock blocks up to timeout acquiring LOCK_EX|LOCK_NB.
// A timeout of 0 tries once without waiting (fail immediately); negative
// means the default (5s). WithMutationLocks translates 0 → default so
// existing callers keep the 5s default — only direct AcquireFileLock callers
// can opt into try-lock with 0.
func AcquireFileLock(path string, timeout time.Duration) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, anyerr.Wrapf(err, "create lock dir", "FS_PATH")
	}
	if timeout < 0 {
		timeout = defaultTimeout
	}
	mu := pathMutex(path)
	mu.Lock()

	platform, err := acquirePlatformFileLock(path, timeout)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return &FileLock{path: path, platform: platform, mu: mu}, nil
}

func (l *FileLock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true // idempotent: double release must not unlock the mutex twice
	err := l.platform.release()
	l.mu.Unlock()
	return err
}

// MutationLockPath ports mutationLockPath: scope sanitization is lossy, which
// is exactly why held-lock identity is the resolved path.
func MutationLockPath(root, scope string) string {
	var b strings.Builder
	for _, r := range scope {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '@', r == '/', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	safe := strings.ReplaceAll(b.String(), "/", "__")
	return filepath.Join(paths.LocksDir(paths.Root(root)), safe+".lock")
}

type heldKey struct{}

// HeldLocks returns the resolved lock paths held by ctx.
func HeldLocks(ctx context.Context) map[string]bool {
	if held, ok := ctx.Value(heldKey{}).(map[string]bool); ok {
		return held
	}
	return map[string]bool{}
}

// Holds reports whether ctx already holds scope.
func Holds(ctx context.Context, root, scope string) bool {
	return HeldLocks(ctx)[MutationLockPath(root, scope)]
}

// WithMutationLock runs fn under one scoped exclusive lock.
func WithMutationLock(ctx context.Context, root, scope string, fn func(ctx context.Context) error) error {
	return WithMutationLocks(ctx, root, []string{scope}, fn, 0)
}

// WithMutationLocks ports withMutationLocks: scopes are de-duplicated and
// acquired in sorted order (deadlock freedom across overlapping mutations);
// scopes already held by this context are skipped instead of re-acquired.
// Re-entrancy replaces the TS AsyncLocalStorage with explicit context
// propagation — nested service calls receive the enriched ctx.
// A timeout of 0 means the default (5s); pass -1 or AcquireFileLock(…,0)
// directly for try-lock.
func WithMutationLocks(ctx context.Context, root string, scopes []string, fn func(context.Context) error, timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultTimeout
	}
	held := HeldLocks(ctx)

	unique := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		unique[s] = true
	}
	ordered := make([]string, 0, len(unique))
	for s := range unique {
		ordered = append(ordered, s)
	}
	sort.Strings(ordered)

	type pendingLock struct{ scope, path string }
	var pending []pendingLock
	nextHeld := make(map[string]bool, len(held)+len(ordered))
	for p := range held {
		nextHeld[p] = true
	}
	for _, scope := range ordered {
		path := MutationLockPath(root, scope)
		if !held[path] && !nextHeld[path] {
			pending = append(pending, pendingLock{scope, path})
			nextHeld[path] = true // dedupe scopes that sanitize to one file
		}
	}

	if len(pending) == 0 {
		// Every scope is held: publish the set so nested calls re-enter.
		return fn(context.WithValue(ctx, heldKey{}, nextHeld))
	}

	var releaseAll []*FileLock
	defer func() {
		for i := len(releaseAll) - 1; i >= 0; i-- {
			_ = releaseAll[i].Release()
		}
	}()
	for _, pl := range pending {
		fl, err := AcquireFileLock(pl.path, timeout)
		if err != nil {
			return err
		}
		releaseAll = append(releaseAll, fl)
	}
	return fn(context.WithValue(ctx, heldKey{}, nextHeld))
}
