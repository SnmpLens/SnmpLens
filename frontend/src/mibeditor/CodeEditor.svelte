<script>
  import { onMount, onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { EditorState, Annotation, Transaction } from '@codemirror/state';
  import {
    EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection,
    highlightSpecialChars, hoverTooltip, dropCursor, rectangularSelection, crosshairCursor,
  } from '@codemirror/view';
  import { history, historyKeymap, defaultKeymap, undo, redo, undoDepth, redoDepth, toggleComment } from '@codemirror/commands';
  import { search, searchKeymap, highlightSelectionMatches, openSearchPanel } from '@codemirror/search';
  import { autocompletion } from '@codemirror/autocomplete';
  import { lintGutter, setDiagnostics, lintKeymap } from '@codemirror/lint';
  import { bracketMatching } from '@codemirror/language';
  import { smiLanguage, smiHighlight, inProse } from './smiLanguage.js';
  import { toCodeMirror } from './cmDiagnostics.js';

  // The MIB editor's editing surface: CodeMirror 6.
  //
  // It replaced a transparent <textarea> laid over a coloured copy of the
  // text, kept aligned to the pixel by arithmetic, and every bug the surface
  // had was that alignment failing: underlines 8 px up and left, gone after a
  // screen of scrolling, placed from a character width measured before any
  // file was open, a line too low after a CR. Here the text drawn IS the text
  // edited, and CodeMirror places what decorates it.
  //
  // The buffer stays in mibEditorStore — this component is destroyed on every
  // tab switch — and every edit is handed to the panel (onChange), which puts
  // it there. Undo, search, completion and the diagnostics' underlines are
  // CodeMirror's. What a diagnostic says is Go's (MibEditorAnalyse), shown
  // through @codemirror/lint with the fixes the panel can write as actions.

  /** The text, as the store holds it. */
  export let doc = '';
  /** The diagnostics to show: the analysis's, muted rules taken out. */
  export let diagnostics = [];
  /** The loaded tree's symbols (MibEditorSymbols): completion and hover. */
  export let symbols = [];
  /** Codes the panel can write a fix for, and codes that may be muted. */
  export let fixable = new Set();
  export let mutable = new Set();
  export let onChange = () => {};
  export let onSave = () => {};
  export let onFix = () => {};
  export let onMute = () => {};
  export let onHistory = () => {};

  let host;
  let view = null;
  // The text last handed to the store, or taken from it: what tells an echo
  // of our own edit from a change made elsewhere.
  let lastDoc = doc;
  // Marks a transaction that brings the store's text in, which is not an edit
  // to hand back to it.
  const fromStore = Annotation.define();

  const t = (key, values) => get(_)(key, values ? { values } : undefined);

  // CodeMirror's own words, in the application's language.
  function phrases() {
    return {
      Find: t('mibEditor.cm.find'), Replace: t('mibEditor.cm.replace'),
      next: t('mibEditor.cm.next'), previous: t('mibEditor.cm.previous'), all: t('mibEditor.cm.all'),
      'match case': t('mibEditor.cm.matchCase'), 'by word': t('mibEditor.cm.byWord'),
      regexp: t('mibEditor.cm.regexp'), replace: t('mibEditor.cm.replaceOne'),
      'replace all': t('mibEditor.cm.replaceAll'), close: t('mibEditor.cm.close'),
      'Go to line': t('mibEditor.cm.goToLine'), go: t('mibEditor.cm.go'),
      Diagnostics: t('mibEditor.cm.diagnostics'), 'No diagnostics': t('mibEditor.cm.noDiagnostics'),
    };
  }

  // A tab is four spaces, as the editor always wrote it: a MIB indented with
  // tabs draws a warning of its own.
  function insertSpaces(v) {
    v.dispatch(v.state.replaceSelection('    '), { userEvent: 'input' });
    return true;
  }

  // Completion from the loaded tree, never inside a string or a comment: a
  // symbol name there is prose, and Enter — pressed to start a line — would
  // accept a suggestion instead.
  function symbolCompletions(context) {
    const word = context.matchBefore(/[A-Za-z][A-Za-z0-9_-]*/);
    if (!word || (word.to - word.from < 2 && !context.explicit)) return null;
    if (inProse(context.state, word.from)) return null;
    const prefix = word.text.toLowerCase();
    const options = [];
    for (const sy of symbols) {
      if (sy.name !== word.text && sy.name.toLowerCase().startsWith(prefix)) {
        options.push({ label: sy.name, detail: sy.module, info: sy.description || undefined, type: 'variable' });
        if (options.length >= 50) break;
      }
    }
    return options.length ? { from: word.from, options, validFor: /^[A-Za-z0-9_-]*$/ } : null;
  }

  // The symbol under the pointer, looked up in the loaded tree. Built with
  // text nodes: a description comes from a MIB somebody else wrote.
  const symbolHover = hoverTooltip((v, pos) => {
    const w = v.state.wordAt(pos);
    if (!w) return null;
    const word = v.state.sliceDoc(w.from, w.to);
    if (word.length < 2) return null;
    const sy = symbols.find((s) => s.name === word);
    if (!sy) return null;
    return {
      pos: w.from,
      end: w.to,
      create() {
        const dom = document.createElement('div');
        dom.className = 'cm-symbol-card';
        const head = dom.appendChild(document.createElement('div'));
        head.className = 'cm-symbol-head';
        head.appendChild(document.createElement('code')).textContent = sy.name;
        const mod = head.appendChild(document.createElement('span'));
        mod.className = 'cm-symbol-module';
        mod.textContent = sy.module;
        if (sy.oid) {
          const oid = dom.appendChild(document.createElement('div'));
          oid.className = 'cm-symbol-oid';
          oid.textContent = sy.oid;
        }
        if (sy.description) dom.appendChild(document.createElement('p')).textContent = sy.description;
        return { dom };
      },
    };
  });

  const theme = EditorView.theme({
    '&': { height: '100%', fontSize: '12px', backgroundColor: 'var(--bg-color)', color: 'var(--text-color)' },
    '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', lineHeight: '18px' },
    '.cm-content': { caretColor: 'var(--text-color)' },
    '&.cm-focused': { outline: 'none' },
    '&.cm-focused .cm-cursor': { borderLeftColor: 'var(--text-color)' },
    '.cm-gutters': {
      backgroundColor: 'var(--bg-light-color)', color: 'var(--text-dimmed)', borderRight: '1px solid var(--border-color)',
    },
    '.cm-activeLine': { backgroundColor: 'var(--hover-overlay)' },
    '.cm-activeLineGutter': { backgroundColor: 'var(--hover-overlay)', color: 'var(--text-color)' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
      { backgroundColor: 'var(--accent-subtle)' },
    '.cm-selectionMatch': { backgroundColor: 'var(--hover-overlay)' },
    '.cm-searchMatch': { backgroundColor: 'var(--warning-subtle)', outline: '1px solid var(--warning-border)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--accent-subtle)' },
    '.cm-panels': { backgroundColor: 'var(--bg-light-color)', color: 'var(--text-color)' },
    '.cm-panels-top': { borderBottom: '1px solid var(--border-color)' },
    '.cm-panels-bottom': { borderTop: '1px solid var(--border-color)' },
    '.cm-textfield': {
      backgroundColor: 'var(--bg-lighter-color)', color: 'var(--text-color)', border: '1px solid var(--border-color)',
      borderRadius: '4px', fontSize: '12px',
    },
    '.cm-button': {
      backgroundImage: 'none', backgroundColor: 'var(--bg-lighter-color)', color: 'var(--text-color)',
      border: '1px solid var(--border-color)', borderRadius: '4px', fontSize: '12px',
    },
    '.cm-panel.cm-search label': { fontSize: '12px' },
    '.cm-tooltip': {
      backgroundColor: 'var(--bg-lighter-color)', color: 'var(--text-color)', border: '1px solid var(--border-color)',
      borderRadius: '6px',
    },
    '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent-color)', color: 'white' },
    '.cm-completionDetail': { color: 'var(--text-muted)', marginLeft: '8px' },
    '.cm-symbol-card': { maxWidth: '420px', padding: '6px 8px', fontSize: '12px' },
    '.cm-symbol-head': { display: 'flex', gap: '8px', alignItems: 'baseline' },
    '.cm-symbol-module': { color: 'var(--text-muted)' },
    '.cm-symbol-oid': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', color: 'var(--text-muted)' },
    '.cm-symbol-card p': { margin: '4px 0 0', whiteSpace: 'pre-wrap' },
    '.cm-diagnostic': { fontSize: '12px' },
    '.cm-diagnosticAction': { backgroundColor: 'var(--accent-color)', borderRadius: '3px' },
  });

  function makeState(text) {
    return EditorState.create({
      doc: text,
      extensions: [
        EditorState.phrases.of(phrases()),
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightSpecialChars(),
        history(),
        drawSelection(),
        dropCursor(),
        EditorState.allowMultipleSelections.of(true),
        EditorState.tabSize.of(4),
        rectangularSelection(),
        crosshairCursor(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        bracketMatching(),
        smiLanguage,
        smiHighlight,
        search({ top: true }),
        autocompletion({ override: [symbolCompletions] }),
        symbolHover,
        lintGutter(),
        keymap.of([
          { key: 'Mod-s', preventDefault: true, run: () => { onSave(); return true; } },
          { key: 'Tab', run: insertSpaces },
          { key: 'Mod-/', run: toggleComment },
          ...defaultKeymap,
          ...searchKeymap,
          ...historyKeymap,
          ...lintKeymap,
        ]),
        EditorView.contentAttributes.of({ spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off' }),
        theme,
        EditorView.updateListener.of((u) => {
          if (u.docChanged && !u.transactions.some((tr) => tr.annotation(fromStore))) {
            const text = u.state.doc.toString();
            lastDoc = text;
            onChange(text);
          }
          if (u.docChanged || u.transactions.length) {
            onHistory({ canUndo: undoDepth(u.state) > 0, canRedo: redoDepth(u.state) > 0 });
          }
        }),
      ],
    });
  }

  function actionsFor(d) {
    const actions = [];
    if (fixable.has(d.code) && d.symbol) {
      actions.push({ name: t('mibEditor.problemTable.fix'), apply: () => onFix(d.code, [d.symbol]) });
    }
    if (mutable.has(d.code) && d.severity !== 'error') {
      actions.push({ name: t('mibEditor.cm.mute', { code: d.code }), apply: () => onMute(d.code) });
    }
    return actions;
  }

  function showDiagnostics(list) {
    if (view) view.dispatch(setDiagnostics(view.state, toCodeMirror(view.state, list, actionsFor)));
  }

  $: if (view) showDiagnostics(diagnostics);

  // The store's text, when it changed other than by an edit here.
  $: if (view && doc !== lastDoc) takeFromStore(doc);

  function takeFromStore(text) {
    lastDoc = text;
    if (text === view.state.doc.toString()) return;
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: text },
      annotations: [fromStore.of(true), Transaction.addToHistory.of(false)],
    });
  }

  onMount(() => {
    view = new EditorView({ state: makeState(doc), parent: host });
    lastDoc = doc;
    showDiagnostics(diagnostics);
  });

  onDestroy(() => view?.destroy());

  /** Another document: a fresh state, and no history to undo into the last one. */
  export function reset(text) {
    if (!view) return;
    lastDoc = text;
    view.setState(makeState(text));
    showDiagnostics(diagnostics);
    onHistory({ canUndo: false, canRedo: false });
  }

  /** Text typed in at the caret, as one edit. */
  export function insert(text) {
    if (!view) return;
    const { from, to } = view.state.selection.main;
    view.dispatch({
      changes: { from, to, insert: text },
      selection: { anchor: from + text.length },
      scrollIntoView: true,
      userEvent: 'input',
    });
    view.focus();
  }

  /**
   * The whole buffer replaced by text, as ONE edit that one Ctrl+Z takes back
   * — a fix, a repair, a restored copy. Only what differs is replaced, so the
   * caret and the scroll stay where they were.
   */
  export function replaceAll(text) {
    if (!view) return;
    const current = view.state.doc.toString();
    if (current === text) return;
    const max = Math.min(current.length, text.length);
    let head = 0;
    while (head < max && current.charCodeAt(head) === text.charCodeAt(head)) head++;
    let tail = 0;
    while (tail < max - head && current.charCodeAt(current.length - 1 - tail) === text.charCodeAt(text.length - 1 - tail)) tail++;
    view.dispatch({
      changes: { from: head, to: current.length - tail, insert: text.slice(head, text.length - tail) },
      userEvent: 'input.replace',
    });
  }

  export function undoEdit() {
    if (view) undo(view);
  }

  export function redoEdit() {
    if (view) redo(view);
  }

  /** The caret on a line and column (1-based), that line brought into view. */
  export function jumpTo(line, column = 1) {
    if (!view) return;
    const text = view.state.doc;
    const l = text.line(Math.min(Math.max(1, line), text.lines));
    const pos = l.from + Math.min(Math.max(0, column - 1), l.length);
    view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
    view.focus();
  }

  export function find() {
    if (view) openSearchPanel(view);
  }
</script>

<div class="code-editor" bind:this={host}></div>

<style>
  .code-editor {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
  }

  .code-editor :global(.cm-editor) {
    flex: 1;
    min-width: 0;
  }
</style>
