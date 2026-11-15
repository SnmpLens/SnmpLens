package app

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"SnmpLens/pkg/service"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/runtime"
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
	// WindowIcon is the Linux window's icon, 256 pixels square. Not the
	// tray's: GDK drops an icon too large for one X11 request without a word,
	// and the tray's 508x517 is — measured under Xwayland, the window then had
	// no _NET_WM_ICON at all.
	WindowIcon []byte
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

	// Before GTK starts, for X11's WM_CLASS (gtk_linux.go).
	setProgramName(programName())

	return wails.Run(&options.App{
		Title:  "SnmpLens",
		Width:  1024,
		Height: 768,
		// Zero on Windows and macOS; windowmax_linux.go says why not on Linux.
		MaxWidth:  windowMaxWidth,
		MaxHeight: windowMaxHeight,
		AssetServer: &assetserver.Options{
			Assets: opts.Assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		Linux: &linux.Options{
			// What the desktop knows the window by (windowid.go).
			ProgramName: programName(),
			// The icon an X11 window shows where no desktop file names it.
			Icon: opts.WindowIcon,
			// What Wails uses when Linux is nil, as it was: given options it
			// would switch to on-demand acceleration, which wailsapp/wails#2977
			// turned off for drawing nothing on some drivers.
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
		OnStartup:  a.startup,
		OnShutdown: a.shutdown,
		// Launching straight to the tray, for a login item. If no tray
		// materialises, startup shows the window anyway rather than leaving a
		// process the user cannot reach.
		StartHidden: svcCfg.StartHidden,
		// HideWindowOnClose is deliberately NOT set. It is fixed here, before
		// we know whether a tray icon actually appeared, and an app that
		// refuses to close with no tray to quit from is unusable. OnBeforeClose
		// makes the same decision later, when the answer is known.
		OnBeforeClose: func(ctx context.Context) bool {
			if a.hideInsteadOfClosing() {
				runtime.WindowHide(ctx)
				return true // swallow the close
			}
			return false
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.wasabules.snmplens",
			// Relaunching is how you get back to a hidden instance when the
			// desktop has no usable tray.
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				a.RevealWindow()
			},
		},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
		},
		Bind: []interface{}{
			a,
		},
	})
}
