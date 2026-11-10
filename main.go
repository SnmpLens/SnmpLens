package main

import (
	"embed"
	"os"

	"SnmpLens/internal/app"
	"SnmpLens/internal/cli"
)

// Everything the binary carries is embedded here and only here: //go:embed
// cannot reach above the directory of the file it sits in, and frontend/dist,
// mibs/, presets/ and build/ all sit at the root. The application itself is
// internal/app.

//go:embed all:frontend/dist
var assets embed.FS

//go:embed mibs
var mibs embed.FS

// The example presets, extracted on FIRST RUN only — see ensureBundledPresets.
//
//go:embed presets
var presets embed.FS

// Tray artwork. Windows needs an .ico; the other desktops want a .png. Both
// are a couple of kilobytes, so embedding both beats a build-tagged file.
//
//go:embed build/appicon.png
var iconPNG []byte

//go:embed build/windows/icon.ico
var iconICO []byte

func main() {
	// The supervision with no window, and the help: before anything of the
	// desktop application exists.
	if handled, code := cli.Run(os.Args[1:]); handled {
		os.Exit(code)
	}
	err := app.Run(app.Options{
		Assets:  assets,
		MIBs:    mibs,
		Presets: presets,
		TrayPNG: iconPNG,
		TrayICO: iconICO,
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
