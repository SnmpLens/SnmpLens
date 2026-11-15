//go:build linux && (production || dev)

package app

// Built only with the tags `wails build` and `wails dev` give, as Wails's own
// GTK code is: `go build`, `go vet` and `go test` then need no GTK headers.

/*
#cgo pkg-config: gtk+-3.0
#include <stdlib.h>
#include <gtk/gtk.h>

static gboolean snmplensApplyPreferDark(gpointer data) {
	GtkSettings *settings = gtk_settings_get_default();
	if (settings != NULL) {
		g_object_set(settings, "gtk-application-prefer-dark-theme", GPOINTER_TO_INT(data), NULL);
	}
	return G_SOURCE_REMOVE;
}

// GTK belongs to the thread running its main loop; g_idle_add is how any other
// thread hands it work.
static void snmplensPreferDark(int dark) {
	g_idle_add(snmplensApplyPreferDark, GINT_TO_POINTER(dark));
}
*/
import "C"

import "unsafe"

// setGTKPreferDark asks GTK for its theme's dark variant, or not. WebKitGTK
// answers prefers-color-scheme from it, and the title bar is drawn with it.
func setGTKPreferDark(dark bool) {
	v := 0
	if dark {
		v = 1
	}
	C.snmplensPreferDark(C.int(v))
}

// setProgramName names the program before GTK starts. Wails's ProgramName
// sets it too, but after gtk_init and after creating the window: soon enough
// for the Wayland app_id, which is read later, and too late for X11, whose
// WM_CLASS GDK has fixed by then — measured under Xwayland, a binary called
// SnmpLens-renamed-copy kept that as its class.
func setProgramName(name string) {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	C.g_set_prgname(cs)
}
