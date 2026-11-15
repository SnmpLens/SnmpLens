//go:build darwin

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// label is the reverse-DNS identifier launchd uses, and also the plist name.
const label = "com.wasabules.snmplens"

var errUnsupported = errors.New("autostart: not supported on this platform")

func supported() bool { return true }

// keepRunning: only a Flatpak asks the desktop to keep running with no window.
func keepRunning() (bool, error) { return false, errUnsupported }

func plistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func location() string {
	if p := plistPath(); p != "" {
		return p
	}
	return "~/Library/LaunchAgents/" + label + ".plist"
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// Inside a .app bundle the binary sits in Contents/MacOS. launchd is
	// perfectly happy to run it directly, and doing so avoids depending on
	// `open`, which detaches and loses the exit status.
	return exe, nil
}

// escapeXML keeps a path containing & or < from producing a plist that launchd
// silently refuses to load.
func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
	).Replace(s)
}

func isEnabled() (bool, string, error) {
	path := plistPath()
	if path == "" {
		return false, "", fmt.Errorf("cannot determine the home directory")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("read %s: %w", path, err)
	}
	// Report the registered command rather than the whole plist, so the
	// settings screen can show something a person can read.
	return true, programArgument(string(raw)), nil
}

// programArgument pulls the first <string> of the ProgramArguments array out of
// a LaunchAgent plist.
//
// It has to look for that key. Taking the document's first <string> — which is
// what this did — returns the LABEL, because <key>Label</key> comes first in
// every plist we write. So the settings screen offered `com.wasabules.snmplens`
// as the command that runs at login: the one thing on that screen whose whole
// purpose is to say WHAT starts, saying instead what the entry is called.
//
// Never caught because pkg/autostart's tests ran on Linux only, where this file
// is not even compiled.
func programArgument(plist string) string {
	const key = "<key>ProgramArguments</key>"
	i := strings.Index(plist, key)
	if i < 0 {
		return ""
	}
	rest := plist[i+len(key):]
	start := strings.Index(rest, "<string>")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return unescapeXML(rest[:end])
}

// unescapeXML reverses escapeXML, so a path containing & or < comes back as it
// was written rather than as its entity.
func unescapeXML(s string) string {
	r := strings.NewReplacer(
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&amp;", "&",
	)
	return r.Replace(s)
}

func enable() error {
	path := plistPath()
	if path == "" {
		return fmt.Errorf("cannot determine the home directory")
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	// RunAtLoad only; deliberately NOT KeepAlive. KeepAlive would relaunch the
	// app every time the user quits it, which is indistinguishable from
	// malware from the user's point of view.
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, escapeXML(label), escapeXML(exe))

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

func disable() error {
	path := plistPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
