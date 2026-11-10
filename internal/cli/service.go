package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"SnmpLens/internal/sysvc"
	"SnmpLens/pkg/updater"
)

// serviceCommand manages the system service. The commands that change it run
// elevated: the window starts them through the administrator prompt with
// --result, and reads back what they did, since an elevated copy has no
// console to say it on.
func serviceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "SnmpLens service: status, install, allow, uninstall, start or stop")
		return 2
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	bundle := fs.String("bundle", "", "the configuration a window prepared")
	db := fs.String("db", "", "a copy of the window's monitoring database")
	operator := fs.String("operator", "", "the SID of an account to let drive the service")
	operatorSelf := fs.Bool("operator-self", false, "let the account running this drive the service: the installer's")
	export := fs.String("export", "", "a directory to write the service's history and credentials into first")
	purge := fs.Bool("purge", false, "delete the service's data as well")
	result := fs.String("result", "", "a file to record the outcome in")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	var res sysvc.Result
	var err error
	switch args[0] {
	case "status":
		return status(stdout, stderr)
	case "run":
		if sysvc.RunsFromCommandLine() {
			// systemd's ExecStart.
			err = sysvc.Run(updater.Version)
			break
		}
		// Typed by hand: Windows' service manager starts the service in a way
		// caught before this.
		err = errors.New("service run is how the service manager starts SnmpLens; use service start")
	case "install":
		exe, xerr := os.Executable()
		if xerr != nil {
			err = xerr
			break
		}
		if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = real
		}
		if *operatorSelf && *operator == "" {
			// The installer's elevated copy runs as the account that approved
			// it: on a machine one person administers, the person who will
			// open the window.
			*operator, _ = sysvc.CurrentUserSID()
		}
		res, err = sysvc.Install(exe, *bundle, *db, *operator)
	case "allow":
		if *operator == "" {
			err = errors.New("--operator is required")
			break
		}
		err = sysvc.Allow(*operator)
	case "uninstall":
		err = sysvc.Uninstall(*purge, *export)
	case "start":
		err = sysvc.Start()
	case "stop":
		err = sysvc.Stop()
	default:
		fmt.Fprintf(stderr, "SnmpLens service: unknown command %q\n", args[0])
		return 2
	}
	sysvc.WriteResult(*result, res, err)
	if err != nil {
		fmt.Fprintln(stderr, "SnmpLens service "+args[0]+":", err)
		return 1
	}
	if res.KeptHistory {
		fmt.Fprintln(stdout, "The service had a history of its own and kept it.")
	}
	fmt.Fprintln(stdout, "Done.")
	return 0
}

func status(stdout, stderr io.Writer) int {
	if !sysvc.Supported() {
		fmt.Fprintln(stderr, sysvc.ErrUnsupported)
		return 1
	}
	info, err := sysvc.Query()
	if err != nil {
		fmt.Fprintln(stderr, "SnmpLens service status:", err)
		return 1
	}
	if !info.Installed {
		fmt.Fprintln(stdout, "The SnmpLens service is not installed.")
		return 0
	}
	fmt.Fprintf(stdout, "The SnmpLens service is %s (%s, as %s).\n", info.State, info.Executable, info.Account)
	if dir, err := sysvc.StatusDir(); err == nil {
		if st, ok := sysvc.ReadStatus(dir); ok && st.Error != "" {
			fmt.Fprintln(stdout, "Last error:", st.Error)
		}
	}
	return 0
}
