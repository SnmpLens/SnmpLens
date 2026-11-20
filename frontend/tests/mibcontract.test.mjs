// The renderer against the tree the MIB engine really produces.
//
// pkg/mib/testdata/reference.jsonl is everything pkg/mib tells SnmpLens about
// the bundled MIBs (TestTheMibEngineMatchesItsReference). The renderer decides
// from two of its strings what it offers: the kind of a node — GET on a scalar,
// a walk on a table, nothing on a notification — and its access — SET when it
// reads as writable. Both are the engine's spelling, and an engine that spells
// them otherwise turns actions off with nothing failing: gosmi folds read-create
// into "ReadWrite", and an engine reporting "read-create" would leave SET off on
// every column a row is created with. So the truth here is the MIB sources'
// own MAX-ACCESS, and the strings are whatever the reference holds.

import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import {
  VALUE_KINDS, WALK_KINDS, TABLE_KINDS, INERT_KINDS,
  canGet, canWalk, canWalkAsTable, isWritable, normalizeAccess,
} from '../src/utils/mibNode.js';

let failures = 0;
function check(name, ok, detail) {
  if (ok) {
    console.log('PASS ', name);
  } else {
    failures++;
    console.error('FAIL ', name, detail === undefined ? '' : '— ' + detail);
  }
}

const root = new URL('../../', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const lines = readFileSync(join(root, 'pkg/mib/testdata/reference.jsonl'), 'utf8')
  .split(/\r?\n/).filter(Boolean).map((l) => JSON.parse(l));
const nodes = lines.filter((l) => l.section === 'node');
const tables = lines.filter((l) => l.section === 'table').map((l) => l.table);
const byOid = new Map(nodes.map((n) => [n.oid, n]));
// The renderer reads mibType and access; the reference calls them kind and access.
const asNode = (n) => ({ ...n, mibType: n.kind });

check('the reference holds the tree', nodes.length > 500 && tables.length > 20, `${nodes.length} nodes, ${tables.length} tables`);

/* --- every kind the engine emits means something here ----------------------- */
const known = new Set([...VALUE_KINDS, ...WALK_KINDS, ...INERT_KINDS]);
const emitted = new Set(nodes.map((n) => n.kind));
{
  const unknown = [...emitted].filter((k) => !known.has(k));
  check('every kind the engine emits is one the renderer gives a meaning', unknown.length === 0, unknown.join(', '));
  for (const k of ['Scalar', 'Column', 'Table', 'Row', 'Node', 'Group', 'Notification']) {
    check(`the engine emits ${k}`, emitted.has(k));
  }
}

/* --- and every kind the renderer compares against is one the engine emits ---- */
{
  const src = join(root, 'frontend/src');
  const files = [];
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.name === 'node_modules' || e.name === 'wailsjs') continue;
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.(svelte|js)$/.test(e.name)) files.push(p);
    }
  };
  walk(src);
  const compared = new Map();
  for (const f of files) {
    const text = readFileSync(f, 'utf8');
    for (const m of text.matchAll(/mibType\s*[!=]==?\s*'([A-Za-z]+)'/g)) compared.set(m[1], f);
    // The tree's type filter and the favourites' icons name kinds too.
    for (const m of text.matchAll(/\{#each \[([^\]]*)\] as type\}/g)) {
      for (const k of m[1].matchAll(/'([A-Za-z]+)'/g)) compared.set(k[1], f);
    }
    const icons = text.match(/function getNodeIconFromType[\s\S]*?const icons = \{([\s\S]*?)\}/);
    if (icons) for (const k of icons[1].matchAll(/'([A-Za-z]+)':/g)) compared.set(k[1], f);
  }
  for (const k of [...VALUE_KINDS, ...WALK_KINDS, ...TABLE_KINDS]) compared.set(k, 'utils/mibNode.js');
  const stale = [...compared].filter(([k]) => !emitted.has(k)).map(([k, f]) => `${k} (${f.replace(root, '')})`);
  check('every kind the renderer compares a node against is one the engine emits', stale.length === 0, stale.join(', '));
}

/* --- what is offered on each kind ------------------------------------------- */
{
  const wrong = [];
  for (const n of nodes) {
    const node = asNode(n);
    const value = ['Scalar', 'Column'].includes(n.kind);
    if (canGet(node) !== value) wrong.push(`${n.name} (${n.kind}) GET`);
    if (canWalk(node) !== ['Table', 'Row', 'Node', 'Group'].includes(n.kind)) wrong.push(`${n.name} (${n.kind}) walk`);
    if (canWalkAsTable(node) !== ['Table', 'Row'].includes(n.kind)) wrong.push(`${n.name} (${n.kind}) table`);
    if (INERT_KINDS.includes(n.kind) && (canGet(node) || canWalk(node))) wrong.push(`${n.name} (${n.kind}) is offered an operation`);
  }
  check('each kind is offered what it can be asked', wrong.length === 0, wrong.slice(0, 8).join('; '));
}

/* --- SET and the access filter follow the MIB's own MAX-ACCESS --------------- */
const declared = new Map();
{
  for (const f of readdirSync(join(root, 'mibs'))) {
    const text = readFileSync(join(root, 'mibs', f), 'utf8');
    for (const m of text.matchAll(/^\s*([a-z][A-Za-z0-9-]*)\s+OBJECT-TYPE\b([\s\S]*?)::=/gm)) {
      const access = m[2].match(/\b(?:MAX-ACCESS|ACCESS)\s+([a-z-]+)/);
      if (!access) continue;
      // A name two modules declare differently proves nothing either way.
      const prev = declared.get(m[1]);
      declared.set(m[1], prev === undefined || prev === access[1] ? access[1] : null);
    }
  }
}
{
  const WRITABLE = ['read-write', 'read-create', 'write-only'];
  const BUCKET = { 'read-only': 'read-only', 'read-write': 'read-write', 'read-create': 'read-write', 'not-accessible': 'not-accessible' };
  const counted = {};
  const wrongSet = [];
  const wrongFilter = [];
  for (const n of nodes) {
    if (!['Scalar', 'Column'].includes(n.kind)) continue;
    const access = declared.get(n.name);
    if (!access) continue;
    counted[access] = (counted[access] || 0) + 1;
    const node = asNode(n);
    if (isWritable(node) !== WRITABLE.includes(access)) wrongSet.push(`${n.name}: ${access} reported as "${n.access}"`);
    if (BUCKET[access] && normalizeAccess(n.access) !== BUCKET[access]) {
      wrongFilter.push(`${n.name}: ${access} filtered as ${normalizeAccess(n.access)}`);
    }
  }
  check('the MIB sources declare objects of every access', ['read-only', 'read-write', 'read-create', 'not-accessible', 'accessible-for-notify'].every((a) => counted[a] > 0),
    JSON.stringify(counted));
  check('SET is offered exactly on what the MIB declares writable, read-create included', wrongSet.length === 0, wrongSet.slice(0, 8).join('; '));
  check('the access filter puts each object where its MAX-ACCESS says', wrongFilter.length === 0, wrongFilter.slice(0, 8).join('; '));
}

/* --- a table is drawn from its row's columns -------------------------------- */
{
  const broken = [];
  for (const t of tables) {
    const table = byOid.get(t.oid);
    const row = byOid.get(t.rowOid);
    if (!table || table.kind !== 'Table') broken.push(`${t.name}: not a Table node`);
    if (!row || row.kind !== 'Row' || row.name !== t.rowName) broken.push(`${t.name}: its row is not a Row node`);
    const columns = nodes.filter((n) => n.oid.startsWith(t.rowOid + '.') && n.oid.split('.').length === t.rowOid.split('.').length + 1);
    if (columns.length === 0 || columns.some((c) => c.kind !== 'Column')) broken.push(`${t.name}: the row's children are not all columns`);
    if (columns.length !== t.columns.length) broken.push(`${t.name}: ${columns.length} column nodes, ${t.columns.length} columns`);
  }
  check('every table has a Row of Column nodes, as the table view reads it', broken.length === 0, broken.slice(0, 6).join('; '));
}

if (failures > 0) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
