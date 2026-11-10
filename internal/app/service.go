package app

import (
	"log"
	goruntime "runtime"

	"SnmpLens/pkg/autostart"
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/tray"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayIcons carries the embedded artwork from main.go.
type trayIcons struct {
	png []byte
	ico []byte
}

// forOS picks the format the platform's tray expects.
func (t trayIcons) forOS() []byte {
	if goruntime.GOOS == "windows" {
		return t.ico
	}
	return t.png
}

// initBackgroundMode installs the tray icon and applies the pre-GUI settings.
//
// Ordering matters: the tray decides whether closing the window is allowed to
// merely hide it, so it has to be attempted before the user can close anything.
func (a *App) initBackgroundMode() {
	if a.serviceCfg.RunInBackground {
		ctrl, ok := tray.Start(tray.Options{
			Icon:    a.trayIcons.forOS(),
			Tooltip: "SnmpLens",
			Labels:  tray.DefaultLabels(),
			OnShow:  a.RevealWindow,
			OnQuit:  a.QuitApp,
		})
		a.tray, a.trayLive = ctrl, ok
		if !ok {
			// Say it in the journal rather than only in a log line: the user
			// asked for background mode and is not getting it.
			a.recordSystemEvent(events.KindSystemInfo, "warning",
				"Background mode is enabled but this desktop provided no system tray; closing the window will quit SnmpLens.")
		}
	}

	// Launched hidden with nowhere to click? Show the window rather than
	// leaving a process the user can neither see nor stop.
	if a.serviceCfg.StartHidden && !a.trayLive {
		log.Printf("started hidden but no tray is available; showing the window")
		a.RevealWindow()
	}
}

// hideInsteadOfClosing answers the window's close button.
//
// Hiding is only correct when there is a tray icon to bring the window back
// from. Without one, closing must really close.
func (a *App) hideInsteadOfClosing() bool {
	return a.serviceCfg.RunInBackground && a.trayLive
}

// RevealWindow brings the window back from the tray, from a hidden start, or
// from a second launch.
func (a *App) RevealWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}

// QuitApp exits for real, bypassing the hide-on-close behaviour.
func (a *App) QuitApp() {
	if a.ctx == nil {
		return
	}
	// Closing the window is intercepted while a tray is live, so the tray's
	// own Quit entry has to disarm that first.
	a.serviceCfg.RunInBackground = false
	runtime.Quit(a.ctx)
}

// refreshTrayStatus rewrites the tray read-out.
func (a *App) refreshTrayStatus() {
	if a.tray == nil {
		return
	}
	if a.host == nil {
		return
	}
	a.tray.SetStatus(tray.StatusLine(len(a.MonitorRunning()), a.host.TrapListenerRunning()))
}

// TraySetLabels lets the renderer translate the tray menu once svelte-i18n has
// resolved the locale. Go keeps no catalogue of its own — a second one would
// drift from the five JSON files that are the real source of truth.
func (a *App) TraySetLabels(show, quit string) {
	a.tray.SetLabels(tray.Labels{Show: show, Quit: quit})
	a.refreshTrayStatus()
}

// AutostartGet reports whether SnmpLens is registered to start at login.
//
// The answer comes from the operating system every time rather than from a
// stored flag: the entry can be removed through Task Manager, System Settings
// or by deleting a file, and a remembered "yes" would be a lie the settings
// screen keeps telling.
func (a *App) AutostartGet() autostart.Status {
	return autostart.Get()
}

// AutostartSet registers or removes the login entry.
func (a *App) AutostartSet(enabled bool) (autostart.Status, error) {
	return autostart.Set(enabled)
}

// ServiceGetConfig returns the pre-GUI preferences.
func (a *App) ServiceGetConfig() service.Config {
	return a.serviceCfg
}

// ServiceStatus reports what the preferences actually produced on this machine,
// which is not always what was asked for.
type ServiceStatus struct {
	Config service.Config `json:"config"`
	// TrayAvailable is false when the desktop refused a tray icon. The UI uses
	// it to explain why background mode is not in effect.
	TrayAvailable bool `json:"trayAvailable"`
	// TrapListenerRunning reflects the auto-started listener.
	TrapListenerRunning bool   `json:"trapListenerRunning"`
	Platform            string `json:"platform"`
}

// ServiceGetStatus returns the effective state.
func (a *App) ServiceGetStatus() ServiceStatus {
	return ServiceStatus{
		Config:              a.serviceCfg,
		TrayAvailable:       a.trayLive,
		TrapListenerRunning: a.host != nil && a.host.TrapListenerRunning(),
		Platform:            goruntime.GOOS,
	}
}

// ServiceSetConfig persists the pre-GUI preferences.
//
// Most of them only take effect at the next launch, which the UI says plainly
// rather than pretending the change was live.
func (a *App) ServiceSetConfig(cfg service.Config) (ServiceStatus, error) {
	if err := service.Save(a.configDir, cfg); err != nil {
		return a.ServiceGetStatus(), err
	}
	prev := a.serviceCfg
	// Re-read so the caller sees the same normalisation the next launch will.
	saved, err := service.Load(a.configDir)
	if err != nil {
		return a.ServiceGetStatus(), err
	}
	a.serviceCfg = saved
	if eng := a.localEngine(); eng != nil {
		eng.SetConfig(saved)
	}

	// Turning background mode on is worth honouring immediately: it is the one
	// setting whose whole point is that the window may be closed at any moment.
	if saved.RunInBackground && !prev.RunInBackground {
		a.initBackgroundModeTrayOnly()
	} else if !saved.RunInBackground && prev.RunInBackground {
		a.tray.Stop()
		a.tray, a.trayLive = nil, false
	}
	return a.ServiceGetStatus(), nil
}

// initBackgroundModeTrayOnly installs just the tray, for a mid-session switch.
func (a *App) initBackgroundModeTrayOnly() {
	if a.trayLive {
		return
	}
	ctrl, ok := tray.Start(tray.Options{
		Icon:    a.trayIcons.forOS(),
		Tooltip: "SnmpLens",
		Labels:  tray.DefaultLabels(),
		OnShow:  a.RevealWindow,
		OnQuit:  a.QuitApp,
	})
	a.tray, a.trayLive = ctrl, ok
	a.refreshTrayStatus()
}

// recordSystemEvent writes a system-category entry, ignoring the error: this is
// used on paths that are already reporting a problem.
func (a *App) recordSystemEvent(kind, severity, summary string) {
	if a.host != nil {
		a.host.RecordSystemEvent(kind, severity, summary)
	}
}
