// The renderer's half of credential custody, run against a stubbed bridge.
//
// The key used to sit in the same localStorage as the ciphertext it protected,
// so the encryption was decorative. It now lives in the OS store and the
// renderer never holds it — which introduces failure modes localStorage never
// had: a locked keychain, a machine with no store, a profile moved between
// Windows accounts.
//
// Every case below is about what happens THEN, because the dangerous answers
// are the tempting ones: fall back to writing the plaintext, or blank the
// stored value and call it clean. The first defeats the whole change; the
// second turns one bad second into permanently lost credentials.
import * as esbuild from 'esbuild';
import { writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

let failures = 0;

// Anything that escapes is a failure, not a silent death. Without this a
// mutation that made an unrelated block throw ended the process while the
// checks that would have caught it had not run yet — and the run read as
// clean.
process.on('uncaughtException', (e) => {
  console.log('FAIL  unexpected error — ' + (e && e.message));
  process.exit(1);
});
process.on('unhandledRejection', (e) => {
  console.log('FAIL  unexpected rejection — ' + (e && e.message));
  process.exit(1);
});
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

// --- a localStorage good enough to observe ---
function makeStorage(initial = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v); },
    removeItem: (k) => { delete data[k]; },
    clear: () => { for (const k of Object.keys(data)) delete data[k]; },
  };
}

const stubSource = `const s = globalThis.__stub;
export const SettingsKeyStatus = (...a) => s.SettingsKeyStatus(...a);
export const SettingsSeal = (...a) => s.SettingsSeal(...a);
export const SettingsOpen = (...a) => s.SettingsOpen(...a);
export const SettingsAdoptKey = (...a) => s.SettingsAdoptKey(...a);
export const SettingsForgetKey = (...a) => s.SettingsForgetKey(...a);
export const EventsOn = () => {};
`;

const dir = mkdtempSync(join(tmpdir(), 'snmplens-cred-'));
writeFileSync(join(dir, 'stub.js'), stubSource);

const bundle = await esbuild.build({
  // settingsStore, not crypto alone. crypto.js never writes the settings blob
  // — settingsStore.js does — so asserting "nothing was written to
  // localStorage" against crypto.js proved nothing: a plaintext fallback added
  // to the store passed the suite untouched. Bundling both is what makes the
  // invariant testable where it actually lives.
  entryPoints: [join(process.cwd(), 'tests/fixtures/credentials-entry.js')],
  bundle: true,
  format: 'esm',
  write: false,
  logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /bridge[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
writeFileSync(join(dir, 'crypto.mjs'), bundle.outputFiles[0].text);

/** A fresh module instance per scenario — the state stores are module-level. */
async function load(stub, storage) {
  globalThis.__stub = stub;
  globalThis.localStorage = storage;
  // A distinct query string forces a new module instance.
  const url = pathToFileURL(join(dir, 'crypto.mjs')).href + '?n=' + load.n++;
  return import(url);
}
load.n = 0;

/**
 * Wait for a condition instead of for a duration.
 *
 * The store decrypts asynchronously after import, and a fixed sleep made these
 * scenarios race: when the wait was short the store still held the SEALED
 * values, sealing them again was a no-op, and the test passed while the code
 * under it was broken. Polling the state is the difference between a test and
 * a coin flip.
 */
async function waitFor(cond, what) {
  for (let i = 0; i < 200; i++) {
    if (cond()) return;
    await new Promise((r) => setTimeout(r, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

const settings = () => ({
  community: 'enc:SEALED-COMMUNITY',
  v3: { authPass: 'enc:SEALED-AUTH', privPass: '' },
  targetOverrides: {
    '10.0.0.1': { community: 'enc:SEALED-OVERRIDE', v3: { authPass: '', privPass: '' } },
  },
});

const working = {
  SettingsKeyStatus: async () => ({ backend: 'test-store', available: true, hasKey: true }),
  SettingsSeal: async (v) => v.map((x) => (x && !x.startsWith('enc:') ? 'enc:' + x : x)),
  SettingsOpen: async (v) => v.map((x) => (typeof x === 'string' && x.startsWith('enc:') ? x.slice(4) : x)),
  SettingsAdoptKey: async () => {},
  SettingsForgetKey: async () => {},
};

// --- the ordinary path ---
{
  const store = makeStorage();
  const m = await load(working, store);
  const s = settings();
  await m.decryptSettings(s);

  check('sealed values are opened', s.community === 'SEALED-COMMUNITY', s.community);
  check('nested values are opened', s.v3.authPass === 'SEALED-AUTH');
  check('per-target overrides are opened',
    s.targetOverrides['10.0.0.1'].community === 'SEALED-OVERRIDE');
  check('an empty field stays empty', s.v3.privPass === '');

  const sealed = await m.encryptSettings(s);
  check('sealing covers every field',
    sealed.community.startsWith('enc:') &&
    sealed.v3.authPass.startsWith('enc:') &&
    sealed.targetOverrides['10.0.0.1'].community.startsWith('enc:'));
  check('an empty field is not sealed', sealed.v3.privPass === '');
  check('the original object is not mutated by sealing', s.community === 'SEALED-COMMUNITY');
}

// --- no OS store: work for this session, save nothing, leak nothing ---
{
  const store = makeStorage();
  const m = await load({
    ...working,
    SettingsKeyStatus: async () => ({ backend: 'unavailable', available: false, hasKey: false }),
    SettingsSeal: async () => { throw new Error('no secret store'); },
    SettingsOpen: async () => { throw new Error('no secret store'); },
  }, store);

  const s = settings();
  await m.decryptSettings(s);
  check('with no store the state says so', m.getState() === 'nostore', m.getState());
  check('an enc: string never survives into a usable field',
    s.community === '' && s.v3.authPass === '',
    JSON.stringify([s.community, s.v3.authPass]));

  // The contract is not "throw": it is "never put a credential in
  // localStorage". Carrying the stored values forward satisfies that and lets
  // unrelated settings still persist, which throwing did not.
  // Reported, not thrown: an unexpected throw here used to kill the process
  // before the later scenarios ran, so a mutation that broke them looked like
  // a clean run.
  let out = null;
  let threw = null;
  try {
    out = await m.encryptSettings({ community: 'public', v3: {}, targetOverrides: {} }, null);
  } catch (e) {
    threw = e;
  }
  check('an unsealable credential is not written as plaintext',
    threw !== null || (out && out.community === ''),
    threw ? String(threw.message) : JSON.stringify(out && out.community));
  check('nothing was written to localStorage', Object.keys(store.data).length === 0,
    JSON.stringify(store.data));
}

// The invariant at the layer that actually writes.
//
// These four ran green while settingsStore could be mutated into writing the
// plaintext, because the old suite only ever called crypto.js. The scenario is
// the one the review reproduced: the store is locked, decryptSettings blanks
// the in-memory copies, and the user then changes something unrelated.
for (const scenario of [
  { name: 'locked', expected: 'locked',
    stub: { SettingsOpen: async () => { throw new Error('locked'); } },
    status: { backend: 'windows-dpapi', available: true, hasKey: true } },
  { name: 'no store', expected: 'nostore', stub: {
      SettingsOpen: async () => { throw new Error('none'); },
      SettingsSeal: async () => { throw new Error('none'); } },
    status: { backend: 'unavailable', available: false, hasKey: false } },
]) {
  const expected = scenario.expected;
  const original = JSON.stringify(settings());
  const store = makeStorage({ settings: original });
  const m = await load({
    ...working,
    ...scenario.stub,
    SettingsKeyStatus: async () => scenario.status,
  }, store);

  // Let the store finish its startup decrypt, which is what blanks memory.
  await waitFor(() => m.getState() === expected, `state ${expected}`);

  let current;
  const stop = m.settingsStore.subscribe((v) => { current = v; });
  await m.settingsStore.save({ ...current, theme: 'dark' });
  stop();

  const after = JSON.parse(store.getItem('settings'));
  check(`[${scenario.name}] the stored community survives an unrelated save`,
    after.community === 'enc:SEALED-COMMUNITY', JSON.stringify(after.community));
  check(`[${scenario.name}] the stored v3 passphrase survives`,
    after.v3.authPass === 'enc:SEALED-AUTH', JSON.stringify(after.v3.authPass));
  check(`[${scenario.name}] the per-target override survives`,
    after.targetOverrides['10.0.0.1'].community === 'enc:SEALED-OVERRIDE',
    JSON.stringify(after.targetOverrides['10.0.0.1'].community));
  check(`[${scenario.name}] no credential was written in the clear`,
    !JSON.stringify(after).includes('SEALED-COMMUNITY"') || after.community.startsWith('enc:'));
  check(`[${scenario.name}] the unrelated change still persisted`,
    after.theme === 'dark', JSON.stringify(after.theme));
}

// The scenario that actually distinguishes the guard: opening FAILS while
// sealing WORKS — a wrong or rotated key, a corrupt blob, a profile moved
// between accounts. Without the state check, encryptSettings would happily
// seal the values decryptSettings just blanked and write enc:"" over the real
// credentials. Both stubs succeed, so nothing throws and no catch can save it.
{
  const store = makeStorage({ settings: JSON.stringify(settings()) });
  const m = await load({
    ...working,
    SettingsKeyStatus: async () => ({ backend: 'windows-dpapi', available: true, hasKey: true }),
    SettingsOpen: async () => { throw new Error('cannot be decrypted with the stored key'); },
    // Sealing is fine: the key is readable, it just does not open this blob.
  }, store);
  const expected = 'locked';
  await waitFor(() => m.getState() === expected, `state ${expected}`);

  let current;
  const stop = m.settingsStore.subscribe((v) => { current = v; });
  await m.settingsStore.save({ ...current, theme: 'dark' });
  stop();

  const after = JSON.parse(store.getItem('settings'));
  check('[open fails, seal works] the credential is not re-sealed from blanks',
    after.community === 'enc:SEALED-COMMUNITY', JSON.stringify(after.community));
  check('[open fails, seal works] the override is not re-sealed from blanks',
    after.targetOverrides['10.0.0.1'].community === 'enc:SEALED-OVERRIDE',
    JSON.stringify(after.targetOverrides['10.0.0.1'].community));
}

// And the scenario that reaches the catch: the state is fine, so sealing is
// attempted, and it fails between the status check and the seal. The stored
// blob must be left alone — writing the plaintext "so the setting is not lost"
// is the tempting answer and the wrong one.
{
  const before = JSON.stringify(settings());
  const store = makeStorage({ settings: before });
  const m = await load({
    ...working,
    SettingsSeal: async () => { throw new Error('the store went away mid-save'); },
  }, store);
  const expected = 'ok';
  await waitFor(() => m.getState() === expected, `state ${expected}`);

  let current;
  const stop = m.settingsStore.subscribe((v) => { current = v; });
  await m.settingsStore.save({ ...current, community: 'typed-just-now', theme: 'dark' });
  stop();

  const after = store.getItem('settings');
  check('[seal throws] the stored blob is untouched', after === before,
    after === before ? '' : after);
  check('[seal throws] the typed credential is not written in the clear',
    !after.includes('typed-just-now'), after.slice(0, 120));
}

// And with a working store the credentials are re-sealed normally, so the
// carry-forward path cannot quietly become permanent.
{
  const store = makeStorage({ settings: JSON.stringify(settings()) });
  const m = await load(working, store);
  const expected = 'ok';
  await waitFor(() => m.getState() === expected, `state ${expected}`);

  let current;
  const stop = m.settingsStore.subscribe((v) => { current = v; });
  await m.settingsStore.save({ ...current, community: 'brand-new-community' });
  stop();

  const after = JSON.parse(store.getItem('settings'));
  check('a working store still seals a new credential',
    after.community === 'enc:brand-new-community', JSON.stringify(after.community));
}

// --- locked store: the stored values must be left ALONE ---
{
  const store = makeStorage({ settings: JSON.stringify(settings()) });
  const before = store.data.settings;
  const m = await load({
    ...working,
    SettingsKeyStatus: async () => ({ backend: 'windows-dpapi', available: true, hasKey: true }),
    SettingsOpen: async () => { throw new Error('cannot be decrypted with the stored key'); },
  }, store);

  const s = settings();
  await m.decryptSettings(s);
  check('a locked store is reported as locked', m.getState() === 'locked', m.getState());
  check('the in-memory credential is blanked', s.community === '');
  check('the STORED blob is untouched', store.data.settings === before);
  check('the backend is named for the banner', m.getBackend() === 'windows-dpapi', m.getBackend());
}

// --- migration ---
{
  const legacy = JSON.stringify({ kty: 'oct', k: 'AAAA', alg: 'A256GCM' });
  const store = makeStorage({ _snmplens_ek: legacy });
  const adopted = [];
  const m = await load({
    ...working,
    SettingsAdoptKey: async (k) => { adopted.push(k); },
  }, store);

  await m.decryptSettings(settings());
  check('the legacy key is handed to the store', adopted.join() === 'AAAA', adopted.join());
  check('the legacy key is removed once adoption is verified',
    store.getItem('_snmplens_ek') === null);
}

// The old key must survive a failed adoption, or the credentials are lost.
{
  const legacy = JSON.stringify({ kty: 'oct', k: 'AAAA' });
  const store = makeStorage({ _snmplens_ek: legacy });
  const m = await load({
    ...working,
    SettingsAdoptKey: async () => { throw new Error('a settings key is already stored'); },
  }, store);

  await m.decryptSettings(settings());
  check('a failed adoption keeps the local key', store.getItem('_snmplens_ek') === legacy);
}

// Adoption that is accepted but does not actually open the blob must NOT be
// treated as success: GCM authenticates, so a failed open means a wrong key.
{
  const legacy = JSON.stringify({ kty: 'oct', k: 'AAAA' });
  // With a stored blob, which is the only case where the verification has
  // anything to verify against — and the only case where getting it wrong
  // costs anything.
  const store = makeStorage({ _snmplens_ek: legacy, settings: JSON.stringify(settings()) });
  const m = await load({
    ...working,
    SettingsOpen: async () => { throw new Error('cannot be decrypted with the stored key'); },
  }, store);

  await m.decryptSettings(settings());
  check('a key that does not open the blob is not trusted',
    store.getItem('_snmplens_ek') === legacy);
}

// And with no blob at all the key protects nothing, so taking custody and
// dropping the extractable copy is right — that copy used to sit in
// localStorage indefinitely because the migration only ran when a blob
// existed.
{
  const store = makeStorage({ _snmplens_ek: JSON.stringify({ kty: 'oct', k: 'AAAA' }) });
  const m = await load(working, store);
  await waitFor(() => store.getItem('_snmplens_ek') === null, 'the stranded key to go');
  check('a stranded legacy key is not left in localStorage',
    store.getItem('_snmplens_ek') === null);
  void m;
}

// --- reset ---
{
  const store = makeStorage({ _snmplens_ek: '{"k":"AAAA"}', settings: '{}' });
  let forgotten = false;
  const m = await load({ ...working, SettingsForgetKey: async () => { forgotten = true; } }, store);

  await m.forgetCredentials();
  check('reset forgets the key in the store', forgotten);
  check('reset removes the legacy key too', store.getItem('_snmplens_ek') === null);
}

// --- credential profiles ---
//
// A profile's secrets are sealed and opened like every other credential, and
// they are KEYED BY THE PROFILE'S ID: a profile renamed, reordered, added or
// deleted while the store was locked must get its own sealed values back,
// never a neighbour's.
const withProfiles = () => ({
  ...settings(),
  credentialProfiles: [
    { id: 'p-aaaa1111', name: 'Edge', version: 'v2c', community: 'enc:EDGE-COMMUNITY' },
    { id: 'p-bbbb2222', name: 'Core', version: 'v3',
      v3: { user: 'ops', secLevel: 'AuthPriv', authProto: 'SHA', authPass: 'enc:CORE-AUTH',
        privProto: 'AES', privPass: 'enc:CORE-PRIV', contextName: '' } },
  ],
});

{
  const m = await load(working, makeStorage());
  const s = withProfiles();
  await m.decryptSettings(s);
  check('a profile community is opened', s.credentialProfiles[0].community === 'EDGE-COMMUNITY',
    s.credentialProfiles[0].community);
  check('a profile passphrase is opened',
    s.credentialProfiles[1].v3.authPass === 'CORE-AUTH' && s.credentialProfiles[1].v3.privPass === 'CORE-PRIV');
  check('a user name is not a secret and is left alone', s.credentialProfiles[1].v3.user === 'ops');

  const sealed = await m.encryptSettings(s);
  check('profile secrets are sealed',
    sealed.credentialProfiles[0].community.startsWith('enc:') &&
    sealed.credentialProfiles[1].v3.privPass.startsWith('enc:'));
  check('and nothing a profile does not have is invented', !('community' in sealed.credentialProfiles[1]),
    JSON.stringify(Object.keys(sealed.credentialProfiles[1])));
}

{
  const store = makeStorage({ settings: JSON.stringify(withProfiles()) });
  const m = await load({
    ...working,
    SettingsKeyStatus: async () => ({ backend: 'windows-dpapi', available: true, hasKey: true }),
    SettingsOpen: async () => { throw new Error('cannot be decrypted with the stored key'); },
  }, store);
  await waitFor(() => m.getState() === 'locked', 'state locked');

  let current;
  const stop = m.settingsStore.subscribe((v) => { current = v; });
  await m.settingsStore.save({
    ...current,
    credentialProfiles: [
      { ...current.credentialProfiles[1], name: 'Core (renamed)' },
      { id: 'p-cccc3333', name: 'New', version: 'v2c', community: 'typed-while-locked' },
      current.credentialProfiles[0],
    ],
  });
  stop();

  const raw = store.getItem('settings');
  const byId = Object.fromEntries(JSON.parse(raw).credentialProfiles.map((p) => [p.id, p]));
  check('[locked] a moved profile keeps ITS sealed passphrase',
    byId['p-bbbb2222']?.v3?.authPass === 'enc:CORE-AUTH', JSON.stringify(byId['p-bbbb2222']?.v3?.authPass));
  check('[locked] and its neighbour keeps its own',
    byId['p-aaaa1111']?.community === 'enc:EDGE-COMMUNITY', JSON.stringify(byId['p-aaaa1111']?.community));
  check('[locked] a profile added while locked is not written in the clear', !raw.includes('typed-while-locked'));
  check('[locked] the rename, which is not a secret, still persisted', byId['p-bbbb2222']?.name === 'Core (renamed)');
}

process.exit(failures ? 1 : 0);
