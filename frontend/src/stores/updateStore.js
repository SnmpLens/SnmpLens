import { writable } from 'svelte/store';
import {
  CheckForUpdate,
  DownloadAndApplyUpdate,
  GetAppVersion,
} from '../bridge/App';
import { EventsOn } from '../bridge/runtime';

// Central state for the auto-update flow. Mirrors updater.UpdateInfo plus UI state.
const initial = {
  currentVersion: '',
  available: false,
  checking: false,
  latestVersion: '',
  releaseNotes: '',
  releaseUrl: '',
  assetUrl: '',
  canSelfApply: false,
  downloading: false,
  progress: 0,
  error: null,
};

const strip = (v) => (v || '').replace(/^v/i, '');

function createUpdateStore() {
  const { subscribe, update } = writable({ ...initial });

  // Download progress emitted by the Go backend (0-100).
  EventsOn('update:progress', (pct) => {
    if (typeof pct === 'number') {
      update((s) => ({ ...s, progress: pct }));
    }
  });

  async function loadVersion() {
    try {
      const v = await GetAppVersion();
      update((s) => ({ ...s, currentVersion: strip(v) }));
    } catch (_) {
      /* ignore */
    }
  }

  // Query GitHub for a newer release. Returns the raw UpdateInfo (or null on error).
  // `silent` swallows errors (used for the automatic startup check).
  async function check({ silent = true } = {}) {
    update((s) => ({ ...s, checking: true, error: null }));
    try {
      const info = await CheckForUpdate();
      update((s) => ({
        ...s,
        checking: false,
        // A check that could not be answered is an ERROR, not "up to date".
        // Go returns no error across the bridge — it returns UpdateInfo with
        // the reason in .error — so this is the only place the difference
        // exists.
        error: info.error || null,
        currentVersion: strip(info.currentVersion) || s.currentVersion,
        available: !!info.available,
        latestVersion: strip(info.latestVersion),
        releaseNotes: info.releaseNotes || '',
        releaseUrl: info.releaseUrl || '',
        assetUrl: info.assetUrl || '',
        canSelfApply: !!info.canSelfApply,
      }));
      return info;
    } catch (e) {
      update((s) => ({ ...s, checking: false, error: String(e) }));
      if (!silent) throw e;
      return null;
    }
  }

  // Download + verify + apply (self-replace/installer), or open the browser for
  // formats we can't self-apply. On a successful self-apply the app relaunches.
  async function apply() {
    update((s) => ({ ...s, downloading: true, progress: 0, error: null }));
    try {
      await DownloadAndApplyUpdate();
      update((s) => ({ ...s, downloading: false, available: false }));
    } catch (e) {
      update((s) => ({ ...s, downloading: false, error: String(e) }));
    }
  }

  // Hide the banner until the next check ("Later").
  function dismiss() {
    update((s) => ({ ...s, available: false }));
  }

  return { subscribe, loadVersion, check, apply, dismiss };
}

export const updateStore = createUpdateStore();
