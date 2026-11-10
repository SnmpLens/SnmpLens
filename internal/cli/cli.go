// Package cli is every way of starting the executable other than the desktop
// application. One binary rather than several, because they must always be the
// same version: the supervision reads what the window wrote.
//
//	SnmpLens                          the desktop application
//	SnmpLens --headless [--data-dir DIR]
//	                                  the supervision alone, in the foreground
//	SnmpLens service <command>        the system service, and managing it
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
	"SnmpLens/internal/sysvc"
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
  SnmpLens service status            show the system service's state
  SnmpLens service install [--bundle FILE --db FILE] [--operator SID]
                                     install it, or update it, and start it;
                                     without a bundle it starts empty, on the
                                     defaults, until a window hands it a
                                     configuration
  SnmpLens service allow --operator SID
                                     let an account drive it from its window
  SnmpLens service uninstall [--export DIR] [--purge]
                                     remove it; --export first writes its
                                     history and credentials into DIR
  SnmpLens service start | stop      start or stop it

The service is normally installed from Settings > Background, which prepares
what to hand over and asks for administrator rights itself. Once it runs, the
window of an allowed account drives it: its sessions, its journal, its
notifications.
`

// Run handles args when they are not the desktop application's. handled is
// false when the desktop application should start.
func Run(args []string) (handled bool, code int) {
	// Started by the service manager: nothing else applies.
	if sysvc.IsService() {
		if err := sysvc.Run(updater.Version); err != nil {
			return true, 1
		}
		return true, 0
	}
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "service":
		attachConsole()
		return true, serviceCommand(args[1:], os.Stdout, os.Stderr)
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
