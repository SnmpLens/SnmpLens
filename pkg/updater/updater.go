// Package updater implements assisted in-app updates backed by GitHub Releases.
//
// The flow is: CheckForUpdate() queries the repository's latest release through
// the GitHub API and compares its tag against the build-time Version. If a newer
// release exists, DownloadAndApply() fetches the platform-appropriate asset,
// verifies its SHA-256 against the release checksums file, and applies it
// (self-replace + relaunch, launch installer, or open the browser as a
// fallback — see the platform-specific apply_*.go files).
package updater

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// applyMode describes how a downloaded update is applied on the current platform.
type applyMode int

const (
	applyReplace   applyMode = iota // self-replace the running binary, then relaunch
	applyInstaller                  // run the downloaded installer, then quit
	applyBrowser                    // open the asset URL in the browser (no download)
)

// UpdateInfo is the result of a check. It crosses the Wails bridge to the
// frontend, so every exported field is serialized to JSON.
type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseNotes   string `json:"releaseNotes"`
	ReleaseURL     string `json:"releaseUrl"`
	PublishedAt    string `json:"publishedAt"`
	AssetName      string `json:"assetName"`
	AssetURL       string `json:"assetUrl"`
	// CanSelfApply is true when the update can be installed from within the app
	// (self-replace or installer). When false the frontend offers a plain
	// "Download" button that opens the browser.
	CanSelfApply bool `json:"canSelfApply"`

	// Error says why the check could not be answered.
	//
	// It exists because App.CheckForUpdate returns no error across the bridge:
	// a failed check produced the zero value, Available was false, and the
	// renderer said "You're on the latest version". Every reason a check fails
	// — no network, GitHub rate-limiting, a proxy, a refused response — was
	// reported to the operator as reassurance. In a product whose updates carry
	// security fixes, "I could not ask" and "there is nothing new" are not the
	// same answer, and the difference is exactly the one an operator needs.
	Error string `json:"error,omitempty"`
}

// pending caches the details needed by DownloadAndApply between the check and
// the user confirming the install.
type pending struct {
	version        string
	assetURL       string
	assetName      string
	checksumURL    string
	checksumSigURL string
	mode           applyMode
}

// Service checks for and applies updates for a single GitHub repository.
type Service struct {
	owner  string
	repo   string
	client *http.Client

	mu      sync.Mutex
	host    Host
	pending *pending
}

// NewService creates an updater for github.com/<owner>/<repo>.
func NewService(owner, repo string) *Service {
	return &Service{
		owner:  owner,
		repo:   repo,
		client: &http.Client{}, // per-request context deadlines; downloads may be large
	}
}

// Host is what applying an update asks of the application around it.
type Host struct {
	// OpenURL sends the operator to a page in their browser.
	OpenURL func(url string)
	// Progress reports a download's progress in whole percent.
	Progress func(percent int)
	// Quit ends the application so that what replaces it can start.
	Quit func()
}

// SetHost says how to reach the application. Set it before any update is applied.
func (s *Service) SetHost(h Host) {
	s.mu.Lock()
	s.host = h
	s.mu.Unlock()
}

func (s *Service) currentHost() Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.host
}

// CheckForUpdate queries the latest release and reports whether it is newer than
// the running build. Dev/local builds always report "no update".
func (s *Service) CheckForUpdate() (UpdateInfo, error) {
	info := UpdateInfo{CurrentVersion: displayVersion(Version)}

	if isDevVersion() {
		return info, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rel, err := s.latestRelease(ctx)
	if err != nil {
		return info, err
	}

	info.LatestVersion = displayVersion(rel.TagName)
	info.ReleaseNotes = strings.TrimSpace(rel.Body)
	info.ReleaseURL = rel.HTMLURL
	info.PublishedAt = rel.PublishedAt

	if !isNewer(Version, rel.TagName) {
		return info, nil // already up to date
	}

	assetName, mode := target()
	assetURL := rel.assetURL(assetName)
	if assetURL == "" {
		return info, fmt.Errorf("release %s has no asset %q for this platform", rel.TagName, assetName)
	}

	info.Available = true
	info.AssetName = assetName
	info.AssetURL = assetURL
	info.CanSelfApply = mode != applyBrowser

	s.mu.Lock()
	s.pending = &pending{
		version:        rel.TagName,
		assetURL:       assetURL,
		assetName:      assetName,
		checksumURL:    rel.assetURL(checksumsAsset),
		checksumSigURL: rel.assetURL(checksumsAsset + ".sig"),
		mode:           mode,
	}
	s.mu.Unlock()

	return info, nil
}

// isNewer reports whether latest is a strictly greater semver than current.
// Non-semver inputs are treated conservatively (no update).
func isNewer(current, latest string) bool {
	c, l := normalizeVersion(current), normalizeVersion(latest)
	if !semver.IsValid(c) || !semver.IsValid(l) {
		return false
	}
	return semver.Compare(c, l) < 0
}

// normalizeVersion ensures a leading "v" so golang.org/x/mod/semver accepts it.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if v[0] != 'v' && v[0] != 'V' {
		return "v" + v
	}
	return "v" + strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
}

// displayVersion strips a leading "v" for user-facing display.
func displayVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
