package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadCappedBodyRejectsOversize pins the reason the eight inbound reads
// share one helper. io.LimitReader alone truncates in silence: an oversized
// upload became a syntactically broken JSON document, forwarded upstream
// verbatim on the hub path, where the vendor then answered 400 for a body we
// had mangled.
func TestReadCappedBodyRejectsOversize(t *testing.T) {
	oversize := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 9)))
	if _, err := ReadCappedBody(oversize, 8); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("oversize body: err = %v, want ErrBodyTooLarge", err)
	}

	exact := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 8)))
	got, err := ReadCappedBody(exact, 8)
	if err != nil {
		t.Fatalf("body exactly at the cap was rejected: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("body at the cap: read %d bytes, want 8", len(got))
	}
}

// TestReadCappedBodyNilIsEmpty covers the GET/no-body paths, which reach the
// same helper without a body to close.
func TestReadCappedBodyNilIsEmpty(t *testing.T) {
	if got, err := ReadCappedBody(nil, 8); err != nil || got != nil {
		t.Fatalf("nil request: got %v, %v; want nil, nil", got, err)
	}
	if got, err := ReadCappedBody(&http.Request{}, 8); err != nil || got != nil {
		t.Fatalf("nil body: got %v, %v; want nil, nil", got, err)
	}
}
