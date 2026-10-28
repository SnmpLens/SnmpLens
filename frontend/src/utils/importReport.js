// What a MIB import did, organised by MODULE.
//
// ImportMibFiles answers one result per file it found, and an archive dropped
// whole can be sixty files: several releases of one module, files for another
// tool, a PDF, a Thumbs.db. Listed file by file, with a sentence each, the one
// module that does not load was somewhere on the third screen. The question in
// front of the person who dropped the folder is the other one: which MODULES
// do I have now, are they right, and what was left out and why.
//
// So the report is one row per module — the version kept, whether it loads,
// the versions set aside under it — then what could not be imported, then the
// files that are not MIBs at all, grouped by what they are rather than listed.
//
// Each module's state is the verdict every other screen shows, decided in Go
// (pkg/mib/verdict.go, MibFileStatuses); the load result adds what only it
// has, the warnings and the diagnosis of a failure.
//
// Pure, like mibDependencies.js: it reads what the bridge already returned and
// calls nothing.

import { byVerdict, FAILED, ERROR, WARNING, OK } from './mibVerdict.js';

/** Outcomes ImportMibFiles reports (internal/app/mibimport.go). */
export const IMPORTED = 'imported';
export const REPLACED = 'replaced';
export const PRESENT = 'present';
export const SET_ASIDE = 'setaside';
export const IGNORED = 'ignored';
export const ERROR_OUTCOME = 'error';

/** A version set aside because the person chose another one (chooseVersion). */
export const CHOSEN = 'chosen';

/** A module row's state: the verdict's levels. */
export const LOAD_OK = OK;
export const LOAD_WARN = WARNING;
export const LOAD_ERROR = ERROR;
export const LOAD_FAIL = FAILED;

/**
 * @param {Array} results MibImportResult[] as ImportMibFiles returns them
 * @param {Array} diagnostics MibLoadResult[] from the reload that followed
 * @param {Array} [statuses] MibFileStatus[]: every file's verdict, from Go
 */
export function buildImportReport(results, diagnostics, statuses) {
  const list = (Array.isArray(results) ? results : []).filter((r) => r && typeof r === 'object');
  const index = indexes(diagnostics, statuses);

  const modules = new Map();
  const errors = [];
  const ignored = new Map();

  const rowFor = (key, module) => {
    let row = modules.get(key);
    if (!row) {
      row = {
        module, storedAs: '', source: '', path: '', outcome: '', date: '', replaced: '',
        setAside: [], load: null,
      };
      modules.set(key, row);
    }
    return row;
  };

  for (const r of list) {
    const outcome = outcomeOf(r);
    if (outcome === IGNORED) {
      const kind = r.reason || 'binary';
      if (!ignored.has(kind)) ignored.set(kind, []);
      ignored.get(kind).push(r.fileName);
      continue;
    }
    if (outcome === ERROR_OUTCOME) {
      errors.push({ fileName: r.fileName, module: r.module || '', reason: r.reason || '', error: r.error || '' });
      continue;
    }
    const key = r.module || r.storedAs || r.fileName;
    const row = rowFor(key, r.module || r.storedAs || r.fileName);
    if (outcome === SET_ASIDE) {
      row.setAside.push({ source: r.fileName, path: r.source || '', reason: r.reason || 'undecided', date: r.date || '' });
      continue;
    }
    row.storedAs = r.storedAs || '';
    row.source = r.fileName;
    row.path = r.source || '';
    row.outcome = outcome;
    row.date = r.date || '';
    row.replaced = r.replaced || '';
  }

  const rows = [];
  for (const row of modules.values()) {
    // Versions set aside for a module whose kept version then failed to be
    // written: the failure is in the errors, and the row says so.
    if (!row.outcome) row.outcome = ERROR_OUTCOME;
    row.load = loadOf(row.storedAs, index);
    rows.push(row);
  }

  const ignoredGroups = [...ignored.entries()]
    .map(([kind, files]) => ({ kind, files }))
    .sort((a, b) => b.files.length - a.files.length || a.kind.localeCompare(b.kind));

  return finish({ modules: rows, errors, ignored: ignoredGroups });
}

/**
 * The report once a version set aside has been imported in place of the one
 * kept: that version is the row, and the one it replaced is set aside under it
 * as the person's choice.
 *
 * @param {object} report buildImportReport's answer
 * @param {string} module the row
 * @param {object} chosen the set-aside entry imported
 * @param {object} result ImportMibFiles's result for it
 * @param {Array} diagnostics MibLoadResult[] from the reload that followed
 * @param {Array} statuses MibFileStatus[] after it
 */
export function chooseVersion(report, module, chosen, result, diagnostics, statuses) {
  if (!report || !chosen || !result) return report;
  const index = indexes(diagnostics, statuses);
  const modules = report.modules.map((row) => {
    if (row.module !== module) return row;
    const setAside = row.setAside.filter((v) => v.path !== chosen.path);
    if (row.source) setAside.unshift({ source: row.source, path: row.path, reason: CHOSEN, date: row.date });
    const storedAs = result.storedAs || row.storedAs;
    return {
      ...row,
      storedAs,
      source: chosen.source,
      path: chosen.path,
      outcome: result.outcome || REPLACED,
      date: result.date || chosen.date || '',
      replaced: result.replaced || row.replaced,
      setAside,
      load: loadOf(storedAs, index),
    };
  });
  return finish({ ...report, modules });
}

function indexes(diagnostics, statuses) {
  const loads = new Map();
  for (const d of Array.isArray(diagnostics) ? diagnostics : []) {
    if (d && d.fileName) loads.set(d.fileName, d);
  }
  const verdicts = new Map();
  for (const s of Array.isArray(statuses) ? statuses : []) {
    if (s && s.name && s.verdict) verdicts.set(s.name, s.verdict);
  }
  return { loads, verdicts };
}

// What the row says about loading: the verdict Go gave the stored file, and
// the warnings and diagnosis only the load result carries. Nothing when the
// file was not loaded, or the load has not answered for it.
function loadOf(storedAs, { loads, verdicts }) {
  const d = loads.get(storedAs);
  if (!d) return null;
  const v = verdicts.get(storedAs) || (d.success ? { level: OK, count: 0 } : { level: FAILED, count: 1, detail: d.error });
  return {
    status: v.level,
    count: v.count || 0,
    detail: v.detail || '',
    warnings: Array.isArray(d.warnings) ? d.warnings : [],
    error: d.error || '',
    diagnosis: d.diagnosis || null,
  };
}

function finish(report) {
  const rows = [...report.modules].sort((a, b) => rank(a) - rank(b) || String(a.module).localeCompare(String(b.module)));
  const counts = {
    modules: rows.filter((r) => r.outcome !== ERROR_OUTCOME).length,
    warned: rows.filter((r) => r.load?.status === LOAD_WARN).length,
    errored: rows.filter((r) => r.load?.status === LOAD_ERROR).length,
    failed: rows.filter((r) => r.load?.status === LOAD_FAIL).length,
    setAside: rows.reduce((n, r) => n + r.setAside.length, 0),
    ignored: report.ignored.reduce((n, g) => n + g.files.length, 0),
    errors: report.errors.length,
  };
  return { ...report, modules: rows, counts };
}

/**
 * Whether the report is worth opening: a clean import of modules that all
 * load says everything it has to say in a notification.
 */
export function reportNeedsAttention(report) {
  if (!report) return false;
  const c = report.counts;
  return c.errors > 0 || c.failed > 0 || c.errored > 0 || c.warned > 0 || c.setAside > 0 || c.ignored > 0
    || report.modules.some((r) => r.outcome === REPLACED);
}

/**
 * Whether a version set aside can be imported instead: one the import read, by
 * a path it gave back, and not a copy of the version kept.
 */
export function canChoose(version) {
  return !!version && !!version.path && version.reason !== 'identical';
}

/**
 * The end of a path that tells it apart: the file and the folder it is in,
 * after an ellipsis when there is more. The whole path goes in a tooltip.
 */
export function shortPath(path, keep = 2) {
  const parts = String(path || '').split(/[/\\]/).filter(Boolean);
  if (parts.length <= keep) return parts.join('/');
  return '…/' + parts.slice(-keep).join('/');
}

function outcomeOf(r) {
  if (r.outcome) return r.outcome;
  // An older bridge, or a result built by hand: read the booleans.
  if (!r.success) return ERROR_OUTCOME;
  return r.skipped ? PRESENT : IMPORTED;
}

function rank(row) {
  if (row.outcome === 'error') return -1;
  return row.load ? byVerdict({ level: row.load.status }, { level: OK }) : 4;
}
