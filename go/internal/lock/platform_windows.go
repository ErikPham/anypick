//go:build windows

package lock

import (
	"errors"
	"os"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"golang.org/x/sys/windows"
)

type windowsFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquirePlatformFileLock(path string, timeout time.Duration) (platformFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, anyerr.Wrapf(err, "open lock "+path, "FS_PATH")
	}
	lock := &windowsFileLock{file: file}
	deadline := time.Now().Add(timeout)
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&lock.overlapped,
		)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) &&
			!errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			_ = file.Close()
			return nil, anyerr.Wrapf(err, "lock "+path, "STATE_CONFLICT")
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, anyerr.Newf("Timed out waiting for lock "+path, "STATE_CONFLICT")
		}
		time.Sleep(pollInterval)
	}
}

func (l *windowsFileLock) release() error {
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	cerr := l.file.Close()
	if err != nil {
		return err
	}
	return cerr
}
