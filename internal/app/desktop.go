package app

import (
	"context"
	"errors"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// What App asks of the desktop, in one place. Every one of these answers
// sensibly with no window at all, because the tests build an App without one.

var errNoWindow = errors.New("no window")

// emit tells the renderer something happened. With no application it says
// nothing, the way a headless snmp.Client receives traps without emitting.
func (a *App) emit(name string, data ...any) {
	if a.wails != nil {
		a.wails.Event.Emit(name, data...)
	}
}

// lifetime is cancelled when the application stops.
func (a *App) lifetime() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (a *App) openBrowser(url string) {
	if a.wails == nil {
		return
	}
	if err := a.wails.Browser.OpenURL(url); err != nil {
		log.Printf("could not open %s in the browser: %v", url, err)
	}
}

func fileFilter(name, pattern string) application.FileFilter {
	return application.FileFilter{DisplayName: name, Pattern: pattern}
}

func (a *App) openDialog(title string, filters []application.FileFilter) (*application.OpenFileDialogStruct, error) {
	if a.wails == nil {
		return nil, errNoWindow
	}
	return a.wails.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title:          title,
		CanChooseFiles: true,
		Filters:        filters,
		Window:         a.window,
	}), nil
}

// openFiles asks for any number of files. Cancelling is no paths and no error.
func (a *App) openFiles(title string, filters ...application.FileFilter) ([]string, error) {
	d, err := a.openDialog(title, filters)
	if err != nil {
		return nil, err
	}
	return d.PromptForMultipleSelection()
}

// openFile asks for one file. Cancelling is an empty path and no error.
func (a *App) openFile(title string, filters ...application.FileFilter) (string, error) {
	d, err := a.openDialog(title, filters)
	if err != nil {
		return "", err
	}
	return d.PromptForSingleSelection()
}

// openFolder asks for a directory. Cancelling is an empty path and no error.
func (a *App) openFolder(title string) (string, error) {
	if a.wails == nil {
		return "", errNoWindow
	}
	return a.wails.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title:                title,
		CanChooseDirectories: true,
		CanCreateDirectories: true,
		Window:               a.window,
	}).PromptForSingleSelection()
}

// saveFile asks where to write. Cancelling is an empty path and no error.
func (a *App) saveFile(title, name string, filters ...application.FileFilter) (string, error) {
	if a.wails == nil {
		return "", errNoWindow
	}
	return a.wails.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:                title,
		Filename:             name,
		CanCreateDirectories: true,
		Filters:              filters,
		Window:               a.window,
	}).PromptForSingleSelection()
}
