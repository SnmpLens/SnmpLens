// Package autostart registers SnmpLens to start when the user logs in.
//
// This is the other half of background mode: an application that keeps polling
// with its window closed is only useful if it comes back by itself after a
// reboot. Nobody is going to remember to launch a monitoring tool by hand every
// morning, and the first sign that they forgot is the alert that never arrived.
//
// Each platform gets the mechanism its desktop actually reads — the HKCU Run
// key on Windows, a LaunchAgent on macOS, an XDG .desktop entry on Linux — and
// all three are per-user, never machine-wide: writing to a machine-wide
// location needs elevation, and an NMS is not worth an admin prompt.
//
// IsEnabled always asks the operating system rather than trusting a stored
// flag. The user can remove the entry through Task Manager, System Settings or
// by deleting a file, and a remembered "yes" would then be a lie the settings
// screen keeps telling.
package autostart

// AppName is the identity used for the registry value, the plist label and the
// .desktop filename. It must stay stable: changing it would orphan the entry
// already registered on every existing installation and quietly leave two.
const AppName = "SnmpLens"

// Status is what the settings UI needs to describe the current state.
type Status struct {
	// Supported is false where no mechanism is implemented.
	Supported bool `json:"supported"`
	// Enabled reflects what the OS says right now, not what was last asked for.
	Enabled bool `json:"enabled"`
	// Location names where the entry lives, so a user can verify or remove it
	// themselves. Being able to see what an application did to your machine is
	// part of it being trustworthy.
	Location string `json:"location"`
	// Command is the exact command line registered.
	Command string `json:"command,omitempty"`
	// Error carries a diagnosis when the state could not be read.
	Error string `json:"error,omitempty"`
}

// Get reports the current state, never failing outright: a settings screen that
// cannot render because an entry is unreadable is worse than one that says so.
func Get() Status {
	st := Status{Supported: supported(), Location: location()}
	if !st.Supported {
		return st
	}
	enabled, cmd, err := isEnabled()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Enabled, st.Command = enabled, cmd
	return st
}

// KeepRunningInBackground asks the desktop to let SnmpLens keep running with no
// window, and reports whether it may. Only a Flatpak needs to ask: GNOME lists
// a sandboxed application running with no window under Background Apps in its
// quick settings, where it can be stopped, and without the permission the
// portal may stop it itself. Anywhere else the answer is no, with an error.
func KeepRunningInBackground() (bool, error) {
	return keepRunning()
}

// Set enables or disables the login entry and returns the resulting state.
func Set(enabled bool) (Status, error) {
	if !supported() {
		return Get(), errUnsupported
	}
	var err error
	if enabled {
		err = enable()
	} else {
		err = disable()
	}
	if err != nil {
		return Get(), err
	}
	return Get(), nil
}
