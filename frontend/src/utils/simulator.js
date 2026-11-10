/**
 * The simulator's rules on the renderer's side, kept out of the components so
 * they run under node (tests/simulator.test.mjs).
 *
 * Go is the authority on a device (simulator.Device.Validate, the loopback rule
 * included). What is here spares a round trip for the obvious, and decides the
 * one thing Go cannot: how a device becomes a TARGET, which lives in the
 * renderer's settings.
 *
 * The editor holds each SNMPv3 user in the shape UsmFields edits — `user`,
 * `secLevel`, the protocols and passphrases — and devicePayload turns them into
 * Go's `users` list. What the device sends, `traps`, is Go's shape already, its
 * destinations' communities filled in from the credential store by ID.
 */
import { usesAuth, usesPriv } from './snmpSecurity.js';
import { buildSnmpRequest } from './snmpParams.js';
import { getEffectiveSettings, getTargetsAsArray } from './targets.js';

/** The versions a device may answer, in the order they are shown. */
export const SIM_VERSIONS = ['v1', 'v2c', 'v3'];

/** The shortest passphrase a simulated user may have, as pkg/simulator holds it. */
export const MIN_PASSPHRASE = 8;

/** The longest a schedule waits between two notifications, as pkg/simulator holds it: a day. */
export const MAX_EVERY = 86400;

/** How many destinations and schedules one device keeps, as pkg/simulator bounds them. */
export const MAX_DESTINATIONS = 8;
export const MAX_SCHEDULES = 8;

/** What a new device sends: nothing, to nowhere. */
export function blankTraps() {
  return { destinations: [], onStart: false, onAuthFailure: false, schedules: [] };
}

/**
 * A new destination: SnmpLens on this machine, at the port its trap listener
 * takes, in v2c — the first place a person testing SnmpLens wants traps sent.
 */
export function blankDestination(trapPort) {
  return {
    id: '',
    host: '127.0.0.1',
    port: Number(trapPort) || 162,
    version: 'v2c',
    inform: false,
    community: 'public',
    user: '',
    engineId: '',
  };
}

/** A new schedule: a notification at random, every minute. */
export function blankSchedule() {
  return { notification: '', every: 60, irregular: false };
}

/** A new SNMPv3 user; `n` numbers the ones after the first. */
export function blankV3(n = 1) {
  return {
    user: n === 1 ? 'simulator' : `user${n}`,
    secLevel: 'AuthPriv',
    authProto: 'SHA256',
    authPass: '',
    privProto: 'AES',
    privPass: '',
    contextName: '',
    write: false,
  };
}

/**
 * The editor's tabs, in order, and the fields of deviceProblems each holds: a
 * tab with something wrong in it is marked, since what is wrong may be on a tab
 * nobody is looking at.
 */
export const EDITOR_TABS = [
  { id: 'identity', fields: ['name', 'port'] },
  { id: 'access', fields: ['versions', 'community', 'writeCommunity', 'noUser', 'users'] },
  { id: 'traps', fields: ['destinations', 'schedules'] },
  { id: 'data', fields: ['params', 'overrides'] },
  { id: 'faults', fields: [] },
];

/** The tabs holding at least one of a device's problems. */
export function tabsWithProblems(problems) {
  const keys = Object.keys(problems || {});
  return EDITOR_TABS.filter((t) => t.fields.some((f) => keys.includes(f))).map((t) => t.id);
}

/**
 * The types a value of a device's own may be given, by the SMI's names — the
 * ones pkg/simulator reads in a model file (customTypes), less its aliases.
 */
export const OVERRIDE_TYPES = [
  'OctetString', 'Integer', 'Counter32', 'Gauge32', 'TimeTicks', 'Counter64', 'ObjectIdentifier', 'IpAddress', 'Opaque',
];

/** How many values of its own one device keeps, as pkg/simulator bounds them. */
export const MAX_OVERRIDES = 64;

/** The agent's own subtrees, which no value of a device's own may be under (pkg/simulator's agentSubtrees). */
export const AGENT_SUBTREES = ['1.3.6.1.2.1.11', '1.3.6.1.6.3'];

const NUMERIC_TYPES = ['Integer', 'Counter32', 'Gauge32', 'TimeTicks', 'Counter64'];

/** A new value of a device's own. */
export function blankOverride() {
  return { oid: '', type: 'OctetString', value: '', hex: false };
}

/** An OID as Go reads it — dotted numbers, a leading dot allowed — or '' when it is not one. */
export function normalOid(s) {
  const t = String(s || '').trim().replace(/^\./, '');
  return /^\d+(\.\d+)+$/.test(t) ? t : '';
}

/** The parameters given, as numbers: an empty field is the model's own, and is not sent. */
function paramsPayload(params) {
  const out = {};
  for (const [name, v] of Object.entries(params || {})) {
    if (v === '' || v === null || v === undefined) continue;
    out[name] = Number(v);
  }
  return out;
}

/**
 * A new device, where the backend suggested it answer. v2c alone, with the
 * community every tool tries first: a first device should answer before anyone
 * has typed a passphrase.
 */
export function blankDevice(model, suggestion) {
  return {
    id: '',
    name: '',
    model,
    address: suggestion?.address || '127.0.0.2',
    port: suggestion?.port || 1161,
    versions: ['v2c'],
    community: 'public',
    // Read-only until someone gives it a way to be written.
    writeCommunity: '',
    users: [blankV3()],
    traps: blankTraps(),
    location: '',
    contact: '',
    autoStart: false,
    // The model as the catalogue describes it, and nothing of its own.
    params: {},
    overrides: [],
  };
}

/**
 * A listed device in the editor's shape, its credentials filled in. The list
 * never carries them; they come from SimulatorDeviceCredentials, by user name.
 */
export function editableDevice(view, creds) {
  const users = (view.users || []).map((u) => ({
    user: u.name,
    secLevel: u.secLevel,
    authProto: u.authProto || 'SHA256',
    authPass: creds?.users?.[u.name]?.authPass || '',
    privProto: u.privProto || 'AES',
    privPass: creds?.users?.[u.name]?.privPass || '',
    contextName: '',
    write: !!u.write,
  }));
  return {
    id: view.id,
    name: view.name,
    model: view.model,
    address: view.address,
    port: view.port,
    versions: [...(view.versions || [])],
    community: creds?.community || '',
    writeCommunity: creds?.writeCommunity || '',
    users: users.length ? users : [blankV3()],
    // Shown, never sent: the engine is the backend's.
    engineId: view.engineId || '',
    engineBoots: view.engineBoots || 0,
    location: view.location || '',
    contact: view.contact || '',
    autoStart: !!view.autoStart,
    params: { ...(view.params || {}) },
    overrides: (view.overrides || []).map((o) => ({ oid: o.oid, type: o.type, value: o.value, hex: !!o.hex })),
    traps: {
      destinations: (view.traps?.destinations || []).map((t) => ({
        id: t.id,
        host: t.host,
        port: t.port,
        version: t.version,
        inform: !!t.inform,
        community: creds?.destinations?.[t.id] || '',
        user: t.user || '',
        engineId: t.engineId || '',
      })),
      onStart: !!view.traps?.onStart,
      onAuthFailure: !!view.traps?.onAuthFailure,
      schedules: (view.traps?.schedules || []).map((s) => ({ ...s })),
    },
  };
}

const usesCommunity = (versions) => versions.includes('v1') || versions.includes('v2c');

/**
 * What a device would be refused for, as i18n key suffixes
 * (simulator.problem.<key>): by field, and under `users`, one entry per SNMPv3
 * user — present only when one of them has something wrong. Mirrors
 * Device.Validate for what a form can get wrong; Go still checks all of it, and
 * the address. `model`, the device's as Go lists it, bounds its parameters.
 */
export function deviceProblems(d, model) {
  const problems = {};
  const versions = d.versions || [];
  if (!d.name?.trim()) problems.name = 'nameRequired';
  const port = Number(d.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) problems.port = 'port';
  if (!versions.length) problems.versions = 'versionRequired';
  if (usesCommunity(versions) && !d.community) problems.community = 'communityRequired';
  // One community for both is every manager that reads also writing.
  if (usesCommunity(versions) && d.writeCommunity && d.writeCommunity === d.community) {
    problems.writeCommunity = 'writeSameAsRead';
  }
  if (versions.includes('v3')) {
    const users = d.users || [];
    if (!users.length) problems.noUser = 'userRequired';
    const count = new Map();
    for (const u of users) {
      const name = (u.user || '').trim();
      count.set(name, (count.get(name) || 0) + 1);
    }
    const perUser = users.map((u) => {
      const p = {};
      const name = (u.user || '').trim();
      if (!name) p.user = 'userRequired';
      else if (count.get(name) > 1) p.user = 'userDuplicate';
      if (usesAuth(u.secLevel) && (u.authPass || '').length < MIN_PASSPHRASE) p.authPass = 'passphraseShort';
      if (usesPriv(u.secLevel) && (u.privPass || '').length < MIN_PASSPHRASE) p.privPass = 'passphraseShort';
      return p;
    });
    if (perUser.some((p) => Object.keys(p).length)) problems.users = perUser;
  }
  const answersV3 = versions.includes('v3');
  const userNames = answersV3 ? (d.users || []).map((u) => (u.user || '').trim()) : [];
  const perDestination = (d.traps?.destinations || []).map((t) => destinationProblems(t, userNames, answersV3));
  if (perDestination.some((p) => Object.keys(p).length)) problems.destinations = perDestination;
  const perSchedule = (d.traps?.schedules || []).map((s) => {
    const every = Number(s.every);
    return Number.isInteger(every) && every >= 1 && every <= MAX_EVERY ? {} : { every: 'every' };
  });
  if (perSchedule.some((p) => Object.keys(p).length)) problems.schedules = perSchedule;
  const perParam = {};
  for (const p of model?.params || []) {
    const v = d.params?.[p.name];
    if (v === '' || v === null || v === undefined) continue;
    const n = Number(v);
    if (!Number.isInteger(n) || n < p.min || n > p.max) perParam[p.name] = 'paramRange';
  }
  if (Object.keys(perParam).length) problems.params = perParam;
  const oids = (d.overrides || []).map((o) => normalOid(o.oid));
  const perOverride = (d.overrides || []).map((o, i) => {
    const p = {};
    const oid = oids[i];
    if (!oid) p.oid = 'overrideOid';
    else if (AGENT_SUBTREES.some((s) => oid === s || oid.startsWith(`${s}.`))) p.oid = 'overrideAgent';
    else if (oids.indexOf(oid) !== i) p.oid = 'overrideDuplicate';
    const value = String(o.value ?? '').trim();
    if (NUMERIC_TYPES.includes(o.type) && !(o.type === 'Integer' ? /^-?\d+$/ : /^\d+$/).test(value)) p.value = 'overrideNumber';
    return p;
  });
  if (perOverride.some((p) => Object.keys(p).length)) problems.overrides = perOverride;
  return problems;
}

/**
 * What one destination would be refused for. A v3 notification is sent as one
 * of the device's SNMPv3 users, and a device has users only when it answers v3.
 */
function destinationProblems(t, userNames, answersV3) {
  const p = {};
  if (!hostOf(t.host)) p.host = 'hostRequired';
  const port = Number(t.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) p.port = 'port';
  if (t.version === 'v3') {
    if (!answersV3) p.user = 'trapNeedsV3';
    else if (!userNames.includes((t.user || '').trim())) p.user = 'trapUser';
    if (t.inform && t.engineId && !/^([0-9a-f]{2}){5,32}$/.test(engineIdOf(t.engineId))) p.engineId = 'engineId';
  } else if (!t.community) {
    p.community = 'communityRequired';
  }
  return p;
}

/** A host as Go takes it: without the brackets people put round an IPv6 literal. */
function hostOf(host) {
  return (host || '').trim().replace(/^\[(.*)\]$/, '$1');
}

/** An engine ID as Go takes it: hex, without a 0x, colons or spaces. */
export function engineIdOf(s) {
  return (s || '').trim().replace(/^0x/i, '').replace(/[\s:]/g, '').toLowerCase();
}

/**
 * The device as SimulatorSaveDevice takes it: the community only if a version
 * uses one, the users only if v3 is answered. The engine is the backend's to
 * give, and is not sent.
 */
export function devicePayload(d) {
  const versions = SIM_VERSIONS.filter((v) => d.versions.includes(v));
  return {
    id: d.id || '',
    name: d.name.trim(),
    model: d.model,
    address: d.address.trim(),
    port: Number(d.port),
    versions,
    community: usesCommunity(versions) ? d.community : '',
    writeCommunity: usesCommunity(versions) ? d.writeCommunity || '' : '',
    users: versions.includes('v3')
      ? (d.users || []).map((u) => ({
          name: (u.user || '').trim(),
          secLevel: u.secLevel,
          authProto: u.authProto,
          authPass: u.authPass,
          privProto: u.privProto,
          privPass: u.privPass,
          write: !!u.write,
        }))
      : [],
    location: (d.location || '').trim(),
    contact: (d.contact || '').trim(),
    autoStart: !!d.autoStart,
    params: paramsPayload(d.params),
    overrides: (d.overrides || []).map((o) => ({
      oid: String(o.oid || '').trim(),
      type: o.type,
      value: String(o.value ?? ''),
      hex: o.type === 'OctetString' && !!o.hex,
    })),
    engineId: '',
    engineBoots: 0,
    // Each destination with only what its version uses, as the users are.
    traps: {
      destinations: (d.traps?.destinations || []).map((t) => {
        const v3 = t.version === 'v3';
        const inform = t.version !== 'v1' && !!t.inform;
        return {
          id: t.id || '',
          host: hostOf(t.host),
          port: Number(t.port),
          version: t.version,
          inform,
          community: v3 ? '' : t.community,
          user: v3 ? (t.user || '').trim() : '',
          engineId: v3 && inform ? engineIdOf(t.engineId) : '',
        };
      }),
      onStart: !!d.traps?.onStart,
      onAuthFailure: !!d.traps?.onAuthFailure,
      schedules: (d.traps?.schedules || []).map((s) => ({
        notification: s.notification || '',
        every: Number(s.every),
        irregular: !!s.irregular,
      })),
    },
  };
}

/** The preview's columns, in order (simulator.data.column.<id>). */
export const PREVIEW_COLUMNS = ['oid', 'name', 'type', 'behaviour', 'value'];

/**
 * What SimulatorPreview is asked for: the rows under an OID or whose MIB name
 * holds the filter, only those that move when asked, read as if the device had
 * run since the Data tab was opened at `openedAt` — so that what moves has moved
 * each time it is read again.
 */
export function previewQuery(filter, dynamicOnly, openedAt, now = Date.now()) {
  return {
    filter: String(filter || '').trim(),
    dynamicOnly: !!dynamicOnly,
    sinceSeconds: openedAt ? Math.max(0, (now - openedAt) / 1000) : 0,
  };
}

/**
 * The device as SimulatorPreview takes it: what makes its answers, and not one
 * secret — a preview needs none, so none crosses the bridge for it.
 */
export function previewPayload(d) {
  const p = devicePayload(d);
  return {
    ...p,
    community: '',
    writeCommunity: '',
    users: p.users.map((u) => ({ ...u, authPass: '', privPass: '' })),
    traps: { ...p.traps, destinations: p.traps.destinations.map((t) => ({ ...t, community: '' })) },
  };
}

/**
 * The models grouped by category, in the order Go lists them: a category comes
 * where its first model does.
 */
export function modelGroups(models) {
  const groups = [];
  for (const m of models || []) {
    let group = groups.find((g) => g.category === m.category);
    if (!group) {
      group = { category: m.category, models: [] };
      groups.push(group);
    }
    group.models.push(m);
  }
  return groups;
}

/** The categories the models are in, in the order Go lists them. */
export function categoriesOf(models) {
  return modelGroups(models).map((g) => g.category);
}

/**
 * The Lucide icon a model is drawn with when it has no picture of its own: its
 * category's. A custom model that came with an icon is drawn with that.
 */
export const CATEGORY_ICONS = {
  server: 'server',
  network: 'network',
  security: 'shield',
  wireless: 'wifi',
  storage: 'hard-drive',
  power: 'battery-charging',
  printing: 'printer',
  environment: 'thermometer',
  telephony: 'phone',
  other: 'package',
};

export function categoryIcon(category) {
  return CATEGORY_ICONS[category] || CATEGORY_ICONS.other;
}

/* --- the bench's cables -------------------------------------------------- */

/**
 * The cables as the list shows them: each end with its device's name, the
 * devices in the order they are listed and the ports as a person orders them.
 * A cable to a device no longer listed is left out, as Go drops it.
 */
export function cableRows(links, devices) {
  const byId = new Map((devices || []).map((d, i) => [d.id, { name: d.name, order: i }]));
  const rows = [];
  for (const l of links || []) {
    const a = byId.get(l.a?.device);
    const b = byId.get(l.b?.device);
    if (!a || !b) continue;
    const ends = [{ id: l.a.device, name: a.name, port: l.a.port, order: a.order },
      { id: l.b.device, name: b.name, port: l.b.port, order: b.order }];
    if (ends[1].order < ends[0].order) ends.reverse();
    rows.push({ id: l.id, a: ends[0], b: ends[1] });
  }
  return rows.sort((x, y) => x.a.order - y.a.order || portOrder(x.a.port, y.a.port) || x.b.order - y.b.order);
}

/** Port names in the order a person reads them: Fa0/2 before Fa0/10. */
export function portOrder(a, b) {
  return String(a).localeCompare(String(b), undefined, { numeric: true });
}

/** The ports of a device a cable can still go into: those no cable is in. */
export function freePorts(ports, links, deviceId) {
  const used = new Set();
  for (const l of links || []) {
    if (l.a?.device === deviceId) used.add(l.a.port);
    if (l.b?.device === deviceId) used.add(l.b.port);
  }
  return (ports || []).filter((p) => !used.has(p.name));
}

/** Whether a cable being made names two ports of two devices. */
export function cableReady(c) {
  return !!(c?.a?.device && c.a.port && c.b?.device && c.b.port && c.a.device !== c.b.device);
}

/** The model of that ID — undefined for a custom model since deleted. */
export function modelOf(models, id) {
  return (models || []).find((m) => m.id === id);
}

/**
 * What a model is called: a built-in one in the locale, a custom one in the
 * words of its file, which no locale knows. A model that is gone is called by
 * the ID its devices still carry.
 */
export function modelName(model, t, id = '') {
  if (!model) return id.replace(/^custom:/, '');
  return model.custom ? model.name : t(`simulator.model.${model.id}.name`);
}

export function modelDescription(model, t) {
  if (!model) return '';
  return model.custom ? model.description || '' : t(`simulator.model.${model.id}.description`);
}

/** Text as a search compares it: lower case, accents dropped — "ecran" finds "écran". */
export function fold(s) {
  return String(s ?? '').normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
}

/**
 * The models a search keeps, in the order given: those of the category, when
 * one is chosen, where every word of the query is found in what describes the
 * model — its name, vendor, description, category, ID, or a notification it
 * sends. "cisco bgp" finds the router; "onduleur" finds the UPS in French.
 */
export function filterModels(models, query, category, t) {
  const words = fold(query).split(/\s+/).filter(Boolean);
  return (models || []).filter((m) => {
    if (category && m.category !== category) return false;
    if (!words.length) return true;
    const text = fold([
      modelName(m, t), m.vendor, modelDescription(m, t), t(`simulator.category.${m.category}`), m.id,
      ...(m.notifications || []).map((n) => n.name),
    ].join(' '));
    return words.every((w) => text.includes(w));
  });
}

/** The devices made from a model: while there is one, the model is not deleted. */
export function devicesOfModel(devices, id) {
  return (devices || []).filter((d) => d.model === id);
}

/**
 * What to say about an import, as i18n keys: a line per model imported, updated
 * or refused, and one per warning about a model imported anyway.
 */
export function importReport(results) {
  const out = [];
  for (const r of results || []) {
    if (!r.success) {
      out.push({ key: 'simulator.models.failed', values: { file: r.file, error: r.error }, level: 'error' });
      continue;
    }
    out.push({
      key: r.replaced ? 'simulator.models.replaced' : 'simulator.models.imported',
      values: { name: r.name },
      level: 'success',
    });
    for (const w of r.warnings || []) {
      out.push({ key: `simulator.models.warning.${w.key}`, values: { name: r.name, detail: w.detail }, level: 'warning' });
    }
  }
  return out;
}

/** The first model an import brought in, which the picker then selects. */
export function firstImported(results) {
  return (results || []).find((r) => r.success)?.modelId || '';
}

/**
 * What to say about an import of simulated devices, as i18n keys: a line per
 * device imported or refused — or for the file, when it was refused whole —
 * and one per warning about a device imported anyway.
 */
export function deviceImportReport(results) {
  const out = [];
  for (const r of results || []) {
    if (!r.success) {
      out.push({ key: 'simulator.devices.failed', values: { name: r.name, error: r.error }, level: 'error' });
      continue;
    }
    out.push({ key: 'simulator.devices.imported', values: { name: r.name }, level: 'success' });
    for (const w of r.warnings || []) {
      out.push({ key: `simulator.devices.warning.${w.key}`, values: { name: r.name, detail: w.detail }, level: 'warning' });
    }
  }
  return out;
}

/** The speeds a device's counters can run at, and the longest latency, as pkg/simulator bounds them. */
export const COUNTER_SPEEDS = [1, 10, 100, 1000];
export const MAX_LATENCY_MS = 10000;

/** A device doing nothing wrong. */
export function blankFaults() {
  return { latencyMs: 0, jitterMs: 0, lossPercent: 0, mute: false, error: '', errorPercent: 0, counterSpeed: 1 };
}

/**
 * A device's faults in the panel's shape: an error, once chosen, answers every
 * request until a share is given.
 */
export function editableFaults(f) {
  return {
    ...blankFaults(),
    ...(f || {}),
    counterSpeed: Math.max(Number(f?.counterSpeed) || 1, 1),
    errorPercent: f?.error ? f.errorPercent : 100,
  };
}

/** The faults as SimulatorSetFaults takes them: whole numbers in bounds, and no share of errors without an error. */
export function faultsPayload(f) {
  const whole = (v, lo, hi) => Math.min(Math.max(Math.round(Number(v) || 0), lo), hi);
  const error = f.error === 'tooBig' || f.error === 'genErr' ? f.error : '';
  return {
    latencyMs: whole(f.latencyMs, 0, MAX_LATENCY_MS),
    jitterMs: whole(f.jitterMs, 0, MAX_LATENCY_MS),
    lossPercent: whole(f.lossPercent, 0, 100),
    mute: !!f.mute,
    error,
    errorPercent: error ? whole(f.errorPercent, 1, 100) : 0,
    counterSpeed: COUNTER_SPEEDS.includes(Number(f.counterSpeed)) ? Number(f.counterSpeed) : 1,
  };
}

/**
 * What a device is made to do wrong, as the chips its row shows: i18n key
 * suffixes (simulator.faults.chip.<key>) and what each sentence quotes.
 */
export function faultChips(f) {
  const out = [];
  if (!f) return out;
  if (f.mute) out.push({ key: 'mute', values: {} });
  if (f.latencyMs || f.jitterMs) {
    out.push({ key: f.jitterMs ? 'latencyJitter' : 'latency', values: { ms: f.latencyMs, jitter: f.jitterMs } });
  }
  if (f.lossPercent) out.push({ key: 'loss', values: { percent: f.lossPercent } });
  if (f.error) out.push({ key: 'error', values: { error: f.error, percent: f.errorPercent } });
  if (f.counterSpeed > 1) out.push({ key: 'counters', values: { speed: f.counterSpeed } });
  return out;
}

/**
 * The devices grouped by their model's category, the categories in the order
 * the catalogue gives them; a device of a model since deleted is filed under
 * "other".
 */
export function deviceGroups(devices, models) {
  const order = categoriesOf(models);
  const rank = (c) => (order.includes(c) ? order.indexOf(c) : order.length);
  const groups = new Map();
  for (const d of devices || []) {
    const category = modelOf(models, d.model)?.category || 'other';
    if (!groups.has(category)) groups.set(category, []);
    groups.get(category).push(d);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => rank(a) - rank(b))
    .map(([category, list]) => ({ category, devices: list }));
}

/** The categories a recorded model may be filed under: Go's customCategories. */
export const CUSTOM_CATEGORIES = Object.keys(CATEGORY_ICONS);

/** The targets a device may be recorded from: the addresses the settings list. */
export function recordableTargets(settings) {
  return getTargetsAsArray(settings?.targets || '');
}

/**
 * What SimulatorRecordDevice is asked: the device, reached as every request to
 * that target is — its profile, its own overrides or the defaults, through
 * getEffectiveSettings —, and what the model it makes is to be called.
 */
export function recordRequest(settings, target, { name, description = '', vendor = '', category = 'other' }) {
  return {
    ...buildSnmpRequest(getEffectiveSettings(settings, target), [target], ''),
    name,
    description,
    vendor,
    category,
  };
}

/** What a device of the model can send, as Go lists it. */
export function notificationsOf(models, modelId) {
  return (models || []).find((m) => m.id === modelId)?.notifications || [];
}

/** What a running device has sent, over all its destinations. */
export function trapActivity(traps) {
  const out = { sent: 0, failed: 0, dropped: 0, lastError: '' };
  for (const d of traps?.destinations || []) {
    out.sent += d.sent || 0;
    out.failed += d.failed || 0;
    out.dropped += d.dropped || 0;
    if (d.lastError) out.lastError = d.lastError;
  }
  return out;
}

/**
 * What to say about one notification sent on request, as i18n keys: one line
 * for everywhere it went, and one for each destination it did not reach — an
 * INFORM nobody acknowledged is one of those.
 */
export function deliveryReport(name, deliveries) {
  const ok = (deliveries || []).filter((d) => !d.error);
  const out = (deliveries || [])
    .filter((d) => d.error)
    .map((d) => ({ key: 'simulator.traps.failed', values: { name, destination: d.destination, error: d.error }, level: 'error' }));
  if (ok.length === 1) {
    const d = ok[0];
    out.unshift({
      key: d.acknowledged ? 'simulator.traps.acknowledged' : 'simulator.traps.sent',
      values: { name, destination: d.destination },
      level: 'success',
    });
  } else if (ok.length > 1) {
    out.unshift({ key: 'simulator.traps.sentAll', values: { name, count: ok.length }, level: 'success' });
  }
  return out;
}

/** The version a target reaches a device in: the most secure one it answers. */
export function preferredVersion(versions = []) {
  return ['v3', 'v2c', 'v1'].find((v) => versions.includes(v)) || 'v2c';
}

/**
 * The target a device is reached at: its address, and its port too unless that
 * is SNMP's own 161 — "127.0.0.1:1162", "[::1]:1161". On macOS, where every
 * device shares 127.0.0.1, the port is all that tells two devices apart, and a
 * target that names it is a target of its own.
 */
export function targetOf(device) {
  if (device.port === 161) return device.address;
  const host = device.address.includes(':') ? `[${device.address}]` : device.address;
  return `${host}:${device.port}`;
}

function targetOfLine(line) {
  return line.trim().replace(/^\/\//, '').split('#')[0].trim();
}

/**
 * The settings with a device added as a target: the target in the list, named
 * after the device, and an override carrying its identifiers in the most secure
 * version it answers — the ones that WRITE when it has any, since they read as
 * well and a device on a bench is there to be written to: its first user with
 * write access for v3, else its first user; its write community, else its
 * community.
 *
 * The port travels in the target when it is not 161, and the override then
 * leaves it out: the target's would win over it anyway. A device added again
 * gives its target its identifiers once more instead of listing it twice;
 * `added` says which.
 */
export function addDeviceAsTarget(settings, device, creds) {
  const target = targetOf(device);
  const version = preferredVersion(device.versions);
  const override = { snmpVersion: version };
  if (target === device.address) override.port = device.port;
  if (version === 'v3') {
    const u = device.users.find((x) => x.write) || device.users[0];
    override.v3 = {
      user: u.name,
      secLevel: u.secLevel,
      authProto: u.authProto,
      authPass: creds?.users?.[u.name]?.authPass || '',
      privProto: u.privProto,
      privPass: creds?.users?.[u.name]?.privPass || '',
      contextName: '',
    };
  } else {
    override.community = creds?.writeCommunity || creds?.community || '';
  }
  const lines = (settings.targets || '').split('\n').filter((l) => l.trim());
  const added = !lines.some((l) => targetOfLine(l) === target);
  const targets = added ? [...lines, `${target} # ${device.name}`].join('\n') : settings.targets;
  return {
    settings: { ...settings, targets, targetOverrides: { ...(settings.targetOverrides || {}), [target]: override } },
    added,
    version,
    target,
  };
}
