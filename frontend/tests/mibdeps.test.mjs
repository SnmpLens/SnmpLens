// One missing module fails twelve files, and reads as twelve unrelated
// problems until something rolls it the other way.
//
// The per-file diagnostics list answers "did this file load". The question in
// front of someone with a folder of vendor MIBs is "what is missing, and who
// needs it" — the same facts, one row per absent module instead of one row per
// broken file.
import { dependencyRoll, symbolsWanted, ABSENT, FAILED, NOT_LOADED } from '../src/utils/mibDependencies.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const fail = (fileName, missing, moduleName) => ({
  fileName, success: false, error: 'x',
  diagnosis: { fileName, moduleName, stage: 'imports', missing },
});
const ok = (fileName, moduleName) => ({ fileName, success: true, moduleName });

/* --- the roll ------------------------------------------------------------- */
{
  const diags = [
    fail('ACME-POE-MIB', [
      { module: 'ACME-SMI', symbols: ['acmeProducts'], reason: ABSENT },
      { module: 'SNMPv2-TC', symbols: ['DisplayString'], reason: NOT_LOADED },
    ], 'ACME-POE-MIB'),
    fail('ACME-PSU-MIB', [
      { module: 'ACME-SMI', symbols: ['acmeProducts', 'acmeModules'], reason: ABSENT },
    ], 'ACME-PSU-MIB'),
    fail('ACME-FAN-MIB', [
      { module: 'ACME-SMI', symbols: ['acmeProducts'], reason: NOT_LOADED },
    ], 'ACME-FAN-MIB'),
    ok('IF-MIB', 'IF-MIB'),
    ok('SNMPv2-SMI', 'SNMPv2-SMI'),
  ];

  const roll = dependencyRoll(diags);

  check('successes and failures are counted', roll.loaded === 2 && roll.failed === 3,
    `${roll.loaded} loaded, ${roll.failed} failed`);
  check('three broken files become two missing modules', roll.missing.length === 2,
    roll.missing.map((m) => m.module).join(', '));

  const top = roll.missing[0];
  check('the module blocking the most files comes first', top.module === 'ACME-SMI', top.module);
  check('and it names every file that wanted it', top.neededBy.length === 3,
    top.neededBy.map((n) => n.file).join(', '));

  // The same module reported absent by one file and merely not-loaded by
  // another: absent needs a download, not-loaded needs a checkbox. Reporting
  // the milder one would send someone to the wrong fix.
  check('the worst reason wins', top.reason === ABSENT, top.reason);

  check('symbols are deduplicated across the files that asked',
    JSON.stringify(symbolsWanted(top)) === JSON.stringify(['acmeModules', 'acmeProducts']),
    JSON.stringify(symbolsWanted(top)));
}

/* --- a file named otherwise than its module ------------------------------- */
// gosmi is given every module under the name it declares (pkg/mib/mibfs.go),
// so a file named after a product is found like any other: not a row.
{
  const roll = dependencyRoll([ok('vendor_power.txt', 'VENDOR-POWER-MIB'), ok('IF-MIB', 'IF-MIB')]);
  check('a file named otherwise than its module is not reported', !('mismatched' in roll) && roll.missing.length === 0);
}

/* --- it must not fall over on what the bridge really sends ---------------- */
{
  const cases = [
    ['nothing', undefined],
    ['an empty list', []],
    ['a non-array', { nope: true }],
    ['a null entry', [null, { fileName: 'A', success: true }]],
    ['a failure with no diagnosis', [{ fileName: 'A', success: false, error: 'boom' }]],
    ['a diagnosis with no missing list', [{ fileName: 'A', success: false, diagnosis: { stage: 'parse' } }]],
    ['a missing entry with no module', [{ fileName: 'A', success: false, diagnosis: { missing: [{ symbols: ['x'] }] } }]],
  ];
  for (const [name, input] of cases) {
    let threw = null;
    try {
      const r = dependencyRoll(input);
      if (!r || !Array.isArray(r.missing)) threw = 'shape';
    } catch (e) {
      threw = e.message;
    }
    check(`survives ${name}`, threw === null, threw || '');
  }
}

/* --- prove the checks can fail ------------------------------------------- */
{
  const one = dependencyRoll([
    fail('A', [{ module: 'X', symbols: ['s'], reason: ABSENT }], 'A'),
  ]);
  check('the detector: one broken file yields one missing module',
    one.missing.length === 1 && one.missing[0].neededBy.length === 1);
  check('the detector: FAILED outranks NOT_LOADED but not ABSENT', (() => {
    const r = dependencyRoll([
      fail('A', [{ module: 'X', reason: NOT_LOADED }], 'A'),
      fail('B', [{ module: 'X', reason: FAILED }], 'B'),
    ]);
    return r.missing[0].reason === FAILED;
  })());
}

process.exit(failures ? 1 : 0);
