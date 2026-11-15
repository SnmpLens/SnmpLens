package portal

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestAnAnswerIsNotYetAYes(t *testing.T) {
	cases := []struct {
		name    string
		code    uint32
		results map[string]dbus.Variant
		want    Background
		wantErr error
	}{
		{"granted", 0, map[string]dbus.Variant{"background": dbus.MakeVariant(true), "autostart": dbus.MakeVariant(true)},
			Background{Background: true, Autostart: true}, nil},
		{"background only", 0, map[string]dbus.Variant{"background": dbus.MakeVariant(true), "autostart": dbus.MakeVariant(false)},
			Background{Background: true}, nil},
		// Answered with nothing: kept out of the background.
		{"answered with nothing", 0, nil, Background{}, nil},
		{"dismissed", 1, nil, Background{}, ErrDismissed},
		{"refused", 2, nil, Background{}, ErrRefused},
	}
	for _, c := range cases {
		got, err := backgroundAnswer(c.code, c.results)
		if got != c.want || !errors.Is(err, c.wantErr) {
			t.Errorf("%s: got (%+v, %v), want (%+v, %v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

// Read answers the value inside one more variant than ReadOne, and a value the
// specification does not name must change nothing.
func TestTheColorSchemeIsReadThroughAnyNumberOfVariants(t *testing.T) {
	cases := []struct {
		name  string
		value dbus.Variant
		want  ColorScheme
	}{
		{"ReadOne", dbus.MakeVariant(uint32(1)), PreferDark},
		{"Read", dbus.MakeVariant(dbus.MakeVariant(uint32(2))), PreferLight},
		{"no preference", dbus.MakeVariant(uint32(0)), NoPreference},
		{"unknown value", dbus.MakeVariant(uint32(7)), NoPreference},
		{"wrong type", dbus.MakeVariant("dark"), NoPreference},
	}
	for _, c := range cases {
		if got := colorSchemeOf(c.value); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOnlyTheColorSchemeChangeIsAColorSchemeChange(t *testing.T) {
	signal := func(namespace, key string, value interface{}) *dbus.Signal {
		return &dbus.Signal{
			Path: objectPath,
			Name: settingsInterface + ".SettingChanged",
			Body: []interface{}{namespace, key, dbus.MakeVariant(value)},
		}
	}
	if got, ok := colorSchemeChange(signal(appearanceNamespace, colorSchemeKey, uint32(1))); !ok || got != PreferDark {
		t.Errorf("a change to dark read as (%d, %v)", got, ok)
	}
	if _, ok := colorSchemeChange(signal(appearanceNamespace, "accent-color", uint32(1))); ok {
		t.Error("the accent colour was taken for the colour scheme")
	}
	if _, ok := colorSchemeChange(signal("org.gnome.desktop.interface", colorSchemeKey, "prefer-dark")); ok {
		t.Error("GNOME's own key was taken for the portal's")
	}
}
