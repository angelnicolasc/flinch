//go:build !windows

package localstate

import (
	"errors"
	"os"
	"syscall"
)

// TryLock takes a crash-released, nonblocking lock on a stable file.
// The caller owns the parent directory and must keep the lock file in place
// while another process could open it. Busy locks return (nil, false, nil).
func TryLock(path string) (release func(), acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}
