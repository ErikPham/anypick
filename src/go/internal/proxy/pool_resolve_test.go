package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
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
