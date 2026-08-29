//go:build windows

package proxy

import "testing"

func TestWindowsProcessHelpersRejectInvalidPID(t *testing.T) {
	if processAlive(0) {
		t.Fatal("pid zero must never be considered alive")
	}
	if err := signalPID(0); err == nil {
		t.Fatal("pid zero must be rejected")
	}
}
