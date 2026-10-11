package update

import (
	"fmt"
	"net/url"
	"strings"
)

// Allowed download hosts for release assets. Exact host match, https only.
// (Steering for slice 1: github.com + objects.githubusercontent.com.)
var allowedDownloadHosts = map[string]struct{}{
	"github.com":                    {},
	"objects.githubusercontent.com": {},
}

// ValidateDownloadURL reports whether raw is an https URL on an allowlisted
// exact host. Rejects other schemes, other hosts, and empty values.
func ValidateDownloadURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("empty download URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse download URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("download URL scheme %q not allowed (https only)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := allowedDownloadHosts[host]; !ok {
		return fmt.Errorf("download host %q not allowed", host)
	}
	return nil
}

// ValidateAssetName rejects asset names that contain path separators or "..".
// No basename trimming — a renamed or path-like name fails closed.
func ValidateAssetName(name string) error {
	if name == "" {
		return fmt.Errorf("empty asset name")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("asset name %q contains ..", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("asset name %q contains path separator", name)
	}
	return nil
}
