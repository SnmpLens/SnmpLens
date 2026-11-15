package app

import (
	"context"
	"log"
	goruntime "runtime"
	"sync"

	"SnmpLens/pkg/portal"
)

// The renderer draws light or dark by prefers-color-scheme when its theme is
// "system", and in a GTK window WebKitGTK answers that from GTK's theme. GNOME's
// dark-mode switch sets color-scheme and nothing else, so on a GNOME that is not
// Ubuntu's — which changes the GTK theme as well — the window stayed light,
// title bar and content, on a desktop the user had made dark. Measured in a
// headless GNOME Shell set to prefer-dark with the Adwaita theme: the same
// brightness as light mode.
//
// So Go reads the preference where every desktop publishes it, the Settings
// portal, and gives GTK the dark variant of its theme when it applies. The
// renderer then follows through the media query it already listens to, and the
// title bar with it. An explicit theme in the settings wins over the desktop,
// for the title bar as for the content: hence AppearanceSetTheme.
type appearanceState struct {
	mu sync.Mutex
	// theme is the renderer's setting: "system", "dark" or "light".
	theme string
	// scheme is the desktop's preference.
	scheme portal.ColorScheme
	cancel context.CancelFunc
}

// preferDark decides GTK's variant. The desktop decides only when the setting
// is "system", and only when it states a preference: with none, GTK keeps its
// theme's own, which is dark already where the theme is.
func preferDark(theme string, scheme portal.ColorScheme) bool {
	switch theme {
	case "dark":
		return true
	case "light":
		return false
	}
	return scheme == portal.PreferDark
}

// AppearanceSetTheme tells Go which theme the renderer is set to, so the window
// around it agrees.
func (a *App) AppearanceSetTheme(theme string) {
	a.appearance.mu.Lock()
	a.appearance.theme = theme
	dark := preferDark(theme, a.appearance.scheme)
	a.appearance.mu.Unlock()
	setGTKPreferDark(dark)
}

// followDesktopAppearance reads the desktop's preference and follows its
// changes until the application stops.
func (a *App) followDesktopAppearance() {
	if goruntime.GOOS != "linux" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.appearance.mu.Lock()
	a.appearance.cancel = cancel
	a.appearance.mu.Unlock()

	go func() {
		scheme, err := portal.ReadColorScheme()
		if err != nil {
			// No portal is a desktop that publishes no preference: GTK keeps
			// its theme, which is what happened before.
			log.Printf("appearance: %v; the window keeps GTK's theme", err)
			return
		}
		a.desktopColorScheme(scheme)
		if err := portal.WatchColorScheme(ctx, a.desktopColorScheme); err != nil {
			log.Printf("appearance: %v; a change of the desktop's theme needs a restart", err)
		}
	}()
}

func (a *App) desktopColorScheme(scheme portal.ColorScheme) {
	a.appearance.mu.Lock()
	a.appearance.scheme = scheme
	dark := preferDark(a.appearance.theme, scheme)
	a.appearance.mu.Unlock()
	setGTKPreferDark(dark)
}

func (a *App) stopAppearance() {
	a.appearance.mu.Lock()
	cancel := a.appearance.cancel
	a.appearance.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
