// The editor's problem list, sorted and filtered.
//
// The list came in the order the checks ran: the syntax error, then the
// structure, then style advice, then the semantic pass — so on a vendor MIB the
// one error that stops it loading sat under a dozen "tabs" and "unused import"
// infos. A table sorted by severity puts it first, and a filter takes the
// advice out of the way.
//
// Pure, so tests/problems.test.mjs runs it under node.

/** Severity order: what stops a module loading first. */
export const SEVERITIES = ['error', 'warning', 'info'];

const RANK = { error: 0, warning: 1, info: 2 };

/** Columns a problem list sorts by. */
export const SORT_KEYS = ['severity', 'line', 'message', 'code'];

/**
 * @param {Array} list Diagnostic[] as the analysis returns them
 * @param {Set<string>|string[]} visible the severities to keep
 */
export function filterProblems(list, visible) {
  const keep = visible instanceof Set ? visible : new Set(visible || SEVERITIES);
  return (Array.isArray(list) ? list : []).filter((d) => d && keep.has(normalSeverity(d.severity)));
}

/**
 * Sorted by one column, then by severity, line and column, so equal keys keep a
 * reading order rather than whatever order the checks ran in.
 *
 * @param {Array} list
 * @param {string} key one of SORT_KEYS
 * @param {'asc'|'desc'} dir
 */
export function sortProblems(list, key = 'severity', dir = 'asc') {
  const sign = dir === 'desc' ? -1 : 1;
  const primary = comparators[key] || comparators.severity;
  return [...(Array.isArray(list) ? list : [])]
    .filter(Boolean)
    .sort((a, b) => {
      if (key === 'line') {
        // Last both ways: a problem with no line cannot be jumped to, and it is
        // not "before line 1" either.
        const na = !(a.line > 0);
        const nb = !(b.line > 0);
        if (na !== nb) return na ? 1 : -1;
      }
      return sign * primary(a, b) || bySeverity(a, b) || byPosition(a, b);
    });
}

/** How many problems of each severity, for the filter's labels. */
export function countBySeverity(list) {
  const out = { error: 0, warning: 0, info: 0 };
  for (const d of Array.isArray(list) ? list : []) {
    if (d) out[normalSeverity(d.severity)]++;
  }
  return out;
}

/** A position worth showing: "12:5", "12" without a column, "—" without a line. */
export function positionOf(d) {
  if (!d || !(d.line > 0)) return '—';
  return d.column > 0 ? `${d.line}:${d.column}` : String(d.line);
}

function normalSeverity(s) {
  return s in RANK ? s : 'info';
}

function bySeverity(a, b) {
  return RANK[normalSeverity(a.severity)] - RANK[normalSeverity(b.severity)];
}

// A problem with no line goes after every one that has one.
function byPosition(a, b) {
  const la = a.line > 0 ? a.line : Infinity;
  const lb = b.line > 0 ? b.line : Infinity;
  if (la !== lb) return la < lb ? -1 : 1;
  return (a.column || 0) - (b.column || 0);
}

const comparators = {
  severity: bySeverity,
  line: byPosition,
  message: (a, b) => String(a.message || '').localeCompare(String(b.message || '')),
  code: (a, b) => String(a.code || '').localeCompare(String(b.code || '')),
};

/**
 * The rules a person may mute: advice, never an error (pkg/mib/mute.go, which
 * decides; tests/problems.test.mjs holds the two lists together).
 */
export const MUTABLE = new Set([
  'bad-date', 'escaped-quote', 'import-alias', 'long-line', 'no-description', 'row-access', 'tabs', 'unused-import',
]);

/** The codes the editor can write a fix for (pkg/mib/quickfix.go). */
export const FIXABLE = new Set(['row-access', 'unused-import']);

/**
 * The problems left once muted rules are taken out. An error stays whatever
 * is muted, as it does in Go's verdict.
 *
 * @param {Array} list
 * @param {Set<string>} muted
 */
export function withoutMuted(list, muted) {
  const all = Array.isArray(list) ? list : [];
  if (!muted || muted.size === 0) return all;
  return all.filter((d) => d && (d.severity === 'error' || !MUTABLE.has(d.code) || !muted.has(d.code)));
}
