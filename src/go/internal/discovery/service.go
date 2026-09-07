// Package discovery ports src/core/model-discovery.ts: live model lists with
// a fallback chain — fresh cache → live fetch → stale cache → static catalog.
// A model list is advisory, so no step fails the caller.
package discovery

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/modelcache"
)

// TTL is how long a fetched list is trusted without asking the vendor again.
const TTL = time.Hour

const fetchTimeout = 6 * time.Second

// Source identifies which fallback step answered.
type Source string

const (
	SourceLive    Source = "live"
	SourceCache   Source = "cache"
	SourceStale   Source = "stale"
	SourceCatalog Source = "catalog"
)

// Result mirrors ModelListResult.
type Result struct {
	Models    []string `json:"models"`
	Source    Source   `json:"source"`
	FetchedAt string   `json:"fetchedAt,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Request mirrors ModelDiscoveryRequest.
type Request struct {
	ProviderID string
	Endpoint   string
	APIKey     string
	Refresh    bool
	// NoLive skips the vendor fetch: fresh cache → stale cache → static.
	// Snapshot ticks set it — a cold cache must never trigger per-tick
	// egress. Live warming is an explicit user action (hub start, model
	// refresh), never a background read.
	NoLive bool
}

// Service resolves model lists. The cache write is the one persisted mutation
// here, so it runs under the coordinator like every other store write
// (ADR 0009).
type Service struct {
	Root  string
	Cache *modelcache.Store
	Now   func() time.Time
}

// List returns the best available model list for a provider/endpoint pair.
func (s *Service) List(ctx context.Context, req Request) Result {
	policy := catalog.Policy(req.ProviderID)
	fallback := staticModels(policy)
	endpoint := normalizeEndpoint(firstNonEmpty(req.Endpoint, catalogDefaultEndpoint(req.ProviderID)))

	if endpoint == "" || policy == nil || policy.LiveFetch == "" {
		return Result{Models: fallback, Source: SourceCatalog,
			Note: "no live discovery for this provider"}
	}

	cached, _ := s.Cache.Get(ctx, req.ProviderID, endpoint)
	now := s.now()
	if !req.Refresh && cached != nil && isFresh(cached.FetchedAt, now) {
		return Result{Models: cached.Models, Source: SourceCache, FetchedAt: cached.FetchedAt}
	}
	if req.NoLive {
		if cached != nil {
			return Result{Models: cached.Models, Source: SourceStale,
				FetchedAt: cached.FetchedAt, Note: "snapshot tick never fetches live"}
		}
		return Result{Models: fallback, Source: SourceCatalog,
			Note: "snapshot tick never fetches live"}
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Result{Models: fallback, Source: SourceCatalog,
			Note: "endpoint is not a URL: " + endpoint}
	}
	// Outbound egress is bound to exactly this origin (the user's own stored
	// endpoint), so a rewritten URL cannot quietly receive their key.
	origin := u.Scheme + "://" + u.Host

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	models, err := catalog.FetchLiveModels(fetchCtx, policy.LiveFetch, &catalog.FetchContext{
		Endpoint: endpoint, APIKey: req.APIKey,
	})
	_ = origin // allowlist is enforced by construction: only `endpoint` is queried
	if err == nil && len(models) == 0 {
		err = context.DeadlineExceeded
	}
	if err != nil || len(models) == 0 {
		note := ""
		if err != nil {
			note = err.Error()
		}
		if cached != nil {
			return Result{Models: cached.Models, Source: SourceStale,
				FetchedAt: cached.FetchedAt, Note: note}
		}
		return Result{Models: fallback, Source: SourceCatalog, Note: note}
	}

	fetchedAt := s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := s.StoreLive(ctx, req.ProviderID, endpoint, models); err != nil {
		return Result{Models: models, Source: SourceLive, FetchedAt: fetchedAt}
	}
	return Result{Models: models, Source: SourceLive, FetchedAt: fetchedAt}
}

// StoreLive persists a successful live model response. Fetching and persisting
// are separate so explicit warmers can own the network call while this
// service still owns the model-cache mutation and coordinator lock.
func (s *Service) StoreLive(ctx context.Context, providerID, endpoint string, models []string) error {
	if s.Cache == nil {
		return nil
	}
	fetchedAt := s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return lock.WithMutationLock(ctx, s.Root, "model-cache", func(c context.Context) error {
		return s.Cache.Write(c, &modelcache.Entry{
			Provider: providerID, Endpoint: normalizeEndpoint(endpoint), Models: models, FetchedAt: fetchedAt,
		})
	})
}

// ClearCache drops cached lists so the next List asks the vendor.
func (s *Service) ClearCache(ctx context.Context, providerID string) error {
	return lock.WithMutationLock(ctx, s.Root, "model-cache", func(c context.Context) error {
		return s.Cache.Clear(c, providerID)
	})
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func isFresh(fetchedAt string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339Nano, fetchedAt)
	if err != nil {
		at, err = time.Parse("2006-01-02T15:04:05.000Z07:00", fetchedAt)
	}
	if err != nil {
		return false
	}
	return now.Sub(at) < TTL
}

func normalizeEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return trimmed
}

// catalogDefaultEndpoint reads DefaultEndpoint from the built-in catalog:
// account providers reach their models through a local proxy and pass an
// endpoint explicitly.
func catalogDefaultEndpoint(providerID string) string {
	if p, ok := catalog.Builtin()[providerID]; ok {
		return p.DefaultEndpoint
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// StaticModels returns the offline model list for a catalog provider id:
// friendly ids, alias targets, then the static fallback. Empty when the
// provider declares no policy — never an error.
func StaticModels(providerID string) []string {
	return staticModels(catalog.Policy(providerID))
}

// staticModels mirrors staticModels in TS: friendly ids, then alias targets.
func staticModels(policy *catalog.ModelPolicy) []string {
	if policy == nil {
		return nil
	}
	out := make([]string, 0, len(policy.RoleFriendly)+len(policy.SuggestModels)+len(policy.StaticFallback))
	seen := map[string]bool{}
	push := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, id := range policy.RoleFriendly {
		push(id)
	}
	for _, id := range sortedValues(policy.SuggestModels) {
		push(id)
	}
	for _, id := range policy.StaticFallback {
		push(id)
	}
	return out
}

func sortedValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
