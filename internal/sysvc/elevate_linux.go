//go:build linux

package sysvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// IsElevated reports whether this process runs as root.
func IsElevated() bool { return os.Geteuid() == 0 }

// IsService is false: systemd starts the service as `service run`, which the
// command line handles.
func IsService() bool { return false }

// pkexec's exit codes when no administrator authorised the command.
const (
	pkexecDismissed    = 126
	pkexecNotAuthorize = 127
)

// RunElevated runs exe with args as root, through polkit's prompt, and waits
// for it to finish. It returns the process's exit code.
func RunElevated(exe string, args []string, timeout time.Duration) (uint32, error) {
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return 0, fmt.Errorf("pkexec is not installed, so this window cannot ask for administrator rights; "+
			"run this in a terminal instead: sudo %s %s", exe, strings.Join(args, " "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err = exec.CommandContext(ctx, pkexec, append([]string{exe}, args...)...).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		code := exit.ExitCode()
		if code == pkexecDismissed || code == pkexecNotAuthorize {
			return uint32(code), ErrCancelled
		}
		return uint32(code), nil
	case ctx.Err() != nil:
		return 0, fmt.Errorf("the administrator step did not finish in %s", timeout)
	default:
		return 0, err
	}
}
