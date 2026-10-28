// A line diff, for comparing a MIB with the copy that ships in SnmpLens or
// with one of its backups.
//
// Myers's algorithm (1986): the shortest edit script, so a change reads as the
// lines that changed and not as everything after them. What the two texts
// share at either end is set aside first, which is most of a MIB compared
// with its own backup. The trace grows with the square of the number of edits,
// so past MAX_EDITS the texts are said to be too different rather than
// compared at any cost — two unrelated files compare as that anyway.
//
// Pure, so tests/diff.test.mjs runs it under node.

export const MAX_EDITS = 2000;

/** Lines of context kept around each change. */
export const CONTEXT = 3;

/**
 * @param {string} before
 * @param {string} after
 * @returns {{rows: Array<{type: 'same'|'del'|'add', a: number|null, b: number|null, text: string}>,
 *   added: number, removed: number, tooDifferent: boolean}}
 */
export function diffLines(before, after, maxEdits = MAX_EDITS) {
  const a = splitLines(before);
  const b = splitLines(after);

  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }

  const ops = myers(a.slice(start, endA), b.slice(start, endB), maxEdits);
  if (!ops) return { rows: [], added: 0, removed: 0, tooDifferent: true };

  const rows = [];
  for (let i = 0; i < start; i++) rows.push({ type: 'same', a: i + 1, b: i + 1, text: a[i] });
  let added = 0;
  let removed = 0;
  for (const [op, x, y] of ops) {
    if (op === '=') rows.push({ type: 'same', a: start + x + 1, b: start + y + 1, text: a[start + x] });
    else if (op === '-') {
      removed++;
      rows.push({ type: 'del', a: start + x + 1, b: null, text: a[start + x] });
    } else {
      added++;
      rows.push({ type: 'add', a: null, b: start + y + 1, text: b[start + y] });
    }
  }
  for (let i = 0; endA + i < a.length; i++) {
    rows.push({ type: 'same', a: endA + i + 1, b: endB + i + 1, text: a[endA + i] });
  }
  return { rows, added, removed, tooDifferent: false };
}

/**
 * The rows worth showing: every change with CONTEXT lines either side, and in
 * place of each run left out, one row saying how many lines it holds.
 */
export function hunks(rows, context = CONTEXT) {
  const list = Array.isArray(rows) ? rows : [];
  const keep = new Array(list.length).fill(false);
  list.forEach((r, i) => {
    if (r.type === 'same') return;
    for (let j = Math.max(0, i - context); j <= Math.min(list.length - 1, i + context); j++) keep[j] = true;
  });
  const out = [];
  let skipped = 0;
  list.forEach((r, i) => {
    if (keep[i]) {
      if (skipped) out.push({ type: 'skip', count: skipped });
      skipped = 0;
      out.push(r);
    } else {
      skipped++;
    }
  });
  if (skipped && out.length) out.push({ type: 'skip', count: skipped });
  return out;
}

function splitLines(text) {
  const s = String(text ?? '').replace(/\r\n?/g, '\n');
  if (s === '') return [];
  const lines = s.split('\n');
  // A final newline ends the last line; it does not start another.
  if (lines.at(-1) === '') lines.pop();
  return lines;
}

// [op, x, y]: '=' a[x] is b[y], '-' a[x] is removed, '+' b[y] is added.
function myers(a, b, maxEdits) {
  const n = a.length;
  const m = b.length;
  const max = n + m;
  if (max === 0) return [];
  const off = max + 1;
  const v = new Int32Array(2 * max + 3);
  // v as it stood at the start of each round, k from -d-1 to d+1.
  const trace = [];
  for (let d = 0; d <= max; d++) {
    if (d > maxEdits) return null;
    trace.push(v.slice(off - d - 1, off + d + 2));
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[off + k] = x;
      if (x >= n && y >= m) return backtrack(trace, n, m, d);
    }
  }
  return null;
}

function backtrack(trace, n, m, last) {
  const ops = [];
  let x = n;
  let y = m;
  for (let d = last; d > 0; d--) {
    const snap = trace[d];
    const at = (k) => snap[k + d + 1];
    const k = x - y;
    const prevK = k === -d || (k !== d && at(k - 1) < at(k + 1)) ? k + 1 : k - 1;
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push(['=', x - 1, y - 1]);
      x--;
      y--;
    }
    if (x === prevX) ops.push(['+', x, y - 1]);
    else ops.push(['-', x - 1, y]);
    x = prevX;
    y = prevY;
  }
  while (x > 0 && y > 0) {
    ops.push(['=', x - 1, y - 1]);
    x--;
    y--;
  }
  return ops.reverse();
}
