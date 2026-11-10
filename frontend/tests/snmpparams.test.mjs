// The request objects the renderer builds match the Go structs they are
// unmarshalled into.
//
// This is the contract CLAUDE.md calls out as hand-maintained: the frontend
// constructs plain objects in utils/snmpParams.js, Go unmarshals them into the
// structs in pkg/snmp/params.go, and the JSON field names are the whole of the
// agreement. Nothing checked it.
//
// BOTH DIRECTIONS ARE SILENT, which is why both are checked:
//
//   a key the renderer sends that Go does not declare is DROPPED by
//   encoding/json without a word — the operation runs with that parameter
//   missing, so a renamed `retries` becomes zero retries;
//
//   a field Go declares that the renderer never sets arrives as the ZERO
//   VALUE — port 0, timeout 0 — which gosnmp then fails on somewhere much
//   further from the cause.
//
// Neither shows up in a build, a lint, or any test that does not compare the
// two sides. This is the one thing a TypeScript migration would buy that the
// other static checks here do not, and it costs one file.
import { readFileSync } from 'node:fs';
import {
  buildSnmpRequest,
  buildSetRequest,
  buildSetMultiRequest,
  buildGetBulkRequest,
  buildTestRequest,
  buildDiscoverRequest,
  buildTrapListenerRequest,
  buildMonitorConnection,
} from '../src/utils/snmpParams.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

/* --- the Go side ---------------------------------------------------------- */

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');

/**
 * Every `type X struct { ... }` in a Go source, as its JSON field names plus
 * the names of the structs it embeds.
 *
 * A per-struct parse rather than one regex over the file, because params.go
 * holds six structs and three of them EMBED SnmpRequest — a file-wide sweep for
 * `json:"..."` would report one flat set and could not tell which request is
 * missing what.
 */
function goStructs(source) {
  const out = new Map();
  const re = /type\s+([A-Za-z0-9_]+)\s+struct\s*\{/g;
  let m;
  while ((m = re.exec(source)) !== null) {
    let depth = 0;
    let end = -1;
    for (let i = m.index + m[0].length - 1; i < source.length; i++) {
      if (source[i] === '{') depth++;
      else if (source[i] === '}') { depth--; if (depth === 0) { end = i; break; } }
    }
    if (end < 0) continue;

    const fields = [];
    const embeds = [];
    for (let line of source.slice(m.index + m[0].length, end).split('\n')) {
      line = line.trim();
      if (!line || line.startsWith('//')) continue;
      const tag = line.match(/json:"([A-Za-z0-9_]+)/);
      if (tag) { fields.push(tag[1]); continue; }
      // A bare identifier on its own line is an embedded struct.
      const bare = line.match(/^([A-Za-z][A-Za-z0-9_]*)$/);
      if (bare) embeds.push(bare[1]);
    }
    out.set(m[1], { fields, embeds });
  }
  return out;
}

const structs = new Map([
  ...goStructs(read('../../pkg/snmp/params.go')),
  ...goStructs(read('../../pkg/snmp/client.go')),
  ...goStructs(read('../../pkg/snmp/setmulti.go')),
  ...goStructs(read('../../internal/engine/monitor.go')),
]);

/** The JSON keys a struct accepts, embedded structs flattened in. */
function keysOf(name, seen = new Set()) {
  if (seen.has(name)) return [];
  seen.add(name);
  const s = structs.get(name);
  if (!s) return null;
  return [...s.fields, ...s.embeds.flatMap((e) => keysOf(e, seen) || [])];
}

check('the Go structs were parsed', structs.size >= 9, `${structs.size} structs`);
check('embedding is resolved, not flattened by accident',
  (keysOf('SetRequest') || []).includes('targets'),
  'SetRequest embeds SnmpRequest, so it must accept its fields too');

/* --- the renderer side ---------------------------------------------------- */

// Every field the builders read, all set to something distinguishable from a
// zero value so a field that never reaches the object is visible.
const settings = {
  community: 'public',
  snmpVersion: 'v3',
  port: 161,
  timeout: 5,
  retries: 2,
  trapPort: 162,
  v3: {
    user: 'snmplens',
    authPass: 'authpass123',
    authProto: 'SHA',
    privPass: 'privpass123',
    privProto: 'AES',
    secLevel: 'authPriv',
    contextName: 'ctx',
  },
};

const TARGETS = ['10.0.0.1'];
const OID = '1.3.6.1.2.1.1.1.0';

const cases = [
  ['SnmpRequest', buildSnmpRequest(settings, TARGETS, OID)],
  ['SetRequest', buildSetRequest(settings, TARGETS, OID, '1', 'Integer')],
  ['SetMultiRequest', buildSetMultiRequest(settings, TARGETS, '', [{ oid: OID, value: '1', type: 'Integer' }])],
  ['GetBulkRequest', buildGetBulkRequest(settings, TARGETS, OID, 0, 10)],
  ['TestRequest', buildTestRequest(settings, TARGETS[0])],
  ['DiscoverRequest', buildDiscoverRequest(settings, '10.0.0.0/24', 5)],
  ['TrapListenerRequest', buildTrapListenerRequest(settings)],
  ['MonitorConnection', buildMonitorConnection(settings)],
];

for (const [name, built] of cases) {
  const declared = keysOf(name);
  if (!declared) {
    check(`${name} exists in the Go source`, false, 'struct not found');
    continue;
  }
  const sent = Object.keys(built);

  const dropped = sent.filter((k) => !declared.includes(k));
  check(`${name}: every key the renderer sends is declared in Go`,
    dropped.length === 0,
    dropped.length ? `${dropped.join(', ')} — encoding/json discards these silently` : '');

  const unset = declared.filter((k) => !sent.includes(k));
  check(`${name}: every field Go declares is sent`,
    unset.length === 0,
    unset.length ? `${unset.join(', ')} — these arrive as the zero value` : '');
}

/* --- the nested objects, which travel inside every request ---------------- */

const v3 = buildSnmpRequest(settings, TARGETS, OID).v3;
const v3Declared = keysOf('V3Params');
check('V3Params was found', !!v3Declared);
if (v3Declared) {
  const dropped = Object.keys(v3).filter((k) => !v3Declared.includes(k));
  const unset = v3Declared.filter((k) => !Object.keys(v3).includes(k));
  // Capitalised on BOTH sides — `json:"User"`, not `json:"user"`. That is an
  // exact match rather than encoding/json's case-insensitive fallback, and it
  // has to stay that way on both sides at once.
  check('v3: every key the renderer sends is declared in Go', dropped.length === 0, dropped.join(', '));
  check('v3: every field Go declares is sent', unset.length === 0, unset.join(', '));
  check('v3 keys are capitalised, matching the Go tags exactly',
    Object.keys(v3).every((k) => /^[A-Z]/.test(k)), Object.keys(v3).join(', '));
}

// The trap listener takes a LIST of users, one V3Params each: the default
// identifiers' and every v3 credential profile's.
{
  const users = buildTrapListenerRequest({
    ...settings,
    credentialProfiles: [{
      id: 'p-core1234', name: 'Core', version: 'v3',
      v3: { user: 'ops', secLevel: 'AuthNoPriv', authProto: 'SHA256', authPass: 'ops-auth-123',
        privProto: 'AES', privPass: '', contextName: '' },
    }],
  }).users;
  check('the trap listener is sent the default user and every v3 profile',
    Array.isArray(users) && users.length === 2, JSON.stringify(users?.map((u) => u.User)));
  if (v3Declared) {
    for (const u of users || []) {
      const dropped = Object.keys(u).filter((k) => !v3Declared.includes(k));
      const unset = v3Declared.filter((k) => !Object.keys(u).includes(k));
      check(`trap user ${u.User}: shaped exactly like V3Params`,
        dropped.length === 0 && unset.length === 0, [...dropped, ...unset].join(', '));
    }
  }
}

const vars = buildSetMultiRequest(settings, TARGETS, '', [{ oid: OID, value: '1', type: 'Integer' }]).vars;
const varDeclared = keysOf('SetVar');
check('SetVar was found', !!varDeclared);
if (varDeclared) {
  const dropped = Object.keys(vars[0]).filter((k) => !varDeclared.includes(k));
  const unset = varDeclared.filter((k) => !Object.keys(vars[0]).includes(k));
  check('a varbind: every key sent is declared in Go', dropped.length === 0, dropped.join(', '));
  check('a varbind: every field Go declares is sent', unset.length === 0, unset.join(', '));
}

/* --- prove the detector detects ------------------------------------------- */
{
  const src = `
type Probe struct {
	Targets []string \`json:"targets"\`
	Port    int      \`json:"port"\`
}

type Wrapper struct {
	Probe
	Extra string \`json:"extra"\`
}
`;
  const parsed = goStructs(src);
  check('the parser reads a struct', (parsed.get('Probe') || {}).fields?.length === 2);
  check('and records an embedded struct rather than its fields',
    (parsed.get('Wrapper') || {}).embeds?.[0] === 'Probe');

  // A renamed field is the failure this whole file exists for.
  const declared = ['targets', 'port'];
  const renamed = { targets: [], prt: 161 };
  const dropped = Object.keys(renamed).filter((k) => !declared.includes(k));
  const unset = declared.filter((k) => !Object.keys(renamed).includes(k));
  check('a renamed key is seen as both dropped and unset',
    dropped.length === 1 && dropped[0] === 'prt' && unset.length === 1 && unset[0] === 'port',
    JSON.stringify({ dropped, unset }));
}

process.exit(failures ? 1 : 0);
