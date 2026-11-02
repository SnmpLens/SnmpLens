import { writable, get } from 'svelte/store';
import {
  SettingsSeal,
  SettingsOpen,
  SettingsAdoptKey,
  SettingsKeyStatus,
  SettingsForgetKey,
} from '../bridge/App';

/**
 * Sealing the credentials kept in localStorage.
 *
 * This file used to generate an AES-256-GCM key, export it as an EXTRACTABLE
 * JWK, and write it to the SAME localStorage as the ciphertext it protected.
 * Anyone who could read the WebView2 Local Storage folder had both, so the
 * encryption kept a credential out of a casual glance at the file and out of
 * nothing else.
 *
 * The key now lives in the OS credential store (pkg/secrets: DPAPI on Windows,
 * the Keychain on macOS, a 0600 file in a 0700 directory elsewhere) and the
 * renderer never holds it. The values stay here, sealed, in the same `enc:` +
 * base64(12-byte IV || GCM output) format as before — which is why an existing
 * user's blob keeps opening after the key moves, with nothing re-encrypted.
 *
 * What this does NOT do: the plaintext still lives in this process's memory
 * while the app runs, because every request builder reads it from the settings
 * store. Moving that too means the Go side resolving credentials by profile,
 * which is a different and much larger change. The banner in settings says so
 * rather than implying more.
 */

const KEY_STORAGE = '_snmplens_ek';
const PREFIX = 'enc:';

/**
 * Whether the stored credentials can be read.
 *
 *   'ok'       — sealed and opened normally
 *   'locked'   — a store exists and refused; the stored values are INTACT and
 *                must not be overwritten
 *   'nostore'  — no OS credential store here; this session works, nothing is
 *                saved
 */
export const credentialState = writable('ok');

/** The backend name to show, e.g. "windows-dpapi". */
export const credentialBackend = writable('');

function isEncrypted(value) {
  return typeof value === 'string' && value.startsWith(PREFIX);
}

// Paths to sensitive fields in the settings object
const SENSITIVE_PATHS = [
  ['community'],
  ['v3', 'authPass'],
  ['v3', 'privPass'],
];

/**
 * Every sensitive field of a settings object, as get/set pairs in a stable
 * order.
 *
 * One list, walked once, so sealing and opening cannot disagree about which
 * fields they cover — the previous code repeated the per-target walk in both
 * directions and a field added to one was easy to forget in the other.
 */
function fields(settings) {
  const out = [];
  for (const path of SENSITIVE_PATHS) {
    out.push({
      key: path.join('.'),
      get: () => getNestedValue(settings, path),
      set: (v) => setNestedValue(settings, path, v),
    });
  }
  for (const addr of Object.keys(settings.targetOverrides || {})) {
    const ov = settings.targetOverrides[addr];
    // Only fields the override actually HAS.
    //
    // A slot was pushed for every override whether or not it defined a
    // community, and writing the answer back unconditionally turned
    // `{ port: 1161 }` into `{ port: 1161, community: '' }`.
    // getEffectiveSettings tests `!== undefined`, so that empty string then
    // overrode the global community and the target authenticated with nothing.
    //
    // A key, not a position: restoring the previous blob has to survive a
    // target being added or removed while the store was locked.
    if ('community' in ov) {
      out.push({ key: `o:${addr}.community`, get: () => ov.community, set: (v) => { ov.community = v; } });
    }
    if (ov.v3) {
      if ('authPass' in ov.v3) {
        out.push({ key: `o:${addr}.v3.authPass`, get: () => ov.v3.authPass, set: (v) => { ov.v3.authPass = v; } });
      }
      if ('privPass' in ov.v3) {
        out.push({ key: `o:${addr}.v3.privPass`, get: () => ov.v3.privPass, set: (v) => { ov.v3.privPass = v; } });
      }
    }
  }
  // Credential profiles, keyed by the profile's ID — never its position and
  // never its name — for the reason the override keys are addresses: a profile
  // added, renamed or deleted while the store was locked must still get ITS
  // OWN sealed values back. Only the fields a profile has: normaliseProfile has
  // already dropped the credential of the other kind.
  const profiles = Array.isArray(settings.credentialProfiles) ? settings.credentialProfiles : [];
  for (const p of profiles) {
    if (!p || typeof p.id !== 'string') continue;
    if ('community' in p) {
      out.push({ key: `p:${p.id}.community`, get: () => p.community, set: (v) => { p.community = v; } });
    }
    if (p.v3) {
      if ('authPass' in p.v3) {
        out.push({ key: `p:${p.id}.v3.authPass`, get: () => p.v3.authPass, set: (v) => { p.v3.authPass = v; } });
      }
      if ('privPass' in p.v3) {
        out.push({ key: `p:${p.id}.v3.privPass`, get: () => p.v3.privPass, set: (v) => { p.v3.privPass = v; } });
      }
    }
  }
  return out;
}

async function refreshStatus() {
  try {
    const st = await SettingsKeyStatus();
    credentialBackend.set(st.backend || '');
    if (!st.available) {
      credentialState.set('nostore');
      return false;
    }
    if (st.error) {
      credentialState.set('locked');
      return false;
    }
    return true;
  } catch (e) {
    credentialState.set('nostore');
    return false;
  }
}

/**
 * Encrypt sensitive fields in a settings object (returns a deep clone).
 *
 * Throws when the values could not be sealed. The caller must then leave the
 * stored blob alone: writing an unsealed object over good ciphertext would put
 * the credentials in localStorage in the clear, which is worse than the
 * problem this file exists to solve.
 */
export async function encryptSettings(settings, previous) {
  const clone = JSON.parse(JSON.stringify(settings));
  const slots = fields(clone);

  // Decided by the STATE, never inferred from the values.
  //
  // When the store is locked, decryptSettings has already blanked the
  // in-memory credentials — so "every sensitive field is empty" is exactly
  // what a locked store looks like, and an earlier version of this function
  // read that as "nothing to seal", returned without calling the bridge and
  // without throwing, and let the caller write those blanks over the user's
  // intact ciphertext. That is credential loss caused by the guard meant to
  // prevent it.
  if (get(credentialState) !== 'ok') {
    carryForward(slots, previous);
    return clone;
  }

  const values = slots.map((s) => {
    const v = s.get();
    return typeof v === 'string' ? v : '';
  });

  const sealed = await SettingsSeal(values);
  slots.forEach((s, i) => {
    if (values[i] !== '' || sealed[i] !== '') s.set(sealed[i]);
  });
  return clone;
}

/**
 * Put the previously stored sealed values back, by key.
 *
 * The rest of the object — theme, targets, everything that is not a credential
 * — is written normally, so a locked keychain does not stop the app from
 * remembering anything at all. Only the credentials are left exactly as they
 * were on disk.
 */
function carryForward(slots, previous) {
  const stored = new Map();
  if (previous && typeof previous === 'object') {
    for (const s of fields(previous)) stored.set(s.key, s.get());
  }
  for (const s of slots) {
    const was = stored.get(s.key);
    s.set(typeof was === 'string' ? was : '');
  }
}

/**
 * Decrypt sensitive fields in a settings object (mutates in place).
 *
 * On failure the sealed values are left EXACTLY as they are and the in-memory
 * ones are blanked. An `enc:…` string must never reach buildSnmpRequest and go
 * on the wire as a community; and blanking the stored copy — which is what the
 * previous code did on any error — turned one locked keychain into permanently
 * lost credentials.
 */
export async function decryptSettings(settings) {
  const usable = await refreshStatus();
  await migrateLegacyKey(settings);

  const slots = fields(settings);
  const values = slots.map((s) => s.get());
  if (!values.some(isEncrypted)) {
    if (usable) credentialState.set('ok');
    return settings;
  }

  if (!usable) {
    blankSealed(slots);
    return settings;
  }

  try {
    const opened = await SettingsOpen(values.map((v) => (typeof v === 'string' ? v : '')));
    slots.forEach((s, i) => s.set(opened[i]));
    credentialState.set('ok');
  } catch (e) {
    console.warn('stored credentials could not be opened:', e);
    credentialState.set('locked');
    blankSealed(slots);
  }
  return settings;
}

/** Blank only the IN-MEMORY copy, never what is stored. */
function blankSealed(slots) {
  for (const s of slots) {
    if (isEncrypted(s.get())) s.set('');
  }
}

/**
 * Hand the legacy localStorage key over to the OS store, once.
 *
 * The old key is removed only after the new custody has been observed opening
 * this user's own ciphertext. GCM authenticates, so an open that succeeds is
 * proof the adopted key is the right one — there is no window in which the
 * credentials exist in neither place. If anything fails, the legacy key stays
 * exactly where it was and the next start tries again.
 */
async function migrateLegacyKey(settings) {
  const stored = localStorage.getItem(KEY_STORAGE);
  if (!stored) return;

  let k;
  try {
    k = JSON.parse(stored).k;
  } catch (e) {
    k = null;
  }
  if (!k) {
    // Not a key we can adopt, and not one we can use either.
    localStorage.removeItem(KEY_STORAGE);
    return;
  }

  try {
    await SettingsAdoptKey(k);
    const sealed = fields(settings).map((s) => s.get()).filter(isEncrypted);
    if (sealed.length) {
      await SettingsOpen([sealed[0]]);
    }
    localStorage.removeItem(KEY_STORAGE);
    console.info('settings key moved into the OS credential store');
  } catch (e) {
    console.warn('keeping the local settings key: the store did not take it:', e);
  }
}

/**
 * Learn the store's state without needing a stored blob to open.
 *
 * decryptSettings only runs when localStorage already holds settings, so on a
 * fresh profile nothing ever asked the store anything: credentialState kept
 * its 'ok' default, the banner never appeared, and the first save assumed it
 * could seal.
 */
export async function initCredentialState() {
  await refreshStatus();
  // The legacy key too. migrateLegacyKey only ran from decryptSettings, which
  // only runs when a settings blob exists — so a profile whose blob had been
  // reset kept the old extractable JWK in localStorage indefinitely, next to
  // nothing it could open but still there to be read.
  await migrateLegacyKey({});
}

/** Forget the key, for "reset settings". */
export async function forgetCredentials() {
  localStorage.removeItem(KEY_STORAGE);
  try {
    await SettingsForgetKey();
  } catch (e) {
    console.warn('could not forget the settings key:', e);
  }
}

/** Whether the caller may safely overwrite the stored sealed values. */
export function canPersistCredentials() {
  return get(credentialState) === 'ok';
}

/** The current state, for callers that are not Svelte components. */
export function getState() {
  return get(credentialState);
}

/** The backend name, for callers that are not Svelte components. */
export function getBackend() {
  return get(credentialBackend);
}

function getNestedValue(obj, path) {
  let current = obj;
  for (const key of path) {
    if (current == null) return undefined;
    current = current[key];
  }
  return current;
}

function setNestedValue(obj, path, value) {
  let current = obj;
  for (let i = 0; i < path.length - 1; i++) {
    if (current[path[i]] == null) current[path[i]] = {};
    current = current[path[i]];
  }
  current[path[path.length - 1]] = value;
}
