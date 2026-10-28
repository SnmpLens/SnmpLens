// Turn a directory's load results into the question people actually ask.
//
// The per-file list answers "did this file load". The question in front of
// someone with a folder of vendor MIBs is the other one: WHICH MODULES ARE
// MISSING, and who needs them. Those are the same facts, rolled the other way
// — one row per absent module instead of one row per broken file — and the
// difference matters because a single missing `VENDOR-SMI` fails twelve files
// and reads as twelve unrelated problems.
//
// This is pure, and deliberately so. It rolls up what LoadMibsWithDiagnostics
// ALREADY returns: nothing here calls the bridge, and in particular nothing
// runs Diagnose per file. Diagnose re-reads and re-parses, and it takes
// pkg/mib's package-level EXCLUSIVE gosmi mutex — a sweep of a 400-file
// directory to draw a panel would stop every OID translation in every other
// tab while it ran.
//
// What that costs, stated rather than hidden: the Go side diagnoses FAILURES
// only, so the imports of a file that loaded fine are not in this data. This
// answers "what is missing", not "what depends on what". The second question
// needs a different source and is not worth an exclusive lock to draw.

/**
 * Why an imported module is unavailable. Mirrors pkg/mib's constants — the Go
 * side is the authority, and a value it adds later shows up here as itself
 * rather than being silently folded into another bucket.
 */
export const ABSENT = 'absent';
export const FAILED = 'failed';
export const NOT_LOADED = 'notloaded';

/**
 * @param {Array} diagnostics MibLoadResult[] as LoadMibsWithDiagnostics returns
 * @returns {{missing: Array, loaded: number, failed: number}}
 */
export function dependencyRoll(diagnostics) {
  const list = Array.isArray(diagnostics) ? diagnostics : [];

  // module -> { module, reason, cause, neededBy: [{ file, symbols }] }
  const byModule = new Map();
  let loaded = 0;
  let failed = 0;

  for (const d of list) {
    if (!d || typeof d !== 'object') continue;
    if (d.success) loaded++;
    else failed++;

    // A file whose name is not its module is no longer a row here: gosmi is
    // given every module under the name it DECLARES (pkg/mib/mibfs.go), so the
    // mismatch this roll used to report no longer hides anything.

    const missing = (d.diagnosis && d.diagnosis.missing) || [];
    for (const m of missing) {
      if (!m || !m.module) continue;
      let entry = byModule.get(m.module);
      if (!entry) {
        entry = { module: m.module, reason: m.reason || ABSENT, cause: m.cause || '', neededBy: [] };
        byModule.set(m.module, entry);
      }
      // The worst reason wins: the same module can be reported absent by one
      // file and merely not-loaded by another, and "absent" is the one that
      // needs a download rather than a checkbox.
      if (severity(m.reason) > severity(entry.reason)) entry.reason = m.reason;
      if (!entry.cause && m.cause) entry.cause = m.cause;
      entry.neededBy.push({ file: d.fileName, symbols: m.symbols || [] });
    }
  }

  // Most-wanted first: the module blocking twelve files is the one to fix, and
  // it is not necessarily the first alphabetically or the first to fail.
  const missing = [...byModule.values()].sort(
    (a, b) => b.neededBy.length - a.neededBy.length || a.module.localeCompare(b.module)
  );

  return { missing, loaded, failed };
}

/**
 * Every distinct symbol a module was needed for, deduplicated across the files
 * that asked. Twelve files importing `DisplayString` from the same place is one
 * fact, not twelve.
 */
export function symbolsWanted(entry) {
  const seen = new Set();
  for (const n of entry.neededBy || []) {
    for (const s of n.symbols || []) seen.add(s);
  }
  return [...seen].sort();
}

function severity(reason) {
  if (reason === ABSENT) return 3;
  if (reason === FAILED) return 2;
  if (reason === NOT_LOADED) return 1;
  return 0;
}
