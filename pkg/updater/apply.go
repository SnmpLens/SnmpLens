package updater

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/minio/selfupdate"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DownloadAndApply downloads the pending update's asset, verifies its SHA-256
// against the release checksums file, and applies it. For platforms/install
// methods that cannot be updated in place (macOS, Linux .deb) it simply opens
// the asset URL in the browser.
//
// On a successful self-apply the application relaunches and quits, so callers
// should not expect this method to return in that case.
// releaseHosts are the hosts a GitHub release asset is served from.
//
// browser_download_url is github.com; the API also hands out
// objects.githubusercontent.com for the same bytes, and both are accepted so a
// change on GitHub's side does not break updating.
var releaseHosts = map[string]bool{
	"github.com":                           true,
	"www.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// checkReleaseHost refuses a download address that is not GitHub's.
func checkReleaseHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the release names a download address that cannot be parsed")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("refusing to open the download over %q; it must be https", u.Scheme)
	}
	if !releaseHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("refusing to open a download from %q: a release asset is served "+
			"from GitHub, and this address is not", u.Hostname())
	}
	return nil
}

func (s *Service) DownloadAndApply() error {
	s.mu.Lock()
	p := s.pending
	s.mu.Unlock()
	if p == nil {
		return fmt.Errorf("no update ready; run a check first")
	}

	ctx := s.context()

	// Fallback: let the OS/browser handle formats we can't self-apply.
	//
	// This path applies nothing — the operator downloads the .dmg or .deb over
	// HTTPS and installs it by hand — so it is not the "install an unverified
	// binary" the self-apply path would be. What it DOES do is send the
	// operator to an address taken from the GitHub JSON, and that address is
	// the only thing here nobody checks. Anyone able to change what the app
	// reads back could point it at any host at all, and the operator would
	// have every reason to trust the destination: the application sent them.
	//
	// The self-apply path does not need this, because a redirected asset fails
	// the checksum from the signed manifest. This one has no such backstop, so
	// the host is checked instead.
	if p.mode == applyBrowser {
		if err := checkReleaseHost(p.assetURL); err != nil {
			return err
		}
		wruntime.BrowserOpenURL(ctx, p.assetURL)
		return nil
	}

	if p.checksumURL == "" {
		return fmt.Errorf("release %s is missing %s; cannot verify the download", p.version, checksumsAsset)
	}

	wantSum, err := s.verifiedChecksum(ctx, p.checksumURL, p.checksumSigURL, p.assetName, p.version)
	if err != nil {
		return err
	}

	tmpPath, gotSum, err := s.download(ctx, p.assetURL, p.assetName)
	if tmpPath != "" {
		defer os.Remove(tmpPath)
	}
	if err != nil {
		return fmt.Errorf("downloading update: %w", err)
	}

	if !strings.EqualFold(gotSum, wantSum) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", wantSum, gotSum)
	}

	switch p.mode {
	case applyReplace:
		return s.applyBinary(ctx, tmpPath)
	case applyInstaller:
		return s.runInstaller(ctx, tmpPath)
	default:
		return fmt.Errorf("unsupported apply mode")
	}
}

// download streams url to a temp file named after asset, emitting progress
// events, and returns the temp path and the file's hex SHA-256.
func (s *Service) download(ctx context.Context, url, asset string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "SnmpLens-updater")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "snmplens-update-*-"+asset)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	h := sha256.New()
	pr := &progressReader{
		ctx:    ctx,
		reader: resp.Body,
		total:  resp.ContentLength,
	}
	if _, err := io.Copy(io.MultiWriter(f, h), pr); err != nil {
		return f.Name(), "", err
	}

	wruntime.EventsEmit(ctx, "update:progress", 100)
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// verifiedChecksum downloads the checksums manifest and, when a public key is
// embedded, its Ed25519 signature; it verifies authenticity and returns the
// SHA-256 recorded for the given asset. This is the trust anchor: once the
// manifest is authenticated, the asset is trusted by its SHA-256.
func (s *Service) verifiedChecksum(ctx context.Context, checksumURL, sigURL, asset, version string) (string, error) {
	manifest, err := s.fetchBytes(ctx, checksumURL)
	if err != nil {
		return "", fmt.Errorf("fetching checksums: %w", err)
	}

	if signatureEnforced() {
		if sigURL == "" {
			return "", fmt.Errorf("release is missing %s.sig; refusing an unsigned update", checksumsAsset)
		}
		sig, err := s.fetchBytes(ctx, sigURL)
		if err != nil {
			return "", fmt.Errorf("fetching signature: %w", err)
		}
		if err := VerifySignature(manifest, sig); err != nil {
			return "", err
		}
		// AFTER the signature, not before: an unsigned manifest's version line
		// is worth nothing, and reporting "wrong release" for a forged file
		// would say the wrong thing about what is wrong with it.
		if err := checkManifestVersion(manifest, version); err != nil {
			return "", err
		}
	}

	return parseChecksum(manifest, asset)
}

// fetchBytes downloads a small resource fully into memory (1 MiB cap).
func (s *Service) fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SnmpLens-updater")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// parseChecksum extracts the hex SHA-256 for asset from a `sha256sum`-format
// manifest ("<hex>  <name>", with an optional "*" binary-mode prefix on name).
func parseChecksum(manifest []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(manifest))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// The `version v1.2.3` header has two fields like every checksum entry,
		// so without this it reaches the match below and answers "version" as
		// the SHA-256 of an asset named after the tag. Nothing is named that, so
		// it was harmless — but a parser that only works because no file has a
		// particular name is one bad release-asset name from being wrong.
		if strings.HasPrefix(line, manifestVersionPrefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum entry for %s", asset)
}

// applyBinary self-replaces the running executable with the downloaded binary,
// then relaunches it and quits. Used for the portable/raw-binary distributions.
func (s *Service) applyBinary(ctx context.Context, binPath string) error {
	f, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := selfupdate.Apply(f, selfupdate.Options{}); err != nil {
		// Best-effort rollback of the partial replacement.
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return fmt.Errorf("update failed and rollback also failed: %w", rerr)
		}
		return fmt.Errorf("applying update: %w", err)
	}

	s.relaunch(ctx)
	return nil
}

// runInstaller launches the downloaded installer detached, then quits so it can
// replace the running files. Used for the Windows NSIS distribution.
func (s *Service) runInstaller(ctx context.Context, installerPath string) error {
	cmd := exec.Command(installerPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching installer: %w", err)
	}
	// Do not Wait: the installer outlives us.
	s.quitApp(ctx)
	return nil
}

// AfterUpdateFlag, followed by a process ID, tells a relaunched copy to wait
// for that process to exit before it opens its window (see internal/cli).
const AfterUpdateFlag = "--after-update"

// relaunch starts a fresh copy of the (now updated) executable and quits.
//
// The fresh copy is told to wait for this one. The single-instance lock is
// held until this process exits, and a second instance that finds it held
// hands its arguments over and exits: started without waiting — the copy
// used to be given 300 ms, and shutting the engine down takes longer — the
// updated copy vanished at once and, once this one had quit, nothing was
// running.
func (s *Service) relaunch(ctx context.Context) {
	if exe, err := os.Executable(); err == nil {
		_ = exec.Command(exe, AfterUpdateFlag, strconv.Itoa(os.Getpid())).Start()
	}
	s.quitApp(ctx)
}

// cleanupLeftoverOld removes the previous executable a Windows self-replace
// leaves beside the new one: minio/selfupdate renames the running file aside
// and cannot delete it while it runs, so it hides it instead, and it lingers
// until the next launch removes it.
func cleanupLeftoverOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir, base := filepath.Split(exe)
	_ = os.Remove(exe + ".old")
	_ = os.Remove(filepath.Join(dir, "."+base+".old"))
}

// progressReader wraps a reader and emits throttled "update:progress" events
// (integer percent) as bytes flow through.
type progressReader struct {
	ctx      context.Context
	reader   io.Reader
	total    int64
	read     int64
	lastEmit int
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		pct := int(p.read * 100 / p.total)
		if pct != p.lastEmit && pct >= 0 && pct <= 100 {
			p.lastEmit = pct
			wruntime.EventsEmit(p.ctx, "update:progress", pct)
		}
	}
	return n, err
}
