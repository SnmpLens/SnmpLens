// Package cli is every way of starting the executable other than the desktop
// application. One binary rather than several, because they must always be the
// same version: the supervision reads what the window wrote.
//
//	SnmpLens                          the desktop application
//	SnmpLens --headless [--data-dir DIR]
//	                                  the supervision alone, in the foreground
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"SnmpLens/internal/headless"
	"SnmpLens/pkg/updater"
)

const usageText = `SnmpLens %s

Usage:
  SnmpLens                           open the application
  SnmpLens --headless [--data-dir DIR]
                                     supervise with no window, in the
                                     foreground, until interrupted (for a
                                     terminal, systemd or launchd): poll the
                                     monitoring sessions, evaluate their
                                     thresholds, receive traps and send the
                                     notifications. DIR defaults to the
                                     desktop application's own directory,
                                     whose service.json says whether the trap
                                     listener starts and the sessions resume.
`

// Run handles args when they are not the desktop application's. handled is
// false when the desktop application should start.
func Run(args []string) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "--headless", "headless":
		attachConsole()
		return true, headlessCommand(args[1:], os.Stderr)
	case "--help", "-h", "help", "/?":
		attachConsole()
		fmt.Fprintf(os.Stdout, usageText, updater.Version)
		return true, 0
	}
	return false, 0
}

func headlessCommand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("headless", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "the data directory (default: the desktop application's)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := *dataDir
	if dir == "" {
		d, err := headless.DefaultDataDir()
		if err != nil {
			fmt.Fprintln(stderr, "SnmpLens:", err)
			return 1
		}
		dir = d
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := headless.Run(ctx, headless.Options{DataDir: dir, Version: updater.Version}); err != nil {
		fmt.Fprintln(stderr, "SnmpLens:", err)
		return 1
	}
	return 0
}
