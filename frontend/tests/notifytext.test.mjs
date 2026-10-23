// What reaches a native OS notification.
//
// Part of a notification's text is written by the network — a trap's
// snmpTrapOID.0 is whatever the sender typed — and on Windows, Wails up to 2.16
// handed that text to a PowerShell fallback that evaluated `$(...)`. Wails 2.17
// removed the fallback; utils/notifyText.js is the layer of our own, and this
// holds it in place: the cleaning itself, and the one door every notification
// goes through.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, dirname, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { notificationText, trapOidText } from '../src/utils/notifyText.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

// --- the cleaning ---
check('ordinary text is unchanged',
  notificationText('linkDown (1.3.6.1.6.3.1.1.5.3)') === 'linkDown (1.3.6.1.6.3.1.1.5.3)');
check('accents and CJK survive', notificationText('Échec — 端口') === 'Échec — 端口');
check('null and undefined are empty', notificationText(null) === '' && notificationText(undefined) === '');
for (const [label, input] of [
  ['a subexpression', 'a$(b)c'],
  ['a variable', 'a$env:Pathc'],
  ['a backtick escape', 'a`nb'],
]) {
  const out = notificationText(input);
  check(`${label} loses its $ and backtick`, !/[$`]/.test(out), JSON.stringify(out));
}
const cdata = notificationText('x]]><action/>y');
check('a CDATA terminator does not survive', !cdata.includes(']]>'), JSON.stringify(cdata));
const ctl = notificationText('a\r\nb\u0000c\u0085d');
check('control characters become spaces', !/[\u0000-\u001f\u007f-\u009f]/.test(ctl), JSON.stringify(ctl));
check('the length is bounded', Array.from(notificationText('x'.repeat(5000))).length === 256);
const emoji = notificationText('😀'.repeat(300));
check('a cut never splits a surrogate pair', emoji === '😀'.repeat(256), `${emoji.length} code units`);

// --- the trap OID ---
check('a numeric OID is kept', trapOidText('.1.3.6.1.6.3.1.1.5.3') === '1.3.6.1.6.3.1.1.5.3');
for (const v of ['$(calc)', 'linkDown', '1.3.6.1 ; x', '1', '', null, '1..3', '1.3.6.a']) {
  check(`${JSON.stringify(v)} is not shown as a trap OID`, trapOidText(v) === '');
}

// --- the one door ---
const src = join(dirname(fileURLToPath(import.meta.url)), '..', 'src');
const files = [];
(function walk(d) {
  for (const name of readdirSync(d)) {
    const p = join(d, name);
    if (statSync(p).isDirectory()) walk(p);
    else if (/\.(js|svelte)$/.test(name)) files.push(p);
  }
})(src);
const callers = files
  .filter((f) => /\bSendNotification\b/.test(readFileSync(f, 'utf8')))
  .map((f) => relative(src, f).replace(/\\/g, '/'));
check('only utils/nativeNotify.js reaches SendNotification',
  callers.length === 1 && callers[0] === 'utils/nativeNotify.js', callers.join(', '));

const door = readFileSync(join(src, 'utils', 'nativeNotify.js'), 'utf8');
// Anchored on a word boundary: `subtitle: notificationText(` contains
// `title: notificationText(`, and the unanchored pattern passed with the title
// left raw.
for (const field of ['title', 'body', 'subtitle']) {
  check(`nativeNotify.js cleans the ${field}`,
    new RegExp(`\\b${field}:\\s*notificationText\\(\\s*${field}\\s*\\)`).test(door));
}

process.exit(failures ? 1 : 0);
