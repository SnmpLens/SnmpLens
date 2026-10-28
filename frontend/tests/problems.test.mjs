// The MIB editor's problem table: sorted by severity by default, sortable by
// any column, filterable by severity. On a vendor MIB the one error that stops
// it loading used to sit under a dozen style infos.
import {
  sortProblems, filterProblems, countBySeverity, positionOf, withoutMuted, MUTABLE, FIXABLE,
} from '../src/mibeditor/problems.js';
import { readFileSync, readdirSync } from 'node:fs';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const list = [
  { severity: 'info', code: 'tabs', line: 3, column: 1, message: 'tabs' },
  { severity: 'warning', code: 'escaped-quote', line: 40, column: 9, message: 'quote' },
  { severity: 'info', code: 'unused-import', line: 2, column: 5, message: 'unused' },
  { severity: 'error', code: 'syntax', line: 120, column: 3, message: 'boom' },
  { severity: 'warning', code: 'bad-date', line: 12, column: 20, message: 'date' },
  { severity: 'error', code: 'unknown-module', line: 0, column: 0, message: 'no line' },
  null,
];
const lines = (xs) => xs.map((d) => d.line).join(',');

const bySev = sortProblems(list);
check('severity first, errors leading', bySev[0].severity === 'error' && bySev[1].severity === 'error'
  && bySev[2].severity === 'warning' && bySev.at(-1).severity === 'info', bySev.map((d) => d.severity).join(','));
check('within a severity, in reading order, a problem with no line last', lines(bySev) === '120,0,12,40,2,3', lines(bySev));

const reversed = sortProblems(list, 'severity', 'desc');
check('descending puts the infos first', reversed[0].severity === 'info' && reversed.at(-1).severity === 'error');

const byLine = sortProblems(list, 'line');
check('by line', lines(byLine) === '2,3,12,40,120,0', lines(byLine));
check('descending by line, a problem with no line is still last', lines(sortProblems(list, 'line', 'desc')) === '120,40,12,3,2,0',
  lines(sortProblems(list, 'line', 'desc')));

const byCode = sortProblems(list, 'code');
check('by code', byCode[0].code === 'bad-date' && byCode.at(-1).code === 'unused-import', byCode.map((d) => d.code).join(','));

check('the input is not reordered', list[0].code === 'tabs');

const errorsOnly = filterProblems(list, new Set(['error']));
check('a filter keeps the severities asked for', errorsOnly.length === 2 && errorsOnly.every((d) => d.severity === 'error'));
check('a filter given an array works too', filterProblems(list, ['warning', 'info']).length === 4);
check('an unknown severity counts as info', filterProblems([{ severity: 'hint' }], ['info']).length === 1);

const counts = countBySeverity(list);
check('counts', counts.error === 2 && counts.warning === 2 && counts.info === 2, JSON.stringify(counts));

check('position with a column', positionOf({ line: 12, column: 5 }) === '12:5');
check('position without a column', positionOf({ line: 12 }) === '12');
check('no position', positionOf({ line: 0 }) === '—');

for (const input of [undefined, null, 'x', {}]) {
  let threw = '';
  try {
    sortProblems(input);
    filterProblems(input);
    countBySeverity(input);
  } catch (e) {
    threw = e.message;
  }
  check(`survives ${JSON.stringify(input) ?? 'undefined'}`, threw === '', threw);
}

/* --- muted rules ----------------------------------------------------------- */
{
  const muted = new Set(['tabs', 'bad-date', 'syntax', 'unknown-module']);
  const left = withoutMuted(list, muted);
  check('a muted rule leaves the table', !left.some((d) => d && (d.code === 'tabs' || d.code === 'bad-date')),
    left.map((d) => d && d.code).join(','));
  check('an error stays whatever is muted', left.some((d) => d && d.code === 'syntax') && left.some((d) => d && d.code === 'unknown-module'));
  check('the rest stays', left.some((d) => d && d.code === 'unused-import') && left.some((d) => d && d.code === 'escaped-quote'));
  check('nothing muted, nothing taken', withoutMuted(list, new Set()).length === list.length);
}

/* --- the lists are Go's ------------------------------------------------------ */
{
  const dir = new URL('../../pkg/mib/', import.meta.url);
  const sources = readdirSync(dir).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .map((f) => readFileSync(new URL(f, dir), 'utf8')).join('\n');
  const codes = Object.fromEntries([...sources.matchAll(/\b(Code\w+)\s*=\s*"([\w-]+)"/g)].map((m) => [m[1], m[2]]));
  const namesIn = (block) => [...block.matchAll(/\b(Code\w+)\s*:\s*true/g)].map((m) => codes[m[1]]).sort();
  const mutable = namesIn(sources.match(/var mutable = map\[string\]bool\{([\s\S]*?)\n\}/)?.[1] || '');
  const fixable = namesIn(sources.match(/var QuickFixable = map\[string\]bool\{([^}]*)\}/)?.[1] || '');
  check('the rules offered for muting are the ones Go mutes', mutable.join() === [...MUTABLE].sort().join(),
    `go ${mutable.join()} / js ${[...MUTABLE].sort().join()}`);
  check('the fixes offered are the ones Go writes', fixable.join() === [...FIXABLE].sort().join(),
    `go ${fixable.join()} / js ${[...FIXABLE].sort().join()}`);
}

if (failures > 0) {
  console.error(`\n${failures} problem table check(s) failed`);
  process.exit(1);
}
console.log('problem table: all checks passed');
