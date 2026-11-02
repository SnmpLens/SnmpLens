package app

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"

	"SnmpLens/pkg/service"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Options is what package main hands over: the files embedded in the binary.
// They are embedded there because //go:embed cannot reach above the directory
// of the file it sits in, and frontend/dist, mibs/, presets/ and build/ are all
// at the root.
type Options struct {
	// Assets is the built interface (frontend/dist).
	Assets fs.FS
	// MIBs holds mibs/, the standard MIBs restored on every startup.
	MIBs fs.FS
	// Presets holds presets/, the example presets written out on first run.
	Presets fs.FS
	// TrayPNG and TrayICO are the tray artwork: Windows needs an .ico, the
	// other desktops want a .png.
	TrayPNG, TrayICO []byte
}

// Run starts SnmpLens and returns once it has exited.
//
// A function and not a method, on purpose: Wails binds every EXPORTED method of
// App to the renderer, so the lifecycle — startup, shutdown, the close decision
// — stays unexported, and so does what calls it.
func Run(opts Options) error {
	// The background-mode preferences have to be read before the window
	// exists, so they live in a small JSON file rather than localStorage.
	cfgDir := ""
	if dir, err := os.UserConfigDir(); err == nil {
		cfgDir = filepath.Join(dir, "SnmpLens")
	}
	svcCfg, err := service.Load(cfgDir)
	if err != nil {
		log.Printf("WARNING: could not read %s, using defaults: %v", service.Path(cfgDir), err)
	}

	a := NewApp(opts.MIBs, opts.Presets)
	a.configDir = cfgDir
	a.serviceCfg = svcCfg
	a.trayIcons = trayIcons{png: opts.TrayPNG, ico: opts.TrayICO}

	a.wails = application.New(application.Options{
		Name: "SnmpLens",
		// Every exported method of App is bound to the renderer. The lifecycle
		// is the second service, and has no method the renderer can call.
		Services: []application.Service{
			application.NewService(a),
			application.NewService(&lifecycle{a}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(opts.Assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.wasabules.snmplens",
			// Relaunching is how you get back to a hidden instance when the
			// desktop has no usable tray.
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				a.RevealWindow()
			},
		},
	})

	a.window = a.wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "SnmpLens",
		Width:            1024,
		Height:           768,
		URL:              startURL(),
		BackgroundColour: application.NewRGB(27, 38, 54),
		// Launching straight to the tray, for a login item. If no tray
		// materialises, startup shows the window anyway rather than leaving a
		// process the user cannot reach.
		Hidden:         svcCfg.StartHidden,
		EnableFileDrop: true,
	})
	// Closing is decided here, late, and not by an option fixed before we know
	// whether a tray icon actually appeared: an app that refuses to close with no
	// tray to quit from is unusable.
	a.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if a.hideInsteadOfClosing() {
			a.window.Hide()
			e.Cancel()
		}
	})
	a.window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		a.emit("files:dropped", e.Context().DroppedFiles())
	})

	return a.wails.Run()
}

// startURL keeps the page at the origin it has always had. localStorage belongs
// to an origin, and localStorage is where the settings, the targets, the sealed
// credentials and the history live. Wails 2 loaded wails://wails/ on macOS and
// Linux; Wails 3's default is wails://localhost/, a different origin, so an
// update would have opened on an empty application with everything still on disk
// out of its reach. Windows is http://wails.localhost/ in both. A development
// server has an origin of its own and nothing to keep.
func startURL() string {
	if goruntime.GOOS == "windows" || os.Getenv("FRONTEND_DEVSERVER_URL") != "" {
		return ""
	}
	return "wails://wails/"
}

// lifecycle is App's startup and shutdown, apart from App: a service's hooks are
// exported methods, and nothing the renderer can reach should be able to call
// them or be confused with them.
type lifecycle struct{ app *App }

// ServiceStartup runs before any window exists.
func (l *lifecycle) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	l.app.startup(ctx)
	return nil
}

// ServiceShutdown runs once the windows are gone.
func (l *lifecycle) ServiceShutdown() error {
	l.app.shutdown()
	return nil
}
