package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/upstream"
)

// blockingResolver announces each entry and then waits for release, so a test
// can tell "two resolves in flight" apart from "one resolve, then the next".
type blockingResolver struct {
	entered chan string
	release chan struct{}

	mu   sync.Mutex
	seen []string
}

func (b *blockingResolver) ResolveUpstream(_ context.Context, _, account string) (*Upstream, error) {
	b.mu.Lock()
	b.seen = append(b.seen, account)
	b.mu.Unlock()
	b.entered <- account
	<-b.release
	u, _ := url.Parse("https://example.test/v1")
	return &Upstream{BaseURL: u}, nil
}

func (b *blockingResolver) accounts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

// TestPoolResolvesDifferentMembersInParallel pins a scalability property:
// resolving one member must not block another. ResolveUpstream reads the
// account store and may refresh a credential over the network, so a single
// pool-wide lock held across it turns the ring's cold start into a queue —
// the slowest member's first resolve stalls every concurrent request.
func TestPoolResolvesDifferentMembersInParallel(t *testing.T) {
	res := &blockingResolver{entered: make(chan string, 4), release: make(chan struct{})}
	s := &PoolServer{ProviderID: "p", Members: []string{"a", "b"}, Resolver: res}

	done := make(chan struct{}, 2)
	for _, acct := range []string{"a", "b"} {
		go func(acct string) {
			_ = s.upstreamFor(httptest.NewRequest(http.MethodPost, "/v1/messages", nil), acct)
			done <- struct{}{}
		}(acct)
	}

	first := <-res.entered
	select {
	case second := <-res.entered:
		if first == second {
			t.Fatalf("the same member %q resolved twice instead of two members resolving", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("only %q reached the resolver within 2s: members are serialized behind one lock", first)
	}
	close(res.release)
	<-done
	<-done

	if got := len(res.accounts()); got != 2 {
		t.Fatalf("resolver saw %d accounts, want 2: %v", got, res.accounts())
	}
}

// countingResolver records how many members the ring asked for.
type countingResolver struct {
	inner UpstreamResolver
	calls atomic.Int32
}

func (c *countingResolver) ResolveUpstream(ctx context.Context, p, a string) (*Upstream, error) {
	c.calls.Add(1)
	return c.inner.ResolveUpstream(ctx, p, a)
}

// TestPoolRingStopsWhenTheClientHasGone pins the early exit: once the caller
// has hung up, resolving another member only to fail on a dead context is
// wasted store reads and credential refreshes. The resolver call count is the
// discriminator — the upstream never sees the request either way, because the
// transport rejects a cancelled context before dialling.
func TestPoolRingStopsWhenTheClientHasGone(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	res := &countingResolver{inner: mapResolver{
		"a": {BaseURL: upA.url},
		"b": {BaseURL: upB.url},
	}}
	server := &PoolServer{
		ProviderID: "deepseek", InstanceID: mustNewInstanceID(t), Token: mustNewToken(t),
		Members:  []string{"a", "b"},
		Resolver: res,
		Guard: upstream.NewGuard(upstream.GuardOptions{
			Enabled:      true,
			CooldownMs:   60_000,
			AccountNames: []string{"a", "b"},
			StatePath:    filepath.Join(t.TempDir(), "guard.json"),
			ProviderID:   "deepseek",
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req = req.WithContext(ctx)

	server.ServeAuthorized(httptest.NewRecorder(), req)

	if got := res.calls.Load(); got != 0 {
		t.Fatalf("resolver called %d times for a cancelled request, want 0", got)
	}
}

// TestPoolCachesResolvedMember collapses repeat lookups onto one resolve —
// the per-account lock must also prevent a burst of requests for the same
// member from stampeding the store.
func TestPoolCachesResolvedMember(t *testing.T) {
	res := &blockingResolver{entered: make(chan string, 8), release: make(chan struct{})}
	close(res.release) // never block
	s := &PoolServer{ProviderID: "p", Members: []string{"a"}, Resolver: res}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.upstreamFor(nil, "a")
		}()
	}
	wg.Wait()

	if got := len(res.accounts()); got != 1 {
		t.Fatalf("8 concurrent lookups for one member resolved %d times, want 1", got)
	}
}
