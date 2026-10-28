// The verdict is Go's (pkg/mib/verdict.go); this checks what the renderer does
// with it: one map every screen reads, the open file overlaid, counted and
// ordered worst first.

import { verdictMap, countVerdicts, byVerdict, isProblem, LEVELS, ICONS, FAILED, ERROR, WARNING, OK } from '../src/utils/mibVerdict.js';
import { readFileSync } from 'node:fs';

let failures = 0;
function check(name, ok, detail) {
  if (ok) {
    console.log('PASS ', name);
  } else {
    failures++;
    console.error('FAIL ', name, detail === undefined ? '' : '— ' + detail);
  }
}

const statuses = [
  { name: 'A-MIB', loaded: true, verdict: { level: 'failed', count: 1, detail: 'refused' } },
  { name: 'B-MIB', loaded: true, verdict: { level: 'error', count: 2, detail: 'defined twice' } },
  { name: 'C-MIB', loaded: false, verdict: { level: 'warning', count: 1, detail: 'tabs' } },
  { name: 'D-MIB', loaded: true, verdict: { level: 'ok', count: 0 } },
  { name: 'E-MIB', loaded: true, verdict: { level: 'nonsense' } },
  null,
];

/* --- the map -------------------------------------------------------------- */
{
  const m = verdictMap(statuses);
  check('every file with a verdict is in the map', Object.keys(m).sort().join() === 'A-MIB,B-MIB,C-MIB,D-MIB',
    Object.keys(m).join());
  check('a level the renderer does not know is left out, not shown as nothing', !('E-MIB' in m));
  check('what Go said is kept', m['B-MIB'].level === ERROR && m['B-MIB'].count === 2 && m['B-MIB'].detail === 'defined twice');
  check('and whether the file was loaded', m['C-MIB'].loaded === false && m['A-MIB'].loaded === true);
  check('a missing detail is an empty string', m['D-MIB'].detail === '' && m['D-MIB'].count === 0);
  check('nothing is live without an open file', Object.values(m).every((v) => !v.live));
}

/* --- the open file -------------------------------------------------------- */
{
  const m = verdictMap(statuses, 'B-MIB', { level: 'warning', count: 3, detail: 'long line' });
  check('the open file is judged as the editor sees it', m['B-MIB'].level === WARNING && m['B-MIB'].live,
    JSON.stringify(m['B-MIB']));
  check('and stays a file that was loaded', m['B-MIB'].loaded === true);
  check('the others are untouched', m['A-MIB'].level === FAILED && !m['A-MIB'].live);

  const n = verdictMap(statuses, 'NEW-MIB', { level: 'ok', count: 0 });
  check('an open file the list has not seen yet still gets one', n['NEW-MIB'].level === OK && n['NEW-MIB'].live);
  check('no verdict yet for the open file leaves the listed one', verdictMap(statuses, 'B-MIB', null)['B-MIB'].level === ERROR);
}

/* --- counts and order ----------------------------------------------------- */
{
  const c = countVerdicts(verdictMap(statuses));
  check('counted by level', c[FAILED] === 1 && c[ERROR] === 1 && c[WARNING] === 1 && c[OK] === 1, JSON.stringify(c));
  const sorted = Object.values(verdictMap(statuses)).sort(byVerdict).map((v) => v.level);
  check('worst first', sorted.join() === LEVELS.join(), sorted.join());
  check('ok is not a problem, the rest are', !isProblem(OK) && [FAILED, ERROR, WARNING].every(isProblem));
  check('every level has an icon', LEVELS.every((l) => typeof ICONS[l] === 'string'));
}

/* --- the levels are Go's --------------------------------------------------- */
{
  const go = readFileSync(new URL('../../pkg/mib/verdict.go', import.meta.url), 'utf8');
  const levels = [...go.matchAll(/Verdict\w+\s*=\s*"(\w+)"/g)].map((m) => m[1]);
  check('the levels are the ones Go judges with, worst first', levels.join() === LEVELS.join(), levels.join());
}

if (failures) {
  console.error(`\n${failures} failure(s)`);
  process.exit(1);
}
