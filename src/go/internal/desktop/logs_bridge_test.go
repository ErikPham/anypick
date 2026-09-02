package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
)

// stubForwardingLifecycle stands in for a plugin provider whose logs live
// somewhere only readProxyLogs can reach.
type stubForwardingLifecycle struct {
	logText string
}

func (s *stubForwardingLifecycle) ForwardsLifecycle(providerID string) bool {
	return providerID == "pluginprov"
}

func (s *stubForwardingLifecycle) StartProxy(
	context.Context, proxy.PluginRequest,
) (*proxy.PluginStart, error) {
	return nil, errors.New("not used in this test")
}

func (s *stubForwardingLifecycle) StopProxy(context.Context, proxy.PluginRequest) error {
	return proxy.ErrPluginMethodAbsent
}

func (s *stubForwardingLifecycle) ProxyStatus(
	context.Context, proxy.PluginRequest,
) (*proxy.Status, error) {
	return nil, proxy.ErrPluginMethodAbsent
}

func (s *stubForwardingLifecycle) ReadProxyLogs(
	_ context.Context, _ proxy.PluginRequest, _ int,
) (string, error) {
	return s.logText, nil
}

func (s *stubForwardingLifecycle) Terminate(context.Context, proxy.PluginRuntimeIdentity) error {
	return nil
}

// TestHandleLogsBridgesProviderOwnedLogSource proves the native-app log frame
// flows through Proxy.ProxyLogs: a forwarding provider answers from its own
// source even with no engine proxy.log file, secret-screened before framing,
// and history survives a stopped proxy.
func TestHandleLogsBridgesProviderOwnedLogSource(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	const engineToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	stub := &stubForwardingLifecycle{}
	env.Deps.Proxy.PluginLifecycle = stub

	// Seed a saved account for the forwarding provider plus runtime state so
	// the Service knows the engine token to screen for.
	staging, _, err := env.Deps.Store.PrepareSnapshot("pluginprov", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging+"/auth.json", []byte(`{"k":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := env.Deps.Store.WriteMetaContext(context.Background(), store.Meta{
		Name: "work", Provider: "pluginprov", CreatedAt: now, UpdatedAt: now,
	}, staging); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(context.Background(), "pluginprov", "work",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = env.Deps.Store.DiscardPreparedSnapshot(staging)
	if err := env.Deps.Store.WriteProxyStateContext(context.Background(), "pluginprov", "work", store.ProxyRuntimeState{
		AccountName: "work", Endpoint: "http://127.0.0.1:43199",
		PID: 1, InstanceID: "inst-dead", StartedAt: now, Token: engineToken,
	}); err != nil {
		t.Fatal(err)
	}

	request := func() ProxyLogsResult {
		t.Helper()
		out.Reset()
		raw, mErr := json.Marshal(logsPayload{Version: 1, RequestID: "r-bridge",
			ProviderID: "pluginprov", Name: "work", Lines: 50})
		if mErr != nil {
			t.Fatal(mErr)
		}
		if hErr := srv.handleLogs(context.Background(), raw); hErr != nil {
			t.Fatal(hErr)
		}
		decoded := decodeFramePayload(t, firstFrameLine(out.String()))
		var res ProxyLogsResult
		if err := json.Unmarshal(decoded, &res); err != nil {
			t.Fatalf("decode %s: %v", decoded, err)
		}
		return res
	}

	// Provider-owned source, no engine file anywhere: the bridge must surface
	// it AND screen the engine token out of the frame.
	stub.logText = "provider line\nauthorization: Bearer " + engineToken + "\nend " + engineToken
	res := request()
	if res.State != "ready" || !strings.Contains(res.Text, "provider line") {
		t.Fatalf("bridge result = %+v", res)
	}
	if strings.Contains(res.Text, engineToken) {
		t.Fatal("desktop log frame leaked the engine token")
	}
	if !strings.Contains(res.Text, "[redacted]") {
		t.Fatalf("credential pattern not screened: %q", res.Text)
	}

	// History survives a stopped proxy: dead instance echo ⇒ not running, yet
	// the provider-sourced text still frames as ready.
	res = request()
	if res.State != "ready" || res.Text == "" {
		t.Fatalf("stopped-history result = %+v", res)
	}
}
