<script>
  import { onMount, onDestroy, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from './Icon.svelte';
  import { renderWindow } from './mibeditor/render';
  import { notificationStore } from './stores/notifications';
  import { highlight, SNIPPETS, stringStateAt } from './mibeditor/tokenize.js';
  import { charWidth, positionOf, lineColumnAt, wordAt, offsetAt, lineStarts, lineColumnFrom } from './mibeditor/metrics.js';
  import { findMatches, findAllMatches, applyReplaceAll, nextIndex, MAX_MATCHES } from './mibeditor/search.js';
  import { createHistory } from './mibeditor/history.js';
  import {
    sortProblems, filterProblems, countBySeverity, positionOf as problemPosition, SEVERITIES,
    withoutMuted, MUTABLE, FIXABLE,
  } from './mibeditor/problems';
  import { mutedRules } from './stores/mutedRules';
  import { copyToClipboard } from './utils/clipboard';
  import { verdictMap, countVerdicts, isProblem, ICONS as VERDICT_ICONS, FAILED, ERROR, WARNING } from './utils/mibVerdict';
  import { mibStatusStore } from './stores/mibStatusStore';
  import { mibEditorStore } from './stores/mibEditorStore';
  import { mibPathsStore } from './stores/mibPathsStore';
  import { mibStore } from './stores/mibStore';
  import {
    MibEditorList,
    MibEditorOpenExternal,
    MibEditorSave,
    MibEditorRestoreBundled,
    MibEditorReload,
    MibEditorSymbols,
    MibEditorFixImports,
    MibEditorRepair,
    MibEditorQuickFix,
  } from '../wailsjs/go/app/App';

  let files = [];
  let filter = '';
  let saving = false;
  let atomicEdit = false;
  let showSnippets = false;
  let showSymbols = false;
  let outlineFilter = '';
  let showOutline = true;
  let symbolFilter = '';
  let catalogue = { modules: [], symbols: [] };
  let textarea;
  let mirror;
  let gutter;
  let surface;

  // Overlay geometry. The mirror lays out identically to the text — that is
  // unit-tested — and both are monospace with a fixed line height, so a
  // position is arithmetic once the character width is measured.
  const LINE_HEIGHT = 18;
  let cw = 7.2;
  let scrollTop = 0;
  let scrollLeft = 0;

  let hover = null;        // { x, y, symbol }
  let completion = null;   // { x, y, items, index, range }
  // The app ships on macOS too, where redo is Cmd+Shift+Z rather than Ctrl+Y.
  // A tooltip naming the wrong key teaches the wrong thing.
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');
  const UNDO_KEYS = isMac ? '⌘Z' : 'Ctrl+Z';
  const REDO_KEYS = isMac ? '⇧⌘Z' : 'Ctrl+Y';

  // Our own history, because the browser's steps back one character at a time
  // and loses the caret. Ctrl+Z is intercepted below so there is exactly one.
  let hist = createHistory('');
  let prevText = '';
  let prevSel = { start: 0, end: 0 };
  let canUndo = false;
  let canRedo = false;

  function syncHistoryFlags() {
    canUndo = hist.canUndo;
    canRedo = hist.canRedo;
  }

  function selectionNow() {
    return textarea
      ? { start: textarea.selectionStart, end: textarea.selectionEnd }
      : { start: 0, end: 0 };
  }
  let findOpen = false;
  let showReplace = false;
  let findTerm = '';
  let replaceTerm = '';
  let findIndex = 0;
  // Case sensitivity. Off by default, which is what someone hunting for a name
  // wants — but `Counter32` and `counter32` are different things in a MIB, and
  // replacing one used to rewrite the other with no way to say otherwise.
  let matchCase = false;
  // Where to resume after a replace, so the Replace button advances instead of
  // rewriting the same spot when the replacement contains the search term.
  let resumeFrom = -1;
  // Set when the term changes: the first match must be scrolled to, and it can
  // only be done once `matches` has been recomputed.
  let pendingFirst = false;
  let findCount = 0;

  $: lines = buffer.split('\n');
  // Only errors and warnings get an underline; advice would be visual noise.
  //
  // Placed by TEXT, not by arithmetic. Each underline is its line's own text
  // up to the column, transparent, then the characters it underlines, in the
  // mirror's font and tab size: it lands where the mirror drew them whatever
  // the font resolved to, however wide a tab is, whichever characters sit
  // before it. Pixels from a measured character width put it beside the word
  // as soon as the measure was wrong — and it was: taken once, at mount,
  // before any file was open, it stayed at its 7.2 px default against
  // Consolas's 6.6, drifting further right with every column.
  $: marks = listed
    .filter((d) => d.line > 0 && d.severity !== 'info')
    .slice(0, 200)
    .map((d) => {
      const text = lines[d.line - 1] || '';
      const at = Math.min(Math.max(0, (d.column || 1) - 1), text.length);
      const chars = Math.max(2, (d.symbol || '').length || wordAt(text, at).word.length || 6);
      const under = text.slice(at, at + chars);
      return {
        ...d,
        y: (d.line - 1) * LINE_HEIGHT,
        before: text.slice(0, at),
        // Past the end of the line, something to underline all the same.
        under: under + 'x'.repeat(Math.max(0, chars - under.length)),
      };
    });

  // The buffer lives in a store, not here: this component is destroyed on
  // every tab switch, and component state would take the user's edits with it.
  $: source = $mibEditorStore.source;
  $: buffer = $mibEditorStore.buffer;
  $: diagnostics = $mibEditorStore.diagnostics;
  $: missingImports = $mibEditorStore.missingImports;
  $: checking = $mibEditorStore.checking;
  $: dirty = source !== null && buffer !== source.content;
  // An externally opened MIB is savable even untouched: saving it COPIES it
  // into the MIB directory, which is the documented way to import one. Gating
  // the button on `dirty` meant you had to edit a file to be allowed to import
  // it, so the path was dead.
  $: savable = source !== null && (dirty || source.external);
  $: matchingSymbols = symbolFilter.length < 2
    ? []
    : catalogue.symbols
        .filter((sy) => sy.name.toLowerCase().includes(symbolFilter.toLowerCase()))
        .slice(0, 60);
  // Derived from `lines`, not a second split. Splitting a 185 KB buffer twice
  // per keystroke allocated two 5,000-element arrays for one answer.
  $: lineCount = lines.length;

  // The gutter as ONE text node instead of 5,000 DOM nodes. An {#each} over
  // the line count made Svelte create, diff and keep a node per line of
  // IP-MIB — by far the most expensive thing on screen, and invisible in a
  // profile of the analysis.
  $: gutterText = buildGutter(lineCount);
  let gutterCache = { n: 0, text: '' };
  function buildGutter(n) {
    if (n === gutterCache.n) return gutterCache.text;
    let text;
    // Typing adds a line at a time, so append rather than rebuild.
    if (n > gutterCache.n && gutterCache.n > 0 && n - gutterCache.n < 32) {
      const extra = [];
      for (let i = gutterCache.n + 1; i <= n; i++) extra.push(i);
      text = gutterCache.text + extra.join('\n') + '\n';
    } else {
      const parts = new Array(n);
      for (let i = 0; i < n; i++) parts[i] = i + 1;
      text = parts.join('\n') + '\n';
    }
    gutterCache = { n, text };
    return text;
  }
  // Only the visible lines are coloured.
  //
  // Tokenising the whole buffer cost about 45 ms on a 185 KB MIB, on every
  // pause in typing, to produce markup for four thousand lines nobody was
  // looking at. A real IDE does not do that either: it colours the viewport
  // and parses incrementally. We cannot parse incrementally — participle
  // offers no way in — but the viewport half is free.
  //
  // Highlighting carries `inString` across lines, so starting mid-file needs
  // the state at that point. stringStateAt answers it by scanning without
  // producing anything: 4 ms where tokenising is 29.
  // The width of one character, for what is still placed by arithmetic: search
  // hits, the completion popup, the hover card. Measured whenever the mirror
  // APPEARS — it exists only while a file is open, and measuring once at
  // mount, usually before any file was, left the 7.2 px default in place.
  $: if (mirror) cw = charWidth(mirror);

  // The padding of the mirror and the textarea (.mirror, .surface textarea):
  // where the first character of the first line is drawn.
  const TEXT_PAD = 8;
  const VIEW_MARGIN = 40; // lines rendered beyond the viewport, so small scrolls do not re-render
  let viewportHeight = 600;

  $: firstVisible = Math.max(0, Math.floor(scrollTop / LINE_HEIGHT) - VIEW_MARGIN);
  $: lastVisible = Math.min(
    lineCount,
    Math.ceil((scrollTop + viewportHeight) / LINE_HEIGHT) + VIEW_MARGIN,
  );
  $: highlighted = renderWindow(buffer, lines, firstVisible, lastVisible);

  // A mark beside every file that does not load, loads with errors or with
  // warnings — the one verdict the MIB settings and the import report show
  // too, decided in Go (pkg/mib/verdict.go) — and a switch to list only those:
  // in a directory of two hundred MIBs the broken ones were found by opening
  // them one by one. The open file, while it has unsaved changes, is judged
  // as it is being typed.
  let onlyProblems = false;
  $: openName = source && !source.external ? source.name : '';
  $: statuses = verdictMap($mibStatusStore, openName, dirty ? $mibEditorStore.verdict : null);
  $: problemCounts = countVerdicts(statuses);
  $: shown = files.filter((f) => (!filter || f.name.toLowerCase().includes(filter.toLowerCase()))
    && (!onlyProblems || isProblem(statuses[f.name]?.level)));
  $: errorCount = listed.filter((d) => d.severity === 'error').length;
  $: warnCount = listed.filter((d) => d.severity === 'warning').length;

  // What is shown of the diagnostics once the rules this viewer muted are taken
  // out (stores/mutedRules.js): the table, the underlines and the counts read
  // this, and Go leaves the same findings out of the verdict.
  $: listed = withoutMuted(diagnostics, $mutedRules);

  // The problem table (mibeditor/problems.js): by severity unless a column is
  // chosen, and filtered by severity, so the one error that stops a module
  // loading is not under a dozen style infos.
  const PROBLEM_COLUMNS = ['severity', 'line', 'message', 'code'];
  let sortKey = 'severity';
  let sortDir = 'asc';
  let visibleSeverities = new Set(SEVERITIES);
  $: severityCounts = countBySeverity(listed);
  $: shownProblems = sortProblems(filterProblems(listed, visibleSeverities), sortKey, sortDir);
  // A fix for every problem of a kind, offered once there are several.
  $: fixAllCodes = [...FIXABLE]
    .map((code) => ({ code, count: listed.filter((d) => d.code === code).length }))
    .filter((f) => f.count > 1);

  function sortBy(key) {
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = key;
      sortDir = 'asc';
    }
  }

  function toggleSeverity(sev) {
    const next = new Set(visibleSeverities);
    if (next.has(sev)) next.delete(sev);
    else next.add(sev);
    visibleSeverities = next;
  }

  // The problem table's height, dragged from its top edge: a vendor MIB can
  // have forty problems, and a fixed 150 px showed six. The editor gives up
  // what the table takes. Remembered per viewer; a store that refuses it only
  // costs the default next time.
  const PROBLEMS_KEY = 'snmplens.mibEditor.problemsHeight';
  const PROBLEMS_MIN = 72;
  const PROBLEMS_DEFAULT = 180;
  let mainEl;
  let problemsHeight = storedProblemsHeight();
  let resizing = null;

  function storedProblemsHeight() {
    try {
      const v = Number(localStorage.getItem(PROBLEMS_KEY));
      return v >= PROBLEMS_MIN ? v : PROBLEMS_DEFAULT;
    } catch (e) {
      return PROBLEMS_DEFAULT;
    }
  }

  function keepProblemsHeight() {
    try {
      localStorage.setItem(PROBLEMS_KEY, String(Math.round(problemsHeight)));
    } catch (e) {
      /* remembered for this session only */
    }
  }

  // At least the minimum, and never so tall that the editor has no room left.
  function clampProblemsHeight(h) {
    const max = Math.max(PROBLEMS_MIN, (mainEl?.clientHeight || 600) - 180);
    return Math.min(max, Math.max(PROBLEMS_MIN, h));
  }

  function startResize(e) {
    resizing = { y: e.clientY, h: problemsHeight };
    e.currentTarget.setPointerCapture(e.pointerId);
  }

  function onResize(e) {
    if (!resizing) return;
    problemsHeight = clampProblemsHeight(resizing.h + (resizing.y - e.clientY));
  }

  async function endResize() {
    if (!resizing) return;
    resizing = null;
    keepProblemsHeight();
    await tick();
    measureViewport();
  }

  async function resizeByKey(e) {
    const step = e.shiftKey ? 80 : 20;
    if (e.key === 'ArrowUp') problemsHeight = clampProblemsHeight(problemsHeight + step);
    else if (e.key === 'ArrowDown') problemsHeight = clampProblemsHeight(problemsHeight - step);
    else return;
    e.preventDefault();
    keepProblemsHeight();
    await tick();
    measureViewport();
  }

  // Every problem SHOWN — filtered and sorted as on screen — as text, one per
  // line, to paste into a ticket or a message to whoever wrote the MIB.
  function copyProblems() {
    const t = get(_);
    const label = t('mibEditor.problemTable.copiedLabel', { values: { count: shownProblems.length } });
    const rows = shownProblems.map((d) =>
      `${t('mibEditor.problemTable.sev.' + d.severity).padEnd(14)} ${problemPosition(d).padEnd(9)} ${d.message} [${d.code}]`);
    copyToClipboard([`${source?.name || ''} — ${label}`, ...rows].join('\n'), label);
  }

  // The fix Go writes for a problem (pkg/mib/quickfix.go), into the buffer as
  // one edit, so it is undone as one. symbols empty fixes every one of a kind.
  async function quickFix(code, symbols) {
    try {
      const fix = await MibEditorQuickFix(buffer, code, symbols);
      if (!fix || !fix.content) return;
      insertAtCaret(fix.content, [0, buffer.length]);
      notificationStore.add(
        get(_)('mibEditor.problemTable.fixed', { values: { count: (fix.fixed || []).length } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // Muting changes the verdict of every file; the list asks again by itself
  // (stores/mibStatusStore.js), the open buffer here.
  async function mute(code) {
    await mutedRules.mute(code);
    mibEditorStore.refresh();
  }

  async function unmute(code) {
    await mutedRules.unmute(code);
    mibEditorStore.refresh();
  }

  // The visible window depends on how much is on screen, so it is re-read on
  // resize.
  function measureViewport() {
    if (textarea) viewportHeight = textarea.clientHeight || viewportHeight;
  }

  // onDestroy has to be registered while the component is initialising — the
  // component body, not inside onMount. Called from there it throws, which
  // meant the listener was never removed; the panel is unmounted on every tab
  // switch, so they accumulated on window one per visit.
  onDestroy(() => window.removeEventListener('resize', measureViewport));

  onMount(async () => {
    // The buffer survives a tab switch in the store, deliberately — but the
    // component does not, and it was re-initialising the history against an
    // EMPTY document. The first keystroke then recorded ''-to-whole-file as
    // one entry, and one Ctrl+Z emptied the file.
    resetHistory();
    measureViewport();
    window.addEventListener('resize', measureViewport);
    await refreshList();
    try {
      catalogue = (await MibEditorSymbols()) || { modules: [], symbols: [] };
    } catch (e) {
      catalogue = { modules: [], symbols: [] };
    }
  });

  async function refreshList() {
    try {
      files = (await MibEditorList()) || [];
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
    // The marks fill in after the list: the first analysis of a large vendor
    // directory takes a moment, and the names should not wait for it.
    mibStatusStore.refresh();
  }

  async function open(name) {
    if (!(await confirmDiscard())) return;
    try {
      const { recovered } = await mibEditorStore.open(name);
      resetHistory();
      if (recovered) {
        notificationStore.add(get(_)('mibEditor.draftRecovered', { values: { name } }), 'warning');
      }
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function resetHistory() {
    prevText = get(mibEditorStore).buffer;
    prevSel = { start: 0, end: 0 };
    hist = createHistory(prevText);
    syncHistoryFlags();
  }

  async function openExternal() {
    if (!(await confirmDiscard())) return;
    try {
      const s = await MibEditorOpenExternal();
      if (!s || !s.name) return; // cancelled
      mibEditorStore.openSource(s);
      resetHistory();
      notificationStore.add(get(_)('mibEditor.externalOpened', { values: { name: s.name } }), 'info');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // Losing an edit to a misclick is the one failure this editor must not have.
  async function confirmDiscard() {
    if (!dirty) return true;
    if (!window.confirm(get(_)('mibEditor.discardConfirm'))) return false;
    // Throwing the changes away has to reach the draft on disk too, or
    // reopening the file recovers exactly what was just refused.
    await mibEditorStore.discardDraft(source?.name);
    return true;
  }

  // Validation runs in Go and touches nothing: no file, no gosmi state. That is
  // what makes it safe to run while someone types.
  function onInput(e) {
    const after = e.target.value;
    // atomicEdit marks the deliberate ones — a snippet, replace-all, the
    // IMPORTS fix — so they never merge into a run of typing.
    hist.record(prevText, after, prevSel, selectionNow(), atomicEdit);
    atomicEdit = false;
    prevText = after;
    prevSel = selectionNow();
    syncHistoryFlags();

    mibEditorStore.setBuffer(after);
    updateCompletion(after);
  }

  // The selection BEFORE the edit is what undo has to restore, and it is gone
  // by the time input fires.
  function onBeforeInput() {
    prevSel = selectionNow();
  }

  // Undo and redo drive OUR stack, and the keyboard is intercepted so the
  // browser's is never consulted. Two histories that disagree — the button
  // walking back through one while Ctrl+Z walks another — would be worse than
  // a coarse one.
  function applyHistory(step) {
    if (!step || !textarea) return;
    const active = document.activeElement;
    textarea.value = step.text;
    textarea.setSelectionRange(step.selection.start, step.selection.end);
    prevText = step.text;
    prevSel = step.selection;
    mibEditorStore.setBuffer(step.text);
    syncHistoryFlags();
    syncScroll();
    if (active === textarea) textarea.focus();
  }

  function undoEdit() {
    applyHistory(hist.undo(prevText));
  }

  function redoEdit() {
    applyHistory(hist.redo(prevText));
  }

  async function save(force = false) {
    if (!source) return;
    saving = true;
    try {
      // The text as it is NOW, kept for markSaved. The user can type during
      // the round trip, and marking the current buffer as "on disk" told them
      // unsaved work was saved — and discarded the draft that held it.
      const written = buffer;
      const res = await MibEditorSave(source.name, written, source.sha256, force);
      if (res.conflict) {
        const overwrite = window.confirm(get(_)('mibEditor.conflictConfirm'));
        saving = false;
        if (overwrite) await save(true);
        return;
      }
      if (!res.saved) {
        notificationStore.add(get(_)('mibEditor.saveFailed'), 'error');
        return;
      }
      await mibEditorStore.markSaved(res.sha256, written);
      notificationStore.add(
        get(_)(res.backupPath ? 'mibEditor.savedWithBackup' : 'mibEditor.saved',
          { values: { name: source.name } }), 'success');
      await refreshList();
      await reload();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      saving = false;
    }
  }

  // The MIBs the user actually enabled. Passing an empty list used to make the
  // backend fall back to "everything on disk", which silently switched back on
  // every MIB somebody had deliberately turned off.
  // The same list the MIB tab loads.
  //
  // This used to walk enabledMibs itself and keep only entries explicitly
  // true, which is the OPPOSITE default from mibStore's (unknown means
  // enabled). A fresh profile has no entries at all, so saving in the editor
  // reloaded with [] — gosmi torn down to the two core modules, the health
  // probe failing on sysDescr and ifInOctets, and a "major" system event sent
  // to every configured syslog, webhook and mail sink.
  async function enabledMibFiles() {
    return mibStore.getEnabledMibFiles();
  }

  // Rebuilding is the only way to see the effect of an edit: gosmi has no
  // unload, so without a full teardown the previously parsed module stays.
  async function reload() {
    try {
      const res = await MibEditorReload(await enabledMibFiles());
      const failed = (res.diagnostics || []).filter((d) => !d.success);
      if (res.health && !res.health.ok) {
        notificationStore.add(
          get(_)('mibEditor.treeBroken', { values: { failures: res.health.failures.join(', ') } }), 'error');
      } else if (failed.length) {
        notificationStore.add(
          get(_)('mibEditor.reloadedWithFailures', { values: { count: failed.length } }), 'warning');
      } else {
        notificationStore.add(get(_)('mibEditor.reloaded', { values: { count: res.health?.modules ?? 0 } }), 'success');
      }
      // The rebuilt tree was being thrown away, so the MIB browser kept showing
      // the state from before the edit. mibStore owns that tree, so it is the
      // one that has to be told. Then the open file is judged again: its
      // verdict weighs what the load just did with it.
      await mibStore.loadSilent();
      mibEditorStore.refresh();
      try {
        catalogue = (await MibEditorSymbols()) || catalogue;
      } catch (e) {
        /* keep the previous catalogue */
      }
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function restoreBundled() {
    if (!source?.bundled) return;
    if (!window.confirm(get(_)('mibEditor.restoreConfirm', { values: { name: source.name } }))) return;
    try {
      mibEditorStore.openSource(await MibEditorRestoreBundled(source.name));
      // The document was replaced wholesale, so the history describing the old
      // one is meaningless: one Ctrl+Z would have discarded the restore, or
      // built a document that never existed by replaying an edit against
      // different text.
      resetHistory();
      await refreshList();
      await reload();
      notificationStore.add(get(_)('mibEditor.restored', { values: { name: source.name } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function revert() {
    if (!source || !dirty) return;
    if (!window.confirm(get(_)('mibEditor.revertConfirm'))) return;
    mibEditorStore.revert();
    // Same reason as restoreBundled: the buffer is now the file, and an undo
    // of edits made before it would resurrect what was just discarded.
    resetHistory();
  }

  // The one place the editor goes from "here is the problem" to "here is the
  // repair": the diagnostic knows the symbol, the catalogue knows its module.
  async function fixImports() {
    try {
      const fix = await MibEditorFixImports(buffer);
      if (!fix.content) return;
      // Replace the whole buffer as one edit so it stays undoable.
      insertAtCaret(fix.content, [0, buffer.length]);
      notificationStore.add(
        get(_)('mibEditor.importsFixed', { values: { count: fix.missing.length } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // What SnmpLens reads leniently and other MIB tools refuse: an escaped quote,
  // a date without its time, a misspelt module name. The file loads here; the
  // fix writes the repair in, so it loads everywhere.
  const REPAIRABLE = new Set(['escaped-quote', 'bad-date', 'import-alias']);
  $: repairable = diagnostics.filter((d) => REPAIRABLE.has(d.code));

  async function repair() {
    try {
      const fix = await MibEditorRepair(buffer);
      if (!fix.content) return;
      // One edit for the whole buffer, so it stays undoable.
      insertAtCaret(fix.content, [0, buffer.length]);
      notificationStore.add(
        get(_)('mibEditor.repaired', { values: { count: fix.fixes.length } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // Inserting by reassigning textarea.value wipes the browser's native undo
  // stack, so Ctrl+Z stopped working the moment you used a snippet or the
  // IMPORTS fix. execCommand('insertText') is deprecated but it is the only
  // way to write into a textarea AS AN EDIT, which is what undo records.
  function insertAtCaret(text, replaceRange) {
    atomicEdit = true;
    if (!textarea) {
      mibEditorStore.setBuffer(buffer + text);
      return;
    }
    textarea.focus();
    if (replaceRange) {
      textarea.setSelectionRange(replaceRange[0], replaceRange[1]);
    }
    const inserted = document.execCommand && document.execCommand('insertText', false, text);
    if (!inserted) {
      // Fallback for a webview that refuses it: correct, but undo is lost.
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      mibEditorStore.setBuffer(buffer.slice(0, start) + text + buffer.slice(end));
      tick().then(() => textarea.setSelectionRange(start + text.length, start + text.length));
      return;
    }
    mibEditorStore.setBuffer(textarea.value);
  }

  // Find and replace.
  //
  // Matches are drawn in an overlay rather than through the textarea's own
  // selection, because showing a selection requires focusing the textarea —
  // which is what used to throw you out of the search box on Enter. The
  // overlay lets focus stay where you are typing, which is how every editor
  // behaves and what makes Enter-to-cycle usable at all.
  $: matches = findOpen ? findMatches(buffer, findTerm, MAX_MATCHES, matchCase) : [];
  // Whether the list stops short of the real total. The counter used to read
  // "1/2000" with nothing saying it was a cap, and Enter could not reach past
  // it.
  $: matchesCapped = matches.length >= MAX_MATCHES;
  $: findCount = matches.length;
  $: if (findIndex >= matches.length) findIndex = matches.length ? matches.length - 1 : 0;

  // Land on the FIRST match when the term changes, and on the one after a
  // replacement otherwise. Typing a term used to leave the view where it was
  // and show "1/N" for a match nobody had scrolled to, so the first Enter
  // jumped to the SECOND one.
  $: if (findOpen && matches.length) {
    if (pendingFirst) {
      pendingFirst = false;
      gotoMatch(0);
    } else if (resumeFrom >= 0) {
      const at = resumeFrom;
      resumeFrom = -1;
      const next = matches.findIndex((m) => m >= at);
      gotoMatch(next >= 0 ? next : 0);
    }
  }
  // Only the on-screen matches become boxes; a file can hold thousands.
  // Line starts once per buffer, then a binary search per match.
  //
  // lineColumnAt slices the whole prefix and splits it, and this called it
  // TWICE per match for up to 2000 matches — measured at 212 ms per keystroke
  // on IP-MIB with the find bar open, against 2.7 ms with it closed. Typing
  // was unusable, and the same recompute fired on every scroll.
  $: starts = lineStarts(lines);
  $: matchBoxes = matches
    .map((at, i) => ({ at, i, pos: lineColumnFrom(starts, at) }))
    .filter(({ pos }) => pos.line >= firstVisible && pos.line <= lastVisible)
    .map(({ i, pos }) => {
      const xy = positionOf(lines, pos.line, pos.column, cw, LINE_HEIGHT);
      return { x: xy.x, y: xy.y, width: findTerm.length * cw, current: i === findIndex };
    });

  // Move to a match WITHOUT focusing the textarea: scrolling works unfocused,
  // and the highlight comes from the overlay.
  function gotoMatch(index) {
    if (!matches.length || !textarea) return;
    findIndex = (index + matches.length) % matches.length;
    const { line } = lineColumnAt(buffer, matches[findIndex]);
    const top = (line - 1) * LINE_HEIGHT;
    const view = textarea.clientHeight;
    // Only scroll when the match is off screen, so cycling through matches on
    // one screen does not make the view jump about.
    if (top < textarea.scrollTop || top > textarea.scrollTop + view - LINE_HEIGHT * 2) {
      textarea.scrollTop = Math.max(0, top - view / 3);
    }
    syncScroll();
  }

  function findNext(backwards = false) {
    if (!matches.length) return;
    gotoMatch(nextIndex(findIndex, matches.length, backwards));
  }

  // Replacing needs the textarea focused — execCommand writes into the focused
  // element — so focus is borrowed and given straight back, which keeps the
  // edit on the native undo stack without moving the user out of the box.
  function replaceRange(start, end, text) {
    if (!textarea) return;
    atomicEdit = true;
    const active = document.activeElement;
    textarea.focus();
    textarea.setSelectionRange(start, end);
    const done = document.execCommand && document.execCommand('insertText', false, text);
    if (!done) {
      mibEditorStore.setBuffer(buffer.slice(0, start) + text + buffer.slice(end));
    } else {
      mibEditorStore.setBuffer(textarea.value);
    }
    if (active && active !== textarea) tick().then(() => active.focus());
  }

  function replaceCurrent() {
    if (!matches.length || !findTerm) return;
    const at = matches[findIndex];
    // Resume PAST the replacement, so the button advances. Without this,
    // replacing "ip" with "ipAddress" found the "ip" inside the replacement
    // and rewrote the same spot on every click, for ever.
    resumeFrom = at + replaceTerm.length;
    replaceRange(at, at + findTerm.length, replaceTerm);
  }

  // One edit, so one undo: replacing occurrence by occurrence would make
  // Ctrl+Z walk back through a hundred steps.
  //
  // The match list used for HIGHLIGHTING is capped, so this recomputes without
  // a cap. Replacing the first two thousand and reporting success would leave
  // a file half-rewritten, which is worse than refusing.
  function replaceAll() {
    if (!findTerm) return;
    const all = findAllMatches(buffer, findTerm, matchCase);
    if (!all.length) return;
    replaceRange(0, buffer.length, applyReplaceAll(buffer, all, findTerm.length, replaceTerm));
    findIndex = 0;
    notificationStore.add(
      get(_)('mibEditor.replacedAll', { values: { count: all.length } }), 'success');
  }

  function closeFind() {
    findOpen = false;
    showReplace = false;
    findTerm = '';
    replaceTerm = '';
    findIndex = 0;
    textarea?.focus();
  }

  // Clicking a problem puts the caret on it. Without this the line number is
  // just a number.
  async function jumpTo(d) {
    if (!textarea || !d.line) return;
    let offset = 0;
    for (let i = 0; i < Math.min(d.line - 1, lines.length); i++) offset += lines[i].length + 1;
    offset += Math.max(0, (d.column || 1) - 1);

    textarea.focus();
    textarea.setSelectionRange(offset, offset);
    await tick();
    // Scroll the target line to roughly a third down the pane.
    const lineHeight = textarea.scrollHeight / Math.max(1, lineCount);
    textarea.scrollTop = Math.max(0, (d.line - 1) * lineHeight - textarea.clientHeight / 3);
    syncScroll();
  }

  // What the buffer defines. Derived in a reactive statement rather than in the
  // markup: Svelte 5 tracks what the EXPRESSION reads, and a store read inside
  // a callee is not a dependency it can see — reactive.test.mjs exists for
  // exactly that.
  $: outline = $mibEditorStore.outline || [];
  $: shownOutline = filterOutline(outline, outlineFilter);

  // Takes what it reads as arguments, for the same reason.
  function filterOutline(items, needle) {
    const q = needle.trim().toLowerCase();
    if (!q) return items;
    return items.filter((it) =>
      it.name.toLowerCase().includes(q) || (it.syntax || '').toLowerCase().includes(q));
  }

  // A one-character badge, because the list is narrow and the kind is the thing
  // that tells a table from the row inside it from the columns inside that —
  // three definitions that read almost identically in the source.
  const OUTLINE_BADGE = {
    module: 'M', object: 'o', table: 'T', row: 'R', identity: 'i',
    notification: 'N', group: 'G', compliance: 'C', capabilities: 'A',
    trap: 'N', node: '·', type: 't', macro: 'm',
  };
  const badgeOf = (kind) => OUTLINE_BADGE[kind] || '·';

  // Hover: the word under the pointer, looked up in the loaded tree.
  let hoverTimer;
  function onMouseMove(e) {
    clearTimeout(hoverTimer);
    const rect = textarea.getBoundingClientRect();
    const px = e.clientX - rect.left + textarea.scrollLeft - 8;
    const py = e.clientY - rect.top + textarea.scrollTop - 8;
    hoverTimer = setTimeout(() => {
      const offset = offsetAt(lines, buffer, px, py, cw, LINE_HEIGHT);
      if (offset === null) { hover = null; return; }
      const { word } = wordAt(buffer, offset);
      if (word.length < 2) { hover = null; return; }
      const symbol = catalogue.symbols.find((sy) => sy.name === word);
      hover = symbol ? { x: e.clientX - rect.left, y: e.clientY - rect.top, symbol } : null;
    }, 220);
  }

  function onMouseLeave() {
    clearTimeout(hoverTimer);
    hover = null;
  }

  // Completion, anchored at the caret. Possible here because the mirror
  // guarantees identical layout; in a bare textarea the caret has no
  // measurable position at all.
  /**
   * @param {string} [text] the buffer as it is NOW.
   *
   * Passed in from onInput rather than read from the `$:` local: Svelte only
   * reassigns that during the flush microtask, so inside a synchronous input
   * handler it still holds the text from BEFORE the keystroke. The caret
   * offset came from the live textarea, so `end` was computed one character
   * short of `offset`, the `offset !== end` guard fired every time, and the
   * completion popup never opened at all.
   */
  function updateCompletion(text) {
    if (!textarea) return;
    const src = typeof text === 'string' ? text : buffer;
    const offset = textarea.selectionStart;
    const { word, start, end } = wordAt(src, offset);
    if (word.length < 2 || offset !== end) { completion = null; return; }

    // Not inside a DESCRIPTION or a comment. A symbol name there is prose, and
    // offering a completion made Enter — pressed to start a new line — insert
    // one instead, because the popup captures Enter whenever it is open.
    if (inProse(src, start)) { completion = null; return; }

    const items = catalogue.symbols
      .filter((sy) => sy.name.toLowerCase().startsWith(word.toLowerCase()) && sy.name !== word)
      .slice(0, 12);
    if (items.length === 0) { completion = null; return; }

    const { line, column } = lineColumnAt(src, start);
    // From `src` too: `lines` is derived from the same stale local, so the
    // popup would be placed against the previous text.
    const pos = positionOf(src.split('\n'), line, column, cw, LINE_HEIGHT);
    completion = {
      x: pos.x - scrollLeft + 8,
      y: pos.y - scrollTop + LINE_HEIGHT + 8,
      items, index: 0, range: [start, end],
    };
  }

  /**
   * Whether an offset is inside a string or a comment.
   *
   * The string half comes from the same scanner the highlighter uses, so the
   * two cannot disagree about where a DESCRIPTION ends. The comment half is
   * per-line: `--` starts one and a second `--` ends it, which is the SMI
   * rule the tokeniser follows too.
   */
  function inProse(text, offset) {
    if (stringStateAt(text, offset)) return true;

    const lineStart = text.lastIndexOf('\n', Math.max(0, offset - 1)) + 1;
    const before = text.slice(lineStart, offset);
    let open = false;
    for (let i = 0; i + 1 < before.length; i++) {
      if (before[i] === '-' && before[i + 1] === '-') {
        open = !open;
        i++;
      }
    }
    return open;
  }

  function acceptCompletion() {
    if (!completion) return;
    const chosen = completion.items[completion.index];
    const range = completion.range;
    completion = null;
    insertAtCaret(chosen.name, range);
  }

  function insertSnippet(snippet) {
    showSnippets = false;
    // ${Name} is the capitalised form: an SMI TYPE reference must start with
    // an upper-case letter, so the table snippet's SEQUENCE type cannot reuse
    // the object prefix verbatim.
    const name = 'myObject';
    const Name = name.charAt(0).toUpperCase() + name.slice(1);
    insertAtCaret(
      snippet.text
        .replace(/\$\{Name\}/g, Name)
        .replace(/\$\{name\}/g, name),
    );
  }

  // The mirror only stays under the text if it scrolls with it.
  function syncScroll() {
    if (textarea) {
      scrollTop = textarea.scrollTop;
      scrollLeft = textarea.scrollLeft;
    }
    if (mirror && textarea) {
      mirror.scrollTop = textarea.scrollTop;
      mirror.scrollLeft = textarea.scrollLeft;
    }
    if (gutter && textarea) gutter.scrollTop = textarea.scrollTop;
    if (textarea) viewportHeight = textarea.clientHeight || viewportHeight;
  }

  function onKeydown(e) {
    if (completion) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        completion.index = (completion.index + 1) % completion.items.length;
        completion = completion;
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        completion.index = (completion.index - 1 + completion.items.length) % completion.items.length;
        completion = completion;
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        acceptCompletion();
        return;
      }
      if (e.key === 'Escape') {
        completion = null;
        return;
      }
    }
    // Intercepted so the browser's stack is never used: ours is the only one.
    if ((e.ctrlKey || e.metaKey) && (e.key === 'z' || e.key === 'Z')) {
      e.preventDefault();
      if (e.shiftKey) redoEdit(); else undoEdit();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === 'y' || e.key === 'Y')) {
      e.preventDefault();
      redoEdit();
      return;
    }
    // A Wails webview has no Ctrl+F of its own, and IP-MIB is 4,993 lines.
    if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
      e.preventDefault();
      findOpen = true;
      tick().then(() => document.getElementById('mib-find')?.focus());
      return;
    }
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      // Guarded like the button. Two saves of one file race on a fixed staging
      // path in mib-temp/, and the second reports a raw OS error the user can
      // do nothing with.
      if (!saving && savable) save();
      return;
    }
    // A textarea would otherwise move focus out of the editor on Tab.
    if (e.key === 'Tab') {
      e.preventDefault();
      insertAtCaret('    ');
    }
  }
</script>

<div class="mib-editor">
  <!-- ---------------- file rail ---------------- -->
  <aside class="rail">
    <div class="rail-head">
      <input type="search" bind:value={filter} placeholder={$_('mibEditor.filter')} />
      <button class="btn-copy-small" on:click={refreshList} title={$_('mibEditor.refresh')}>
        <Icon name="refresh-cw" size={13} />
      </button>
    </div>
    <button class="btn btn-small full" on:click={openExternal}>
      <Icon name="folder-open" size={13} /> {$_('mibEditor.openExternal')}
    </button>

    {#if problemCounts[FAILED] + problemCounts[ERROR] + problemCounts[WARNING] > 0 || onlyProblems}
      <button class="only-problems" aria-pressed={onlyProblems} on:click={() => (onlyProblems = !onlyProblems)}
        title={onlyProblems ? $_('mibEditor.files.showAll') : $_('mibEditor.files.onlyProblems')}>
        <Icon name="list-filter" size={13} />
        <span class="label">{onlyProblems ? $_('mibEditor.files.showAll') : $_('mibEditor.files.onlyProblems')}</span>
        {#each [FAILED, ERROR, WARNING] as level}
          {#if problemCounts[level] > 0}
            <span class="count {level}" title={$_('mibVerdict.short.' + level)}>
              <Icon name={VERDICT_ICONS[level]} size={12} /> {problemCounts[level]}
            </span>
          {/if}
        {/each}
      </button>
    {/if}

    <ul class="files">
      {#each shown as f (f.name)}
        <li>
          <button class="file" class:active={source && !source.external && source.name === f.name}
            on:click={() => open(f.name)}
            title={isProblem(statuses[f.name]?.level)
              ? $_('mibVerdict.' + statuses[f.name].level, { values: { count: statuses[f.name].count } })
                + (statuses[f.name].live ? ' (' + $_('mibVerdict.live') + ')' : '')
                + (statuses[f.name].detail ? ' — ' + statuses[f.name].detail : '')
              : f.name}>
            <!-- One element whatever the verdict, its icon chosen by name:
                 swapping {#if} branches is a destroy and a create, and showed
                 the previous icon for a frame on every hover. -->
            <span class="file-mark {statuses[f.name]?.level || ''}" aria-hidden="true">
              {#if isProblem(statuses[f.name]?.level)}<Icon name={VERDICT_ICONS[statuses[f.name].level]} size={12} />{/if}
            </span>
            <span class="fname">{f.name}</span>
            {#if f.bundled}
              <span class="tag" class:changed={f.modified}
                title={f.modified ? $_('mibEditor.bundledModified') : $_('mibEditor.bundled')}>
                {f.modified ? '±' : '◆'}
              </span>
            {/if}
          </button>
        </li>
      {/each}
      {#if shown.length === 0}
        <li class="empty">{onlyProblems ? $_('mibEditor.files.noProblems') : $_('mibEditor.noFiles')}</li>
      {/if}
    </ul>

    <!-- The outline. A MIB has no indentation to navigate by and no folding to
         collapse, so scrolling is the only tool and a 185 KB file is a lot of
         scrolling. It costs nothing: every field comes off the parse the
         analysis already makes on every pause in typing. -->
    {#if source}
      <div class="outline">
        <button class="outline-head" on:click={() => (showOutline = !showOutline)}
          aria-expanded={showOutline}>
          <Icon name={showOutline ? 'chevron-down' : 'chevron-right'} size={12} />
          {$_('mibEditor.outline')}
          <span class="count">{outline.length}</span>
        </button>

        {#if showOutline}
          <input type="search" class="outline-filter" bind:value={outlineFilter}
            placeholder={$_('mibEditor.outlineFilter')} />

          <ul class="outline-list">
            {#each shownOutline as item (item.kind + item.name + item.line)}
              <li>
                <button class="outline-item" on:click={() => jumpTo(item)}
                  title={item.description || item.name}>
                  <span class="badge kind-{item.kind}">{badgeOf(item.kind)}</span>
                  <span class="oname">{item.name}</span>
                  {#if item.syntax}<span class="osyntax">{item.syntax}</span>{/if}
                </button>
              </li>
            {/each}
            {#if outline.length === 0}
              <li class="empty">{$_('mibEditor.outlineEmpty')}</li>
            {:else if shownOutline.length === 0}
              <li class="empty">{$_('mibEditor.outlineNoMatch')}</li>
            {/if}
          </ul>
        {/if}
      </div>
    {/if}
  </aside>

  <!-- ---------------- editor ---------------- -->
  <section class="main" bind:this={mainEl}>
    {#if !source}
      <div class="placeholder">
        <Icon name="book-marked" size={28} />
        <p>{$_('mibEditor.placeholder')}</p>
      </div>
    {:else}
      <div class="toolbar">
        <span class="title">
          {source.name}
          {#if source.external}<span class="chip">{$_('mibEditor.external')}</span>{/if}
          {#if source.bundled}<span class="chip warn">{$_('mibEditor.bundled')}</span>{/if}
          {#if dirty}<span class="chip dot">{$_('mibEditor.unsaved')}</span>{/if}
        </span>
        <span class="spacer"></span>

        <div class="history">
          <button class="btn-copy-small" on:click={undoEdit} disabled={!canUndo}
            title="{$_('mibEditor.undo')} ({UNDO_KEYS})" aria-label={$_('mibEditor.undo')}>
            <Icon name="undo-2" size={14} />
          </button>
          <button class="btn-copy-small" on:click={redoEdit} disabled={!canRedo}
            title="{$_('mibEditor.redo')} ({REDO_KEYS})" aria-label={$_('mibEditor.redo')}>
            <Icon name="redo-2" size={14} />
          </button>
        </div>

        <div class="snip">
          <button class="btn btn-small" on:click={() => (showSnippets = !showSnippets)}>
            <Icon name="file-plus" size={13} /> {$_('mibEditor.insert')}
          </button>
          {#if showSnippets}
            <ul class="snip-menu">
              {#each SNIPPETS as s (s.key)}
                <li><button on:click={() => insertSnippet(s)}>{$_('mibEditor.snippet.' + s.key)}</button></li>
              {/each}
            </ul>
          {/if}
        </div>

        <div class="snip">
          <button class="btn btn-small" on:click={() => (showSymbols = !showSymbols)}>
            <Icon name="book-marked" size={13} /> {$_('mibEditor.symbols')}
          </button>
          {#if showSymbols}
            <div class="sym-menu">
              <input type="search" bind:value={symbolFilter}
                placeholder={$_('mibEditor.symbolSearch')} />
              <ul>
                {#each matchingSymbols as sy (sy.module + '.' + sy.name)}
                  <li>
                    <button on:click={() => { showSymbols = false; insertAtCaret(sy.name); }}
                      title={sy.description}>
                      <span class="sy-name">{sy.name}</span>
                      <span class="sy-mod">{sy.module}</span>
                    </button>
                  </li>
                {/each}
                {#if symbolFilter.length < 2}
                  <li class="sy-hint">{$_('mibEditor.symbolHint', { values: { count: catalogue.symbols.length } })}</li>
                {:else if matchingSymbols.length === 0}
                  <li class="sy-hint">{$_('mibEditor.symbolNone')}</li>
                {/if}
              </ul>
            </div>
          {/if}
        </div>

        {#if source.bundled}
          <button class="btn btn-small" on:click={restoreBundled}>{$_('mibEditor.restore')}</button>
        {/if}
        <button class="btn btn-small" on:click={revert} disabled={!dirty}>{$_('mibEditor.revert')}</button>
        <button class="btn btn-small" on:click={() => save()} disabled={saving || !savable}>
          <Icon name={saving ? 'loader-circle' : 'download'} size={13} class={saving ? 'icon-spin' : ''} />
          {$_('common.save')}
        </button>
      </div>

      {#if source.bundled}
        <p class="banner">
          <Icon name="triangle-alert" size={13} /> {$_('mibEditor.bundledWarning')}
        </p>
      {/if}
      {#if source.external}
        <p class="banner">
          <Icon name="triangle-alert" size={13} /> {$_('mibEditor.externalWarning', { values: { name: source.name } })}
        </p>
      {/if}
      {#if source.encoding === 'windows-1252'}
        <p class="banner">
          <Icon name="triangle-alert" size={13} /> {$_('mibEditor.encodingWarning')}
        </p>
      {/if}

      <!-- The textarea is transparent and sits over the coloured mirror. Both
           layers must use the same font metrics or they drift apart. -->
      {#if findOpen}
        <div class="findbar">
          <button class="btn-copy-small" on:click={() => (showReplace = !showReplace)}
            title={$_('mibEditor.toggleReplace')}>
            <Icon name={showReplace ? 'chevron-down' : 'chevron-right'} size={13} />
          </button>
          <div class="find-fields">
            <div class="find-row">
              <Icon name="search" size={13} />
              <input id="mib-find" type="search" bind:value={findTerm}
                on:input={() => { findIndex = 0; pendingFirst = true; }}
                on:keydown={(e) => {
                  if (e.key === 'Enter') { e.preventDefault(); findNext(e.shiftKey); }
                  else if (e.key === 'Escape') { e.preventDefault(); closeFind(); }
                }}
                placeholder={$_('mibEditor.find')} />
              <button class="btn-copy-small" class:active={matchCase}
                on:click={() => { matchCase = !matchCase; findIndex = 0; pendingFirst = true; }}
                title={$_('mibEditor.matchCase')} aria-pressed={matchCase}>Aa</button>
              <span class="find-count" class:capped={matchesCapped}
                title={matchesCapped ? $_('mibEditor.matchesCapped', { values: { max: MAX_MATCHES } }) : ''}>
                {findCount
                  ? `${findIndex + 1}/${findCount}${matchesCapped ? '+' : ''}`
                  : $_('mibEditor.noMatch')}
              </span>
              <button class="btn-copy-small" on:click={() => findNext(true)} title={$_('mibEditor.findPrev')}>
                <Icon name="chevron-up" size={13} />
              </button>
              <button class="btn-copy-small" on:click={() => findNext()} title={$_('mibEditor.findNext')}>
                <Icon name="chevron-down" size={13} />
              </button>
              <button class="btn-copy-small" on:click={closeFind}><Icon name="circle-x" size={13} /></button>
            </div>

            {#if showReplace}
              <div class="find-row">
                <Icon name="pencil" size={13} />
                <input type="text" bind:value={replaceTerm}
                  on:keydown={(e) => {
                    if (e.key === 'Enter') { e.preventDefault(); e.shiftKey ? replaceAll() : replaceCurrent(); }
                    else if (e.key === 'Escape') { e.preventDefault(); closeFind(); }
                  }}
                  placeholder={$_('mibEditor.replace')} />
                <button class="btn btn-small" on:click={replaceCurrent} disabled={!findCount}>
                  {$_('mibEditor.replaceOne')}
                </button>
                <button class="btn btn-small" on:click={replaceAll} disabled={!findCount}>
                  {$_('mibEditor.replaceAll')}
                </button>
              </div>
            {/if}
          </div>
        </div>
      {/if}

      <div class="surface" bind:this={surface}>
        <pre class="gutter" bind:this={gutter} aria-hidden="true">{gutterText}</pre>
        <div class="code">
          <pre class="mirror" bind:this={mirror} aria-hidden="true">{@html highlighted}<br /></pre>
          <!-- Underlines sit between the mirror and the textarea, so they are
               visible through the transparent text layer without catching the
               pointer. -->
          <!-- Search hits and problem underlines, drawn under the transparent
               text so focus can stay where it is.

               Two boxes, because one cannot do both jobs. The outer one is the
               visible area and CLIPS; the inner one carries the content and
               MOVES with the scroll, from where the text starts (TEXT_PAD, the
               mirror's padding). It used to be one box doing both, and a box
               that moves with the scroll and clips to its own size is off
               screen after one screen of scrolling — taking every underline
               with it — while its padding, which absolutely positioned
               children ignore, put each one 8 px up and left: through the
               middle of the word rather than under it. -->
          <div class="overlay" aria-hidden="true">
            <div class="overlay-layer"
              style="transform: translate({TEXT_PAD - scrollLeft}px, {TEXT_PAD - scrollTop}px)">
              {#each matchBoxes as m, i (i)}
                <span class="hit" class:current={m.current}
                  style="left: {m.x}px; top: {m.y}px; width: {m.width}px;"></span>
              {/each}
              {#each marks as m, i (i)}
                <span class="mark-line" style="top: {m.y}px;">{m.before}<span class="mark {m.severity}"
                  data-line={m.line} data-column={m.before.length + 1} data-length={m.under.length}>{m.under}</span></span>
              {/each}
            </div>
          </div>
          <textarea
            bind:this={textarea}
            value={buffer}
            on:input={onInput}
            on:beforeinput={onBeforeInput}
            on:scroll={syncScroll}
            on:keydown={onKeydown}
            on:click={() => (completion = null)}
            on:blur={() => (completion = null)}
            on:mousemove={onMouseMove}
            on:mouseleave={onMouseLeave}
            spellcheck="false"
            autocomplete="off"
            autocapitalize="off"
            wrap="off"></textarea>

          {#if hover}
            <div class="hovercard" style="left: {hover.x + 12}px; top: {hover.y + 18}px;">
              <div class="hc-head">
                <code>{hover.symbol.name}</code>
                <span class="hc-mod">{hover.symbol.module}</span>
              </div>
              {#if hover.symbol.oid}<div class="hc-oid">{hover.symbol.oid}</div>{/if}
              {#if hover.symbol.description}<p>{hover.symbol.description}</p>{/if}
            </div>
          {/if}

          {#if completion}
            <ul class="complete" style="left: {completion.x}px; top: {completion.y}px;">
              {#each completion.items as item, i (item.module + '.' + item.name)}
                <li>
                  <button class:sel={i === completion.index}
                    on:mousedown|preventDefault={() => { completion.index = i; acceptCompletion(); }}>
                    <span class="sy-name">{item.name}</span>
                    <span class="sy-mod">{item.module}</span>
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </div>
      </div>

      <div class="status">
        <span>{$_('mibEditor.lines', { values: { count: lineCount } })}</span>
        <span>{source.eol === 'crlf' ? 'CRLF' : 'LF'}</span>
        <span class="spacer"></span>
        {#if checking}
          <span class="pill">{$_('mibEditor.checking')}</span>
        {:else if errorCount > 0}
          <span class="pill err">{$_('mibEditor.errorCount', { values: { count: errorCount } })}</span>
        {:else if warnCount > 0}
          <span class="pill warn">{$_('mibEditor.warnCount', { values: { count: warnCount } })}</span>
        {:else}
          <span class="pill ok">{$_('mibEditor.parses')}</span>
        {/if}
      </div>

      {#if missingImports.length}
        <div class="fixbar">
          <Icon name="circle-alert" size={13} />
          <span>{$_('mibEditor.missingImports', {
            values: { count: missingImports.length, symbols: missingImports.slice(0, 4).map((m) => m.symbol).join(', ') } })}</span>
          <span class="spacer"></span>
          <button class="btn btn-small" on:click={fixImports}>{$_('mibEditor.fixImports')}</button>
        </div>
      {/if}

      {#if repairable.length}
        <div class="fixbar">
          <Icon name="triangle-alert" size={13} />
          <span>{$_('mibEditor.repairable', { values: { count: repairable.length } })}</span>
          <span class="spacer"></span>
          <button class="btn btn-small" on:click={repair}>{$_('mibEditor.repair')}</button>
        </div>
      {/if}

      {#if diagnostics.length}
        <!-- Dragged, or moved with the arrow keys, to give the table more of
             the height and the editor less. -->
        <div class="problems-resize" class:active={resizing !== null}
          role="slider" tabindex="0" aria-orientation="vertical"
          aria-label={$_('mibEditor.problemTable.resize')} title={$_('mibEditor.problemTable.resize')}
          aria-valuemin={PROBLEMS_MIN} aria-valuenow={Math.round(problemsHeight)}
          on:pointerdown={startResize} on:pointermove={onResize}
          on:pointerup={endResize} on:pointercancel={endResize} on:keydown={resizeByKey}>
          <span class="grip"></span>
        </div>
        <div class="problems">
          <div class="problems-filter" role="group" aria-label={$_('mibEditor.problemTable.filter')}>
            {#each SEVERITIES as sev}
              <button class="sev-filter {sev}" aria-pressed={visibleSeverities.has(sev)}
                disabled={severityCounts[sev] === 0} title={$_('mibEditor.problemTable.filterHint')}
                on:click={() => toggleSeverity(sev)}>
                {$_('mibEditor.problemTable.sev.' + sev)} <span class="n">{severityCounts[sev]}</span>
              </button>
            {/each}
            {#each fixAllCodes as f (f.code)}
              <button class="problems-action" on:click={() => quickFix(f.code, [])}
                title={$_('mibEditor.problemTable.fixAllHint')}>
                <Icon name="zap" size={13} /> {$_('mibEditor.problemTable.fixAll', { values: { count: f.count, code: f.code } })}
              </button>
            {/each}
            <button class="problems-action problems-copy" on:click={copyProblems} disabled={shownProblems.length === 0}
              title={$_('mibEditor.problemTable.copyHint')}>
              <Icon name="clipboard-list" size={13} /> {$_('mibEditor.problemTable.copy')}
            </button>
          </div>
          {#if $mutedRules.size > 0}
            <!-- What is hidden stays one click from coming back. -->
            <div class="problems-muted">
              <Icon name="eye-off" size={12} />
              <span>{$_('mibEditor.problemTable.muted', { values: { count: diagnostics.length - listed.length } })}</span>
              {#each [...$mutedRules] as code (code)}
                <button class="muted-chip" on:click={() => unmute(code)} title={$_('mibEditor.problemTable.unmuteHint')}>
                  {code} <Icon name="x" size={11} />
                </button>
              {/each}
            </div>
          {/if}
          <div class="problems-scroll" style="height: {problemsHeight}px;">
            <table>
              <thead>
                <tr>
                  {#each PROBLEM_COLUMNS as col}
                    <th class="col-{col}"
                      aria-sort={sortKey === col ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}>
                      <button on:click={() => sortBy(col)} title={$_('mibEditor.problemTable.sortHint')}>
                        {$_('mibEditor.problemTable.col.' + col)}
                        {#if sortKey === col}
                          <Icon name={sortDir === 'asc' ? 'chevron-up' : 'chevron-down'} size={12} />
                        {/if}
                      </button>
                    </th>
                  {/each}
                  <th class="col-actions"><span class="visually-hidden">{$_('mibEditor.problemTable.col.actions')}</span></th>
                </tr>
              </thead>
              <tbody>
                {#each shownProblems as d, i (i)}
                  <tr class={d.severity}>
                    <td class="col-severity"><span class="problem-sev {d.severity}">{$_('mibEditor.problemTable.sev.' + d.severity)}</span></td>
                    <td class="col-line problem-pos">{problemPosition(d)}</td>
                    <!-- The row's jump: its ::after covers the row up to the
                         actions, so a click anywhere jumps, and the keyboard
                         has a button to reach. -->
                    <td class="col-message"><button class="jump" on:click={() => jumpTo(d)}>{d.message}</button></td>
                    <td class="col-code problem-code">{d.code}</td>
                    <td class="col-actions">
                      {#if FIXABLE.has(d.code) && d.symbol}
                        <button class="row-action" on:click={() => quickFix(d.code, [d.symbol])}
                          title={$_('mibEditor.problemTable.fixHint')} aria-label={$_('mibEditor.problemTable.fix')}>
                          <Icon name="zap" size={12} />
                        </button>
                      {/if}
                      {#if MUTABLE.has(d.code) && d.severity !== 'error'}
                        <button class="row-action" on:click={() => mute(d.code)}
                          title={$_('mibEditor.problemTable.muteHint', { values: { code: d.code } })}
                          aria-label={$_('mibEditor.problemTable.mute', { values: { code: d.code } })}>
                          <Icon name="eye-off" size={12} />
                        </button>
                      {/if}
                    </td>
                  </tr>
                {:else}
                  <tr><td class="none" colspan="5">{$_('mibEditor.problemTable.none')}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </div>
      {/if}
    {/if}
  </section>
</div>

<style>
  .mib-editor {
    display: grid;
    grid-template-columns: 230px minmax(0, 1fr);
    gap: var(--space-sm);
    height: 100%;
    min-height: 0;
  }

  .outline {
    display: flex;
    flex-direction: column;
    /* A share of the rail rather than the remainder of it, and a floor so it
       cannot be squeezed out of existence by a long file list. */
    flex: 1 1 60%;
    min-height: 6rem;
    border-top: 1px solid var(--border-color);
    padding-top: 0.35rem;
  }

  .outline-head {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.2rem 0.3rem;
    background: none;
    border: none;
    color: var(--text-muted);
    font: inherit;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    cursor: pointer;
  }

  .outline-head .count {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
  }

  .outline-filter {
    margin: 0.2rem 0.3rem 0.3rem;
    font-size: 0.75rem;
  }

  .outline-list {
    list-style: none;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    min-height: 0;
    flex: 1 1 auto;
  }

  .outline-item {
    display: flex;
    align-items: baseline;
    gap: 0.35rem;
    width: 100%;
    padding: 0.12rem 0.3rem;
    background: none;
    border: none;
    color: inherit;
    font: inherit;
    font-size: 0.76rem;
    text-align: left;
    cursor: pointer;
    min-width: 0;
  }

  .outline-item:hover {
    background-color: var(--bg-lighter-color);
  }

  .outline-item .badge {
    flex: 0 0 auto;
    width: 1.05em;
    text-align: center;
    border-radius: 2px;
    background-color: var(--bg-lighter-color);
    color: var(--text-muted);
    font-size: 0.66rem;
    font-family: var(--font-mono, monospace);
  }

  /* A table, the row inside it and the columns inside that read almost
     identically in the source, so the badge is where they differ. */
  .outline-item .kind-table,
  .outline-item .kind-row {
    background-color: var(--accent-subtle, var(--bg-lighter-color));
    color: var(--accent-color, var(--text-color));
  }

  .outline-item .oname {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .outline-item .osyntax {
    margin-left: auto;
    flex: 0 1 auto;
    color: var(--text-muted);
    font-size: 0.68rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .rail {
    display: flex;
    flex-direction: column;
    gap: var(--space-xs);
    min-height: 0;
    border-right: 1px solid var(--border-color);
    padding-right: var(--space-sm);
  }

  .rail-head {
    display: flex;
    gap: 4px;
  }

  .rail-head input {
    flex: 1;
    min-width: 0;
    padding: 5px 7px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.8em;
  }

  .full {
    width: 100%;
    justify-content: center;
  }

  /* The rail holds TWO lists and has to divide its height between them.
     `flex: 1` here claimed everything left over, so the outline below rendered
     at zero height: present in the DOM, scrollable, and invisible. With no file
     open there is no outline and this still takes the whole rail. */
  .files {
    list-style: none;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    min-height: 4rem;
    flex: 1 1 40%;
  }

  .file {
    display: flex;
    align-items: center;
    gap: 5px;
    width: 100%;
    padding: 4px 6px;
    background: none;
    border: none;
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.8em;
    text-align: left;
    cursor: pointer;
  }

  .file:hover {
    background-color: var(--hover-overlay);
  }

  .file.active {
    background-color: var(--accent-subtle);
    color: var(--accent-color);
  }

  .fname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Always there, empty for a file without a problem, so the names line up.
     Not `.mark`: that is the editor's squiggle, absolutely positioned and
     underlined, and sharing the name put both on every file in the list. */
  .file-mark {
    flex: 0 0 14px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 14px;
  }

  .file-mark.failed { color: var(--error-color); }
  .file-mark.error { color: var(--error-color); }
  .file-mark.warning { color: var(--warning-color); }

  .only-problems {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 3px 6px;
    background: none;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-muted);
    font: inherit;
    font-size: 0.78em;
    cursor: pointer;
  }

  .only-problems[aria-pressed='true'] {
    border-color: var(--accent-color);
    color: var(--accent-color);
    background: var(--accent-subtle);
  }

  .only-problems .label {
    flex: 1;
    min-width: 0;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .only-problems .count {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .only-problems .count.failed,
  .only-problems .count.error { color: var(--error-color); }
  .only-problems .count.warning { color: var(--warning-color); }

  .tag {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .tag.changed {
    color: var(--warning-color);
  }

  .empty {
    padding: 8px 6px;
    font-size: 0.78em;
    color: var(--text-muted);
  }

  .main {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    gap: var(--space-xs);
  }

  .placeholder {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    color: var(--text-muted);
    font-size: 0.85em;
  }

  .toolbar {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .title {
    font-size: 0.9em;
    font-weight: 600;
    color: var(--text-color);
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .chip {
    padding: 1px 6px;
    border: 1px solid var(--border-color);
    border-radius: 10px;
    font-size: 0.7em;
    font-weight: 400;
    color: var(--text-muted);
  }

  .chip.warn {
    color: var(--warning-color);
    border-color: var(--warning-border);
  }

  .chip.dot {
    color: var(--accent-color);
    border-color: var(--accent-border);
  }

  .spacer {
    flex: 1;
  }

  .history {
    display: flex;
    gap: 2px;
    padding-right: 6px;
    margin-right: 2px;
    border-right: 1px solid var(--border-color);
  }

  .history button:disabled {
    opacity: 0.35;
    cursor: default;
  }

  .snip {
    position: relative;
  }

  .snip-menu {
    position: absolute;
    right: 0;
    top: 100%;
    z-index: 20;
    list-style: none;
    margin: 4px 0 0;
    padding: 4px;
    min-width: 200px;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    box-shadow: 0 6px 18px var(--shadow-color);
  }

  .snip-menu button {
    width: 100%;
    padding: 5px 8px;
    background: none;
    border: none;
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.8em;
    text-align: left;
    cursor: pointer;
  }

  .snip-menu button:hover {
    background-color: var(--hover-overlay);
  }

  .banner {
    margin: 0;
    padding: 5px 8px;
    background-color: var(--warning-subtle);
    border-radius: 4px;
    font-size: 0.76em;
    color: var(--warning-color);
    display: flex;
    align-items: center;
    gap: 6px;
  }

  /* ---- the editing surface ----
     A transparent textarea over a coloured mirror. Every font and spacing
     property below MUST be identical between .mirror and textarea, or the two
     layers drift apart line by line. */
  .surface {
    flex: 1;
    min-height: 0;
    display: flex;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    overflow: hidden;
    background-color: var(--bg-lighter-color);
  }

  .gutter,
  .mirror,
  .surface textarea {
    margin: 0;
    padding: 8px 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 18px;
    tab-size: 4;
  }

  .gutter {
    flex: 0 0 auto;
    padding-right: 8px;
    padding-left: 8px;
    text-align: right;
    color: var(--text-dimmed);
    background-color: var(--bg-light-color);
    border-right: 1px solid var(--border-color);
    overflow: hidden;
    user-select: none;
    white-space: pre;
  }

  .code {
    position: relative;
    flex: 1;
    min-width: 0;
  }

  .mirror,
  .surface textarea {
    position: absolute;
    inset: 0;
    padding-left: 8px;
    padding-right: 8px;
    white-space: pre;
    overflow: auto;
    border: none;
    word-wrap: normal;
  }

  .mirror {
    pointer-events: none;
    color: var(--text-color);
  }

  .surface textarea {
    background: transparent;
    color: transparent;
    caret-color: var(--text-color);
    resize: none;
    outline: none;
  }

  .surface textarea::selection {
    background-color: var(--accent-subtle-strong);
  }

  /* Token colours. Deliberately few: a MIB is mostly prose inside
     DESCRIPTION, and colouring everything would make none of it stand out. */
  .mirror :global(.mac) { color: var(--accent-color); font-weight: 600; }
  .mirror :global(.kw)  { color: var(--name-color); }
  .mirror :global(.ty)  { color: var(--oid-color); }
  .mirror :global(.val) { color: var(--success-color); }
  .mirror :global(.str) { color: var(--text-light); }
  .mirror :global(.com) { color: var(--text-dimmed); font-style: italic; }
  .mirror :global(.num) { color: var(--oid-color); }
  .mirror :global(.op)  { color: var(--favorites-color); }
  .mirror :global(.pun) { color: var(--text-muted); }

  .sym-menu {
    position: absolute;
    right: 0;
    top: 100%;
    z-index: 20;
    width: 300px;
    margin-top: 4px;
    padding: 6px;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    box-shadow: 0 6px 18px var(--shadow-color);
  }

  .sym-menu input {
    width: 100%;
    padding: 5px 7px;
    margin-bottom: 4px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.8em;
  }

  .sym-menu ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 260px;
    overflow-y: auto;
  }

  .sym-menu li button {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    padding: 3px 6px;
    background: none;
    border: none;
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.78em;
    text-align: left;
    cursor: pointer;
  }

  .sym-menu li button:hover {
    background-color: var(--hover-overlay);
  }

  .sy-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }

  .sy-mod {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .sy-hint {
    padding: 6px;
    font-size: 0.75em;
    color: var(--text-muted);
  }

  .fixbar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    background-color: var(--accent-subtle);
    border-radius: 4px;
    font-size: 0.76em;
    color: var(--text-color);
  }

  .findbar {
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 4px 6px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .find-fields {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .find-row {
    display: flex;
    align-items: center;
    gap: 5px;
  }

  .hit {
    position: absolute;
    height: 18px;
    background-color: var(--warning-subtle);
    border-radius: 2px;
    box-sizing: border-box;
  }

  .hit.current {
    background-color: var(--accent-subtle-strong);
    outline: 1px solid var(--accent-color);
  }

  .findbar input {
    flex: 1;
    min-width: 0;
    padding: 4px 6px;
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.8em;
  }

  .find-count {
    font-size: 0.75em;
    color: var(--text-muted);
    min-width: 28px;
    text-align: right;
  }

  /* Underlines live under the transparent textarea, so they never intercept
     the pointer or the caret. */
  .overlay {
    position: absolute;
    inset: 0;
    overflow: hidden;
    pointer-events: none;
  }

  .overlay-layer {
    position: absolute;
    left: 0;
    top: 0;
    /* The mirror's metrics: the text an underline is placed by is laid out
       exactly as the mirror lays out the same text. */
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 18px;
    tab-size: 4;
  }

  .mark-line {
    position: absolute;
    left: 0;
    height: 18px;
    white-space: pre;
    color: transparent;
    user-select: none;
  }

  .mark {
    text-decoration-line: underline;
    text-decoration-style: wavy;
    text-decoration-thickness: 1px;
    text-underline-offset: 3px;
    text-decoration-skip-ink: none;
  }

  .mark.error {
    text-decoration-color: var(--error-color);
  }

  .mark.warning {
    text-decoration-color: var(--warning-color);
  }

  .hovercard {
    position: absolute;
    z-index: 30;
    max-width: 320px;
    padding: 7px 9px;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    box-shadow: 0 6px 18px var(--shadow-color);
    pointer-events: none;
    font-size: 0.75em;
  }

  .hc-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    margin-bottom: 3px;
  }

  .hc-head code {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    color: var(--accent-color);
  }

  .hc-mod {
    color: var(--text-muted);
  }

  .hc-oid {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    color: var(--oid-color);
    margin-bottom: 3px;
  }

  .hovercard p {
    margin: 0;
    color: var(--text-muted);
  }

  .complete {
    position: absolute;
    z-index: 30;
    list-style: none;
    margin: 0;
    padding: 3px;
    min-width: 220px;
    max-height: 200px;
    overflow-y: auto;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    box-shadow: 0 6px 18px var(--shadow-color);
  }

  .complete button {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    padding: 3px 6px;
    background: none;
    border: none;
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.78em;
    text-align: left;
    cursor: pointer;
  }

  .complete button.sel,
  .complete button:hover {
    background-color: var(--accent-subtle);
  }

  .status {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 0.74em;
    color: var(--text-muted);
  }

  .pill {
    padding: 1px 8px;
    border-radius: 10px;
    border: 1px solid var(--border-color);
  }

  .pill.ok { color: var(--success-color); border-color: var(--success-border); }
  .pill.warn { color: var(--warning-color); border-color: var(--warning-border); }
  .pill.err { color: var(--error-color); border-color: var(--error-border); }

  .problems {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 0.76em;
  }

  .problems-filter {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  /* The actions go to the right of the filters. */
  .sev-filter + .problems-action {
    margin-left: auto;
  }

  .sev-filter {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 1px 8px;
    border-radius: 10px;
    border: 1px solid var(--border-color);
    background: none;
    color: var(--text-muted);
    font: inherit;
    cursor: pointer;
  }

  .sev-filter[aria-pressed='true'].error { color: var(--error-color); border-color: var(--error-border); background: var(--error-subtle); }
  .sev-filter[aria-pressed='true'].warning { color: var(--warning-color); border-color: var(--warning-border); background: var(--warning-subtle); }
  .sev-filter[aria-pressed='true'].info { color: var(--text-color); background: var(--hover-overlay); }
  .sev-filter[aria-pressed='false'] { text-decoration: line-through; opacity: 0.7; }
  .sev-filter:disabled { opacity: 0.4; cursor: default; text-decoration: none; }

  .sev-filter .n {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }

  .problems-resize {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: center;
    height: 8px;
    margin: -2px 0;
    cursor: row-resize;
    touch-action: none;
  }

  .problems-resize .grip {
    width: 36px;
    height: 3px;
    border-radius: 2px;
    background: var(--border-color);
  }

  .problems-resize:hover .grip,
  .problems-resize.active .grip,
  .problems-resize:focus-visible .grip {
    background: var(--accent-color);
  }

  .problems-resize:focus-visible {
    outline: none;
  }

  .problems-action {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 8px;
    border-radius: 10px;
    border: 1px solid var(--border-color);
    background: none;
    color: var(--text-muted);
    font: inherit;
    cursor: pointer;
  }

  .problems-action:hover:not(:disabled) {
    color: var(--accent-color);
    border-color: var(--accent-color);
  }

  .problems-action:disabled {
    opacity: 0.4;
    cursor: default;
  }

  .problems-scroll {
    overflow-y: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .problems table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }

  .problems th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--bg-light-color);
    border-bottom: 1px solid var(--border-color);
    padding: 0;
    text-align: left;
  }

  .problems th button {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    width: 100%;
    padding: 3px 8px;
    background: none;
    border: none;
    color: var(--text-muted);
    font: inherit;
    font-weight: 600;
    text-align: left;
    cursor: pointer;
  }

  .problems th[aria-sort='ascending'] button,
  .problems th[aria-sort='descending'] button {
    color: var(--text-color);
  }

  .problems th button:focus-visible,
  .problems .jump:focus-visible {
    outline: 2px solid var(--accent-color);
    outline-offset: -2px;
  }

  .col-severity { width: 92px; }
  .col-line { width: 64px; }
  .col-code { width: 120px; }

  .problems tbody tr {
    position: relative;
    border-top: 1px solid var(--border-color);
  }

  .problems tbody tr:first-child {
    border-top: none;
  }

  .problems tbody tr:hover {
    background-color: var(--hover-overlay);
  }

  .problems td {
    padding: 3px 8px;
    vertical-align: baseline;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .jump {
    padding: 0;
    background: none;
    border: none;
    color: var(--text-color);
    font: inherit;
    text-align: left;
    cursor: pointer;
    overflow-wrap: anywhere;
  }

  .jump::after {
    content: '';
    position: absolute;
    inset: 0;
  }

  /* Prefixed: `.code` is the editor surface (position: relative, flex: 1),
     and a table cell given that name became one. */
  .problem-sev {
    text-transform: uppercase;
    font-size: 0.85em;
    font-weight: 600;
  }

  .problem-sev.error { color: var(--error-color); }
  .problem-sev.warning { color: var(--warning-color); }
  .problem-sev.info { color: var(--text-muted); }

  .problem-pos {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .problem-code {
    color: var(--text-dimmed);
    font-size: 0.9em;
    white-space: nowrap;
  }

  .problems .none {
    color: var(--text-muted);
    font-style: italic;
  }

  .col-actions { width: 52px; }

  /* Above the row's jump, whose ::after covers the row. */
  .problems td.col-actions {
    position: relative;
    z-index: 1;
    padding: 1px 4px;
    text-align: right;
    white-space: nowrap;
  }

  .row-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 18px;
    padding: 0;
    background: none;
    border: 1px solid transparent;
    border-radius: 3px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .row-action:hover,
  .row-action:focus-visible {
    color: var(--accent-color);
    border-color: var(--accent-color);
    outline: none;
  }

  .problems-muted {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    color: var(--text-muted);
  }

  .muted-chip {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    padding: 0 6px;
    border-radius: 10px;
    border: 1px dashed var(--border-color);
    background: none;
    color: var(--text-muted);
    font: inherit;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    cursor: pointer;
  }

  .muted-chip:hover,
  .muted-chip:focus-visible {
    color: var(--accent-color);
    border-color: var(--accent-color);
    outline: none;
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  .find-count.capped {
    color: var(--warning, #b7791f);
    cursor: help;
  }

  .btn-copy-small.active {
    background: var(--bg-hover, #e5e5e5);
    font-weight: 600;
  }
</style>
