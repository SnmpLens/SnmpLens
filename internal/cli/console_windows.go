//go:build windows

package cli

import (
	"log"
	"os"

	"golang.org/x/sys/windows"
)

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// attachParentProcess is ATTACH_PARENT_PROCESS, (DWORD)-1.
const attachParentProcess = ^uintptr(0)

// attachConsole gives a command typed in a terminal somewhere to print.
//
// SnmpLens is built as a GUI program, so Windows gives it no console:
// `SnmpLens.exe --headless` would log into the void. Output redirected to a
// file or a pipe is left alone; otherwise the program attaches to the console
// of the terminal that started it. That terminal does not wait for a GUI
// program, so its prompt comes back at once and the log lands after it.
func attachConsole() {
	if redirected(windows.STD_OUTPUT_HANDLE) {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = out, out
		log.SetOutput(out)
	}
}

func redirected(std uint32) bool {
	h, err := windows.GetStdHandle(std)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && (t == windows.FILE_TYPE_DISK || t == windows.FILE_TYPE_PIPE)
}
