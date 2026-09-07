package proxy

import (
	"errors"
	"io"
	"net/http"
)

// MaxBodyBytes caps a buffered inbound request body.
//
// Every egress path here has to read the whole body — to discover the model,
// to translate Anthropic into chat completions, or to replay it per ring
// member — so the cap is a memory bound, not a protocol limit. It is enforced
// per request and there is no concurrency semaphore yet (see
// BACKEND_ARCHITECTURE_2026-09-07.md §6).
const MaxBodyBytes = 64 << 20

// ErrBodyTooLarge reports a body past the cap.
var ErrBodyTooLarge = errors.New("request body too large")

// ReadCappedBody reads at most limit bytes and reports ErrBodyTooLarge past
// that. A nil request or nil body reads as empty.
//
// io.LimitReader alone is not enough: it truncates in silence. The eight call
// sites this replaced all did that, so an oversized upload became a
// syntactically broken JSON document — forwarded upstream verbatim on the hub
// path, where the vendor then answered 400 for a body we had mangled.
func ReadCappedBody(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}
