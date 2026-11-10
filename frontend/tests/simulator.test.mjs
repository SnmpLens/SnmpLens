// The simulator's renderer-side rules: the device form, with its SNMPv3 users,
// and how a device becomes a target.
//
// The second is the part only the renderer can get wrong — a target lives in
// the settings — so it is checked THROUGH getEffectiveSettings, which is what
// every request is built from: a device added as a target must resolve to its
// own port and identifiers. On macOS every device shares 127.0.0.1, so that
// takes a target that names its port.
import * as esbuild from 'esbuild';
import { readFileSync, readdirSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

// targets.js reaches the settings store, and the store the Wails bridge, so the
// rules are bundled with the bridge stubbed out — as tests/profiles.test.mjs is.
const dir = mkdtempSync(join(tmpdir(), 'snmplens-simulator-'));
writeFileSync(join(dir, 'stub.js'), `
export const SettingsKeyStatus = async () => ({ backend: 'test', available: true, hasKey: false });
export const SettingsSeal = async (v) => v;
export const SettingsOpen = async (v) => v;
export const SettingsAdoptKey = async () => {};
export const SettingsForgetKey = async () => {};
export const EventsOn = () => {};
`);
const bundle = join(dir, 'bundle.mjs');
await esbuild.build({
  entryPoints: [new URL('./fixtures/simulator-entry.js', import.meta.url).pathname.replace(/^[/]([A-Za-z]:)/, '$1')],
  bundle: true, format: 'esm', outfile: bundle, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /wailsjs[/](go[/]app[/]App|runtime[/]runtime)$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
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

const {
  blankDevice,
  blankV3,
  blankTraps,
  blankDestination,
  blankSchedule,
  editableDevice,
  deviceProblems,
  devicePayload,
  EDITOR_TABS,
  tabsWithProblems,
  OVERRIDE_TYPES,
  MAX_OVERRIDES,
  AGENT_SUBTREES,
  blankOverride,
  previewPayload,
  previewQuery,
  PREVIEW_COLUMNS,
  deliveryReport,
  trapActivity,
  modelGroups,
  categoriesOf,
  categoryIcon,
  CATEGORY_ICONS,
  modelName,
  modelDescription,
  filterModels,
  devicesOfModel,
  importReport,
  firstImported,
  deviceImportReport,
  blankFaults,
  editableFaults,
  faultsPayload,
  faultChips,
  deviceGroups,
  COUNTER_SPEEDS,
  CUSTOM_CATEGORIES,
  recordableTargets,
  recordRequest,
  preferredVersion,
  targetOf,
  addDeviceAsTarget,
  getEffectiveSettings,
  portOf,
} = await import(pathToFileURL(bundle).href);

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

/* --- the form ------------------------------------------------------------ */

const fresh = blankDevice('linux-server', { address: '127.0.0.4', port: 161 });
check('a new device answers v2c, where the backend suggested',
  fresh.address === '127.0.0.4' && fresh.port === 161 && fresh.versions.join() === 'v2c');
check('its one problem is its missing name',
  JSON.stringify(deviceProblems(fresh)) === JSON.stringify({ name: 'nameRequired' }),
  JSON.stringify(deviceProblems(fresh)));

const v3With = (users) => ({ ...fresh, name: 'x', versions: ['v3'], users });
const good = (name) => ({ ...blankV3(), user: name, authPass: 'authpass-1', privPass: 'privpass-1' });

const short = deviceProblems(v3With([{ ...blankV3(), authPass: 'short', privPass: 'short' }]));
check('a v3 user with short passphrases is refused on both',
  short.users?.[0]?.authPass === 'passphraseShort' && short.users?.[0]?.privPass === 'passphraseShort');
check('and v3 alone needs no community', !short.community);
check('a passphrase the level does not use is not asked for',
  !deviceProblems(v3With([{ ...good('ops'), secLevel: 'AuthNoPriv', privPass: '' }])).users);
check('two well-formed users are no problem', !deviceProblems(v3With([good('ops'), good('monitor')])).users);
const dup = deviceProblems(v3With([good('ops'), good(' ops ')]));
check('two users with one name are both told so',
  dup.users?.[0]?.user === 'userDuplicate' && dup.users?.[1]?.user === 'userDuplicate');
check('v3 with no user is refused', deviceProblems(v3With([])).noUser === 'userRequired');

/* --- what is sent -------------------------------------------------------- */

const sent = devicePayload({ ...fresh, name: ' srv ' });
check('v2c alone sends no user, and the name trimmed',
  sent.users.length === 0 && sent.community === 'public' && sent.name === 'srv');
check("the engine is not the renderer's to send", sent.engineId === '' && sent.engineBoots === 0);
const sent3 = devicePayload(v3With([good(' ops '), good('monitor')]));
check("v3 alone sends no community, and every user under Go's names",
  sent3.community === '' && sent3.users.length === 2 && sent3.users[0].name === 'ops' && sent3.users[1].privPass === 'privpass-1',
  JSON.stringify(sent3.users.map((u) => u.name)));

/* --- what the device sends ------------------------------------------------ */

const withTraps = (traps, extra = {}) => ({ ...fresh, name: 'x', ...extra, traps: { ...blankTraps(), ...traps } });
const local = blankDestination(1162);
check('a new destination is SnmpLens on this machine, at its trap port, in v2c',
  local.host === '127.0.0.1' && local.port === 1162 && local.version === 'v2c' && local.community === 'public');
check('and asks for nothing more', !deviceProblems(withTraps({ destinations: [local] })).destinations);
const refused = deviceProblems(withTraps({ destinations: [
  { ...local, host: ' ' },
  { ...local, community: '', port: 0 },
  { ...local, version: 'v3', user: '' },
] })).destinations || [];
check('a destination needs a host, a port and, in v1 and v2c, a community',
  refused[0]?.host === 'hostRequired' && refused[1]?.community === 'communityRequired' && refused[1]?.port === 'port',
  JSON.stringify(refused));
check('a v3 destination needs the device to answer v3', refused[2]?.user === 'trapNeedsV3');
const v3dest = (user, extra = {}) =>
  withTraps({ destinations: [{ ...local, version: 'v3', user, ...extra }] }, { versions: ['v3'], users: [good('ops')] });
check("and to be sent as one of the device's users",
  deviceProblems(v3dest('nobody')).destinations?.[0]?.user === 'trapUser' && !deviceProblems(v3dest('ops')).destinations);
check("a receiver's engine ID is hex, 5 to 32 octets, with a 0x or colons forgiven",
  deviceProblems(v3dest('ops', { inform: true, engineId: 'zz' })).destinations?.[0]?.engineId === 'engineId' &&
  !deviceProblems(v3dest('ops', { inform: true, engineId: '0x80:00:00:00:05:01' })).destinations);
check('a schedule waits 1 to 86400 seconds',
  deviceProblems(withTraps({ schedules: [{ ...blankSchedule(), every: 0 }] })).schedules?.[0]?.every === 'every' &&
  !deviceProblems(withTraps({ schedules: [blankSchedule()] })).schedules);

const sentTraps = devicePayload(withTraps({
  onStart: true,
  schedules: [{ notification: 'linkDown', every: '30', irregular: 1 }],
  destinations: [
    { ...local, host: ' [::1] ', inform: true, user: 'stale', engineId: 'aa' },
    { ...local, version: 'v1', inform: true },
    { ...local, version: 'v3', user: ' ops ', community: 'stale', inform: true, engineId: '0x80:00:00:00:05:01' },
  ],
}, { versions: ['v2c', 'v3'], users: [good('ops')] })).traps;
const [sentV2, sentV1, sentV3] = sentTraps.destinations;
check('each destination is sent with what its version uses and nothing else',
  sentV2.host === '::1' && sentV2.inform && sentV2.user === '' && sentV2.engineId === '' &&
  !sentV1.inform && sentV3.community === '' && sentV3.user === 'ops' && sentV3.engineId === '800000000501',
  JSON.stringify(sentTraps.destinations));
check('and the schedules as numbers',
  sentTraps.onStart === true && sentTraps.schedules[0].every === 30 && sentTraps.schedules[0].irregular === true);

const report = deliveryReport('linkDown', [
  { id: 'a', destination: '127.0.0.1:162', inform: false, acknowledged: false },
  { id: 'b', destination: '192.0.2.9:162', inform: true, acknowledged: false, error: 'the INFORM was not acknowledged' },
]);
check('a notification sent on request is reported where it went, and where it did not',
  report.length === 2 && report[0].key === 'simulator.traps.sent' && report[1].key === 'simulator.traps.failed' &&
  report[1].level === 'error', JSON.stringify(report));
check('an acknowledged INFORM says so',
  deliveryReport('linkUp', [{ destination: 'x', inform: true, acknowledged: true }])[0].key === 'simulator.traps.acknowledged');
check('what a device sent is added up over its destinations',
  trapActivity({ destinations: [{ sent: 3, failed: 1, lastError: 'refused' }, { sent: 2 }] }).sent === 5 &&
  trapActivity(undefined).sent === 0);

/* --- editing brings the credentials back --------------------------------- */

const view = {
  id: 'a1', name: 'srv', model: 'linux-server', address: '127.0.0.2', port: 161,
  versions: ['v2c', 'v3'],
  users: [
    { name: 'ops', secLevel: 'AuthPriv', authProto: 'SHA256', privProto: 'AES' },
    { name: 'monitor', secLevel: 'AuthNoPriv', authProto: 'SHA', privProto: 'AES' },
  ],
  traps: {
    destinations: [{ id: 'd1', host: '192.0.2.10', port: 162, version: 'v2c', inform: true, user: '', engineId: '', sent: 3 }],
    onStart: true,
    onAuthFailure: false,
    schedules: [{ notification: 'linkUp', every: 60, irregular: false }],
    suppressed: 0,
  },
};
const creds = {
  community: 'c0mmunity',
  users: { ops: { authPass: 'authpass-1', privPass: 'privpass-1' }, monitor: { authPass: 'monitor-pass', privPass: '' } },
  destinations: { d1: 'trap-community' },
};
const form = editableDevice(view, creds);
check('the editor is given the community, and every user with its passphrases',
  form.community === 'c0mmunity' && form.users.length === 2 &&
  form.users[0].privPass === 'privpass-1' && form.users[1].authPass === 'monitor-pass');
check('and editing nothing sends back the same users',
  JSON.stringify(devicePayload(form).users) === JSON.stringify([
    { name: 'ops', secLevel: 'AuthPriv', authProto: 'SHA256', authPass: 'authpass-1', privProto: 'AES', privPass: 'privpass-1', write: false },
    { name: 'monitor', secLevel: 'AuthNoPriv', authProto: 'SHA', authPass: 'monitor-pass', privProto: 'AES', privPass: '', write: false },
  ]));
check("a destination's community comes back by its ID, and goes back without the counters",
  form.traps.destinations[0].community === 'trap-community' &&
  JSON.stringify(devicePayload(form).traps) === JSON.stringify({
    destinations: [{ id: 'd1', host: '192.0.2.10', port: 162, version: 'v2c', inform: true, community: 'trap-community', user: '', engineId: '' }],
    onStart: true,
    onAuthFailure: false,
    schedules: [{ notification: 'linkUp', every: 60, irregular: false }],
  }), JSON.stringify(devicePayload(form).traps));

/* --- writing ---------------------------------------------------------------- */

check('a new device writes nothing', fresh.writeCommunity === '' && fresh.users.every((u) => !u.write));
const writeForm = editableDevice({ ...view, users: [{ ...view.users[0], write: true }] }, { ...creds, writeCommunity: 'wr1te' });
check('the editor is given the write community, and which users write',
  writeForm.writeCommunity === 'wr1te' && writeForm.users[0].write === true);
check('and sends both back', devicePayload(writeForm).writeCommunity === 'wr1te' && devicePayload(writeForm).users[0].write === true);
check('a device answering v3 alone sends no write community',
  devicePayload({ ...writeForm, versions: ['v3'] }).writeCommunity === '');
check('a write community that is the read community is refused',
  deviceProblems({ ...fresh, name: 'x', writeCommunity: 'public' }).writeCommunity === 'writeSameAsRead' &&
  !deviceProblems({ ...fresh, name: 'x', writeCommunity: 'private' }).writeCommunity);
const booted = editableDevice({ ...view, engineId: '8000000903020000000001', engineBoots: 3 }, creds);
check("the editor shows the device's engine, and sends none",
  booted.engineId === '8000000903020000000001' && booted.engineBoots === 3 &&
  devicePayload(booted).engineId === '' && devicePayload(booted).engineBoots === 0);

/* --- the editor's tabs ------------------------------------------------------ */

check('a problem marks the tab it is on',
  JSON.stringify(tabsWithProblems({ name: 'nameRequired', writeCommunity: 'writeSameAsRead' })) === JSON.stringify(['identity', 'access']) &&
  tabsWithProblems({}).length === 0);
// Every problem deviceProblems can report, from devices wrong in every way: one
// no tab holds would be a Save button greyed out for a reason nobody can see.
const everyProblem = [
  { ...fresh, name: '', port: 0, versions: ['v2c', 'v3'], community: '', users: [],
    traps: { ...blankTraps(), destinations: [{ ...blankDestination(162), host: '' }], schedules: [{ ...blankSchedule(), every: 0 }] } },
  { ...fresh, versions: [] },
  { ...fresh, versions: ['v2c', 'v3'], writeCommunity: 'public', users: [{ ...blankV3(), authPass: 'short' }] },
  { ...fresh, params: { ports: 99 }, overrides: [{ ...blankOverride(), oid: 'sysContact.0' }] },
].flatMap((d) => Object.keys(deviceProblems(d, { params: [{ name: 'ports', min: 2, max: 24, default: 24 }] })));
const onTabs = EDITOR_TABS.flatMap((t) => t.fields);
check('every problem is on a tab', everyProblem.every((k) => onTabs.includes(k)),
  everyProblem.filter((k) => !onTabs.includes(k)).join());
check('and the devices above are wrong in every way a tab names', onTabs.every((k) => everyProblem.includes(k)),
  onTabs.filter((k) => !everyProblem.includes(k)).join());

/* --- the data: numbers a model is given, and values of its own ------------ */

const catalyst = { id: 'cisco-catalyst-24', params: [{ name: 'ports', min: 2, max: 24, default: 24 }] };
check('a new device is its model as catalogued', JSON.stringify(fresh.params) === '{}' && fresh.overrides.length === 0);
check("a number out of its bounds is refused, and an empty one is the model's",
  deviceProblems({ ...fresh, name: 'x', params: { ports: 30 } }, catalyst).params?.ports === 'paramRange' &&
  !deviceProblems({ ...fresh, name: 'x', params: { ports: null } }, catalyst).params &&
  !deviceProblems({ ...fresh, name: 'x', params: { ports: 12 } }, catalyst).params);
check('only the numbers given are sent, as numbers',
  JSON.stringify(devicePayload({ ...fresh, name: 'x', params: { ports: '12', disks: null } }).params) === JSON.stringify({ ports: 12 }));
const ownProblems = (overrides) => deviceProblems({ ...fresh, name: 'x', overrides }).overrides;
check("a value of its own needs an OID, outside the agent's subtrees, and given once",
  ownProblems([{ ...blankOverride(), oid: 'sysContact.0' }])?.[0]?.oid === 'overrideOid' &&
  ownProblems([{ ...blankOverride(), oid: '1.3.6.1.2.1.11.1.0' }])?.[0]?.oid === 'overrideAgent' &&
  ownProblems([{ ...blankOverride(), oid: '1.3.6.1.4.1.1.0' }, { ...blankOverride(), oid: '.1.3.6.1.4.1.1.0' }])?.[1]?.oid === 'overrideDuplicate' &&
  !ownProblems([{ ...blankOverride(), oid: '1.3.6.1.2.1.1.4.0', value: 'ops' }]));
check('and a whole number where its type is a number',
  ownProblems([{ oid: '1.3.6.1.4.1.1.0', type: 'Gauge32', value: '-1', hex: false }])?.[0]?.value === 'overrideNumber' &&
  !ownProblems([{ oid: '1.3.6.1.4.1.1.0', type: 'Integer', value: '-1', hex: false }]));
const withOwn = editableDevice({ ...view, params: { cpus: 4 },
  overrides: [{ oid: '.1.3.6.1.2.1.1.4.0', type: 'OctetString', value: 'ops', hex: false }] }, creds);
check('the editor is given the numbers and the values, and sends them back',
  withOwn.params.cpus === 4 && JSON.stringify(devicePayload(withOwn).overrides) ===
    JSON.stringify([{ oid: '.1.3.6.1.2.1.1.4.0', type: 'OctetString', value: 'ops', hex: false }]));
// A preview needs no secret, so it is sent none.
const previewed = JSON.stringify(previewPayload(writeForm));
check('a preview is sent no secret',
  ['c0mmunity', 'wr1te', 'authpass-1', 'privpass-1', 'trap-community'].every((s) => !previewed.includes(s)), previewed);
check('and all that makes the answers', JSON.parse(previewed).model === writeForm.model && JSON.parse(previewed).name === writeForm.name);

/* --- the port a target names --------------------------------------------- */

check('a target names its port in each form a person writes one',
  portOf('10.0.0.5:1161') === 1161 && portOf('switch-01:1161') === 1161 &&
  portOf('[2001:db8::5]:1161') === 1161 && portOf(' 127.0.0.1:1162 ') === 1162);
check('an address, a name or a bare IPv6 literal names none',
  portOf('10.0.0.5') === null && portOf('switch-01') === null && portOf('::1') === null &&
  portOf('2001:db8::5') === null && portOf('[::1]') === null);
check('what is not a port is not read as one',
  portOf('10.0.0.5:0') === null && portOf('10.0.0.5:70000') === null && portOf(':1161') === null);
check("a device's target carries its port unless it is 161",
  targetOf({ address: '127.0.0.2', port: 161 }) === '127.0.0.2' &&
  targetOf({ address: '127.0.0.1', port: 1162 }) === '127.0.0.1:1162' &&
  targetOf({ address: '::1', port: 1161 }) === '[::1]:1161');

/* --- a device as a target, resolved as every request resolves it ---------- */

check('a target speaks the most secure version the device answers',
  preferredVersion(['v1', 'v2c', 'v3']) === 'v3' && preferredVersion(['v1', 'v2c']) === 'v2c' && preferredVersion(['v1']) === 'v1');

const settings = {
  targets: '10.0.0.1 # router',
  targetOverrides: {},
  community: 'default-community',
  snmpVersion: 'v2c',
  port: 161,
  timeout: 5,
  retries: 1,
  credentialProfiles: [],
  v3: { user: 'default-user', authPass: '', authProto: 'SHA', privPass: '', privProto: 'AES', secLevel: 'NoAuthNoPriv', contextName: '' },
};

// Windows and Linux: an address of its own, at 161.
const own = addDeviceAsTarget(settings, view, creds);
const effOwn = getEffectiveSettings(own.settings, own.target);
check('a device at 161 is a target by its address, named after it',
  own.added && own.target === '127.0.0.2' && own.settings.targets.split('\n').includes('127.0.0.2 # srv'));
check('the targets already there stay', own.settings.targets.startsWith('10.0.0.1 # router'));
check("it resolves to the device's first SNMPv3 user, at its port",
  effOwn.snmpVersion === 'v3' && effOwn.v3.user === 'ops' && effOwn.v3.secLevel === 'AuthPriv' &&
  effOwn.v3.privPass === 'privpass-1' && effOwn.port === 161,
  JSON.stringify({ version: effOwn.snmpVersion, user: effOwn.v3.user, port: effOwn.port }));
const again = addDeviceAsTarget(own.settings, view, creds);
check('a device added twice is listed once',
  !again.added && again.settings.targets.split('\n').filter((l) => l.startsWith('127.0.0.2')).length === 1);

// macOS: every device on 127.0.0.1, told apart by port alone.
const mac1 = { ...view, id: 'm1', name: 'mac-1', address: '127.0.0.1', port: 1161, versions: ['v2c'], users: [] };
const mac2 = { ...mac1, id: 'm2', name: 'mac-2', port: 1162 };
const r1 = addDeviceAsTarget(settings, mac1, creds);
const r2 = addDeviceAsTarget(r1.settings, mac2, { community: 'other', users: {} });
const e1 = getEffectiveSettings(r2.settings, '127.0.0.1:1161');
const e2 = getEffectiveSettings(r2.settings, '127.0.0.1:1162');
check('two devices sharing 127.0.0.1 are two targets',
  r1.added && r2.added && r2.settings.targets.split('\n').length === 3, r2.settings.targets);
check('each resolving to its own port and community',
  e1.port === 1161 && e1.community === 'c0mmunity' && e2.port === 1162 && e2.community === 'other',
  JSON.stringify({ one: [e1.port, e1.community], two: [e2.port, e2.community] }));
check('with no override of a port the target already names',
  r2.settings.targetOverrides['127.0.0.1:1162'].port === undefined);

const overruled = { ...settings, targetOverrides: { '127.0.0.1:1162': { port: 9999 } } };
check("a port the target names wins over an override's", getEffectiveSettings(overruled, '127.0.0.1:1162').port === 1162);
check('and over the default, with no override at all', getEffectiveSettings(settings, '127.0.0.1:1163').port === 1163);

// A device on a bench is there to be written to: its target writes when it can.
const writer = { ...view, users: [view.users[0], { ...view.users[1], write: true }] };
const w3 = getEffectiveSettings(addDeviceAsTarget(settings, writer, creds).settings, '127.0.0.2');
check('a device with a user who can write is a target as that user',
  w3.v3.user === 'monitor' && w3.v3.authPass === 'monitor-pass', w3.v3.user);
const wc = getEffectiveSettings(addDeviceAsTarget(settings, mac1, { ...creds, writeCommunity: 'wr1te' }).settings, '127.0.0.1:1161');
check('and a device with a write community, with that community', wc.community === 'wr1te', wc.community);

/* --- every model Go serves is named and described ------------------------ */

const en = JSON.parse(readFileSync(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const goDir = new URL('../../pkg/simulator/', import.meta.url);
const ids = new Set();
const categories = new Set();
for (const file of readdirSync(goDir)) {
  if (!file.endsWith('.go') || file.endsWith('_test.go')) continue;
  for (const m of readFileSync(new URL(file, goDir), 'utf8').matchAll(/ModelInfo\{ID:\s*"([^"]+)",\s*Category:\s*"([^"]+)"/g)) {
    ids.add(m[1]);
    categories.add(m[2]);
  }
}
check('the models were found in pkg/simulator', ids.size >= 9, [...ids].join(', '));
for (const category of categories) {
  check(`en.json names the category ${category}`, Boolean(en.simulator?.category?.[category]));
}
const groups = modelGroups([
  { id: 'a', category: 'server' }, { id: 'b', category: 'network' }, { id: 'c', category: 'server' },
]);
check("the models are grouped by category, in the order of each category's first model",
  groups.map((g) => `${g.category}:${g.models.map((m) => m.id).join('+')}`).join(' ') === 'server:a+c network:b');
for (const key of ['hostRequired', 'trapUser', 'trapNeedsV3', 'engineId', 'every']) {
  check(`en.json says simulator.problem.${key}`, Boolean(en.simulator?.problem?.[key]));
}
for (const key of ['sent', 'acknowledged', 'sentAll', 'failed']) {
  check(`en.json says simulator.traps.${key}`, Boolean(en.simulator?.traps?.[key]));
}
for (const id of ids) {
  check(`en.json names and describes the model ${id}`,
    Boolean(en.simulator?.model?.[id]?.name && en.simulator?.model?.[id]?.description));
}

/* --- the model picker ---------------------------------------------------- */

// A custom model may take a category none of the catalogue's has ("other"), so
// the list the Go side accepts is read too; each is named, and drawn.
const { icons } = await import(new URL('../src/icons.js', import.meta.url).href);
const customGo = readFileSync(new URL('custom.go', goDir), 'utf8');
const customCategories = [...(customGo.match(/customCategories = \[\]string\{([^}]*)\}/)?.[1] || '').matchAll(/"([^"]+)"/g)]
  .map((m) => m[1]);
check('the categories a custom model may take were found in pkg/simulator', customCategories.length >= 9, customCategories.join());
for (const category of new Set([...categories, ...customCategories])) {
  check(`en.json names the category ${category}, and it is drawn with an icon the registry has`,
    Boolean(en.simulator?.category?.[category]) && CATEGORY_ICONS[category] in icons);
}

const words = {
  'simulator.model.apc-smart-ups.name': 'Onduleur APC Smart-UPS, triphasé',
  'simulator.model.apc-smart-ups.description': 'Un onduleur de 10 kVA',
  'simulator.model.cisco-isr-4331.name': 'Routeur Cisco ISR 4331',
  'simulator.model.cisco-isr-4331.description': 'Un routeur d’agence',
  'simulator.category.power': 'Énergie',
  'simulator.category.network': 'Réseau',
  'simulator.category.environment': 'Environnement',
};
const t = (key) => words[key] ?? key;
const catalogue = [
  { id: 'apc-smart-ups', category: 'power', custom: false, notifications: [{ name: 'upsTrapOnBattery' }] },
  { id: 'cisco-isr-4331', category: 'network', custom: false, notifications: [{ name: 'bgpEstablishedNotification' }] },
  { id: 'custom:acme-crac', category: 'environment', custom: true, name: 'Acme CRAC-40 cooling unit', vendor: 'Acme',
    description: 'A computer room air conditioner', notifications: [{ name: 'acmeHighTemperature' }] },
];
const found = (query, category = '') => filterModels(catalogue, query, category, t).map((m) => m.id).join(' ');
check('a search finds a model by its name in the locale, with its accents or without',
  found('onduleur') === 'apc-smart-ups' && found('TRIPHASE') === 'apc-smart-ups' && found('énergie') === 'apc-smart-ups');
check('every word must be found, anywhere that describes the model — a notification it sends included',
  found('cisco bgp') === 'cisco-isr-4331' && found('cisco onduleur') === '');
check('a custom model is found by its own name, description and vendor, which no locale knows',
  found('acme') === 'custom:acme-crac' && found('air conditioner') === 'custom:acme-crac');
check('a category narrows the list, and a search of nothing keeps all of it',
  found('', 'network') === 'cisco-isr-4331' && found('') === 'apc-smart-ups cisco-isr-4331 custom:acme-crac' &&
  found('cisco', 'power') === '');
check('a built-in model is named by the locale, a custom one by its file, and a gone one by its ID',
  modelName(catalogue[0], t) === 'Onduleur APC Smart-UPS, triphasé' && modelName(catalogue[2], t) === 'Acme CRAC-40 cooling unit' &&
  modelName(undefined, t, 'custom:gone') === 'gone' && modelDescription(catalogue[2], t) === 'A computer room air conditioner' &&
  modelDescription(undefined, t) === '');
check('the categories come in the order of their first model', categoriesOf(catalogue).join() === 'power,network,environment');
check('a category nobody drew is drawn as a package', categoryIcon('toaster') === 'package' && categoryIcon('wireless') === 'wifi');
check('the devices made from a model are the ones naming it',
  devicesOfModel([{ model: 'custom:acme-crac' }, { model: 'linux-server' }], 'custom:acme-crac').length === 1 &&
  devicesOfModel(undefined, 'x').length === 0);

const imports = importReport([
  { file: 'a.json', success: true, name: 'A', modelId: 'custom:a', replaced: false, warnings: [{ key: 'iconSkipped', detail: 'a.png' }] },
  { file: 'b.zip › b.json', success: false, error: 'line 3, column 5: …', warnings: [] },
  { file: 'c.zip › c.json', success: true, name: 'C', modelId: 'custom:c', replaced: true, warnings: [] },
]);
check('an import is reported model by model, each warning after its model',
  imports.map((l) => `${l.key}:${l.level}`).join(' ') ===
  'simulator.models.imported:success simulator.models.warning.iconSkipped:warning simulator.models.failed:error simulator.models.replaced:success',
  JSON.stringify(imports));
check('and the picker selects the first model an import brought',
  firstImported([{ success: false }, { success: true, modelId: 'custom:c' }]) === 'custom:c' && firstImported([]) === '');

// Every warning Go gives about an import has its sentence: the application's,
// and those pkg/simulator gives about a package's walks.
const appGo = readFileSync(new URL('../../internal/app/simmodels.go', import.meta.url), 'utf8') +
  readFileSync(new URL('../../pkg/simulator/package.go', import.meta.url), 'utf8');
const warningKeys = new Set([...appGo.matchAll(/Key: "([A-Za-z]+)"/g)].map((m) => m[1]));
check('the warnings an import gives were found in internal/app/simmodels.go and pkg/simulator/package.go',
  warningKeys.size >= 7, [...warningKeys].join());
for (const key of warningKeys) {
  check(`en.json says simulator.models.warning.${key}`, Boolean(en.simulator?.models?.warning?.[key]));
}
for (const key of ['imported', 'replaced', 'failed', 'deleted']) {
  check(`en.json says simulator.models.${key}`, Boolean(en.simulator?.models?.[key]));
}
for (const key of ['search', 'all', 'categories', 'noMatch', 'custom', 'import', 'importTitle', 'deleteModel', 'inUse',
  'record', 'recordTitle', 'exportModel']) {
  check(`en.json says simulator.picker.${key}`, Boolean(en.simulator?.picker?.[key]));
}
for (const key of ['hint', 'target', 'name', 'vendor', 'category', 'start', 'stop', 'progress', 'description', 'stopped', 'failed']) {
  check(`en.json says simulator.record.${key}`, Boolean(en.simulator?.record?.[key]));
}
check('en.json says simulator.models.exported', Boolean(en.simulator?.models?.exported));

// A device is recorded with what its target uses — its profile, its own
// overrides or the defaults — and the request carries exactly what Go's
// SimulatorRecordRequest reads, SnmpRequest's fields included: a key Go does
// not declare is dropped by encoding/json, and a field the renderer forgets
// arrives as its zero value, a port of 0 among them.
const goTags = (src, type) => {
  const body = src.match(new RegExp(`type ${type} struct \\{([\\s\\S]*?)\\n\\}`))?.[1] || '';
  return [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1]);
};
const recordGo = readFileSync(new URL('../../internal/app/simrecord.go', import.meta.url), 'utf8');
const paramsGo = readFileSync(new URL('../../pkg/snmp/params.go', import.meta.url), 'utf8');
const declared = [...goTags(recordGo, 'SimulatorRecordRequest'), ...goTags(paramsGo, 'SnmpRequest')].sort();
const recordSettings = {
  targets: '10.0.0.9 # core\n10.0.0.10',
  community: 'public', snmpVersion: 'v2c', port: 161, timeout: 3, retries: 1,
  v3: { user: '', securityLevel: 'noAuthNoPriv', authProtocol: 'SHA', authPass: '', privProtocol: 'AES', privPass: '', contextName: '' },
  targetOverrides: { '10.0.0.10': { community: 'lab-ro', snmpVersion: 'v1', port: 1161 } },
  credentialProfiles: [],
};
const recordReq = recordRequest(recordSettings, '10.0.0.10', { name: 'Core', vendor: 'Acme', category: 'network', description: 'd' });
check('a recording request carries what SimulatorRecordRequest reads, and nothing else',
  declared.length >= 12 && JSON.stringify(Object.keys(recordReq).sort()) === JSON.stringify(declared),
  `${Object.keys(recordReq).sort()} vs ${declared}`);
check('and reaches the device with what its target uses',
  recordReq.community === 'lab-ro' && recordReq.version === 'v1' && recordReq.port === 1161 &&
  JSON.stringify(recordReq.targets) === '["10.0.0.10"]' && recordReq.name === 'Core', JSON.stringify(recordReq));
check('the targets a device may be recorded from are the addresses the settings list',
  JSON.stringify(recordableTargets(recordSettings)) === '["10.0.0.9","10.0.0.10"]' &&
  recordableTargets({}).length === 0, JSON.stringify(recordableTargets(recordSettings)));

// A recorded model is filed under a category Go accepts, and every one Go
// accepts can be chosen.
const goCategories = [...customCategories].sort();
check('the categories a recording offers are those Go accepts',
  goCategories.length > 0 && JSON.stringify([...CUSTOM_CATEGORIES].sort()) === JSON.stringify(goCategories),
  `${CUSTOM_CATEGORIES} vs ${goCategories}`);

// An import of simulated devices is reported device by device, each warning
// after its device; a file refused whole is one line, under its own name.
const benchLines = deviceImportReport([
  { name: 'core-01', success: true, id: 'a', warnings: [{ key: 'addressMoved', detail: '127.0.0.2:161 → 127.0.0.3:161' }] },
  { name: 'gone', success: false, error: '"custom:x" is not a device model', warnings: [] },
  { name: 'edge-01', success: true, id: 'b', warnings: [{ key: 'noSecrets', detail: '' }] },
]);
check('an import of devices is reported device by device, each warning after its device',
  benchLines.map((l) => `${l.key}:${l.level}`).join(' ') ===
  'simulator.devices.imported:success simulator.devices.warning.addressMoved:warning simulator.devices.failed:error ' +
  'simulator.devices.imported:success simulator.devices.warning.noSecrets:warning',
  JSON.stringify(benchLines));

// Every warning Go gives about a device imported has its sentence.
const benchGo = readFileSync(new URL('../../internal/app/simbench.go', import.meta.url), 'utf8');
const benchKeys = new Set([...benchGo.matchAll(/Key: "([A-Za-z]+)"/g)].map((m) => m[1]));
check('the warnings a device import gives were found in internal/app/simbench.go', benchKeys.size >= 2, [...benchKeys].join());
for (const key of benchKeys) {
  check(`en.json says simulator.devices.warning.${key}`, Boolean(en.simulator?.devices?.warning?.[key]));
}
for (const key of ['importDevices', 'importDevicesTitle', 'exportAll', 'exportAllTitle', 'exportDevice', 'duplicate',
  'copyName', 'duplicated', 'restart', 'restarted']) {
  check(`en.json says simulator.${key}`, Boolean(en.simulator?.[key]));
}
for (const key of ['title', 'hint', 'without', 'with', 'done']) {
  check(`en.json says simulator.export.${key}`, Boolean(en.simulator?.export?.[key]));
}
for (const key of ['imported', 'failed']) {
  check(`en.json says simulator.devices.${key}`, Boolean(en.simulator?.devices?.[key]));
}

/* --- faults ---------------------------------------------------------------- */

// What the panel sends is exactly what Go's Faults reads, in bounds: a share of
// errors only with an error, a counter speed Go accepts.
const faultsGo = readFileSync(new URL('../../pkg/simulator/faults.go', import.meta.url), 'utf8');
const faultTags = goTags(faultsGo, 'Faults').sort();
const sentFaults = faultsPayload({ latencyMs: '250.4', jitterMs: 99999, lossPercent: -5, mute: 1, error: 'genErr', errorPercent: 0, counterSpeed: '100' });
check('the faults sent are what Go reads, and nothing else',
  faultTags.length === 7 && JSON.stringify(Object.keys(sentFaults).sort()) === JSON.stringify(faultTags),
  `${Object.keys(sentFaults).sort()} vs ${faultTags}`);
check('and in bounds',
  sentFaults.latencyMs === 250 && sentFaults.jitterMs === 10000 && sentFaults.lossPercent === 0 && sentFaults.mute === true &&
  sentFaults.errorPercent === 1 && sentFaults.counterSpeed === 100, JSON.stringify(sentFaults));
check('no share of errors without an error, and an unknown speed is 1',
  faultsPayload({ ...blankFaults(), errorPercent: 40, error: 'noSuchName', counterSpeed: 7 }).errorPercent === 0 &&
  faultsPayload({ ...blankFaults(), counterSpeed: 7 }).counterSpeed === 1);
check('the speeds offered are those Go accepts',
  JSON.stringify(COUNTER_SPEEDS) === JSON.stringify([...faultsGo.matchAll(/counterSpeeds = \[\]int\{([^}]*)\}/g)][0][1]
    .split(',').map((s) => Number(s.trim())).filter((n) => n > 0)));
check('an error chosen in the panel answers every request until a share is given',
  editableFaults({ ...blankFaults(), error: '' }).errorPercent === 100 &&
  editableFaults({ ...blankFaults(), error: 'tooBig', errorPercent: 30 }).errorPercent === 30);
const chips = faultChips({ latencyMs: 500, jitterMs: 100, lossPercent: 10, mute: true, error: 'tooBig', errorPercent: 20, counterSpeed: 1000 });
check('a device doing everything wrong says so, fault by fault',
  chips.map((c) => c.key).join() === 'mute,latencyJitter,loss,error,counters' && faultChips(blankFaults()).length === 0,
  JSON.stringify(chips));
for (const key of ['button', 'hint', 'mute', 'latency', 'jitter', 'loss', 'error', 'errorNone', 'counters', 'apply', 'clear', 'applied', 'cleared']) {
  check(`en.json says simulator.faults.${key}`, Boolean(en.simulator?.faults?.[key]));
}
for (const key of ['mute', 'latency', 'latencyJitter', 'loss', 'error', 'counters']) {
  check(`en.json says simulator.faults.chip.${key}`, Boolean(en.simulator?.faults?.chip?.[key]));
}
for (const key of ['location', 'contact', 'modelDefault', 'autoStart']) {
  check(`en.json says simulator.field.${key}`, Boolean(en.simulator?.field?.[key]));
}

// The list files each device under its model's category, in the catalogue's
// order, and a device of a model since deleted under "other".
const groupModels = [{ id: 'linux-server', category: 'server' }, { id: 'cisco-catalyst-24', category: 'network' }];
const filed = deviceGroups([
  { id: '1', model: 'cisco-catalyst-24' }, { id: '2', model: 'custom:gone' }, { id: '3', model: 'linux-server' },
  { id: '4', model: 'linux-server' },
], groupModels);
check('devices are grouped by category, in the catalogue order',
  filed.map((g) => `${g.category}:${g.devices.map((d) => d.id).join('+')}`).join(' ') === 'server:3+4 network:1 other:2',
  JSON.stringify(filed));

/* --- a device's data speaks Go's vocabulary ------------------------------- */

const goTypes = [...(customGo.match(/customTypes = map\[string\]gosnmp\.Asn1BER\{([^}]*)\}/)?.[1] || '').matchAll(/"([^"]+)":/g)]
  .map((m) => m[1]);
check('every type offered for a value of its own is one Go reads',
  goTypes.length > 0 && OVERRIDE_TYPES.every((t) => goTypes.includes(t)), OVERRIDE_TYPES.filter((t) => !goTypes.includes(t)).join());
const overridesGo = readFileSync(new URL('../../pkg/simulator/overrides.go', import.meta.url), 'utf8');
check('as many values of its own as Go keeps', Number(overridesGo.match(/MaxOverrides = (\d+)/)?.[1]) === MAX_OVERRIDES);
const packageGo = readFileSync(new URL('../../pkg/simulator/package.go', import.meta.url), 'utf8');
const goSubtrees = [...(packageGo.match(/agentSubtrees = \[\]oid\{(.*)\}/)?.[1] || '').matchAll(/\{([\d, ]+)\}/g)]
  .map((m) => m[1].split(',').map((s) => s.trim()).join('.'));
check("the agent's subtrees are Go's", JSON.stringify(goSubtrees) === JSON.stringify(AGENT_SUBTREES), JSON.stringify(goSubtrees));
const paramNames = new Set();
for (const file of readdirSync(goDir).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))) {
  for (const m of readFileSync(new URL(file, goDir), 'utf8').matchAll(/ModelParam\{\{Name:\s*"([^"]+)"/g)) paramNames.add(m[1]);
}
check('every number a model may be given is named in en.json',
  paramNames.size >= 4 && [...paramNames].every((n) => en.simulator?.param?.[n]), [...paramNames].join());

/* --- the preview speaks Go's vocabulary ---------------------------------- */

const previewGo = readFileSync(new URL('../../pkg/simulator/preview.go', import.meta.url), 'utf8');
const behaviours = [...previewGo.matchAll(/Behaviour\w+\s*=\s*"(\w+)"/g)].map((m) => m[1]);
check('every behaviour a preview gives a value is named in en.json',
  behaviours.length >= 6 && behaviours.every((b) => en.simulator?.data?.behaviour?.[b]), behaviours.join());
check('and every column of the preview', PREVIEW_COLUMNS.every((c) => en.simulator?.data?.column?.[c]));
const queryGo = readFileSync(new URL('../../internal/app/simpreview.go', import.meta.url), 'utf8')
  .match(/type SimulatorPreviewQuery struct \{([^}]*)\}/)?.[1] || '';
const queryTags = [...queryGo.matchAll(/json:"(\w+)"/g)].map((m) => m[1]).sort();
check('a preview is asked for in the fields Go reads, and no other',
  queryTags.length === 3 && JSON.stringify(Object.keys(previewQuery('x', true, 1)).sort()) === JSON.stringify(queryTags),
  queryTags.join());
check('read as if the device had run since the tab was opened',
  previewQuery(' ifDescr ', 1, 1000, 61000).sinceSeconds === 60 && previewQuery(' ifDescr ', 1, 1000, 61000).filter === 'ifDescr' &&
  previewQuery('', false, 0).sinceSeconds === 0);

/* --- the bench's cables -------------------------------------------------- */

const cables = await import(pathToFileURL(bundle).href);
const benchDevices = [{ id: 'core', name: 'core-01' }, { id: 'sw', name: 'sw-floor-2' }, { id: 'ph', name: 'phone' }];
const benchLinks = [
  { id: 'l2', a: { device: 'ph', port: 'eth0' }, b: { device: 'sw', port: 'Fa0/10' } },
  { id: 'l1', a: { device: 'sw', port: 'Gi0/1' }, b: { device: 'core', port: 'Te1/1/1' } },
  { id: 'l3', a: { device: 'gone', port: 'eth0' }, b: { device: 'core', port: 'Gi1/0/1' } },
];
const rows = cables.cableRows(benchLinks, benchDevices);
check('a cable is listed from the end whose device comes first, a cable to a device gone is not',
  rows.length === 2 && rows[0].a.name === 'core-01' && rows[0].a.port === 'Te1/1/1' && rows[0].b.name === 'sw-floor-2' &&
  rows[1].a.name === 'sw-floor-2' && rows[1].a.port === 'Fa0/10' && rows[1].b.name === 'phone',
  JSON.stringify(rows));
const swPorts = [{ name: 'Fa0/2' }, { name: 'Fa0/10' }, { name: 'Gi0/1' }];
check('a port a cable is in is offered to no other cable',
  cables.freePorts(swPorts, benchLinks, 'sw').map((p) => p.name).join() === 'Fa0/2',
  cables.freePorts(swPorts, benchLinks, 'sw').map((p) => p.name).join());
check('ports are ordered as a person reads them',
  ['Fa0/10', 'Fa0/2', 'Gi0/1'].sort(cables.portOrder).join() === 'Fa0/2,Fa0/10,Gi0/1');
check('a cable is ready once it names two ports of two devices',
  cables.cableReady({ a: { device: 'sw', port: 'Fa0/2' }, b: { device: 'core', port: 'Gi1/0/2' } }) &&
  !cables.cableReady({ a: { device: 'sw', port: 'Fa0/2' }, b: { device: 'sw', port: 'Fa0/3' } }) &&
  !cables.cableReady({ a: { device: 'sw', port: '' }, b: { device: 'core', port: 'Gi1/0/2' } }));
check('a phone has an icon of its own', cables.categoryIcon('telephony') === 'phone');

/* --- the demo's simulator ------------------------------------------------ */

// The browser demo has no backend. Its simulator answers from the scenes that
// photograph it (screenshots/demoBindings.js), and what a device DOES is
// refused (bridge/dynamic.js, DEMO_ONLY) — never answered "ok" with nothing done.
const { buildScenes } = await import('../screenshots/scenes.js');
const { demoBindings } = await import('../screenshots/demoBindings.js');
const demo = demoBindings(buildScenes(JSON.parse(
  readFileSync(new URL('../screenshots/bridge/seeds.json', import.meta.url), 'utf8'))));
const demoModels = demo.ListSimulatorModels.map((m) => m.id);
check('the demo offers the catalogue to make a device from', demoModels.length >= 14, demoModels.join());
check('and lists a bench made from it, each device once',
  demo.ListSimulatedDevices.length >= 3 &&
  demo.ListSimulatedDevices.every((d) => demoModels.includes(d.model)) &&
  new Set(demo.ListSimulatedDevices.map((d) => d.id)).size === demo.ListSimulatedDevices.length,
  demo.ListSimulatedDevices.map((d) => `${d.id}:${d.model}`).join());
// A device with no SNMPv3 user is opened on a blank one, as in the application;
// what is checked is that the users a device HAS come with their passphrases.
const demoOpened = demo.ListSimulatedDevices.map((d) => [d, editableDevice(d, demo.SimulatorDeviceCredentials)]);
check('the editor opens a demo device on its identifiers, not on blank fields',
  demoOpened.every(([d, e]) => e.community && (d.users || []).every((v) => {
    const u = e.users.find((x) => x.user === v.name);
    return u && u.authPass && u.privPass;
  })) && demoOpened.some(([d]) => d.users?.length),
  JSON.stringify(demoOpened.map(([, e]) => [e.name, e.community, e.users.map((u) => u.user)])));

// The scenes' catalogue is written by hand, and the demo now shows it to
// anyone: it has to be Go's, model for model. A bound Go computes — the PDU's
// len(rackOutlets) — is not compared; the literals are.
const goModelVars = new Map();
for (const file of readdirSync(goDir).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))) {
  const src = readFileSync(new URL(file, goDir), 'utf8');
  const decl = /(\w+)\s*=\s*model\{\s*ModelInfo:\s*ModelInfo\{ID:\s*"([^"]+)",\s*Category:\s*"([^"]+)"(?:,\s*Params:\s*\[\]ModelParam\{\{([^}]*)\}\})?/g;
  for (const m of src.matchAll(decl)) {
    const param = m[4] ? Object.fromEntries([...m[4].matchAll(/(\w+):\s*("?)([^,"]+)\2/g)].map((p) => [p[1], p[3].trim()])) : null;
    goModelVars.set(m[1], { id: m[2], category: m[3], param });
  }
}
const goOrder = (readFileSync(new URL('model.go', goDir), 'utf8').match(/var models = \[\]model\{([^}]*)\}/)?.[1] || '')
  .split(',').map((s) => s.trim()).filter(Boolean).map((v) => goModelVars.get(v));
const sameAsGo = (go, fx) => {
  if (!go || !fx || go.id !== fx.id || go.category !== fx.category) return false;
  const p = fx.params || [];
  if (!go.param) return p.length === 0;
  return p.length === 1 && p[0].name === go.param.Name &&
    ['Min', 'Max', 'Default'].every((k) => !/^\d+$/.test(go.param[k]) || Number(go.param[k]) === p[0][k.toLowerCase()]);
};
const builtIn = demo.ListSimulatorModels.filter((m) => !m.custom);
check("the demo's catalogue is Go's: every built-in model, in Go's order, with its category and parameters",
  goOrder.length >= 14 && goOrder.length === builtIn.length && goOrder.every((g, i) => sameAsGo(g, builtIn[i])),
  goOrder.map((g) => g?.id).join());

const dynamicJs = readFileSync(new URL('../screenshots/bridge/dynamic.js', import.meta.url), 'utf8');
const refusedInDemo = new Set([...(dynamicJs.match(/const DEMO_ONLY = \{([\s\S]*?)\n\};/)?.[1] || '').matchAll(/^\s+(\w+):/gm)]
  .map((m) => m[1]));
const simulatorCalls = new Set();
const srcDir = new URL('../src/', import.meta.url);
for (const file of readdirSync(srcDir, { recursive: true }).filter((f) => /\.(js|svelte)$/.test(f))) {
  const src = readFileSync(new URL(file.replace(/\\/g, '/'), srcDir), 'utf8');
  for (const m of src.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"][^'"]*wailsjs\/go\/app\/App['"]/g)) {
    for (const name of m[1].split(',').map((s) => s.trim().split(/\s+as\s+/)[0])) {
      if (/Simulat/.test(name)) simulatorCalls.add(name);
    }
  }
}
// Nothing to cancel: a recording is refused before it starts.
const harmless = new Set(['SimulatorCancelRecording']);
const unanswered = [...simulatorCalls].filter((n) => !(n in demo) && !refusedInDemo.has(n) && !harmless.has(n));
check('in the demo every simulator call is answered or refused, none answered "ok" with nothing done',
  simulatorCalls.size >= 15 && refusedInDemo.has('SimulatorStartDevice') && unanswered.length === 0,
  unanswered.join() || `${simulatorCalls.size} calls`);

process.exit(failures ? 1 : 0);
