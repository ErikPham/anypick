package pluginhost

import "io"

// SetTestWriter replaces the outbound frame writer. Tests use this to inject
// zero-byte, partial, and full-frame-then-error writes without racing the
// handshake pipe.
func SetTestWriter(h *Host, w io.Writer) {
	h.writeMu.Lock()
	h.testWriter = w
	h.writeMu.Unlock()
}
