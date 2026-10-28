package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenFolder shows a directory in the system's file manager: the MIB folder,
// or one the user added to the MIB paths.
//
// A DIRECTORY, and nothing else. This is reachable from the renderer, and every
// one of the commands below, handed a file, opens it with its default handler —
// which for an executable is running it. So the path must be absolute and name
// a directory that exists, and the command gets it as one argument, never
// through a shell.
func (a *App) OpenFolder(path string) error {
	dir, err := folderToOpen(path)
	if err != nil {
		return err
	}
	argv := folderCommand(runtime.GOOS, dir)
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not open %s: %w", dir, err)
	}
	// Reaped, not awaited: explorer.exe exits 1 when it succeeds, and the file
	// manager it hands the folder to outlives it anyway.
	go func() { _ = cmd.Wait() }()
	return nil
}

// folderToOpen checks that path is a directory worth handing to a file manager.
func folderToOpen(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("no folder given")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s is not an absolute path", path)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("%s cannot be opened: %w", clean, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", clean)
	}
	return clean, nil
}

// macBundles are the directories Finder treats as one item: `open` on one of
// them LAUNCHES it rather than showing it.
var macBundles = []string{".app", ".bundle", ".framework", ".plugin", ".kext", ".prefpane", ".appex", ".xpc", ".service"}

// folderCommand is the command that shows dir in goos's file manager.
func folderCommand(goos, dir string) []string {
	switch goos {
	case "windows":
		return []string{"explorer.exe", dir}
	case "darwin":
		lower := strings.ToLower(strings.TrimRight(dir, "/"))
		for _, ext := range macBundles {
			if strings.HasSuffix(lower, ext) {
				// Revealed in its parent, selected, never run.
				return []string{"open", "-R", dir}
			}
		}
		return []string{"open", dir}
	default:
		return []string{"xdg-open", dir}
	}
}
