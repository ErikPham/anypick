package update

import (
	"context"
	"errors"
	"time"
)

// CheckThrottle is how long AutoCheck waits between network fetches.
const CheckThrottle = 24 * time.Hour

// Checker runs an update check against a Fetcher.
type Checker struct {
	Fetcher Fetcher
	Clock   Clock
	Env     Env
	GOOS    string
	GOARCH  string
}

// CheckInput is everything Check needs besides the Checker deps.
type CheckInput struct {
	Current  string // running version (buildinfo.Version or hook)
	Settings UpdateSettings
	Channel  InstallChannel
	Manual   bool     // true → bypass throttle and SkippedVersion hide
	Cached   *Release // previous release kept across 304
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	State    UpdateState
	Settings UpdateSettings // updated LastChecked / ETag / HighestSeen
	Release  *Release
}

// Check fetches (unless throttled), compares versions, and returns a state.
// Network/rate-limit failures never panic; they become offline/rateLimited.
func (c *Checker) Check(ctx context.Context, in CheckInput) CheckResult {
	clock := HookClock(c.Env, c.Clock)
	if clock == nil {
		clock = RealClock{}
	}
	now := clock.Now()
	settings := in.Settings.Normalize()
	current := in.Current
	if hook := HookCurrentVersion(c.Env); hook != "" {
		current = hook
	}

	st := UpdateState{
		Kind:        StateChecking,
		Current:     current,
		Channel:     in.Channel,
		LastChecked: settings.LastChecked,
	}

	// Throttle automatic checks.
	if !in.Manual && settings.AutoCheck && !settings.LastChecked.IsZero() {
		if now.Sub(settings.LastChecked) < CheckThrottle {
			// Return prior conclusion without hitting the network.
			return c.concludeFromCached(in, settings, current, now)
		}
	}
	if !settings.AutoCheck && !in.Manual {
		st.Kind = StateIdle
		return CheckResult{State: st, Settings: settings, Release: in.Cached}
	}

	if c.Fetcher == nil {
		st.Kind = StateOffline
		st.Err = errors.New("no fetcher")
		return CheckResult{State: st, Settings: settings}
	}

	rel, newETag, notModified, err := c.Fetcher.Latest(ctx, settings.ETag)
	settings.LastChecked = now
	if newETag != "" {
		settings.ETag = newETag
	}

	if err != nil {
		st.LastChecked = now
		switch {
		case errors.Is(err, ErrRateLimited):
			st.Kind = StateRateLimited
			st.Err = err
		case isOffline(err):
			st.Kind = StateOffline
			st.Err = err
		default:
			st.Kind = StateGitHubDown
			st.Err = err
		}
		return CheckResult{State: st, Settings: settings, Release: in.Cached}
	}

	var release *Release
	if notModified {
		release = in.Cached
		if release == nil {
			st.Kind = StateUpToDate
			st.LastChecked = now
			return CheckResult{State: st, Settings: settings}
		}
	} else {
		release = &rel
	}

	return c.evaluate(settings, current, in.Channel, in.Manual, release, now)
}

func (c *Checker) concludeFromCached(in CheckInput, settings UpdateSettings, current string, now time.Time) CheckResult {
	if in.Cached == nil {
		st := UpdateState{Kind: StateIdle, Current: current, Channel: in.Channel, LastChecked: settings.LastChecked}
		return CheckResult{State: st, Settings: settings}
	}
	return c.evaluate(settings, current, in.Channel, false, in.Cached, now)
}

func (c *Checker) evaluate(settings UpdateSettings, current string, ch InstallChannel, manual bool, release *Release, now time.Time) CheckResult {
	st := UpdateState{
		Current:     current,
		Channel:     ch,
		LastChecked: now,
		Notes:       release.Notes,
		Latest:      release.Version,
	}
	settings.HighestSeen = MaxVersion(settings.HighestSeen, release.Version)

	newer, err := IsNewer(release.Version, current)
	if err != nil {
		st.Kind = StateGitHubDown
		st.Err = err
		return CheckResult{State: st, Settings: settings, Release: release}
	}
	if !newer {
		// Refuse downgrades / equal — including when HighestSeen is ahead of feed.
		st.Kind = StateUpToDate
		st.Latest = current
		return CheckResult{State: st, Settings: settings, Release: release}
	}

	// HighestSeen resistance: if we previously saw something newer than this
	// feed's latest, do not offer a rollback as an update.
	if settings.HighestSeen != "" {
		if ahead, err := IsNewer(settings.HighestSeen, release.Version); err == nil && ahead {
			st.Kind = StateUpToDate
			st.Latest = settings.HighestSeen
			return CheckResult{State: st, Settings: settings, Release: release}
		}
	}

	// Skip: hide for background/auto; manual check still shows it.
	if !manual && settings.SkippedVersion != "" {
		skipped, err := ParseVersion(settings.SkippedVersion)
		latest, err2 := ParseVersion(release.Version)
		if err == nil && err2 == nil && Compare(skipped, latest) == 0 {
			st.Kind = StateUpToDate
			st.SkippedHint = SkipVersionCopy(release.Version)
			return CheckResult{State: st, Settings: settings, Release: release}
		}
	}
	if manual && settings.SkippedVersion != "" {
		skipped, err := ParseVersion(settings.SkippedVersion)
		latest, err2 := ParseVersion(release.Version)
		if err == nil && err2 == nil && Compare(skipped, latest) == 0 {
			st.SkippedHint = SkipVersionCopy(release.Version)
		}
	}

	asset, assetErr := PickAsset(*release, c.GOOS, c.GOARCH, ch)
	if IsPackageManager(ch) || IsCLIOnly(ch) {
		st.Kind = StateAvailablePackageManager
		st.Command = PackageManagerCommand(ch, release.Version)
		if assetErr == nil {
			st.Asset = &asset
		}
		return CheckResult{State: st, Settings: settings, Release: release}
	}
	if assetErr != nil {
		// arm64 desktop / unknown: manual download fallback.
		st.Kind = StateAvailablePackageManager
		st.Command = ManualDownloadURL(*release, Asset{})
		return CheckResult{State: st, Settings: settings, Release: release}
	}
	st.Kind = StateAvailableDirect
	st.Asset = &asset
	return CheckResult{State: st, Settings: settings, Release: release}
}

func isOffline(err error) bool {
	var off *ErrOffline
	return errors.As(err, &off)
}
