// Package portal talks to the desktop's XDG portals over D-Bus.
//
// A portal is what a desktop offers an application whatever its toolkit, and
// the only thing a Flatpak sandbox may ask. Three things SnmpLens needs from a
// Linux desktop go through one: the dark-mode preference (Settings), which GTK
// 3 does not read on a GNOME that only sets color-scheme; and permission to
// keep running with no window, and a login entry (Background).
package portal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	busName    = "org.freedesktop.portal.Desktop"
	objectPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
)

// ErrDismissed is a request the user closed without answering.
var ErrDismissed = errors.New("the request was dismissed")

// ErrRefused is a request the desktop turned down.
var ErrRefused = errors.New("the desktop refused the request")

// request makes a portal call that answers later, through a Request object's
// Response signal, and waits for that answer.
func request(method string, wait time.Duration, args []interface{}, options map[string]dbus.Variant) (uint32, map[string]dbus.Variant, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, nil, fmt.Errorf("reach the desktop's portal: %w", err)
	}
	defer conn.Close()

	// Subscribed BEFORE the call: the answer can come back before the call
	// returns the handle it will be sent on.
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return 0, nil, fmt.Errorf("listen for the portal's answer: %w", err)
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return 0, nil, err
	}
	options["handle_token"] = dbus.MakeVariant("snmplens_" + hex.EncodeToString(token))

	var handle dbus.ObjectPath
	call := conn.Object(busName, objectPath).Call(method, 0, append(args, options)...)
	if err := call.Store(&handle); err != nil {
		return 0, nil, fmt.Errorf("ask the portal: %w", err)
	}

	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	for {
		select {
		case s, ok := <-signals:
			if !ok {
				return 0, nil, errors.New("the session bus closed before the portal answered")
			}
			if s.Path != handle || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			results, _ := s.Body[1].(map[string]dbus.Variant)
			return code, results, nil
		case <-timeout.C:
			return 0, nil, errors.New("the portal did not answer; is xdg-desktop-portal running?")
		}
	}
}

// answered turns a Response code into an error: 0 is an answer, 1 a dismissed
// dialog, anything else a refusal.
func answered(code uint32) error {
	switch code {
	case 0:
		return nil
	case 1:
		return ErrDismissed
	default:
		return ErrRefused
	}
}
