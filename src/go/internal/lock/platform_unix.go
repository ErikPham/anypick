//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package lock

import (
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"golang.org/x/sys/unix"
)

type unixFileLock struct {
	fd int
}

func acquirePlatformFileLock(path string, timeout time.Duration) (platformFileLock, error) {
	// Detached children must never inherit lock descriptors, or they would
	// silently hold the parent's coordinator locks across exec (ADR 0009).
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, anyerr.Wrapf(err, "open lock "+path, "FS_PATH")
	}
	deadline := time.Now().Add(timeout)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &unixFileLock{fd: fd}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN && err != unix.EINTR {
			_ = unix.Close(fd)
			return nil, anyerr.Wrapf(err, "flock "+path, "STATE_CONFLICT")
		}
		if time.Now().After(deadline) {
			_ = unix.Close(fd)
			return nil, anyerr.Newf("Timed out waiting for lock "+path, "STATE_CONFLICT")
		}
		time.Sleep(pollInterval)
	}
}

func (l *unixFileLock) release() error {
	err := unix.Flock(l.fd, unix.LOCK_UN)
	cerr := unix.Close(l.fd)
	l.fd = -1
	if err != nil {
		return err
	}
	return cerr
}
