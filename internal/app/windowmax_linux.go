//go:build linux

package app

// Wails reads a MaxWidth or MaxHeight of 0 — no limit — as the size of the
// monitor the window is on, and imposes THAT as a hard maximum (SetMinMaxSize
// in internal/frontend/desktop/linux/window.c, Wails v2.17.1). It looks once,
// before the window is shown, and never again. On Wayland a surface that has
// entered no output yet is answered with the monitor at the origin, so the
// window could not grow past that one wherever it went: a laptop panel at 0,0
// capped it on every larger external monitor (issue #189).
//
// Reproduced in a headless GNOME Shell with a 1920x1080 monitor at the origin
// and a 2560x1440 one beside it: the window asked for set_max_size(1920, 1117)
// and, maximised on the larger monitor, stayed 1920x1117.
//
// So the limit is given rather than left to Wails, and set where no monitor
// reaches. Linux only: Windows and macOS read 0 as no limit, and a value there
// would add one. Wails v3 sets no hint for a limit nobody configured, so this
// goes with the migration.
const windowMaxWidth, windowMaxHeight = 32767, 32767
