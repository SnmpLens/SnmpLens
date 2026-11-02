// Exercises the real pollingStore against a stubbed Wails bridge, under node.
//
// There is no browser and no Go process here: the point is to prove that the
// store's half of the contract holds — that it creates a session with a usable
// connection, starts the Go clock, and turns pushed samples into the buffer the
// charts read. A ReferenceError in this file's path once silently disabled all
// monitoring, which is exactly the class of bug this catches.
import * as esbuild from 'esbuild';
import { writeFileSync, mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const calls = { created: [], started: [], stopped: [], saved: [], accepted: [], bound: [], updated: [] };
const handlers = {};

globalThis.__stub = {
  // A distinct id per session: targets that authenticate differently get one
  // session each, and two sessions sharing an id would be one in the store.
  MonitorCreateSession: async (...a) => { calls.created.push(a); return 'sess-' + calls.created.length; },
  MonitorStart: async (id) => { calls.started.push(id); },
  MonitorStop: async (id) => { calls.stopped.push(id); },
  MonitorRunning: async () => calls.started.filter((id) => !calls.stopped.includes(id)),
  MonitorSaveDataPoints: async (pts) => { calls.saved.push(pts); },
  MonitorLoadSessions: async () => [],
  MonitorLoadSessionData: async () => [],
  MonitorDeleteSession: async () => {},
  MonitorAcceptSlow: async (id) => { calls.accepted.push(id); },
  MonitorUpdateConnection: async (...a) => { calls.updated.push(a); },
  PresetBind: async (file, target, snmpVersion, conn) => {
    calls.bound.push({ file, target, snmpVersion, conn });
    return {
      id: 'preset-sess-1', name: 'Cisco Catalyst',
      oid: '1.3.6.1.2.1.1.3.0,1.3.6.1.2.1.2.2.1.10.1',
      targets: [target], intervalMs: 45000, snmpVersion,
      startedAt: '2026-01-01T00:00:00Z', conn,
      preset: { file, formatVersion: 1, widgets: [{ kind: 'value', title: 'Uptime', oids: ['1.3.6.1.2.1.1.3.0'] }] },
    };
  },
  EventsOn: (name, fn) => { handlers[name] = fn; },
  // The settings store now seals its credentials through the bridge, and
  // pollingStore imports it. A store that is present and empty is the shape
  // this test wants: nothing sealed, nothing to open.
  SettingsKeyStatus: async () => ({ backend: 'test', available: true, hasKey: false }),
  SettingsSeal: async (values) => values.map((v) => (v ? 'enc:' + v : v)),
  SettingsOpen: async (values) => values.map((v) => (typeof v === 'string' && v.startsWith('enc:') ? v.slice(4) : v)),
  SettingsAdoptKey: async () => {},
  SettingsForgetKey: async () => {},
};

const stub = `const s = globalThis.__stub;
export const MonitorCreateSession = (...a) => s.MonitorCreateSession(...a);
export const MonitorStart = (...a) => s.MonitorStart(...a);
export const MonitorStop = (...a) => s.MonitorStop(...a);
export const MonitorRunning = (...a) => s.MonitorRunning(...a);
export const MonitorSaveDataPoints = (...a) => s.MonitorSaveDataPoints(...a);
export const MonitorLoadSessions = (...a) => s.MonitorLoadSessions(...a);
export const MonitorLoadSessionData = (...a) => s.MonitorLoadSessionData(...a);
export const MonitorDeleteSession = (...a) => s.MonitorDeleteSession(...a);
export const MonitorAcceptSlow = (...a) => s.MonitorAcceptSlow(...a);
export const MonitorUpdateConnection = (...a) => s.MonitorUpdateConnection(...a);
export const PresetBind = (...a) => s.PresetBind(...a);
export const EventsOn = (...a) => s.EventsOn(...a);
export const ListMibFiles = async () => [];
export const SettingsKeyStatus = (...a) => s.SettingsKeyStatus(...a);
export const SettingsSeal = (...a) => s.SettingsSeal(...a);
export const SettingsOpen = (...a) => s.SettingsOpen(...a);
export const SettingsAdoptKey = (...a) => s.SettingsAdoptKey(...a);
export const SettingsForgetKey = (...a) => s.SettingsForgetKey(...a);`;

const dir = mkdtempSync(join(tmpdir(), 'snmplens-smoke-'));
writeFileSync(join(dir, 'stub.js'), stub);

const alias = {
  name: 'alias',
  setup(b) {
    b.onResolve({ filter: /bridge[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
    b.onResolve({ filter: /bridge[/]runtime$/ }, () => ({ path: join(dir, 'stub.js') }));
  },
};

const entry = new URL('./fixtures/polling-entry.js', import.meta.url).pathname.replace(/^[/]([A-Za-z]:)/, '$1');
const out = join(dir, 'bundle.mjs');
await esbuild.build({
  entryPoints: [entry],
  bundle: true, format: 'esm', outfile: out, platform: 'node',
  plugins: [alias], logLevel: 'silent',
});

// localStorage is touched by settingsStore and by the legacy migration.
const mem = new Map();
globalThis.localStorage = {
  getItem: (k) => (mem.has(k) ? mem.get(k) : null),
  setItem: (k, v) => mem.set(k, String(v)),
  removeItem: (k) => mem.delete(k),
};
globalThis.window = { addEventListener() {}, matchMedia: () => ({ matches: false, addEventListener() {} }) };
if (!globalThis.navigator) {
  Object.defineProperty(globalThis, 'navigator', { value: { language: 'en' }, configurable: true });
}

const { pollingStore, normalisePoint, settingsStore, addMessages, initI18n } = await import(pathToFileURL(out).href);
const { get } = await import('svelte/store');

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const OIDS = ['1.3.6.1.2.1.1.3.0', '1.3.6.1.2.1.2.2.1.10.1'];
const TARGETS = ['10.0.0.1', '10.0.0.2'];

const [id] = await pollingStore.startPolling(
  OIDS, TARGETS, 5000,
  { [OIDS[0]]: { min: 0, max: 5, forSeconds: 30 } }, 'v2c', 'smoke',
);

check('the session was created in Go', calls.created.length === 1);
check('the Go clock was started', calls.started.includes(id));
check('the store subscribed to pushed samples', typeof handlers['monitor:samples'] === 'function');

const [oidKey, targets, interval, version, thresholds, name, conn] = calls.created[0] || [];
check('both OIDs were sent, joined', oidKey === OIDS.join(','), oidKey);
check('the targets were sent', Array.isArray(targets) && targets.length === 2);
check('the interval was sent', interval === 5000);
check('the session name was sent', name === 'smoke', name);
check('thresholds were normalised and kept', !!thresholds && thresholds[OIDS[0]].forSeconds === 30);
check('a connection profile was sent', !!conn && typeof conn === 'object');
check('the connection carries what Go needs to authenticate',
  !!conn && 'community' in conn && !!conn.v3 && 'port' in conn);
check('the SNMP version was sent', version === 'v2c', version);

// Push two rounds of samples, as the Go scheduler would.
const push = (n) => handlers['monitor:samples']({
  sessionId: id,
  points: OIDS.flatMap((oid) => TARGETS.map((target, i) => ({
    target, oid, timestamp: new Date(Date.now() + n * 1000).toISOString(),
    value: 100 * n + i, delta: n > 1 ? 100 : null, rate: n > 1 ? 20 : null,
    responseTimeMs: 4, snmpType: 'Counter32', error: '',
  }))),
});
push(1);
push(2);

let s = get(pollingStore).find((x) => x.id === id);
check('pushed samples reached the buffer', !!s && s.results.length === 8, `results=${s ? s.results.length : 'n/a'}`);
check('every OID is represented', !!s && new Set(s.results.map((r) => r.oid)).size === 2);
check('every target is represented', !!s && new Set(s.results.map((r) => r.target)).size === 2);
check('the derived rate survived the bridge', !!s && s.results.some((r) => r.rate === 20));
check('the session reads as running', !!s && s.running === true);

// A sample for another session must not land in this one.
handlers['monitor:samples']({ sessionId: 'other', points: [{ target: 'x', oid: 'y', value: 1 }] });
s = get(pollingStore).find((x) => x.id === id);
check('samples are routed by session id', s.results.length === 8);

// Malformed payloads must be ignored rather than throwing inside the handler.
let threw = false;
try {
  handlers['monitor:samples'](null);
  handlers['monitor:samples']({ sessionId: id });
  handlers['monitor:samples']({ points: [] });
} catch (e) {
  threw = true;
}
check('a malformed push is ignored, not thrown', !threw);

await pollingStore.stopPolling(id);
s = get(pollingStore).find((x) => x.id === id);
check('stopping reaches Go', calls.stopped.includes(id));
check('the session reads as stopped', !!s && s.running === false);

// A RESTORED sample has to arrive in the same shape as a live one.
//
// Go's `omitempty` drops a zero value, so a stored point whose read failed comes
// back with no `value` key at all. The live path normalised that to null and the
// restore path did not, and consumers decide "this sample failed" with
// `=== null` — MetricTiles.svelte:106 for the failing indicator,
// MonitorChart.svelte:243 for the failed count. So after a restart a restored
// session stopped showing which of its points had failed, silently, and only
// for sessions that had been reloaded, which is why it survived.
//
// Tested as the pure function plus a source check that both paths call it,
// because there being ONE shape is the fix — patching the restore path would
// have left the next field free to drift.
{
  // What Go sends for a failed read: no value, no delta, no rate, no timing.
  const failed = normalisePoint({
    target: '10.0.0.1', timestamp: '2026-01-01T00:00:00Z', oid: '1.3.6.1', error: 'timeout',
  });
  check('a point with no value reads as null, not undefined', failed.value === null,
    JSON.stringify(failed.value));
  check('and so do delta and rate', failed.delta === null && failed.rate === null,
    JSON.stringify([failed.delta, failed.rate]));
  check('and a missing responseTimeMs is 0', failed.responseTimeMs === 0,
    JSON.stringify(failed.responseTimeMs));

  // A real reading is left alone, including a legitimate zero.
  const ok = normalisePoint({ target: 't', timestamp: 'x', oid: 'o', value: 0, delta: 0, rate: 0 });
  check('a zero reading stays zero rather than becoming null',
    ok.value === 0 && ok.delta === 0 && ok.rate === 0,
    JSON.stringify([ok.value, ok.delta, ok.rate]));

  const source = readFileSync(new URL('../src/stores/pollingStore.js', import.meta.url), 'utf8');
  const uses = (source.match(/map\(normalisePoint\)/g) || []).length;
  check('both the live and the restored path go through it', uses === 2, String(uses));
}

// The guardrail's report, and the operator's answer to it.
//
// Go widens a session that cannot poll as fast as it promised and says so once.
// That message has to reach the SESSION and stay there: it asks a question, and
// a toast is gone before anyone reads it.
{
  check('the store subscribed to the overrun report', typeof handlers['monitor:overrun'] === 'function');

  handlers['monitor:overrun']({
    sessionId: id, name: 'smoke', intervalMs: 5000, cycleMs: 7400,
    effectiveMs: 14800, oids: 2, targets: 2, accepted: false, recovered: false,
  });
  let o = get(pollingStore).find((x) => x.id === id).overrun;
  check('the report landed on the session', !!o && o.cycleMs === 7400, JSON.stringify(o));

  // Another session's report must not appear on this one.
  handlers['monitor:overrun']({ sessionId: 'other', cycleMs: 1, effectiveMs: 2, intervalMs: 3 });
  o = get(pollingStore).find((x) => x.id === id).overrun;
  check('reports are routed by session id', o.cycleMs === 7400);

  await pollingStore.acceptSlow(id);
  o = get(pollingStore).find((x) => x.id === id).overrun;
  check('accepting reached Go', calls.accepted.includes(id), JSON.stringify(calls.accepted));
  check('and the banner says so at once rather than after the next round',
    o.accepted === true && o.effectiveMs === o.intervalMs, JSON.stringify(o));

  // Recovery clears it: the question has stopped being asked.
  handlers['monitor:overrun']({ sessionId: id, intervalMs: 5000, cycleMs: 900, effectiveMs: 5000, recovered: true });
  o = get(pollingStore).find((x) => x.id === id).overrun;
  check('recovering takes the banner away', o === null, JSON.stringify(o));
}

// One builder for the session shape.
//
// There are two ways a session arrives: created here, and handed back by Go
// (PresetBind, or restored at startup). Two object literals building the same
// thing is the defect normalisePoint was written to end, and it had already
// happened — the local one never set needsConnection, so the field every
// consumer tests was absent on sessions made in this window and present on
// restored ones.
{
  const local = get(pollingStore).find((x) => x.id === id);
  const adopted = pollingStore.adoptSession({
    id: 'bound-1',
    name: 'Cisco Catalyst',
    oid: '1.3.6.1.2.1.1.3.0,1.3.6.1.2.1.2.2.1.10.1',
    targets: ['10.0.0.9'],
    intervalMs: 45000,
    snmpVersion: 'v2c',
    startedAt: '2026-01-01T00:00:00Z',
    conn: { port: 161 },
    preset: { file: 'cisco.json', formatVersion: 1, widgets: [{ kind: 'value', title: 'Uptime', oids: ['1.3.6.1.2.1.1.3.0'] }] },
  });

  const keys = (o) => Object.keys(o).sort().join(',');
  check('a bound session has the same shape as a locally created one',
    keys(local) === keys(adopted), `${keys(local)} vs ${keys(adopted)}`);

  check('the OID list was split out of the stored column',
    adopted.oids.length === 2 && adopted.oid === '1.3.6.1.2.1.1.3.0', JSON.stringify(adopted.oids));
  check('the cadence came across', adopted.interval === 45000, String(adopted.interval));
  check('a session with a connection does not ask to be re-armed', adopted.needsConnection === false);
  check('the dashboard layout came with it', adopted.preset?.widgets?.length === 1);
  check('and it is polling, because Go already started it', adopted.running === true);

  const inStore = get(pollingStore).find((x) => x.id === 'bound-1');
  check('the bound session reached the store', !!inStore && inStore.name === 'Cisco Catalyst');

  // Adopting twice must not produce two rows for one monitoring: a caller that
  // retries after a slow bridge call is the ordinary case, not the exotic one.
  pollingStore.adoptSession({ id: 'bound-1', oid: '1.1', targets: ['10.0.0.9'], intervalMs: 45000, conn: {} });
  check('adopting the same session twice is idempotent',
    get(pollingStore).filter((x) => x.id === 'bound-1').length === 1);

  check('a session with no connection asks to be re-armed',
    pollingStore.adoptSession({ id: 'legacy-1', oid: '1.1', targets: ['x'], intervalMs: 1000 }).needsConnection === true);
}

// Binding a preset uses THAT TARGET's settings, not the global ones: polling a
// device that has an override with the global community makes every reading an
// error, reported far from the cause and looking exactly like an unreachable
// device.
{
  const withOverride = {
    community: 'global-community',
    port: 161,
    targetOverrides: { '10.0.0.9': { community: 'per-target-community', port: 1161, snmpVersion: 'v1' } },
  };

  const bound = await pollingStore.bindPreset('cisco.json', '10.0.0.9', withOverride);
  const call = calls.bound[0];

  check('the preset file and the target reached Go', call?.file === 'cisco.json' && call?.target === '10.0.0.9',
    JSON.stringify(call && { file: call.file, target: call.target }));
  check("the target's own community was used, not the global one",
    call?.conn?.community === 'per-target-community', call?.conn?.community);
  check("and its own port", call?.conn?.port === 1161, String(call?.conn?.port));
  check("and its own SNMP version", call?.snmpVersion === 'v1', call?.snmpVersion);
  check('the bound session reached the store',
    !!bound && get(pollingStore).some((x) => x.id === 'preset-sess-1'));
  check('it is running, because Go started it before answering', bound?.running === true);

  // A target with no override falls back to the global settings rather than to
  // nothing at all.
  await pollingStore.bindPreset('cisco.json', '10.0.0.8', withOverride);
  check('a target with no override uses the global settings',
    calls.bound[1]?.conn?.community === 'global-community', calls.bound[1]?.conn?.community);
}

// Which sessions are bound to one equipment, by ADDRESS.
//
// Never by index or by label: `targets` is a stored JSON array that can hold
// addresses no longer in the settings, the list is reordered whenever somebody
// edits the text field, and two targets can share a label.
{
  const { boundPresetsFor } = await import('../src/utils/presetBindings.js');

  const sessions = [
    { id: 'a', targets: ['10.0.0.1'], preset: { file: 'cisco.json', name: 'Cisco' } },
    { id: 'b', targets: ['10.0.0.2'], preset: { file: 'ups.json', name: 'UPS' } },
    { id: 'c', targets: ['10.0.0.1'], preset: null },
    { id: 'd', targets: ['10.0.0.1', '10.0.0.9'], preset: { file: 'ifaces.json', name: 'Interfaces' } },
  ];

  const bound = boundPresetsFor(sessions, '10.0.0.1').map((s) => s.id);
  check('a session bound to this equipment is found', bound.includes('a'));
  check('and one bound to it among several targets', bound.includes('d'));
  check('a session with no preset is not a binding', !bound.includes('c'), bound.join(','));
  check('another equipment is not this one', !bound.includes('b'));

  check('an unknown address has nothing bound', boundPresetsFor(sessions, '10.9.9.9').length === 0);
  check('and it survives what the store really holds',
    boundPresetsFor(undefined, '10.0.0.1').length === 0 && boundPresetsFor(sessions, '').length === 0);
}

// Credential profiles.
//
// A target given a profile is polled WITH it — its version, its user — and a
// session over targets that authenticate differently is split, one per set of
// identifiers, since one Go session holds one connection. And a session FOLLOWS
// its profile: a rotated passphrase reaches the sessions built from it instead
// of leaving them failing authentication until somebody rebinds them.
{
  // The application's i18n module writes the locale onto <html lang>, and
  // there is no document under node.
  globalThis.document ??= { documentElement: { setAttribute() {} } };
  addMessages('en', JSON.parse(readFileSync(new URL('../src/i18n/en.json', import.meta.url), 'utf8')));
  initI18n({ fallbackLocale: 'en', initialLocale: 'en' });

  const core = {
    id: 'p-core1234', name: 'Core', version: 'v3',
    v3: { user: 'ops', secLevel: 'AuthPriv', authProto: 'SHA', authPass: 'core-auth-123',
      privProto: 'AES', privPass: 'core-priv-123', contextName: '' },
  };
  settingsStore.save({
    ...get(settingsStore),
    community: 'public-default',
    targets: '10.1.0.1\n10.1.0.2',
    credentialProfiles: [core],
    targetOverrides: { '10.1.0.2': { profile: core.id, port: 1161 } },
  });

  const createdBefore = calls.created.length;
  const ids = await pollingStore.startPolling(['1.3.6.1.2.1.1.3.0'], ['10.1.0.1', '10.1.0.2'], 5000, null, 'v2c', 'wan');
  const made = calls.created.slice(createdBefore);

  check('targets that authenticate differently get a session each', ids.length === 2 && made.length === 2,
    `${ids.length} ids, ${made.length} created`);
  const plain = made.find((a) => a[1].join() === '10.1.0.1');
  const profiled = made.find((a) => a[1].join() === '10.1.0.2');
  check('the default target keeps the version picked for the session', plain?.[3] === 'v2c', plain?.[3]);
  check('and the default community', plain?.[6]?.community === 'public-default', plain?.[6]?.community);
  check('the profiled target speaks its profile’s version', profiled?.[3] === 'v3', profiled?.[3]);
  check('as its profile’s user', profiled?.[6]?.v3?.User === 'ops', profiled?.[6]?.v3?.User);
  check('without the default community riding along', profiled?.[6]?.community === '', profiled?.[6]?.community);
  check('on its own port', profiled?.[6]?.port === 1161, String(profiled?.[6]?.port));
  check('and the session records the profile it follows', profiled?.[6]?.profile === core.id, profiled?.[6]?.profile);
  check('the two sessions are told apart by name',
    plain?.[5] !== profiled?.[5] && String(profiled?.[5]).includes('Core'), `${plain?.[5]} / ${profiled?.[5]}`);

  // Rotate the profile's passphrase.
  const now = get(settingsStore);
  const rotated = JSON.parse(JSON.stringify(now));
  rotated.credentialProfiles[0].v3.authPass = 'rotated-auth-456';
  const updatedBefore = calls.updated.length;
  const n = await pollingStore.followCredentials(now, rotated);
  const updates = calls.updated.slice(updatedBefore);
  const profiledId = ids[made.indexOf(profiled)];

  check('a rotated passphrase reaches the session built from the profile',
    updates.some(([sid, , conn]) => sid === profiledId && conn.v3.AuthPass === 'rotated-auth-456'),
    JSON.stringify(updates.map(([sid]) => sid)));
  check('and only that one', n === 1 && updates.length === 1, `${n} updated`);
  check('keeping its own transport', updates[0]?.[2]?.port === 1161, String(updates[0]?.[2]?.port));
  check('and its profile’s version', updates[0]?.[1] === 'v3', updates[0]?.[1]);

  // A restart throws away the samples deltas and rates are derived from, so
  // nothing that did not change may cause one.
  const quiet = calls.updated.length;
  await pollingStore.followCredentials(rotated, JSON.parse(JSON.stringify(rotated)));
  check('an unchanged profile restarts nothing', calls.updated.length === quiet);
}

process.exit(failures ? 1 : 0);
