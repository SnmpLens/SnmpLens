// How the verdict on a MIB file is shown, everywhere it is shown.
//
// The verdict itself is decided in Go (pkg/mib/verdict.go), once, from what
// the last load did with the file and what the editor's analysis finds in it:
//
//   failed   nothing from the file is in the tree;
//   error    it loads, and some of its definitions are wrong or missing;
//   warning  everything resolves; something is still worth fixing;
//   ok       nothing to say.
//
// It used to be decided here, by joining two answers the bridge gave
// separately, and each screen joined them for itself: the editor's list and
// the MIB settings disagreed about the same file. What is left here is
// presentation, and the one overlay the renderer alone can make — the file
// open in the editor, judged as it is being typed.
//
// Pure, so tests/mibverdict.test.mjs runs it under node.

export const FAILED = 'failed';
export const ERROR = 'error';
export const WARNING = 'warning';
export const OK = 'ok';

/** Worst first. */
export const LEVELS = [FAILED, ERROR, WARNING, OK];

const RANK = { [FAILED]: 0, [ERROR]: 1, [WARNING]: 2, [OK]: 3 };

/** The icon each level is shown with, everywhere. */
export const ICONS = { [FAILED]: 'circle-x', [ERROR]: 'circle-alert', [WARNING]: 'triangle-alert', [OK]: 'circle-check' };

/**
 * Every file's verdict, by name.
 *
 * @param {Array} statuses MibFileStatus[] as MibFileStatuses returns them
 * @param {string} [openName] the directory file open in the editor
 * @param {object} [live] its verdict as the editor sees it now, which Go gave
 *   with the buffer's analysis (MibEditorAnalyse)
 * @returns {Object<string, {level: string, count: number, detail: string, live: boolean, loaded: boolean}>}
 */
export function verdictMap(statuses, openName, live) {
  const out = {};
  for (const s of Array.isArray(statuses) ? statuses : []) {
    if (!s || !s.name || !s.verdict || !(s.verdict.level in RANK)) continue;
    out[s.name] = { ...shape(s.verdict), live: false, loaded: !!s.loaded };
  }
  if (openName && live && live.level in RANK) {
    out[openName] = { ...shape(live), live: true, loaded: !!out[openName]?.loaded };
  }
  return out;
}

function shape(v) {
  return { level: v.level, count: v.count || 0, detail: v.detail || '' };
}

/** How many of each level. */
export function countVerdicts(verdicts) {
  const out = { [FAILED]: 0, [ERROR]: 0, [WARNING]: 0, [OK]: 0 };
  for (const v of Object.values(verdicts || {})) {
    if (v && v.level in out) out[v.level]++;
  }
  return out;
}

/** Orders by verdict, worst first; `a` and `b` carry a `level`. */
export function byVerdict(a, b) {
  return (RANK[a?.level] ?? 4) - (RANK[b?.level] ?? 4);
}

/** Whether a level is something to look at. */
export function isProblem(level) {
  return level === FAILED || level === ERROR || level === WARNING;
}
