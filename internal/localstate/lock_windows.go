package localstate

import (
	"errors"
	"syscall"
)

// TryLock shares the Unix contract using an exclusive Windows file handle.
func TryLock(path string) (release func(), acquired bool, err error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		const sharingViolation syscall.Errno = 32
		if errors.Is(err, sharingViolation) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { syscall.CloseHandle(handle) }, true, nil
}
