package tray

import "github.com/godbus/dbus/v5"

// hostAvailable asks the session bus whether anything collects tray icons. Wails
// would register with it and, finding nothing, say so only in its log, while the
// window's close button went on hiding the window into a tray nobody can see.
func hostAvailable() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var owned bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&owned)
	return err == nil && owned
}
