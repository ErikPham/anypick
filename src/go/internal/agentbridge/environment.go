package agentbridge

import (
	"net/url"
	"sort"
	"strings"
)

// EnvironmentReport is the redacted proxy portion of worker diagnostics.
// Values are safe to show in JSON: proxy credentials, paths, queries and
// fragments are never returned.
type EnvironmentReport struct {
	Proxy ProxyEnvironment `json:"proxy"`
}

// ProxyEnvironment describes the environment a spawned worker would inherit.
// Present contains variable names only; the four URL fields are redacted.
type ProxyEnvironment struct {
	Present     []string `json:"present,omitempty"`
	HTTPProxy   string   `json:"httpProxy,omitempty"`
	HTTPSProxy  string   `json:"httpsProxy,omitempty"`
	AllProxy    string   `json:"allProxy,omitempty"`
	NoProxySet  bool     `json:"noProxySet"`
	NoProxySize int      `json:"noProxyEntries,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// InspectEnvironment normalizes the proxy variables using the same
// upper-case-first convention as Go's HTTP proxy resolver. It also detects
// ambiguous mixed-case configuration, which is a common reason a worker and
// the parent process take different network paths.
func InspectEnvironment(environ []string) EnvironmentReport {
	entries := parseEnvironment(environ)
	report := EnvironmentReport{}
	proxy := &report.Proxy

	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if entries[name] != nil {
			proxy.Present = append(proxy.Present, name)
		}
		lower := strings.ToLower(name)
		if entries[lower] != nil && lower != name {
			proxy.Present = append(proxy.Present, lower)
		}
	}
	sort.Strings(proxy.Present)

	proxy.HTTPProxy = selectedProxy(entries, "HTTP_PROXY", proxy)
	proxy.HTTPSProxy = selectedProxy(entries, "HTTPS_PROXY", proxy)
	proxy.AllProxy = selectedProxy(entries, "ALL_PROXY", proxy)
	noProxy := selectedValue(entries, "NO_PROXY", proxy)
	proxy.NoProxySet = noProxy != ""
	if noProxy != "" {
		proxy.NoProxySize = len(nonEmptyCSV(noProxy))
	}
	return report
}

func parseEnvironment(environ []string) map[string][]string {
	out := make(map[string][]string)
	for _, raw := range environ {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = append(out[key], value)
	}
	return out
}

func selectedProxy(entries map[string][]string, name string, report *ProxyEnvironment) string {
	value := selectedValue(entries, name, report)
	if value == "" {
		return ""
	}
	redacted, ok := redactProxyURL(value)
	if !ok {
		report.Warnings = append(report.Warnings, name+" is not a valid proxy URL")
		return "<invalid>"
	}
	return redacted
}

func selectedValue(entries map[string][]string, name string, report *ProxyEnvironment) string {
	upper := entries[name]
	lowerName := strings.ToLower(name)
	lower := entries[lowerName]
	upperValue := lastValue(upper)
	lowerValue := lastValue(lower)
	if upperValue != "" && lowerValue != "" && upperValue != lowerValue {
		report.Warnings = append(report.Warnings,
			name+" and "+lowerName+" differ; using "+name)
	}
	if upperValue != "" {
		return upperValue
	}
	return lowerValue
}

func lastValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[len(values)-1])
}

func redactProxyURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	// Proxies should not carry a path, query, or fragment. Removing all three
	// prevents a token accidentally embedded in a non-standard proxy URL from
	// crossing the diagnostics boundary.
	u.User = nil
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

func nonEmptyCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}
