// Go's diagnostics (MibEditorAnalyse) as CodeMirror's, for @codemirror/lint.
//
// A diagnostic gives a 1-based line and column; CodeMirror wants offsets. The
// underline covers the symbol the diagnostic names, else the word at its
// column, at least two characters, never past the end of its line. Advice
// (info) gets no underline — it would be visual noise — and stays in the
// problem table. Pure, so tests/cmdiagnostics.test.mjs runs it under node.

/** At most this many underlines: past it, the table is the place to read them. */
export const MAX_UNDERLINES = 500;

/**
 * @param {import('@codemirror/state').EditorState} state
 * @param {Array} list Diagnostic[] as Go gives them
 * @param {(d: object) => Array} [actionsFor] the actions a diagnostic offers
 */
export function toCodeMirror(state, list, actionsFor = () => []) {
  const out = [];
  for (const d of Array.isArray(list) ? list : []) {
    if (!d || !(d.line > 0) || d.severity === 'info' || d.line > state.doc.lines) continue;
    const line = state.doc.line(d.line);
    const from = line.from + Math.min(Math.max(0, (d.column || 1) - 1), line.length);
    const word = state.wordAt(from);
    const length = Math.max(2, (d.symbol || '').length || (word ? word.to - word.from : 0) || 6);
    out.push({
      from,
      to: Math.min(line.to, from + length),
      severity: d.severity === 'error' ? 'error' : 'warning',
      message: d.message,
      source: d.code,
      actions: actionsFor(d),
    });
    if (out.length >= MAX_UNDERLINES) break;
  }
  return out;
}
