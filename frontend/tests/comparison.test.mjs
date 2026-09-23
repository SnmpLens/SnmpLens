// The comparison table is published twice and is about other people's products.
// Both halves of that sentence are a failure mode, and this test covers each.
//
// PUBLISHED TWICE. The same table sits in README.md and on docs/comparison.html,
// and it holds prices and release dates — exactly the content that goes stale.
// Two copies maintained by hand disagree with each other long before either
// disagrees with the world, with nothing saying so. This repository has the scar:
// a test carried a copied sample of gosnmp's debug output and claimed
// `SecurityModel:UserSecurityModel` years after gosnmp had stopped printing it,
// and nothing noticed because the redaction keyed on a different field. So both
// renderings come from tools/comparison.json, and this fails when either drifts.
//
// ABOUT OTHER PEOPLE. A cell that is wrong about a competitor is unfair and it
// is the kind of wrong that gets repeated. Two rules are checked structurally
// rather than trusted: every product carries a link to the page its cells were
// read from, and the table is not allowed to be an advertisement — a comparison
// in which one column wins every row is one nobody believes, so the data has to
// name what each of the others is better at.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { data, escapeMd, outOfDate } from '../../tools/comparison.mjs';

const root = new URL('../../', import.meta.url).pathname.replace(/^[/]([A-Za-z]:)/, '$1');

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const read = (rel) => readFileSync(join(root, rel), 'utf8');

// Everything after the first product: the ones this table makes claims about,
// and therefore the ones the rules below are written for.
const others = data.products.slice(1);

// ---------------------------------------------------------------- published twice

const stale = outOfDate();
check(
  'README.md and docs/comparison.html both match tools/comparison.json',
  stale.length === 0,
  stale.length ? `stale: ${stale.map((t) => t.name).join(', ')} — run node tools/comparison.mjs` : '',
);

// ---------------------------------------------------------------- about other people

check(
  'every product links to the page its cells were read from',
  data.products.every((p) => /^https:\/\/\S+$/.test(p.href) && p.sourceLabel),
  data.products.filter((p) => !/^https:\/\/\S+$/.test(p.href)).map((p) => p.id).join(', '),
);

check(
  'the table states the date it was read',
  /^\d{4}-\d{2}-\d{2}$/.test(data.checked ?? ''),
  String(data.checked),
);

// A cell that escapes the pipe but not the backslash is not escaped at all:
// `a\|b` becomes `a\\|b`, which Markdown reads as an escaped backslash and then
// a LIVE column separator, so the pipe returns and every cell after it shifts
// one column left. Found by CodeQL as js/incomplete-sanitization, pinned here.
const escaped = escapeMd('a\\|b');
check(
  'escaping a Markdown cell survives a backslash before the pipe',
  escaped === 'a\\\\\\|b',
  JSON.stringify(escaped),
);

// Every row answers for every product: a blank cell renders as an empty box,
// which reads as "no" while claiming nothing — the one thing this must not do.
const missing = data.rows.flatMap((row) =>
  data.products.filter((p) => row.cells[p.id] === undefined).map((p) => `${row.label}/${p.id}`),
);
check('every row answers for every product', missing.length === 0, missing.join(', '));

const kindOf = (row, id) => {
  const c = row.cells[id];
  return typeof c === 'string' ? undefined : c.kind;
};

// Absence of evidence is not evidence of absence, and the table must not spend
// it as though it were: where a vendor's own list does not mention something,
// the cell is 'unknown' and says so in words.
const unknowns = data.rows.flatMap((row) =>
  others
    .filter((p) => kindOf(row, p.id) === 'unknown')
    .map((p) => ({ row: row.label, id: p.id, text: row.cells[p.id].text })),
);
check(
  'an unverified cell says "Not listed" rather than "No"',
  unknowns.every((u) => /not listed|not dated/i.test(u.text)),
  unknowns.filter((u) => !/not listed|not dated/i.test(u.text)).map((u) => `${u.row}/${u.id}`).join(', '),
);

// ---------------------------------------------------------------- not an advertisement

// At least one row where every product is equal. Without one, a reader is being
// shown a scoreboard rather than a comparison, and stops reading.
const ties = data.rows.filter((row) => data.products.every((p) => kindOf(row, p.id) === 'yes'));
check(
  'at least one row is a tie across all four',
  ties.length > 0,
  ties.map((r) => r.label).join(', '),
);

check(
  'every other product has a stated reason to be chosen over this one',
  others.every((p) => data.strengths.some((s) => s.product === p.id)),
  others.filter((p) => !data.strengths.some((s) => s.product === p.id)).map((p) => p.id).join(', '),
);

check(
  'strengths name products that exist',
  data.strengths.every((s) => data.products.some((p) => p.id === s.product)),
);

// ---------------------------------------------------------------- reachable

const page = read('docs/comparison.html');
check('the page carries the generated block', page.includes('<!-- comparison:start -->'));
check(
  'the page marks itself as the current nav entry',
  /<a href="comparison\.html"[^>]*aria-current="page"/.test(page),
);

const linked = ['docs/index.html', 'docs/documentation.html', 'docs/download.html'].filter((f) =>
  read(f).includes('href="comparison.html"'),
);
check(
  'the site links to it from its main pages',
  linked.length === 3,
  linked.length === 3 ? '' : `missing from ${3 - linked.length}`,
);

check(
  'the sitemap lists it',
  read('docs/sitemap.xml').includes('https://snmplens.com/comparison.html'),
);

check('the README names every product', others.every((p) => read('README.md').includes(p.name)));

console.log(
  failures
    ? `\n${failures} failure(s). The table lives in tools/comparison.json.`
    : `\nComparison: ${data.rows.length} rows over ${data.products.length} products, read ${data.checked}.`,
);
process.exit(failures ? 1 : 0);
