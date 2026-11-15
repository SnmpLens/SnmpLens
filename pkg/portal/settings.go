package portal

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// ColorScheme is org.freedesktop.appearance's color-scheme.
type ColorScheme uint32

const (
	NoPreference ColorScheme = 0
	PreferDark   ColorScheme = 1
	PreferLight  ColorScheme = 2
)

const (
	appearanceNamespace = "org.freedesktop.appearance"
	colorSchemeKey      = "color-scheme"
	settingsInterface   = "org.freedesktop.portal.Settings"
)

// ReadColorScheme reads the desktop's light or dark preference.
//
// It is what GNOME's and KDE's dark-mode switches set, and GNOME sets nothing
// else: GTK 3 reads its theme's name, which stays Adwaita, so a GTK 3
// application — and the WebKitGTK inside it, whose prefers-color-scheme is
// GTK's — stays light on a desktop the user made dark. Ubuntu changes the
// theme as well, which is why the problem does not show there.
func ReadColorScheme() (ColorScheme, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return NoPreference, fmt.Errorf("reach the desktop's portal: %w", err)
	}
	defer conn.Close()

	portal := conn.Object(busName, objectPath)
	var value dbus.Variant
	err = portal.Call(settingsInterface+".ReadOne", 0, appearanceNamespace, colorSchemeKey).Store(&value)
	if err != nil {
		// ReadOne is version 2 of the interface; Read is in every version.
		if err := portal.Call(settingsInterface+".Read", 0, appearanceNamespace, colorSchemeKey).Store(&value); err != nil {
			return NoPreference, fmt.Errorf("read the desktop's color scheme: %w", err)
		}
	}
	return colorSchemeOf(value), nil
}

// colorSchemeOf reads the value, whatever the number of variants around it:
// Read wraps it in one more than ReadOne does. Anything that is not a known
// value is no preference, which changes nothing.
func colorSchemeOf(v dbus.Variant) ColorScheme {
	value := v.Value()
	for {
		inner, ok := value.(dbus.Variant)
		if !ok {
			break
		}
		value = inner.Value()
	}
	if n, ok := value.(uint32); ok && n <= uint32(PreferLight) {
		return ColorScheme(n)
	}
	return NoPreference
}

// WatchColorScheme calls changed with every new preference the desktop
// announces, until ctx is done.
func WatchColorScheme(ctx context.Context, changed func(ColorScheme)) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("reach the desktop's portal: %w", err)
	}
	defer conn.Close()

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(objectPath),
		dbus.WithMatchInterface(settingsInterface),
		dbus.WithMatchMember("SettingChanged"),
	); err != nil {
		return fmt.Errorf("listen for the desktop's settings: %w", err)
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-signals:
			if !ok {
				return nil
			}
			if scheme, ok := colorSchemeChange(s); ok {
				changed(scheme)
			}
		}
	}
}

// colorSchemeChange reads a SettingChanged signal: namespace, key, value.
func colorSchemeChange(s *dbus.Signal) (ColorScheme, bool) {
	if s.Name != settingsInterface+".SettingChanged" || len(s.Body) < 3 {
		return NoPreference, false
	}
	namespace, _ := s.Body[0].(string)
	key, _ := s.Body[1].(string)
	value, ok := s.Body[2].(dbus.Variant)
	if namespace != appearanceNamespace || key != colorSchemeKey || !ok {
		return NoPreference, false
	}
	return colorSchemeOf(value), true
}
