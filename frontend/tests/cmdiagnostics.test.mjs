// Go's diagnostics as the editor's underlines (mibeditor/cmDiagnostics.js).
import { EditorState } from '@codemirror/state';
import { toCodeMirror, MAX_UNDERLINES } from '../src/mibeditor/cmDiagnostics.js';
import { smiLanguage } from '../src/mibeditor/smiLanguage.js';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const doc = [
  'EXAMPLE-MIB DEFINITIONS ::= BEGIN',
  '\tSYNTAX\tIntegerr32',
  '\tDESCRIPTION\t"Température — 温度" ::= { geoRot 1 }',
  'x',
  'END',
].join('\n');
const state = EditorState.create({ doc, extensions: [smiLanguage] });
const text = (r) => state.sliceDoc(r.from, r.to);
const at = (line, needle) => doc.split('\n')[line - 1].indexOf(needle) + 1;

const out = toCodeMirror(state, [
  { line: 2, column: at(2, 'Integerr32'), severity: 'error', code: 'unknown-type', symbol: 'Integerr32', message: 'm' },
  { line: 3, column: at(3, 'geoRot'), severity: 'warning', code: 'unknown-parent', symbol: 'geoRot', message: 'm' },
  { line: 1, column: 1, severity: 'info', code: 'tabs', message: 'advice' },
  { line: 4, column: 1, severity: 'warning', code: 'no-description', symbol: 'xxxxxxxx', message: 'past the end' },
  { line: 1, column: 13, severity: 'error', code: 'syntax', message: 'no symbol: the word at the column' },
  { line: 99, column: 1, severity: 'error', code: 'x', message: 'beyond the file' },
  { line: 0, column: 0, severity: 'error', code: 'x', message: 'no line' },
], (d) => (d.code === 'unknown-parent' ? [{ name: 'fix' }] : []));

check('a symbol after tabs is underlined exactly', text(out[0]) === 'Integerr32', text(out[0]));
check('and after accented and wide characters', text(out[1]) === 'geoRot', text(out[1]));
check('advice gets no underline, nor a line that is not there', out.length === 4, String(out.length));
check('an underline stops at the end of its line', text(out[2]) === 'x', JSON.stringify(text(out[2])));
check('without a symbol, the word at the column', text(out[3]) === 'DEFINITIONS', text(out[3]));
check('severities are kept', out[0].severity === 'error' && out[1].severity === 'warning');
check('the code is the source, the actions are given', out[1].source === 'unknown-parent' && out[1].actions.length === 1);

const many = Array.from({ length: MAX_UNDERLINES + 50 }, () => ({ line: 1, column: 1, severity: 'error', code: 'x', message: 'm' }));
check('capped', toCodeMirror(state, many).length === MAX_UNDERLINES);
check('survives nothing', toCodeMirror(state, null).length === 0 && toCodeMirror(state, [null]).length === 0);

if (failures) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
