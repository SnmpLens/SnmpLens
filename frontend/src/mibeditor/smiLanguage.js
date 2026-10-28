import { StreamLanguage, HighlightStyle, syntaxHighlighting, syntaxTree } from '@codemirror/language';
import { tags as t } from '@lezer/highlight';
import { smiStream } from './tokenize.js';

// SMI for CodeMirror: the tokeniser of tokenize.js, its classes named as tags,
// and the colours the editor has always used, from the theme's variables so
// they follow it.

const TOKEN_TAGS = {
  mac: t.definitionKeyword,
  kw: t.keyword,
  ty: t.typeName,
  val: t.atom,
  str: t.string,
  com: t.comment,
  num: t.number,
  op: t.definitionOperator,
  pun: t.punctuation,
};

export const smiLanguage = StreamLanguage.define({ ...smiStream, tokenTable: TOKEN_TAGS });

export const smiHighlight = syntaxHighlighting(HighlightStyle.define([
  { tag: t.definitionKeyword, color: 'var(--accent-color)', fontWeight: '600' },
  { tag: t.keyword, color: 'var(--name-color)' },
  { tag: t.typeName, color: 'var(--oid-color)' },
  { tag: t.atom, color: 'var(--success-color)' },
  { tag: t.string, color: 'var(--text-light)' },
  { tag: t.comment, color: 'var(--text-dimmed)', fontStyle: 'italic' },
  { tag: t.number, color: 'var(--oid-color)' },
  { tag: t.definitionOperator, color: 'var(--favorites-color)' },
  { tag: t.punctuation, color: 'var(--text-muted)' },
]));

/**
 * Whether pos sits in prose — a string or a comment — where a symbol name is
 * words and not a reference, and completion must not offer one: Enter, pressed
 * to start a new line, would accept it instead.
 */
export function inProse(state, pos) {
  const node = syntaxTree(state).resolveInner(pos, -1);
  return node.name === 'str' || node.name === 'com';
}
