package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/store"
)

func TestPortBindableRefusesNonLoopback(t *testing.T) {
	if portBindable("0.0.0.0", 9) {
		t.Fatal("portBindable opened a non-loopback host")
	}
}

func TestStartProxyRefusesNonLoopbackHost(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "public", true)
	if err := ts.Service.Store.SetProxyConfigContext(ctx, "codex", "public",
		store.ProxyConfig{Enabled: true, Host: "0.0.0.0", Port: 9}); err != nil {
		t.Fatal(err)
	}
	_, err := ts.Service.StartProxy(ctx, "codex", "public")
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROXY_UNSAFE_HOST" {
		t.Fatalf("start = %v, want PROXY_UNSAFE_HOST", err)
	}
	cfg, cerr := ts.Service.Store.GetProxyConfig(ctx, "codex", "public")
	if cerr != nil || cfg.Host != "0.0.0.0" || cfg.Port != 9 {
		t.Fatalf("config after refusal = %+v %v", cfg, cerr)
	}
	if state, _ := ts.Service.Store.ReadProxyState(ctx, "codex", "public"); state != nil {
		t.Fatalf("unsafe host left runtime state: %+v", state)
	}
}
