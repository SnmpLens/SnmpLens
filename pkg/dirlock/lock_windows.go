//go:build windows

package dirlock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockFile opens the lock file sharing nothing: while this handle is open, any
// other open of the file fails with a sharing violation.
func lockFile(path string) (func(), error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, ErrInUse
		}
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}
