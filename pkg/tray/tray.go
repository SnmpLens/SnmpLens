// Package tray puts SnmpLens in the system notification area so it can keep
// polling and listening for traps with no window open.
//
// Everything here is deliberately fail-soft. A tray is not guaranteed to exist:
// a bare Linux session may run no StatusNotifierItem host at all. So Start reports
// whether an icon can appear, and the caller uses that answer to decide whether
// closing the window is allowed to merely hide it. Getting this wrong produces the
// worst possible outcome — an application the user cannot quit — so the failure
// path is the one designed first.
package tray

import (
	"fmt"
	"log"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Options describes the menu to build. Every callback is optional.
type Options struct {
	// Icon is the image bytes: an .ico on Windows, a .png elsewhere.
	Icon []byte
	// Tooltip is the hover text on the icon itself.
	Tooltip string
	// Labels are the menu entries, in English. The renderer replaces them
	// once svelte-i18n knows the user's locale — Go has no translations of
	// its own and inventing a second catalogue would guarantee drift.
	Labels Labels
	// OnShow restores the main window.
	OnShow func()
	// OnQuit really exits, as opposed to hiding.
	OnQuit func()
}

// Labels are the tray menu strings.
type Labels struct {
	Show   string
	Quit   string
	Status string
}

// DefaultLabels are the English fallbacks used until the renderer reports a
// locale.
func DefaultLabels() Labels {
	return Labels{Show: "Open SnmpLens", Quit: "Quit", Status: "Idle"}
}

// Controller manipulates a running tray icon.
type Controller struct {
	mu     sync.Mutex
	tray   *application.SystemTray
	menu   *application.Menu
	show   *application.MenuItem
	status *application.MenuItem
	quit   *application.MenuItem
	closed bool
}

// Start installs the tray icon. The application need not be running yet: Wails
// holds the icon until it is.
//
// It reports ok=false when no icon can be shown, which is a normal outcome on
// some desktops and never an error worth interrupting startup for.
func Start(app *application.App, opts Options) (c *Controller, ok bool) {
	if app == nil || !hostAvailable() {
		log.Printf("tray: this desktop has no system tray; running without one")
		return nil, false
	}
	if opts.Labels.Show == "" {
		opts.Labels = DefaultLabels()
	}

	ctrl := &Controller{menu: app.NewMenu()}
	ctrl.show = ctrl.menu.Add(opts.Labels.Show).OnClick(func(*application.Context) {
		if opts.OnShow != nil {
			opts.OnShow()
		}
	})
	ctrl.menu.AddSeparator()
	ctrl.status = ctrl.menu.Add(opts.Labels.Status).SetEnabled(false) // a read-out, not a command
	ctrl.menu.AddSeparator()
	ctrl.quit = ctrl.menu.Add(opts.Labels.Quit).OnClick(func(*application.Context) {
		if opts.OnQuit != nil {
			opts.OnQuit()
		}
	})

	ctrl.tray = app.SystemTray.New()
	if len(opts.Icon) > 0 {
		ctrl.tray.SetIcon(opts.Icon)
	}
	if opts.Tooltip != "" {
		ctrl.tray.SetTooltip(opts.Tooltip)
	}
	ctrl.tray.SetMenu(ctrl.menu)
	return ctrl, true
}

// SetStatus rewrites the disabled read-out line.
func (c *Controller) SetStatus(text string) {
	if c == nil || text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.status.SetLabel(text)
		c.menu.Update()
	}
}

// SetLabels replaces the menu wording, so the tray follows the language chosen
// in the app rather than being permanently English.
func (c *Controller) SetLabels(l Labels) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if l.Show != "" {
		c.show.SetLabel(l.Show)
	}
	if l.Quit != "" {
		c.quit.SetLabel(l.Quit)
	}
	if l.Status != "" {
		c.status.SetLabel(l.Status)
	}
	c.menu.Update()
}

// Stop removes the icon, for background mode being switched off while the
// application runs: Wails removes it itself when the application exits. It is safe
// to call twice, and safe on a nil receiver so callers do not have to branch on
// whether the tray ever started.
func (c *Controller) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	already := c.closed
	c.closed = true
	c.mu.Unlock()
	if !already {
		c.tray.Destroy()
	}
}

// StatusLine renders the read-out shown in the menu.
func StatusLine(monitors int, trapsOn bool) string {
	traps := "off"
	if trapsOn {
		traps = "on"
	}
	return fmt.Sprintf("Monitors: %d · Traps: %s", monitors, traps)
}
