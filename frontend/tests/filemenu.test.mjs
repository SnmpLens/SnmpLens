// What the MIB editor's file list offers on a right-click (mibeditor/fileMenu.js).

import { readFileSync } from 'node:fs';
import { fileMenu, listMenu, select, copyName } from '../src/mibeditor/fileMenu.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const files = [
  { name: 'IF-MIB', module: 'IF-MIB', bundled: true, modified: true },
  { name: 'SNMPv2-SMI', module: 'SNMPv2-SMI', bundled: true, modified: false },
  { name: 'vendor.txt', module: 'EXAMPLE-VENDOR-MIB' },
  { name: 'vendor-2.txt', module: 'EXAMPLE-VENDOR-MIB' },
  { name: 'loose.mib' },
];
const off = new Set(['vendor-2.txt']);
const isEnabled = (n) => !off.has(n);
const verdicts = {
  'vendor.txt': { level: 'warning', loaded: true },
  'IF-MIB': { level: 'ok', loaded: true },
  'SNMPv2-SMI': { level: 'failed', loaded: true },
};
const menu = (names) => fileMenu({ names, files, isEnabled, verdicts });
const actions = (items) => items.map((i) => i.action);
const item = (items, action) => items.find((i) => i.action === action);

/* --- one vendor file ------------------------------------------------------- */
{
  const m = menu(['vendor.txt']);
  for (const a of ['copyName', 'copyPath', 'copyModule', 'reveal', 'rename', 'renameToModule', 'duplicate', 'delete',
    'backups', 'disable', 'useThisVersion', 'diagnose', 'showInTree', 'imports', 'importers', 'export',
    'copyProblems', 'openAndFix']) {
    check(`a vendor file offers ${a}`, actions(m).includes(a));
  }
  check('nothing about a standard MIB', !actions(m).includes('restoreBundled') && !actions(m).includes('compareBundled'));
  check('"use this version" counts the others', item(m, 'useThisVersion').values.count === 1);
  check('rename and delete are allowed', !item(m, 'rename').disabled && !item(m, 'delete').disabled);
  check('shortcuts are shown', item(m, 'rename').hint === 'F2' && item(m, 'delete').hint === 'Del');
}

/* --- a modified standard MIB ---------------------------------------------- */
{
  const m = menu(['IF-MIB']);
  check('compare and restore the shipped copy', actions(m).includes('compareBundled') && actions(m).includes('restoreBundled'));
  check('rename and delete are refused, with the reason', item(m, 'rename').disabled && item(m, 'rename').reason === 'bundledKeepsName'
    && item(m, 'delete').disabled && item(m, 'delete').reason === 'bundledStays');
  check('no "rename to its module" when it is already named so', !actions(m).includes('renameToModule'));
  check('nor "copy the module name"', !actions(m).includes('copyModule'));
  const unmodified = menu(['SNMPv2-SMI']);
  check('an unmodified one has nothing to compare or restore', !actions(unmodified).includes('restoreBundled'));
  check('a module that does not load cannot be shown in the tree', item(unmodified, 'showInTree').disabled
    && item(unmodified, 'showInTree').reason === 'notInTree');
}

/* --- states ---------------------------------------------------------------- */
{
  check('a disabled file offers enable', actions(menu(['vendor-2.txt'])).includes('enable')
    && !actions(menu(['vendor-2.txt'])).includes('disable'));
  check('a disabled file is not in the tree', item(menu(['vendor-2.txt']), 'showInTree').disabled);
  const loose = menu(['loose.mib']);
  check('a file declaring no module: no versions, no tree, no module name',
    !['useThisVersion', 'showInTree', 'copyModule', 'renameToModule'].some((a) => actions(loose).includes(a)));
}

/* --- several files --------------------------------------------------------- */
{
  const m = menu(['IF-MIB', 'vendor.txt', 'vendor-2.txt']);
  check('several: copy, enable, disable, export, delete', actions(m).filter((a) => a !== '---').join() ===
    'copyName,enable,disable,export,delete', actions(m).join());
  check('the delete counts what it will delete, not the standard MIB', item(m, 'delete').values.count === 2);
  const std = menu(['IF-MIB', 'SNMPv2-SMI']);
  check('only standard MIBs: delete refused', item(std, 'delete').disabled);
  check('all enabled: no enable', !actions(menu(['IF-MIB', 'vendor.txt'])).includes('enable'));
}

/* --- shape ----------------------------------------------------------------- */
{
  let ok = true;
  for (const names of [['vendor.txt'], ['IF-MIB'], ['loose.mib'], ['IF-MIB', 'vendor.txt']]) {
    const a = actions(menu(names));
    ok = ok && a[0] !== '---' && a.at(-1) !== '---' && !a.some((x, i) => x === '---' && a[i + 1] === '---');
  }
  check('no separator at either end or twice in a row', ok);
  check('nothing selected, nothing offered', menu([]).length === 0 && menu(['gone.txt']).length === 0);
  check('the list itself', actions(listMenu()).join() === 'restoreDeleted,openFolder,refresh');
}

/* --- every label exists ------------------------------------------------------ */
{
  const en = JSON.parse(readFileSync(new URL('../src/i18n/en.json', import.meta.url), 'utf8')).mibEditor.fileMenu || {};
  const keys = new Set();
  for (const names of [['vendor.txt'], ['IF-MIB'], ['SNMPv2-SMI'], ['vendor-2.txt'], ['IF-MIB', 'vendor.txt', 'vendor-2.txt'],
    ['IF-MIB', 'SNMPv2-SMI']]) {
    for (const i of menu(names)) {
      if (i.key) keys.add(i.key);
      if (i.reason) keys.add(i.reason);
    }
  }
  for (const i of listMenu()) keys.add(i.key);
  const missing = [...keys].filter((k) => typeof en[k] !== 'string');
  check('en.json names every entry and every reason', missing.length === 0, missing.join(', '));
}

/* --- selection --------------------------------------------------------------- */
{
  const shown = ['a', 'b', 'c', 'd', 'e'];
  check('a click selects one', select(['a', 'b'], 'a', 'c', shown).selected.join() === 'c');
  check('Ctrl adds', select(['a'], 'a', 'c', shown, { toggle: true }).selected.join() === 'a,c');
  check('Ctrl removes', select(['a', 'c'], 'a', 'c', shown, { toggle: true }).selected.join() === 'a');
  check('Shift takes the range, in the order shown', select(['d'], 'd', 'b', shown, { range: true }).selected.join() === 'b,c,d');
  check('Shift keeps the anchor', select(['d'], 'd', 'b', shown, { range: true }).anchor === 'd');
  check('Shift with no anchor is a click', select([], '', 'b', shown, { range: true }).selected.join() === 'b');
}

/* --- the name a copy is offered under ---------------------------------------- */
{
  check('a copy keeps the extension', copyName('vendor.txt', []) === 'vendor copy.txt');
  check('an extension-less MIB', copyName('IF-MIB', []) === 'IF-MIB copy');
  check('a taken name is numbered', copyName('IF-MIB', ['IF-MIB copy', 'IF-MIB copy 2']) === 'IF-MIB copy 3');
  check('a dotted module name is not an extension', copyName('SNMP.FRAMEWORK-MIB-LONGNAME', []) === 'SNMP.FRAMEWORK-MIB-LONGNAME copy');
}

if (failures) {
  console.error(`\n${failures} file menu check(s) failed`);
  process.exit(1);
}
