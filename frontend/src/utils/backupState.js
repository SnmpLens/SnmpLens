/**
 * What the interface keeps, for a backup: the settings and the few other things
 * the person built up here. Not the layout of the window, and not anything
 * that is history (traps, operations) rather than configuration.
 *
 * The credentials are taken out of the settings (crypto.js takeSecrets): Go
 * seals them under the backup's passphrase, and on the way back they are sealed
 * again under this machine's key before anything is written.
 *
 * Pure apart from the storage and the sealing it is given, so tests run it under
 * node.
 */
import { takeSecrets, giveSecrets } from './crypto';

/** The localStorage keys a backup carries beside the settings, and what each holds. */
export const CARRIED = {
  snmplens_saved_queries: 'array',
  snmplens_favorites: 'array',
  snmplens_paths: 'object',
  'snmplens.mibEditor.mutedRules': 'array',
  snmpMonitorView: 'object',
};

const RESTORE_REPORT = 'snmplens.restoreReport';

function kindOf(v) {
  if (Array.isArray(v)) return 'array';
  return v !== null && typeof v === 'object' ? 'object' : typeof v;
}

/** The renderer's part of a backup, and its credentials apart. */
export function collectRendererState(settings, storage) {
  const { settings: bare, secrets } = takeSecrets(settings);
  const stores = {};
  for (const [key, kind] of Object.entries(CARRIED)) {
    try {
      const raw = storage.getItem(key);
      if (raw === null) continue;
      const value = JSON.parse(raw);
      if (kindOf(value) === kind) stores[key] = value;
    } catch {
      // A value this version cannot read is left out rather than carried.
    }
  }
  return { state: { version: 1, settings: bare, stores }, secrets };
}

/**
 * The parts of a backup's renderer state this version accepts. It came from a
 * file, so every part is checked for the shape the store reading it expects,
 * and anything else is dropped: a store handed the wrong shape fails on load.
 */
export function acceptedState(state) {
  if (kindOf(state) !== 'object' || state.version !== 1) return null;
  const out = { settings: null, stores: {} };
  if (kindOf(state.settings) === 'object') out.settings = state.settings;
  if (kindOf(state.stores) === 'object') {
    for (const [key, kind] of Object.entries(CARRIED)) {
      if (key in state.stores && kindOf(state.stores[key]) === kind) out.stores[key] = state.stores[key];
    }
  }
  return out;
}

/**
 * Writes a restored state where the stores read it at the next load. seal is
 * crypto.js encryptSettings; canSeal says whether this machine's credential
 * store is open, since encryptSettings quietly writes blanks when it is not.
 * Returns the problems, as i18n key suffixes.
 */
export async function applyRendererState(state, secrets, storage, seal, canSeal) {
  const accepted = acceptedState(state);
  if (!accepted) return ['rendererUnreadable'];
  const problems = [];
  for (const [key, value] of Object.entries(accepted.stores)) {
    storage.setItem(key, JSON.stringify(value));
  }
  if (accepted.settings) {
    const hasSecrets = secrets && Object.keys(secrets).length > 0;
    if (hasSecrets && !canSeal) problems.push('notSealed');
    try {
      const full = giveSecrets(accepted.settings, canSeal ? secrets : {});
      storage.setItem('settings', JSON.stringify(await seal(full, null)));
    } catch {
      // Never the credentials in the clear: the settings without them.
      storage.setItem('settings', JSON.stringify(takeSecrets(accepted.settings).settings));
      if (hasSecrets && !problems.includes('notSealed')) problems.push('notSealed');
    }
  }
  return problems;
}

/** Keeps what a restore reports across the reload that applies it. */
export function keepRestoreReport(report, session) {
  try {
    session.setItem(RESTORE_REPORT, JSON.stringify(report));
  } catch {
    // Without session storage the report is lost and the restore is not.
  }
}

/** The report a restore left before reloading, once. */
export function takeRestoreReport(session) {
  try {
    const raw = session.getItem(RESTORE_REPORT);
    session.removeItem(RESTORE_REPORT);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}
