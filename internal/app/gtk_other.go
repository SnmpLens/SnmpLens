//go:build !linux || !(production || dev)

package app

// No GTK here, or not in this build (gtk_linux.go).

func setGTKPreferDark(bool) {}

func setProgramName(string) {}
