package update

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubFetcher struct {
	rel         Release
	etag        string
	notModified bool
	err         error
	calls       int
}

func (s *stubFetcher) Latest(ctx context.Context, etag string) (Release, string, bool, error) {
	s.calls++
	if s.err != nil {
		return Release{}, s.etag, false, s.err
	}
	if s.notModified {
		return Release{}, etag, true, nil
	}
	return s.rel, s.etag, false, nil
}

func TestCheckUpgradeDirect(t *testing.T) {
	f := &stubFetcher{
		rel:  Release{Tag: "v1.2.0", Version: "1.2.0", Notes: "hi", Assets: []Asset{{Name: "AnyPick-macos-universal.zip", URL: "u"}}},
		etag: `"e1"`,
	}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Date(2026, 10, 11, 18, 0, 0, 0, time.UTC)}, GOOS: "darwin", GOARCH: "arm64"}
	res := c.Check(context.Background(), CheckInput{
		Current:  "1.1.0",
		Settings: DefaultSettings(),
		Channel:  ChannelDirect,
		Manual:   true,
	})
	if res.State.Kind != StateAvailableDirect {
		t.Fatalf("kind=%s err=%v", res.State.Kind, res.State.Err)
	}
	if res.Settings.ETag != `"e1"` || res.Settings.HighestSeen != "1.2.0" {
		t.Fatalf("settings %+v", res.Settings)
	}
	if res.State.Asset == nil || res.State.Asset.Name != "AnyPick-macos-universal.zip" {
		t.Fatalf("asset %+v", res.State.Asset)
	}
}

func TestCheckRefuseDowngrade(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.0.0", Version: "1.0.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: DefaultSettings(), Channel: ChannelDirect, Manual: true,
	})
	if res.State.Kind != StateUpToDate {
		t.Fatalf("kind=%s", res.State.Kind)
	}
}

func TestCheckHighestSeenBlocksRollback(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.1.0", Version: "1.1.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.HighestSeen = "1.2.0"
	res := c.Check(context.Background(), CheckInput{
		Current: "1.0.0", Settings: s, Channel: ChannelDirect, Manual: true,
	})
	if res.State.Kind != StateUpToDate {
		t.Fatalf("kind=%s want upToDate (rollback refused)", res.State.Kind)
	}
}

func TestCheckSkipVersion(t *testing.T) {
	rel := Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}
	f := &stubFetcher{rel: rel}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.SkippedVersion = "1.2.0"

	auto := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: false,
	})
	if auto.State.Kind != StateUpToDate {
		t.Fatalf("auto skip: kind=%s", auto.State.Kind)
	}
	if auto.State.SkippedHint == "" {
		t.Fatal("expected skip hint")
	}

	manual := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: true,
	})
	if manual.State.Kind != StateAvailableDirect {
		t.Fatalf("manual should still show update, got %s", manual.State.Kind)
	}
	if manual.State.SkippedHint == "" {
		t.Fatal("manual should mention skipped copy")
	}
}

func TestCheckSkipOnlyExactVersion(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.3.0", Version: "1.3.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.SkippedVersion = "1.2.0"
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: false,
	})
	if res.State.Kind != StateAvailableDirect {
		t.Fatalf("newer than skipped should show, got %s", res.State.Kind)
	}
}

func TestCheckThrottle(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}}
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c := &Checker{Fetcher: f, Clock: FixedClock{T: start.Add(1 * time.Hour)}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.LastChecked = start
	cached := &Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: false, Cached: cached,
	})
	if f.calls != 0 {
		t.Fatalf("throttled check must not hit network, calls=%d", f.calls)
	}
	if res.State.Kind != StateAvailableDirect {
		t.Fatalf("kind=%s", res.State.Kind)
	}

	// Manual bypasses throttle.
	res2 := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: true, Cached: cached,
	})
	if f.calls != 1 {
		t.Fatalf("manual should fetch, calls=%d", f.calls)
	}
	if res2.State.Kind != StateAvailableDirect {
		t.Fatalf("kind=%s", res2.State.Kind)
	}
}

func TestCheckThrottleExpires(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}}
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	c := &Checker{Fetcher: f, Clock: FixedClock{T: start.Add(25 * time.Hour)}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.LastChecked = start
	_ = c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: false,
	})
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
}

func TestCheckRateLimitedAndOffline(t *testing.T) {
	c := &Checker{Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	c.Fetcher = &stubFetcher{err: ErrRateLimited}
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: DefaultSettings(), Channel: ChannelDirect, Manual: true,
	})
	if res.State.Kind != StateRateLimited {
		t.Fatalf("kind=%s", res.State.Kind)
	}

	c.Fetcher = &stubFetcher{err: &ErrOffline{Err: errors.New("dial")}}
	res = c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: DefaultSettings(), Channel: ChannelDirect, Manual: true,
	})
	if res.State.Kind != StateOffline {
		t.Fatalf("kind=%s", res.State.Kind)
	}
}

func TestCheckPackageManager(t *testing.T) {
	f := &stubFetcher{rel: Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{
		{Name: "AnyPick_1.2.0_amd64.deb"},
	}}}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "linux", GOARCH: "amd64"}
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: DefaultSettings(), Channel: ChannelDeb, Manual: true,
	})
	if res.State.Kind != StateAvailablePackageManager {
		t.Fatalf("kind=%s", res.State.Kind)
	}
	if res.State.Command != "sudo apt install ./AnyPick_1.2.0_amd64.deb" {
		t.Fatalf("cmd=%q", res.State.Command)
	}
}

func TestCheckETag304UsesCache(t *testing.T) {
	cached := &Release{Tag: "v1.2.0", Version: "1.2.0", Assets: []Asset{{Name: "AnyPick-macos-universal.zip"}}}
	f := &stubFetcher{notModified: true, etag: `"same"`}
	c := &Checker{Fetcher: f, Clock: FixedClock{T: time.Now()}, GOOS: "darwin", GOARCH: "arm64"}
	s := DefaultSettings()
	s.ETag = `"same"`
	res := c.Check(context.Background(), CheckInput{
		Current: "1.1.0", Settings: s, Channel: ChannelDirect, Manual: true, Cached: cached,
	})
	if res.State.Kind != StateAvailableDirect {
		t.Fatalf("kind=%s", res.State.Kind)
	}
}
