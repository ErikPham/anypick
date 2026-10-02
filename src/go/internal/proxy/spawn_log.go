package proxy

import (
	"os"
	"path/filepath"
)

// openProxyLogFile opens the child log for append, creating the runtime dir
// first. Without the MkdirAll, accounts whose runtime dir was never created
// would spawn a child whose log file could not be opened.
func openProxyLogFile(logPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// openProxyChildOutput makes detached proxy output safe for every caller.
// A desktop service owns stdout as a framed protocol pipe, so the child sink is
// explicit: provider output can never be confused with a service frame if a
// caller or process-launch default changes later.
func openProxyChildOutput(logPath string) (*os.File, error) {
	if logPath != "" {
		if file, err := openProxyLogFile(logPath); err == nil {
			return file, nil
		}
	}
	return os.OpenFile(os.DevNull, os.O_RDWR, 0)
}
