// Credential profiles: named SNMP identifiers a target is given instead of the
// default ones.
//
// Nearly every rule here fails SILENTLY. A target that resolves to the wrong
// identity is asked with the default community when it expects a v3 user, and
// every reading comes back an error that looks exactly like an unreachable
// device. A v3 target sent the default community alongside its user hands a
// credential to a device that has no use for it. A profile id left dangling by
// a delete must fall back to the defaults, not to nothing. And a protocol the
// UI offers but Go does not map saves cleanly and fails on the first request.
import * as esbuild from 'esbuild';
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const dir = mkdtempSync(join(tmpdir(), 'snmplens-profiles-'));
writeFileSync(join(dir, 'stub.js'), `
export const SettingsKeyStatus = async () => ({ backend: 'test', available: true, hasKey: false });
export const SettingsSeal = async (v) => v;
export const SettingsOpen = async (v) => v;
export const SettingsAdoptKey = async () => {};
export const SettingsForgetKey = async () => {};
export const EventsOn = () => {};
`);
const out = join(dir, 'bundle.mjs');
await esbuild.build({
  entryPoints: [new URL('./fixtures/profiles-entry.js', import.meta.url).pathname.replace(/^[/]([A-Za-z]:)/, '$1')],
  bundle: true, format: 'esm', outfile: out, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /bridge[/](App|runtime)$/ }, () => ({ path: join(dir, 'stub.js') }));
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

const P = await import(pathToFileURL(out).href);

/* --- what the UI offers is what Go maps ---------------------------------- */
{
  const go = readFileSync(new URL('../../pkg/snmp/client.go', import.meta.url), 'utf8').replace(/\r\n/g, '\n');
  const casesOf = (fn) => {
    const start = go.indexOf(`func ${fn}(`);
    const body = go.slice(start, go.indexOf('\n}\n', start));
    return [...body.matchAll(/case ([^:]+):/g)].flatMap((m) => [...m[1].matchAll(/"([^"]*)"/g)].map((x) => x[1]));
  };
  const auth = casesOf('getAuthProtocol');
  const priv = casesOf('getPrivProtocol');
  const levels = casesOf('getSecurityLevel');
  check('the Go protocol switches were found', auth.length > 3 && priv.length > 3 && levels.length === 3,
    `${auth.length}/${priv.length}/${levels.length}`);

  // getAuthProtocol and getPrivProtocol upper-case what they are given;
  // getSecurityLevel does not, so a level is compared exactly.
  const missingAuth = P.AUTH_PROTOCOLS.map((p) => p.value).filter((v) => !auth.includes(v.toUpperCase()));
  const missingPriv = P.PRIV_PROTOCOLS.map((p) => p.value).filter((v) => !priv.includes(v.toUpperCase()));
  const missingLevels = P.SEC_LEVELS.filter((l) => !levels.includes(l));
  check('every authentication protocol offered is one Go maps', missingAuth.length === 0, missingAuth.join(', '));
  check('every privacy protocol offered is one Go maps', missingPriv.length === 0, missingPriv.join(', '));
  check('every security level offered is one Go maps', missingLevels.length === 0, missingLevels.join(', '));
}

/* --- what a target resolves to ------------------------------------------- */

const core = {
  id: 'p-core0001', name: 'Core', version: 'v3',
  v3: { user: 'ops', secLevel: 'AuthPriv', authProto: 'SHA256', authPass: 'core-auth-123',
    privProto: 'AES', privPass: 'core-priv-123', contextName: 'vrf-mgmt' },
};
const edge = { id: 'p-edge0001', name: 'Edge', version: 'v1', community: 'edge-ro' };
const base = {
  targets: '10.0.0.1\n10.0.0.2\n10.0.0.3\n//10.0.0.4 # spare\n10.0.0.5 # dangling',
  snmpVersion: 'v2c', port: 161, timeout: 5, retries: 1,
  community: 'public',
  v3: { user: 'default-user', secLevel: 'AuthPriv', authProto: 'SHA', authPass: 'default-auth',
    privProto: 'AES', privPass: 'default-priv', contextName: '' },
  credentialProfiles: [core, edge],
  targetOverrides: {
    '10.0.0.2': { profile: core.id, port: 1161 },
    '10.0.0.3': { community: 'own-community', snmpVersion: 'v1' },
    '10.0.0.4': { profile: edge.id },
    '10.0.0.5': { profile: 'p-gone0001' },
  },
};

{
  const plain = P.getEffectiveSettings(base, '10.0.0.1');
  check('a target with nothing of its own uses the defaults',
    plain.community === 'public' && plain.snmpVersion === 'v2c' && plain.credentialRef === P.DEFAULT_REF);

  const v3 = P.getEffectiveSettings(base, '10.0.0.2');
  check('a profiled target speaks its profile’s version', v3.snmpVersion === 'v3', v3.snmpVersion);
  check('as its profile’s user', v3.v3.user === 'ops' && v3.v3.authPass === 'core-auth-123');
  check('without the default community riding along', v3.community === '', JSON.stringify(v3.community));
  check('and keeps its own transport', v3.port === 1161, String(v3.port));
  check('and says which profile it follows', v3.credentialRef === core.id, v3.credentialRef);

  const v1 = P.getEffectiveSettings(base, '10.0.0.4');
  check('a community profile sends no v3 passphrase', v1.v3.authPass === '' && v1.v3.user === '', JSON.stringify(v1.v3));
  check('and its own version and community', v1.snmpVersion === 'v1' && v1.community === 'edge-ro');

  const own = P.getEffectiveSettings(base, '10.0.0.3');
  check('a target with overrides of its own still uses them',
    own.community === 'own-community' && own.snmpVersion === 'v1' && own.credentialRef === '',
    JSON.stringify([own.community, own.credentialRef]));

  const gone = P.getEffectiveSettings(base, '10.0.0.5');
  check('a profile that no longer exists falls back to the defaults, not to nothing',
    gone.community === 'public' && gone.snmpVersion === 'v2c' && gone.credentialRef === P.DEFAULT_REF,
    JSON.stringify([gone.community, gone.credentialRef]));

  const conn = P.buildMonitorConnection(v3);
  check('a session built from it records the profile id', conn.profile === core.id && conn.v3.User === 'ops');
  check('a connection built from the bare settings records none', P.buildMonitorConnection(base).profile === '');
}

/* --- grouping ------------------------------------------------------------ */
{
  const twin = { ...core, id: 'p-twin0001', name: 'Twin' };
  const s = {
    ...base,
    credentialProfiles: [core, twin, edge],
    targetOverrides: { '10.0.0.1': { profile: core.id }, '10.0.0.2': { profile: core.id }, '10.0.0.3': { profile: twin.id } },
  };
  const groups = P.groupTargets(s, ['10.0.0.1', '10.0.0.2', '10.0.0.3']);
  check('targets on one profile share a request', groups.some((g) => g.targets.join() === '10.0.0.1,10.0.0.2'),
    JSON.stringify(groups.map((g) => g.targets)));
  check('two profiles holding the same credentials are still two groups', groups.length === 2, String(groups.length));
  check('usesOwnIdentifiers sees a profile, and only a profile',
    P.usesOwnIdentifiers(s, ['10.0.0.1']) && !P.usesOwnIdentifiers(base, ['10.0.0.1']));
}

/* --- assigning and removing ---------------------------------------------- */
{
  const before = { '10.0.0.3': { community: 'own', snmpVersion: 'v1', v3: { user: 'x' }, port: 1161 } };
  const after = P.assignProfile(before, '10.0.0.3', core.id);
  check('assigning a profile drops the target’s own credentials',
    !('community' in after['10.0.0.3']) && !('snmpVersion' in after['10.0.0.3']) && !('v3' in after['10.0.0.3']),
    JSON.stringify(after['10.0.0.3']));
  check('and keeps its transport', after['10.0.0.3'].port === 1161 && after['10.0.0.3'].profile === core.id);
  check('the overrides passed in are not mutated', before['10.0.0.3'].community === 'own');
  const cleared = P.assignProfile({ '10.0.0.9': { profile: core.id } }, '10.0.0.9', null);
  check('taking the only override away leaves no empty entry', !('10.0.0.9' in cleared), JSON.stringify(cleared));

  const removed = P.removeProfile(base, core.id);
  check('removing a profile removes it', !removed.credentialProfiles.some((p) => p.id === core.id));
  check('and every reference to it', !Object.values(removed.targetOverrides).some((ov) => ov.profile === core.id),
    JSON.stringify(removed.targetOverrides));
  check('its targets keep their transport', removed.targetOverrides['10.0.0.2']?.port === 1161);
  check('and go back to the defaults', P.getEffectiveSettings(removed, '10.0.0.2').community === 'public');
  check('other profiles’ targets are untouched', removed.targetOverrides['10.0.0.4']?.profile === edge.id);
}

/* --- what may be stored -------------------------------------------------- */
{
  check('a malformed entry is not a profile',
    P.normaliseProfile(null) === null && P.normaliseProfile('x') === null && P.normaliseProfile([]) === null);
  check('an id that is not one of ours is refused', P.normaliseProfile({ id: 'abc', name: 'x', version: 'v2c' }) === null);
  check('an unknown version is refused', P.normaliseProfile({ id: 'p-abcd1234', name: 'x', version: 'v4' }) === null);

  const moved = P.normaliseProfile({
    id: 'p-abcd1234', name: 'x', version: 'v3', community: 'left-behind',
    v3: { user: 'u', secLevel: 'AuthNoPriv', authProto: 'SHA', authPass: 'a-pass-123', privProto: 'DES', privPass: 'orphan', contextName: '' },
  });
  check('a v3 profile keeps no community', !('community' in moved), JSON.stringify(Object.keys(moved)));
  check('nor a privacy passphrase its level does not use', moved.v3.privPass === '', moved.v3.privPass);
  const back = P.normaliseProfile({ id: 'p-abcd1234', name: 'x', version: 'v2c', community: 'c', v3: { authPass: 'orphan' } });
  check('a v2c profile keeps no v3 block', !('v3' in back), JSON.stringify(back));
  const odd = P.normaliseProfile({ id: 'p-abcd1234', name: 'x'.repeat(200), version: 'v3',
    v3: { secLevel: 'Bogus', authProto: 'ROT13', privProto: 'XOR' } });
  check('unknown protocols and levels become the defaults',
    odd.v3.secLevel === 'AuthPriv' && odd.v3.authProto === 'SHA' && odd.v3.privProto === 'AES', JSON.stringify(odd.v3));
  check('a name is bounded', odd.name.length === P.MAX_NAME, String(odd.name.length));

  const list = P.normaliseProfiles([core, { ...core }, 'junk', edge, null]);
  check('a stored list keeps one of each id and drops the junk',
    list.length === 2 && list[0].id === core.id && list[1].id === edge.id, JSON.stringify(list.map((p) => p && p.id)));
  check('anything but an array is an empty list', P.normaliseProfiles({}).length === 0 && P.normaliseProfiles(undefined).length === 0);

  const ids = new Set(Array.from({ length: 200 }, () => P.newProfileId([])));
  check('new ids are distinct', ids.size === 200);
  check('and shaped like ids', [...ids].every((id) => /^p-[a-z0-9]{4,32}$/.test(id)));
  check('a new profile is valid to store',
    P.normaliseProfile(P.blankProfile('v3')) !== null && P.normaliseProfile(P.blankProfile('v2c')) !== null);
}

/* --- validation ---------------------------------------------------------- */
{
  const errs = (r) => r.errors.map((e) => e.key);
  const warns = (r) => r.warnings.map((w) => w.key);
  check('a complete profile has no errors', P.validateProfile(core, [core, edge]).errors.length === 0,
    errs(P.validateProfile(core, [core, edge])).join());
  check('a name is required', errs(P.validateProfile({ ...edge, name: '  ' }, [])).includes('profiles.err.nameRequired'));
  check('names are unique, ignoring case',
    errs(P.validateProfile({ ...edge, id: 'p-other001', name: 'CORE' }, [core])).includes('profiles.err.nameTaken'));
  check('a profile may keep its own name', !errs(P.validateProfile(core, [core])).includes('profiles.err.nameTaken'));
  check('a community profile needs a community',
    errs(P.validateProfile({ ...edge, community: '' }, [])).includes('profiles.err.communityRequired'));
  check('a v3 profile needs a user',
    errs(P.validateProfile({ ...core, v3: { ...core.v3, user: '' } }, [])).includes('profiles.err.userRequired'));
  check('a user name is bounded in OCTETS, not characters',
    errs(P.validateProfile({ ...core, v3: { ...core.v3, user: 'é'.repeat(17) } }, [])).includes('profiles.err.userTooLong'));
  check('AuthPriv needs a privacy passphrase',
    errs(P.validateProfile({ ...core, v3: { ...core.v3, privPass: '' } }, [])).includes('profiles.err.privPassRequired'));
  check('NoAuthNoPriv needs no passphrase',
    P.validateProfile({ ...core, v3: { ...core.v3, secLevel: 'NoAuthNoPriv', authPass: '', privPass: '' } }, []).errors.length === 0);

  const weak = { ...core, v3: { ...core.v3, authProto: 'MD5', privProto: 'DES', authPass: 'short', privPass: 'short' } };
  const w = warns(P.validateProfile(weak, []));
  check('MD5, DES and short passphrases are warned about',
    w.includes('profiles.warn.md5') && w.includes('profiles.warn.des') &&
    w.filter((k) => k === 'profiles.warn.shortPass').length === 2, w.join());
  check('and none of them blocks saving: an old agent is a reason to monitor it', P.validateProfile(weak, []).errors.length === 0);
}

/* --- names --------------------------------------------------------------- */
{
  const list = [{ id: 'p-a0000001', name: 'Core' }, { id: 'p-a0000002', name: 'Core 2' }];
  check('a free name is kept', P.uniqueName('Edge', list) === 'Edge');
  check('a taken name is numbered', P.uniqueName('core', list) === 'core 3', P.uniqueName('core', list));
  check('a profile does not collide with itself', P.uniqueName('Core', list, 'p-a0000001') === 'Core');
}

/* --- the trap listener's users ------------------------------------------- */
{
  const users = P.trapUsers(base);
  check('the default user and every v3 profile are accepted for traps',
    users.map((u) => u.user).join() === 'default-user,ops', users.map((u) => u.user).join());
  check('the context name is no part of receiving', users.every((u) => u.contextName === ''));
  const twice = P.trapUsers({ ...base, credentialProfiles: [core, { ...core, id: 'p-copy0001', name: 'Copy', v3: { ...core.v3, contextName: 'other' } }] });
  check('two profiles differing only in context are one user', twice.length === 2, twice.map((u) => u.user).join());
  const lowered = P.trapUsers({ ...base, v3: { ...base.v3, secLevel: 'AuthNoPriv' }, credentialProfiles: [] });
  check('nothing above the security level travels', lowered[0].privPass === '' && lowered[0].privProto === '', JSON.stringify(lowered[0]));
  check('a user with no name is not a user', P.trapUsers({ ...base, v3: { ...base.v3, user: '' }, credentialProfiles: [] }).length === 0);
  const req = P.buildTrapListenerRequest({ ...base, trapPort: 1162 });
  check('the request carries them shaped like V3Params',
    req.port === 1162 && req.users.length === 2 && req.users[1].User === 'ops' && req.users[1].AuthProto === 'SHA256');
}

/* --- what counts as a change --------------------------------------------- */
{
  const clone = (s) => JSON.parse(JSON.stringify(s));
  check('nothing changed, nothing to follow', P.changedCredentialRefs(base, clone(base)).size === 0);

  const renamed = clone(base);
  renamed.credentialProfiles[0].name = 'Core renamed';
  check('a rename is not a credential change', P.changedCredentialRefs(base, renamed).size === 0);

  const reordered = clone(base);
  reordered.credentialProfiles[0].v3 = Object.fromEntries(Object.entries(reordered.credentialProfiles[0].v3).reverse());
  check('key order is not a change: a false positive restarts sessions for nothing',
    P.changedCredentialRefs(base, reordered).size === 0);

  const rotated = clone(base);
  rotated.credentialProfiles[0].v3.authPass = 'rotated-123';
  const r = P.changedCredentialRefs(base, rotated);
  check('a rotated passphrase is a change to that profile only', r.size === 1 && r.has(core.id), [...r].join());

  const moved = clone(base);
  moved.credentialProfiles[1].version = 'v2c';
  check('a version change is a change', P.changedCredentialRefs(base, moved).has(edge.id));

  const defaults = clone(base);
  defaults.community = 'private';
  check('the default community changing is the defaults changing', P.changedCredentialRefs(base, defaults).has(P.DEFAULT_REF));

  const header = clone(base);
  header.snmpVersion = 'v3';
  check('the header version is not the defaults changing: each session keeps its own',
    P.changedCredentialRefs(base, header).size === 0);

  const deleted = clone(base);
  deleted.credentialProfiles = [edge];
  check('a deleted profile is not a change: its sessions keep what they have', P.changedCredentialRefs(base, deleted).size === 0);
}

/* --- the target list, read-only ------------------------------------------ */
{
  const t = P.parseTargetLines('10.0.0.1 # core\n\n//10.0.0.2 # spare\n10.0.0.1 # again\n  fe80::1%eth0  ');
  check('every target is listed once, disabled ones too',
    t.map((x) => `${x.address}:${x.enabled}`).join() === '10.0.0.1:true,10.0.0.2:false,fe80::1%eth0:true', JSON.stringify(t));
  check('with its label', t[0].label === 'core' && t[1].label === 'spare');
}

/* --- a profile applied by id, for a test or a scan ----------------------- */
{
  const w = P.withProfile(base, core.id);
  check('withProfile applies the identity', w.snmpVersion === 'v3' && w.v3.user === 'ops' && w.community === '' && w.credentialRef === core.id);
  check('an unknown id leaves the defaults', P.withProfile(base, 'p-none0001').community === 'public');
}

/* --- which users the trap listener accepts -------------------------------- */
{
  const quiet = { ...core, id: 'p-quiet001', name: 'Polled only', acceptTraps: false, v3: { ...core.v3, user: 'poller' } };
  const optedOut = { ...base, traps: { acceptDefaultUser: false }, credentialProfiles: [core, quiet, edge] };
  const users = P.trapUsers(optedOut).map((u) => u.user);
  check('a profile opted out of traps is not accepted', !users.includes('poller'), users.join());
  check('nor is the default user once unticked', !users.includes('default-user'), users.join());
  check('a profile that says nothing is accepted: opting out is written on purpose', users.includes('ops'), users.join());

  const rx = P.trapReception(optedOut);
  check('the reception lists every v3 identity, accepted or not',
    rx.map((u) => `${u.ref}:${u.accepted}`).join() === `${P.DEFAULT_REF}:false,${core.id}:true,${quiet.id}:false`,
    rx.map((u) => `${u.ref}:${u.accepted}`).join());
  check('a community profile is no part of it: its traps are received whatever the community',
    !rx.some((u) => u.ref === edge.id));
  check('an identity with no user name is not listed',
    P.trapReception({ ...base, v3: { ...base.v3, user: '' }, credentialProfiles: [] }).length === 0);

  check('a new v3 profile accepts traps', P.blankProfile('v3').acceptTraps === true);
  check('a stored v3 profile without the field accepts traps',
    P.normaliseProfile({ id: 'p-abcd1234', name: 'x', version: 'v3', v3: {} }).acceptTraps === true);
  check('one that opted out stays out', P.normaliseProfile({ ...core, acceptTraps: false }).acceptTraps === false);
  check('a community profile has no such switch', !('acceptTraps' in P.normaliseProfile({ ...edge, acceptTraps: false })));

  const toggled = JSON.parse(JSON.stringify(base));
  toggled.credentialProfiles[0].acceptTraps = false;
  check('opting a profile out of traps is not a credential change: no session restarts for it',
    P.changedCredentialRefs(base, toggled).size === 0);
}

process.exit(failures ? 1 : 0);
