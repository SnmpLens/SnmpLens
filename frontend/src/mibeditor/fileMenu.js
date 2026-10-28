// What the MIB editor's file list offers on a right-click, for what is
// selected (internal/app/mibfiles.go does the work).
//
// Decided here, from the facts the list already holds, rather than in the
// panel's markup: which entries exist for one file and for several, and which
// are refused and why, is the part worth testing — tests/filemenu.test.mjs —
// and the part that would rot unseen in a template.
//
// Entries are { action, key, values?, icon?, hint?, disabled?, reason? }:
// `key` is under mibEditor.fileMenu in the locales, as is `reason`. '---'
// separates groups; no group starts or ends the menu, or follows another.

const SEP = { action: '---' };

/**
 * @param {object} ctx
 * @param {string[]} ctx.names the files selected, in list order
 * @param {Array} ctx.files MibEditorList's answer
 * @param {(name: string) => boolean} ctx.isEnabled whether a file is loaded at start
 * @param {Object<string, {level: string, loaded: boolean}>} [ctx.verdicts] verdictMap's answer
 */
export function fileMenu({ names, files, isEnabled, verdicts = {} }) {
  const byName = new Map((Array.isArray(files) ? files : []).map((f) => [f.name, f]));
  const chosen = (Array.isArray(names) ? names : []).map((n) => byName.get(n)).filter(Boolean);
  if (chosen.length === 0) return [];
  return group(chosen.length === 1 ? single(chosen[0], files, isEnabled, verdicts) : several(chosen, isEnabled));
}

/** What a right-click on the list's empty space offers. */
export function listMenu() {
  return [
    { action: 'restoreDeleted', key: 'restoreDeleted', icon: 'history' },
    { action: 'openFolder', key: 'openFolder', icon: 'folder-open' },
    { action: 'refresh', key: 'refresh', icon: 'refresh-cw' },
  ];
}

function single(f, files, isEnabled, verdicts) {
  const bundled = !!f.bundled;
  const enabled = isEnabled(f.name);
  const verdict = verdicts[f.name];
  // Other files declaring the same module: versions of it, one of which loads.
  const versions = f.module
    ? (files || []).filter((o) => o.name !== f.name && o.module && o.module.toUpperCase() === f.module.toUpperCase())
    : [];
  const inTree = !!f.module && enabled && !!verdict && verdict.loaded && verdict.level !== 'failed';
  const keepsName = bundled ? 'bundledKeepsName' : '';

  return [
    [
      { action: 'copyName', key: 'copyName', icon: 'copy' },
      { action: 'copyPath', key: 'copyPath', icon: 'route' },
      f.module && f.module !== f.name ? { action: 'copyModule', key: 'copyModule', values: { module: f.module }, icon: 'tag' } : null,
      { action: 'reveal', key: 'reveal', icon: 'folder-open' },
    ],
    [
      { action: 'rename', key: 'rename', icon: 'pencil', hint: 'F2', disabled: bundled, reason: keepsName },
      f.module && f.module !== f.name && !bundled
        ? { action: 'renameToModule', key: 'renameToModule', values: { module: f.module }, icon: 'tag' }
        : null,
      { action: 'duplicate', key: 'duplicate', icon: 'copy' },
      { action: 'delete', key: 'delete', icon: 'x', hint: 'Del', disabled: bundled, reason: bundled ? 'bundledStays' : '' },
    ],
    [
      bundled && f.modified ? { action: 'compareBundled', key: 'compareBundled', icon: 'layers' } : null,
      bundled && f.modified ? { action: 'restoreBundled', key: 'restoreBundled', icon: 'history' } : null,
      { action: 'backups', key: 'backups', icon: 'history' },
    ],
    [
      { action: enabled ? 'disable' : 'enable', key: enabled ? 'disable' : 'enable', icon: enabled ? 'eye-off' : 'eye' },
      versions.length > 0 ? { action: 'useThisVersion', key: 'useThisVersion', values: { count: versions.length }, icon: 'check' } : null,
      { action: 'diagnose', key: 'diagnose', icon: 'bug' },
      f.module ? { action: 'showInTree', key: 'showInTree', icon: 'search', disabled: !inTree, reason: inTree ? '' : 'notInTree' } : null,
    ],
    [
      { action: 'imports', key: 'imports', icon: 'route' },
      { action: 'importers', key: 'importers', icon: 'layers' },
      { action: 'export', key: 'export', icon: 'download' },
    ],
    [
      { action: 'copyProblems', key: 'copyProblems', icon: 'clipboard-list' },
      { action: 'openAndFix', key: 'openAndFix', icon: 'zap' },
    ],
  ];
}

function several(chosen, isEnabled) {
  const count = chosen.length;
  const deletable = chosen.filter((f) => !f.bundled).length;
  const anyOff = chosen.some((f) => !isEnabled(f.name));
  const anyOn = chosen.some((f) => isEnabled(f.name));
  return [
    [{ action: 'copyName', key: 'copyNames', values: { count }, icon: 'copy' }],
    [
      anyOff ? { action: 'enable', key: 'enableAll', values: { count }, icon: 'eye' } : null,
      anyOn ? { action: 'disable', key: 'disableAll', values: { count }, icon: 'eye-off' } : null,
    ],
    [
      { action: 'export', key: 'exportAll', values: { count }, icon: 'download' },
      {
        action: 'delete', key: 'deleteAll', values: { count: deletable }, icon: 'x', hint: 'Del',
        disabled: deletable === 0, reason: deletable === 0 ? 'bundledStays' : '',
      },
    ],
  ];
}

function group(groups) {
  const out = [];
  for (const g of groups) {
    const items = g.filter(Boolean);
    if (items.length === 0) continue;
    if (out.length) out.push(SEP);
    out.push(...items);
  }
  return out;
}

/**
 * A new selection after a click on `name`, the way a file list does it: a
 * plain click selects it alone, Ctrl (Cmd) adds or removes it, Shift selects
 * the range from the anchor through it, in the order shown.
 *
 * @returns {{selected: string[], anchor: string}}
 */
export function select(selected, anchor, name, shown, { toggle = false, range = false } = {}) {
  const current = Array.isArray(selected) ? selected : [];
  if (range && anchor && shown.includes(anchor) && shown.includes(name)) {
    const [i, j] = [shown.indexOf(anchor), shown.indexOf(name)].sort((x, y) => x - y);
    return { selected: shown.slice(i, j + 1), anchor };
  }
  if (toggle) {
    const next = current.includes(name) ? current.filter((n) => n !== name) : [...current, name];
    return { selected: shown.filter((n) => next.includes(n)), anchor: name };
  }
  return { selected: [name], anchor: name };
}

/**
 * The name a file is renamed to after the module it declares, keeping an
 * extension gosmi finds a module by (pkg/mib/mibfs.go): "vendor.txt" declaring
 * EXAMPLE-MIB becomes "EXAMPLE-MIB.txt", "vendor.asn1" becomes "EXAMPLE-MIB".
 */
export function nameForModule(module, current) {
  const ext = /\.(mib|my|mi2|txt)$/i.exec(current || '');
  return module + (ext ? ext[0] : '');
}

/**
 * The name offered for a copy: "IF-MIB copy", "vendor copy.txt", then a number
 * once that is taken.
 */
export function copyName(name, taken) {
  const dot = name.lastIndexOf('.');
  const hasExt = dot > 0 && name.length - dot <= 5;
  const stem = hasExt ? name.slice(0, dot) : name;
  const ext = hasExt ? name.slice(dot) : '';
  const used = new Set(taken || []);
  for (let i = 1; ; i++) {
    const candidate = `${stem} copy${i > 1 ? ' ' + i : ''}${ext}`;
    if (!used.has(candidate)) return candidate;
  }
}
