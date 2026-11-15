//go:build !windows && !darwin

package autostart

import (
	"errors"

	"SnmpLens/pkg/portal"
)

// portalCommand is what the portal's entry runs inside the sandbox: the
// manifest's command (build/linux/flatpak). Whether the window opens or the
// application starts in the tray is service.json's StartHidden, read at
// startup, as it is for the XDG entry.
var portalCommand = []string{"snmplens"}

// requestAutostart asks the Background portal for a login entry, or to remove
// it. The portal is the only way a sandbox gets one: it writes the entry on the
// host, under the application ID, with the `flatpak run` that starts the
// sandbox again — none of which the sandbox could write or even name.
func requestAutostart(enabled bool) error {
	got, err := portal.RequestBackground(enabled, portalCommand)
	if err != nil {
		return err
	}
	return autostartGranted(enabled, got)
}

// autostartGranted holds an answer to what was asked: a desktop that keeps the
// application out of the background answers without a login entry, and
// reporting that as enabled is the stored "yes" this package refuses to keep.
func autostartGranted(enabled bool, got portal.Background) error {
	if enabled && !got.Autostart {
		return errors.New("the desktop did not allow SnmpLens to start at login; see its settings for background applications")
	}
	return nil
}

// keepRunning asks to keep running with no window, in a sandbox. The request
// states the login entry too and removes it when that is false, so it carries
// the entry as it stands.
func keepRunning() (bool, error) {
	if !sandboxed() {
		return false, errUnsupported
	}
	enabled, _, err := isEnabled()
	if err != nil {
		return false, err
	}
	got, err := portal.RequestBackground(enabled, portalCommand)
	if err != nil {
		return false, err
	}
	return got.Background, nil
}
