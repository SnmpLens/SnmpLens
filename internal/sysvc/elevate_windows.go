//go:build windows

package sysvc

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsElevated reports whether this process runs with administrator rights.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// shellExecuteInfo is SHELLEXECUTEINFOW. x/sys wraps ShellExecute but not the Ex
// form, which is the one that hands back the process to wait on.
type shellExecuteInfo struct {
	cbSize        uint32
	fMask         uint32
	hwnd          windows.Handle
	verb          *uint16
	file          *uint16
	parameters    *uint16
	directory     *uint16
	show          int32
	instApp       windows.Handle
	idList        uintptr
	class         *uint16
	keyClass      windows.Handle
	hotKey        uint32
	iconOrMonitor windows.Handle
	process       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// RunElevated runs exe with args as an administrator, through the UAC prompt,
// and waits for it to finish. It returns the process's exit code.
//
// Only the steps that need the rights run elevated; the caller stays as it was.
// The prompt names the program, so what the user approves is visibly SnmpLens
// and not a script.
func RunElevated(exe string, args []string, timeout time.Duration) (uint32, error) {
	// ShellExecuteEx may hand the request to shell extensions, which expect COM
	// on the calling thread; a binding call arrives on an arbitrary one.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}

	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		fMask:      seeMaskNoCloseProcess | seeMaskNoAsync,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrCancelled
		}
		return 0, fmt.Errorf("start the elevated helper: %w", callErr)
	}
	if info.process == 0 {
		return 0, fmt.Errorf("the elevated helper started without a process to wait on")
	}
	defer windows.CloseHandle(info.process)

	ev, err := windows.WaitForSingleObject(info.process, uint32(timeout/time.Millisecond))
	if err != nil {
		return 0, err
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return 0, fmt.Errorf("the elevated helper did not finish within %s", timeout)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// CurrentUserSID is the SID of the account this process runs as.
func CurrentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
