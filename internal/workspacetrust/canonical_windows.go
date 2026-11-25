package workspacetrust

import (
	"strings"

	"golang.org/x/sys/windows"
)

// The handle identifies the same physical path that Rust's fs::canonicalize
// uses on Windows, including a network drive mapped to a local drive letter.
func codexCanonicalPath(physical, original string) string {
	path := physical
	if !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`) {
		if tail, ok := strings.CutPrefix(path, `\\`); ok {
			path = `\\?\UNC\` + tail
		} else {
			path = `\\?\` + path
		}
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return original
	}
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	handle, err := windows.CreateFile(name, 0, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return original
	}
	defer windows.CloseHandle(handle)
	buf := make([]uint16, 260)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return original
		}
		if n < uint32(len(buf)) {
			return codexDiskPath(windows.UTF16ToString(buf[:n]))
		}
		buf = make([]uint16, n+1)
	}
}
