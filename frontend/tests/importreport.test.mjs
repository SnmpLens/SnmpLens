// An archive dropped whole can be sixty files, and the report of what the
// import did was one sentence per file: the module that did not load was on
// the third screen. The report is one row per MODULE.
import {
  buildImportReport, reportNeedsAttention, shortPath, chooseVersion, canChoose, CHOSEN,
  IMPORTED, REPLACED, PRESENT, SET_ASIDE, IGNORED, ERROR_OUTCOME as ERROR, LOAD_OK, LOAD_WARN, LOAD_ERROR, LOAD_FAIL,
} from '../src/utils/importReport.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const results = [
  { fileName: 'pack/box/2.0/box.mib', module: 'BOX-MIB', storedAs: 'BOX-MIB.mib', outcome: IMPORTED, date: '2023-04-01', success: true, source: 'C:/drop/pack/box/2.0/box.mib' },
  { fileName: 'pack/box/1.0/box.mib', module: 'BOX-MIB', storedAs: 'BOX-MIB.mib', outcome: SET_ASIDE, reason: 'older', date: '2019-01-01', success: true, skipped: true, source: 'C:/drop/pack/box/1.0/box.mib' },
  { fileName: 'pack/box/2.1/box.mib', module: 'BOX-MIB', storedAs: 'BOX-MIB.mib', outcome: SET_ASIDE, reason: 'unparsable', success: true, skipped: true },
  { fileName: 'pack/fan.mib', module: 'FAN-MIB', storedAs: 'FAN-MIB.mib', outcome: IMPORTED, success: true },
  { fileName: 'pack/psu.mib', module: 'PSU-MIB', storedAs: 'PSU-MIB.mib', outcome: REPLACED, replaced: 'psu old.mib', success: true },
  { fileName: 'pack/IF-MIB.txt', module: 'IF-MIB', storedAs: 'IF-MIB.txt', outcome: PRESENT, success: true, skipped: true },
  { fileName: 'pack/smi.txt', module: 'SNMPv2-SMI', outcome: ERROR, reason: 'bundled', error: 'SNMPv2-SMI ships with SnmpLens', success: false },
  { fileName: 'pack/Thumbs.db', outcome: IGNORED, reason: 'binary', success: false },
  { fileName: 'pack/a.smidb', outcome: IGNORED, reason: 'binary', success: false },
  { fileName: 'pack/spec.pdf', outcome: IGNORED, reason: 'pdf', success: false },
  null,
];
const loads = [
  { fileName: 'BOX-MIB.mib', success: true, warnings: [{ code: 'escaped-quote', line: 8, message: 'x' }] },
  { fileName: 'FAN-MIB.mib', success: false, error: 'syntax error', diagnosis: { stage: 'parse' } },
  { fileName: 'PSU-MIB.mib', success: true },
  { fileName: 'IF-MIB.txt', success: true },
  { fileName: 'UNRELATED-MIB', success: false, error: 'not ours' },
];

// Every file's verdict, as Go gives it (MibFileStatuses): PSU-MIB loads and
// has an error the load cannot see.
const analyses = [
  { name: 'BOX-MIB.mib', loaded: true, verdict: { level: 'warning', count: 1, detail: 'quote' } },
  { name: 'FAN-MIB.mib', loaded: true, verdict: { level: 'failed', count: 1, detail: 'syntax error' } },
  { name: 'PSU-MIB.mib', loaded: true, verdict: { level: 'error', count: 2, detail: 'psuThing is defined twice' } },
  { name: 'IF-MIB.txt', loaded: true, verdict: { level: 'ok', count: 0 } },
];
const r = buildImportReport(results, loads, analyses);

check('one row per module, the bundled refusal not among them', r.modules.length === 4,
  r.modules.map((m) => m.module).join(', '));
check('a module that does not load comes first', r.modules[0].module === 'FAN-MIB' && r.modules[0].load.status === LOAD_FAIL);
// It loads, and the editor finds errors in it: the same verdict as the
// editor's list and the MIB settings, not "loaded".
check('then one that loads with errors', r.modules[1].module === 'PSU-MIB' && r.modules[1].load.status === LOAD_ERROR
  && r.modules[1].load.count === 2 && r.modules[1].load.detail.includes('twice'), JSON.stringify(r.modules[1].load));
check('then one with warnings', r.modules[2].module === 'BOX-MIB' && r.modules[2].load.status === LOAD_WARN);

const box = r.modules.find((m) => m.module === 'BOX-MIB');
// The warnings belong to the version KEPT. They were attached to the first
// row naming the module — a version set aside.
check('the kept version is the row', box.source === 'pack/box/2.0/box.mib' && box.date === '2023-04-01', box.source);
check('its warnings are on it', box.load.warnings.length === 1);
check('the versions set aside are under it, each with its reason',
  box.setAside.length === 2 && box.setAside[0].reason === 'older' && box.setAside[1].reason === 'unparsable');

const psu = r.modules.find((m) => m.module === 'PSU-MIB');
check('a replaced module says what it replaced', psu.outcome === REPLACED && psu.replaced === 'psu old.mib');
check('a clean one is ok', r.modules.find((m) => m.module === 'IF-MIB').load.status === LOAD_OK);
check('an unchanged module is there as present', r.modules.some((m) => m.module === 'IF-MIB' && m.outcome === PRESENT));

check('a real error is listed apart', r.errors.length === 1 && r.errors[0].reason === 'bundled');
check('files that are not MIBs are grouped by kind, the biggest group first',
  r.ignored.length === 2 && r.ignored[0].kind === 'binary' && r.ignored[0].files.length === 2, JSON.stringify(r.ignored));
check('a load result for a file this import did not write is not shown', !r.modules.some((m) => m.module === 'UNRELATED-MIB'));

const c = r.counts;
check('the counts', c.modules === 4 && c.warned === 1 && c.errored === 1 && c.failed === 1 && c.setAside === 2 && c.ignored === 3 && c.errors === 1,
  JSON.stringify(c));
check('this one needs reading', reportNeedsAttention(r));

// A clean import of modules that all load opens nothing.
const clean = buildImportReport(
  [{ fileName: 'a.mib', module: 'A-MIB', storedAs: 'A-MIB.mib', outcome: IMPORTED, success: true }],
  [{ fileName: 'A-MIB.mib', success: true }],
);
check('a clean import needs no report', !reportNeedsAttention(clean));

// Results from a bridge that predates outcomes still read.
const old = buildImportReport([{ fileName: 'b.mib', success: true }, { fileName: 'c.mib', success: false, error: 'boom' }], []);
check('booleans alone still read', old.modules.length === 1 && old.errors.length === 1);

check('a short path keeps the file and its folder', shortPath('archive/source/1.10 release/box.mib') === '…/1.10 release/box.mib',
  shortPath('archive/source/1.10 release/box.mib'));
check('a short path leaves a short one alone', shortPath('box.mib') === 'box.mib');
check('a short path reads backslashes', shortPath('a\\b\\c\\d.mib', 3) === '…/b/c/d.mib', shortPath('a\\b\\c\\d.mib', 3));

/* --- choosing a version set aside ------------------------------------------ */
{
  const box = r.modules.find((m) => m.module === 'BOX-MIB');
  const older = box.setAside.find((v) => v.reason === 'older');
  check('a version set aside can be chosen when the import gave its path', canChoose(older));
  check('one with no path cannot', !canChoose(box.setAside.find((v) => v.reason === 'unparsable')));
  check('nor a copy of the version kept', !canChoose({ path: 'x', reason: 'identical' }));

  const after = chooseVersion(r, 'BOX-MIB', older,
    { storedAs: 'BOX-MIB.mib', outcome: REPLACED, date: '2019-01-01', replaced: 'BOX-MIB.mib' },
    loads, [{ name: 'BOX-MIB.mib', loaded: true, verdict: { level: 'ok', count: 0 } }]);
  const row = after.modules.find((m) => m.module === 'BOX-MIB');
  check('the chosen version is the row', row.source === 'pack/box/1.0/box.mib' && row.date === '2019-01-01' && row.outcome === REPLACED);
  check('the one it replaced is set aside, as the choice', row.setAside[0].source === 'pack/box/2.0/box.mib' && row.setAside[0].reason === CHOSEN
    && row.setAside[0].path === 'C:/drop/pack/box/2.0/box.mib');
  check('and it is not listed twice', row.setAside.filter((v) => v.source === 'pack/box/1.0/box.mib').length === 0);
  check('its verdict is the new one', row.load.status === LOAD_OK);
  check('the counts follow', after.counts.warned === 0 && after.counts.setAside === r.counts.setAside);
  check('the original report is untouched', r.modules.find((m) => m.module === 'BOX-MIB').source === 'pack/box/2.0/box.mib');
}

for (const input of [undefined, null, [], {}, 'x']) {
  let threw = '';
  try {
    const x = buildImportReport(input, input);
    if (!Array.isArray(x.modules) || typeof x.counts.modules !== 'number') threw = 'shape';
  } catch (e) {
    threw = e.message;
  }
  check(`survives ${JSON.stringify(input) ?? 'undefined'}`, threw === '', threw);
}

if (failures > 0) {
  console.error(`\n${failures} import report check(s) failed`);
  process.exit(1);
}
console.log('import report: all checks passed');
