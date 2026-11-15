package portal

import (
	"time"

	"github.com/godbus/dbus/v5"
)

// Background is what the desktop granted.
type Background struct {
	// Background: the application may keep running with no window. GNOME lists
	// such an application under Background Apps in its quick settings, where it
	// can be stopped.
	Background bool
	// Autostart: the desktop wrote a login entry that starts it again.
	Autostart bool
}

// backgroundWait bounds the wait for the answer. A desktop may ask the user
// the first time, so it is long.
const backgroundWait = 2 * time.Minute

// RequestBackground asks the Background portal to let the application keep
// running with no window, with a login entry running commandline when autostart
// is true.
//
// Every request states autostart, and false REMOVES an entry the portal wrote:
// a caller asking only to run in the background must pass the login entry's
// current state, or the request takes it away.
func RequestBackground(autostart bool, commandline []string) (Background, error) {
	code, results, err := request("org.freedesktop.portal.Background.RequestBackground", backgroundWait,
		[]interface{}{""}, map[string]dbus.Variant{
			"autostart":        dbus.MakeVariant(autostart),
			"commandline":      dbus.MakeVariant(commandline),
			"dbus-activatable": dbus.MakeVariant(false),
		})
	if err != nil {
		return Background{}, err
	}
	return backgroundAnswer(code, results)
}

// backgroundAnswer reads the Response. An answer is not yet a yes: a desktop
// that keeps the application out of the background answers 0 with both
// results false.
func backgroundAnswer(code uint32, results map[string]dbus.Variant) (Background, error) {
	if err := answered(code); err != nil {
		return Background{}, err
	}
	background, _ := results["background"].Value().(bool)
	autostart, _ := results["autostart"].Value().(bool)
	return Background{Background: background, Autostart: autostart}, nil
}
