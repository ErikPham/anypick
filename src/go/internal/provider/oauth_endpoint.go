package provider

import (
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// oauthTokenEndpoint resolves an OAuth/OIDC token endpoint with an optional
// test/debug override. The override is honored ONLY for loopback URLs: the
// refresh posts the account's refresh_token and client secret, so a poisoned
// or misconfigured environment must never redirect them to an outside server.
// A non-loopback override fails closed with a clear error instead of sending
// credentials somewhere unexpected.
func oauthTokenEndpoint(envVar, def string) (string, error) {
	v := strings.TrimSpace(os.Getenv(envVar))
	if v == "" {
		return def, nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !isLoopbackHost(u.Hostname()) {
		return "", anyerr.Newf(
			envVar+" must name a loopback URL (test hook); refusing to send credentials elsewhere.",
			"INVALID_USAGE",
			anyerr.WithExitCode(anyerr.InvalidUsage))
	}
	return v, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
