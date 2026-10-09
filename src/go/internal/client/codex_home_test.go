package client

import (
	"os"
	"testing"
)

// codexHomePath follows CODEX_HOME ahead of the home argument. A developer
// shell that has the variable set would otherwise make Apply and Inspect
// write the real ~/.codex/config.toml. Tests that need a specific value set
// it themselves; t.Setenv restores this empty value afterwards.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}
