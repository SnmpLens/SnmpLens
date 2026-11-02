---
name: release
description: Cut and publish a new SnmpLens release (version tag + GitHub release) so the in-app auto-updater announces it. Use whenever the user asks to release, publish a version, bump the version, or make the updater see a new version.
---

# Releasing SnmpLens

Follow these conventions **exactly** — they must match the existing releases (see `gh release list`).

## 1. Version number (semver `vMAJOR.MINOR.PATCH`)

- Find the latest version: `git tag --list 'v*' | sort -V | tail -1`.
- **Minor bump** (`v1.X.0`) when the release adds features. **Patch bump** (`v1.X.Y`) for fixes/polish only. Major bump only for breaking changes.
- The git tag is the source of truth: the build injects it via ldflags (`-X SnmpLens/pkg/updater.Version=<tag>`) and the NSIS installer version is substituted from it. No source file needs a manual version edit.

## 2. Release name and notes — NON-NEGOTIABLE

- **Release title/name = the tag verbatim**, e.g. `v1.4.0`. Never `SnmpLens v1.4.0` or anything else.
- **Notes are written in English**, complete, and cover **everything since the last tag** (`git log <lastTag>..HEAD --oneline`). Do not under-report — list all user-facing changes.
- Body format (match previous releases):
  ```
  ## What's new in vX.Y.Z

  ### Features
  - ...

  ### Fixes
  - ...

  ### Under the hood        (optional: refactors/internal)
  - ...
  ```

## 3. Authorship — NON-NEGOTIABLE

- Commits and the release are authored by **Wasabules only**. **Never** add a `Co-Authored-By: Claude` trailer, and never let Claude appear as author.
- The GitHub release must be **owned by Wasabules**, not `github-actions[bot]`. This is why we create the release with `gh` (as the authenticated Wasabules) rather than letting the workflow create it.

## 4. Procedure

1. **Verify it builds** (CI gate) from the repo root:
   ```bash
   go vet -tags gtk3 ./...
   staticcheck ./...
   (cd frontend && npm run build)
   ```
2. **Push main**: `git push origin main`.
3. **Tag and push the tag** — this triggers `.github/workflows/release.yml`:
   ```bash
   git tag vX.Y.Z && git push origin vX.Y.Z
   ```
4. **Immediately create the release as Wasabules** (write the changelog to a file first):
   ```bash
   gh release create vX.Y.Z --title "vX.Y.Z" --notes-file <changelog> --latest
   ```
   The workflow's `release` job has `needs: build`, so it waits for the multi-platform build (minutes) — creating the release now wins the race and locks Wasabules as the author.
5. **Watch the workflow to completion**: `gh run watch <run-id> --exit-status`. Confirm the `build` matrix and `release` jobs succeed.
6. **Verify assets**: `gh release view vX.Y.Z --json assets -q '.assets[].name'` must list all binaries plus `SnmpLens-checksums.txt` and `SnmpLens-checksums.txt.sig`.
7. **Re-assert the notes** — the workflow's `softprops/action-gh-release` runs with `generate_release_notes: true` and may overwrite the body. After the run finishes, fix title + notes:
   ```bash
   gh release edit vX.Y.Z --title "vX.Y.Z" --notes-file <changelog> --latest
   ```
   Then re-check `gh release view vX.Y.Z --json name,body`.

## 5. Auto-updater expectations

- The release must be marked **Latest** (`--latest`) — the updater queries the latest release.
- Checksums are **Ed25519-signed** in CI (secret `UPDATER_PRIVATE_KEY`) and verified against the public key embedded in `pkg/updater/verify.go`. Don't change the key without re-signing.
- winget auto-submission (`.github/workflows/release.yml` `winget` job) only runs when repo var `WINGET_ENABLED=true`; it is skipped otherwise.
