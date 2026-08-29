package app

// processAlive reports liveness only — never ownership. Detached children are
// identified by their structured ownership record, not by a pid being alive
// (ADR 0003).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return platformProcessAlive(pid)
}
