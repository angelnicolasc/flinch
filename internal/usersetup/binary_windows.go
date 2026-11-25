package usersetup

import (
	"errors"
	"os"
	"syscall"
)

func registeredBinary(host, path string) (string, error) {
	if host == "claude" {
		return path, nil // The Claude hook supplies the executable and argv separately.
	}
	return windowsShellPath(path, shortBinaryPath)
}

func shortBinaryPath(path string) (string, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, syscall.MAX_PATH)
	for {
		n, err := syscall.GetShortPathName(name, &buf[0], uint32(len(buf)))
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			break
		}
		buf = make([]uint16, n+1)
	}
	alias := syscall.UTF16ToString(buf)
	original, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	resolved, err := os.Stat(alias)
	if err != nil || !os.SameFile(original, resolved) {
		return "", errors.New("short path does not identify the installed executable")
	}
	return alias, nil
}
