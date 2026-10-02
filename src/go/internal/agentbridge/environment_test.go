package agentbridge

import (
	"reflect"
	"testing"
)

func TestInspectEnvironmentPrefersUppercaseAndRedactsSecrets(t *testing.T) {
	report := InspectEnvironment([]string{
		"HTTPS_PROXY=https://user:secret@example.test:8443/path?token=hidden",
		"https_proxy=https://other:wrong@example.test:9443",
		"NO_PROXY=localhost, 127.0.0.1,,example.test",
	})

	if got, want := report.Proxy.HTTPSProxy, "https://example.test:8443"; got != want {
		t.Fatalf("HTTPS_PROXY = %q, want %q", got, want)
	}
	if report.Proxy.NoProxySize != 3 || !report.Proxy.NoProxySet {
		t.Fatalf("NO_PROXY report = %+v, want three entries", report.Proxy)
	}
	wantPresent := []string{"HTTPS_PROXY", "NO_PROXY", "https_proxy"}
	if !reflect.DeepEqual(report.Proxy.Present, wantPresent) {
		t.Fatalf("present = %v, want %v", report.Proxy.Present, wantPresent)
	}
	if len(report.Proxy.Warnings) != 1 || report.Proxy.Warnings[0] !=
		"HTTPS_PROXY and https_proxy differ; using HTTPS_PROXY" {
		t.Fatalf("warnings = %v", report.Proxy.Warnings)
	}
}

func TestInspectEnvironmentReportsInvalidProxyWithoutEchoingValue(t *testing.T) {
	report := InspectEnvironment([]string{
		"HTTP_PROXY=not-a-url-with-secret=do-not-echo",
		"all_proxy=socks5://proxy.example:1080",
	})
	if report.Proxy.HTTPProxy != "<invalid>" {
		t.Fatalf("invalid proxy = %q", report.Proxy.HTTPProxy)
	}
	if report.Proxy.AllProxy != "socks5://proxy.example:1080" {
		t.Fatalf("all proxy = %q", report.Proxy.AllProxy)
	}
	for _, warning := range report.Proxy.Warnings {
		if warning == "" || warning == "not-a-url-with-secret=do-not-echo" {
			continue
		}
		if warning == "HTTP_PROXY is not a valid proxy URL" {
			return
		}
	}
	t.Fatalf("invalid proxy warning missing or leaked: %v", report.Proxy.Warnings)
}

func TestInspectEnvironmentWithoutProxyIsExplicitlyEmpty(t *testing.T) {
	report := InspectEnvironment([]string{"PATH=/bin", "HOME=/tmp"})
	if len(report.Proxy.Present) != 0 || report.Proxy.NoProxySet || len(report.Proxy.Warnings) != 0 {
		t.Fatalf("empty proxy report = %+v", report.Proxy)
	}
}
