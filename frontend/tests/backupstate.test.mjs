// The renderer's half of a backup: what leaves with the credentials taken out,
// and what comes back. A backup is a file somebody may hand over, so what comes
// back is checked for the shape each store expects, and the credentials it
// brings are sealed under this machine's key before anything is written.
import * as esbuild from 'esbuild';
import { writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

let failures = 0;
process.on('unhandledRejection', (e) => {
  console.log('FAIL  unexpected rejection — ' + (e && e.message));
  process.exit(1);
});
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

function makeStorage(initial = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v); },
    removeItem: (k) => { delete data[k]; },
  };
}

const dir = mkdtempSync(join(tmpdir(), 'snmplens-backup-'));
writeFileSync(join(dir, 'stub.js'), 'export const SettingsKeyStatus = async () => ({ available: true });\n' +
  'export const SettingsSeal = async (v) => v.map((x) => (x ? "enc:" + x : ""));\n' +
  'export const SettingsOpen = async (v) => v;\nexport const SettingsAdoptKey = async () => {};\n' +
  'export const SettingsForgetKey = async () => {};\n');
const bundle = await esbuild.build({
  entryPoints: [join(process.cwd(), 'src/utils/backupState.js')],
  bundle: true,
  format: 'esm',
  write: false,
  logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /wailsjs[/]go[/]app[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
writeFileSync(join(dir, 'backupState.mjs'), bundle.outputFiles[0].text);
const B = await import(pathToFileURL(join(dir, 'backupState.mjs')).href);

const settings = {
  targets: '10.0.0.1',
  community: 'public-s3cret',
  v3: { user: 'ops', authPass: 'auth-s3cret', privPass: '' },
  targetOverrides: { '10.0.0.9': { port: 1161, community: 'override-s3cret' } },
  credentialProfiles: [{ id: 'p1', name: 'ro', version: 'v2c', community: 'profile-s3cret' }],
};

// --- leaving ---
const storage = makeStorage({
  snmplens_saved_queries: JSON.stringify([{ id: 'q1', oid: '1.3.6.1.2.1.1.1.0' }]),
  snmplens_favorites: JSON.stringify([{ oid: '1.3.6.1.2.1.1.5.0' }]),
  snmplens_paths: JSON.stringify({ paths: ['/opt/mibs'] }),
  snmpMonitorView: '{not json',
  snmplens_panel_width: '320',
  trapHistory: JSON.stringify([{ oid: 'x' }]),
});
const { state, secrets } = B.collectRendererState(settings, storage);
const text = JSON.stringify(state);
check('no credential is left in what leaves',
  !/s3cret/.test(text), text.match(/[a-z-]*s3cret/g)?.join(', '));
check('every credential is in the secrets, by its sealing key',
  secrets.community === 'public-s3cret' && secrets['v3.authPass'] === 'auth-s3cret' &&
  secrets['o:10.0.0.9.community'] === 'override-s3cret' && secrets['p:p1.community'] === 'profile-s3cret',
  JSON.stringify(secrets));
check('an empty passphrase is not a secret', !('v3.privPass' in secrets));
check('the stores the person built are carried',
  state.stores.snmplens_saved_queries?.length === 1 && state.stores.snmplens_favorites?.length === 1 &&
  state.stores.snmplens_paths?.paths?.[0] === '/opt/mibs');
check('the window layout and the history are not', !('snmplens_panel_width' in state.stores) && !('trapHistory' in state.stores));
check('a value that does not read is left out, not carried', !('snmpMonitorView' in state.stores));
check('the settings themselves are unchanged', settings.community === 'public-s3cret');

// --- coming back ---
const fresh = makeStorage();
const seal = async (s) => ({ ...s, community: s.community ? 'enc:' + s.community : '' });
let problems = await B.applyRendererState(state, secrets, fresh, seal, true);
const written = JSON.parse(fresh.data.settings);
check('the settings come back with their credentials, sealed', written.community === 'enc:public-s3cret' && problems.length === 0,
  JSON.stringify(problems));
check('the carried stores come back', JSON.parse(fresh.data.snmplens_saved_queries).length === 1);

const locked = makeStorage();
problems = await B.applyRendererState(state, secrets, locked, seal, false);
check('a store that cannot seal: no credential written, and said',
  !/s3cret/.test(locked.data.settings) && problems.includes('notSealed'), locked.data.settings);

const failing = makeStorage();
problems = await B.applyRendererState(state, secrets, failing, async () => { throw new Error('locked'); }, true);
check('sealing that fails writes the settings without their credentials',
  !/s3cret/.test(failing.data.settings) && JSON.parse(failing.data.settings).targets === '10.0.0.1' &&
  problems.includes('notSealed'));

// --- what a file may not do ---
const hostile = makeStorage();
problems = await B.applyRendererState({
  version: 1,
  settings: { targets: '192.0.2.1' },
  stores: { snmplens_saved_queries: 'not an array', snmplens_panel_width: '9999', _snmplens_ek: '{"k":1}' },
}, {}, hostile, seal, true);
check('a store of the wrong shape is dropped', !('snmplens_saved_queries' in hostile.data));
check('only the keys a backup carries are written',
  !('snmplens_panel_width' in hostile.data) && !('_snmplens_ek' in hostile.data), Object.keys(hostile.data).join(', '));
check('a state from another version is refused', B.acceptedState({ version: 2, settings: {} }) === null);

// --- the report across the reload ---
const session = makeStorage();
B.keepRestoreReport({ problems: ['x'] }, session);
check('the report survives the reload once', B.takeRestoreReport(session)?.problems?.[0] === 'x' && B.takeRestoreReport(session) === null);

process.exit(failures ? 1 : 0);
