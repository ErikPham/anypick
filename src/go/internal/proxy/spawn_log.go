package proxy

import (
	"os"
	"path/filepath"
)

// openProxyLogFile opens the child log for append, creating the runtime dir
// first. Without the MkdirAll, accounts whose runtime dir was never created
// spawn a child whose stdout/stderr go nowhere: OpenFile fails, the spawner
// silently keeps nil outputs, the proxy runs blind and Live Logs stays empty
// forever.
func openProxyLogFile(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
