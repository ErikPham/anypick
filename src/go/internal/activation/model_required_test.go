package activation

import (
	"errors"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
)

// A missing default model must name the exact next command: an unsupported
// catalog can never be refreshed, while a failed fetch means a stale login.
func TestModelRequiredErrorTailorsDiscoveryAdvice(t *testing.T) {
	account := &ResolvedSource{Ref: refs.AccountRef("zai", "work"), Display: "zai/work"}

	asAnyPick := func(t *testing.T, err error) *anyerr.Error {
		t.Helper()
		var ae *anyerr.Error
		if !errors.As(err, &ae) {
			t.Fatalf("error type = %T, want *anyerr.Error", err)
		}
		if ae.Code != "MODEL_REQUIRED" {
			t.Fatalf("code = %q, want MODEL_REQUIRED", ae.Code)
		}
		return ae
	}

	t.Run("unsupported catalog never suggests refresh", func(t *testing.T) {
		ae := asAnyPick(t, modelRequiredError("zcode", account, proxy.ErrModelDiscoveryUnsupported))
		if strings.Contains(ae.Error(), "Refresh") {
			t.Fatalf("unsupported catalog must not suggest refresh: %q", ae.Error())
		}
		if !strings.Contains(ae.Error(), "anypick models zcode --with zai/work") {
			t.Fatalf("missing list command: %q", ae.Error())
		}
		if !strings.Contains(ae.Error(), "--model") {
			t.Fatalf("missing --model hint: %q", ae.Error())
		}
	})

	t.Run("failed fetch suggests re-saving the account", func(t *testing.T) {
		ae := asAnyPick(t, modelRequiredError("zcode", account, errors.New("Z.ai user information is invalid")))
		joined := ae.Error() + "\x00" + strings.Join(ae.Suggestions, "\x00")
		if !strings.Contains(joined, "anypick add account zai --name work --force") {
			t.Fatalf("missing re-save command: %q suggestions=%q", ae.Error(), ae.Suggestions)
		}
		if !strings.Contains(joined, "anypick models zcode --with zai/work") {
			t.Fatalf("missing list command: %q suggestions=%q", ae.Error(), ae.Suggestions)
		}
	})

	t.Run("no failure keeps the generic hint", func(t *testing.T) {
		ae := asAnyPick(t, modelRequiredError("zcode", account, nil))
		if !strings.Contains(ae.Error(), "needs a default model before this connection can be applied") {
			t.Fatalf("generic message changed: %q", ae.Error())
		}
	})
}
