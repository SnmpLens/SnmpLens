// The line diff behind the MIB editor's "compare" (mibeditor/diff.js).

import { diffLines, hunks } from '../src/mibeditor/diff.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

// What the rows say must rebuild both texts exactly: the old from the kept and
// removed lines, the new from the kept and added ones, each numbered in order.
function rebuilds(before, after, d) {
  const old = d.rows.filter((r) => r.type !== 'add');
  const neu = d.rows.filter((r) => r.type !== 'del');
  const okA = old.map((r) => r.text).join('\n') === before.replace(/\n$/, '') && old.every((r, i) => r.a === i + 1);
  const okB = neu.map((r) => r.text).join('\n') === after.replace(/\n$/, '') && neu.every((r, i) => r.b === i + 1);
  return okA && okB;
}

const base = ['A-MIB DEFINITIONS ::= BEGIN', 'IMPORTS x FROM Y;', 'a OBJECT IDENTIFIER ::= { x 1 }',
  'b OBJECT IDENTIFIER ::= { x 2 }', 'c OBJECT IDENTIFIER ::= { x 3 }', 'END'].join('\n') + '\n';

{
  const d = diffLines(base, base);
  check('identical texts have no change', d.added === 0 && d.removed === 0 && d.rows.every((r) => r.type === 'same'));
  check('and nothing worth showing', hunks(d.rows).length === 0);
}

{
  const after = base.replace('b OBJECT IDENTIFIER ::= { x 2 }', 'b OBJECT IDENTIFIER ::= { x 20 }');
  const d = diffLines(base, after);
  check('one changed line is one removed and one added', d.removed === 1 && d.added === 1, `${d.removed}/${d.added}`);
  check('at its line', d.rows.find((r) => r.type === 'del')?.a === 4 && d.rows.find((r) => r.type === 'add')?.b === 4);
  check('both texts rebuild', rebuilds(base, after, d));
}

{
  const after = base.replace('IMPORTS x FROM Y;\n', 'IMPORTS x FROM Y;\n-- a comment\n-- and another\n')
    .replace('c OBJECT IDENTIFIER ::= { x 3 }\n', '');
  const d = diffLines(base, after);
  check('insertions and a deletion, nothing else', d.added === 2 && d.removed === 1, `${d.removed}/${d.added}`);
  check('both texts rebuild', rebuilds(base, after, d));
}

{
  check('from nothing', diffLines('', 'a\nb\n').added === 2);
  check('to nothing', diffLines('a\nb\n', '').removed === 2);
  check('CRLF is LF', diffLines('a\r\nb\r\n', 'a\nb\n').added === 0);
  check('a final newline is not a line', diffLines('a\nb', 'a\nb\n').added === 0);
}

{
  // Context: three lines either side, the rest folded and counted.
  const lines = Array.from({ length: 40 }, (_, i) => `line ${i + 1}`);
  const after = lines.map((l, i) => (i === 19 ? 'changed' : l));
  const h = hunks(diffLines(lines.join('\n'), after.join('\n')).rows);
  const shown = h.filter((r) => r.type !== 'skip');
  const skips = h.filter((r) => r.type === 'skip').map((r) => r.count);
  check('three lines either side of the change', shown.length === 8, String(shown.length));
  check('the rest folded, and counted', skips.join() === '16,17', skips.join());
}

{
  // Two unrelated texts past the bound are said to be too different.
  const x = Array.from({ length: 50 }, (_, i) => `x${i}`).join('\n');
  const y = Array.from({ length: 50 }, (_, i) => `y${i}`).join('\n');
  check('too different is said, not computed at any cost', diffLines(x, y, 20).tooDifferent);
  check('within the bound it is a diff', !diffLines(x, y).tooDifferent && rebuilds(x, y, diffLines(x, y)));
}

{
  // Randomised: whatever the edits, the rows rebuild both texts.
  let seed = 7;
  const rnd = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
  let ok = true;
  for (let t = 0; t < 200 && ok; t++) {
    const a = Array.from({ length: Math.floor(rnd() * 30) }, () => 'l' + Math.floor(rnd() * 6));
    const b = a.filter(() => rnd() > 0.2).flatMap((l) => (rnd() > 0.8 ? [l, 'n' + Math.floor(rnd() * 6)] : [l]));
    const d = diffLines(a.join('\n'), b.join('\n'));
    ok = rebuilds(a.join('\n'), b.join('\n'), d);
    // Shortest: never more edits than deleting everything and adding it again.
    ok = ok && d.added + d.removed <= a.length + b.length;
  }
  check('random edits always rebuild both texts', ok);
}

if (failures) {
  console.error(`\n${failures} diff check(s) failed`);
  process.exit(1);
}
