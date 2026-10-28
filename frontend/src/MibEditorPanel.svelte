<script>
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from './Icon.svelte';
  import { notificationStore } from './stores/notifications';
  import { SNIPPETS } from './mibeditor/tokenize.js';
  import { withoutMuted, MUTABLE, FIXABLE } from './mibeditor/problems';
  import CodeEditor from './mibeditor/CodeEditor.svelte';
  import { mutedRules } from './stores/mutedRules';
  import { verdictMap } from './utils/mibVerdict';
  import { mibStatusStore } from './stores/mibStatusStore';
  import { mibEditorStore } from './stores/mibEditorStore';
  import FileActions from './mibeditor/FileActions.svelte';
  import FileList from './mibeditor/FileList.svelte';
  import Outline from './mibeditor/Outline.svelte';
  import ProblemTable from './mibeditor/ProblemTable.svelte';
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
    MibEditorEncodings,
    MibEditorReadAs,
    MibEditorReopenExternalAs,
  } from '../wailsjs/go/app/App';

  let files = [];
  let filter = '';
  // The files selected in the list, for the context menu's actions on several
  // (mibeditor/FileActions.svelte), and the one a Shift-click ranges from.
  let selected = [];
  let anchor = '';
  let fileActions;
  let saving = false;
  let showSnippets = false;
  let showSymbols = false;
  let showOutline = true;
  let symbolFilter = '';
  let catalogue = { modules: [], symbols: [] };
  // The editing surface (mibeditor/CodeEditor.svelte): CodeMirror, which
  // holds the undo history, the search and the completion.
  let editor;

  // The app ships on macOS too, where redo is Cmd+Shift+Z rather than Ctrl+Y.
  // A tooltip naming the wrong key teaches the wrong thing.
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');
  const UNDO_KEYS = isMac ? '⌘Z' : 'Ctrl+Z';
  const REDO_KEYS = isMac ? '⇧⌘Z' : 'Ctrl+Y';

  // Whether there is anything to undo or redo, as the surface reports it.
  let canUndo = false;
  let canRedo = false;

  $: lines = buffer.split('\n');
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

  // Every file's verdict, the open one judged as it is typed while it has
  // unsaved changes (utils/mibVerdict.js): the file list marks it, the file
  // actions read it.
  let onlyProblems = false;
  $: openName = source && !source.external ? source.name : '';
  $: statuses = verdictMap($mibStatusStore, openName, dirty ? $mibEditorStore.verdict : null);

  $: errorCount = listed.filter((d) => d.severity === 'error').length;
  $: warnCount = listed.filter((d) => d.severity === 'warning').length;

  // What is shown of the diagnostics once the rules this viewer muted are taken
  // out (stores/mutedRules.js): the table, the underlines and the counts read
  // this, and Go leaves the same findings out of the verdict.
  $: listed = withoutMuted(diagnostics, $mutedRules);

  // How tall the problem table may grow: never so tall that the editor has no
  // room left.
  let mainEl;
  const problemsMaxHeight = () => (mainEl?.clientHeight || 600) - 180;

  // The fix Go writes for a problem (pkg/mib/quickfix.go), into the buffer as
  // one edit, so it is undone as one. symbols empty fixes every one of a kind.
  async function quickFix(code, symbols) {
    try {
      const fix = await MibEditorQuickFix(buffer, code, symbols);
      if (!fix || !fix.content) return;
      replaceBuffer(fix.content);
      notificationStore.add(
        get(_)('mibEditor.problemTable.fixed', { values: { count: (fix.fixed || []).length } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  onMount(async () => {
    await refreshList();
    try {
      catalogue = (await MibEditorSymbols()) || { modules: [], symbols: [] };
    } catch (e) {
      catalogue = { modules: [], symbols: [] };
    }
    try {
      encodings = (await MibEditorEncodings()) || encodings;
    } catch (e) {
      /* the file's own encoding is still shown */
    }
  });

  // The encodings a MIB can be read as (pkg/mib/encodings.go). The guess —
  // UTF-8, else Windows-1252 — is wrong for a file written in Shift_JIS or GBK,
  // and its prose then reads as accented Latin; the status bar is where the
  // person who can read it says what it is. Saving always writes UTF-8.
  let encodings = ['utf-8', 'windows-1252'];
  const ENCODING_LABELS = {
    'utf-8': 'UTF-8', 'windows-1252': 'Windows-1252', 'iso-8859-1': 'ISO-8859-1', 'iso-8859-15': 'ISO-8859-15',
    'windows-1250': 'Windows-1250', 'windows-1251': 'Windows-1251', 'koi8-r': 'KOI8-R', shift_jis: 'Shift_JIS',
    'euc-jp': 'EUC-JP', gbk: 'GBK', gb18030: 'GB18030', big5: 'Big5', 'euc-kr': 'EUC-KR',
  };

  async function changeEncoding(e) {
    const select = e.currentTarget;
    const encoding = select.value;
    if (!source || encoding === source.encoding) return;
    if (!(await confirmDiscard())) {
      select.value = source.encoding;
      return;
    }
    try {
      const s = source.external ? await MibEditorReopenExternalAs(encoding) : await MibEditorReadAs(source.name, encoding);
      if (!s || !s.name) {
        select.value = source.encoding;
        return;
      }
      mibEditorStore.openSource(s);
      resetEditor();
      notificationStore.add(get(_)('mibEditor.encodingChanged',
        { values: { name: s.name, encoding: ENCODING_LABELS[encoding] || encoding } }), 'info');
    } catch (err) {
      select.value = source.encoding;
      notificationStore.add(String(err?.message || err), 'error');
    }
  }

  async function refreshList() {
    try {
      files = (await MibEditorList()) || [];
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
    // A file renamed or deleted leaves the selection with the list.
    selected = selected.filter((n) => files.some((f) => f.name === n));
    // The marks fill in after the list: the first analysis of a large vendor
    // directory takes a moment, and the names should not wait for it.
    mibStatusStore.refresh();
  }

  // Resolves whether the file is now the one open: the user may keep the
  // unsaved one instead, and what follows an open (a fix, a restored copy)
  // must not then be written into the wrong file.
  async function open(name) {
    if (!(await confirmDiscard())) return false;
    try {
      const { recovered } = await mibEditorStore.open(name);
      resetEditor();
      if (recovered) {
        notificationStore.add(get(_)('mibEditor.draftRecovered', { values: { name } }), 'warning');
      }
      return true;
    } catch (e) {
      notificationStore.add(String(e), 'error');
      return false;
    }
  }

  // The file renamed under the editor, opened again under its new name; its
  // unsaved buffer went with it as a draft and comes back from it.
  async function reopen(name) {
    try {
      await mibEditorStore.open(name);
      resetEditor();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // What an action writes into the buffer is one edit, which one Ctrl+Z takes
  // back.
  function replaceBuffer(text) {
    if (editor) editor.replaceAll(text);
    else mibEditorStore.setBuffer(text);
  }

  async function filesChanged() {
    await refreshList();
    await reload();
  }

  // Another document in the surface: a fresh history, so an undo never walks
  // back into the file that was open before. The buffer is read from the store
  // because the prop has not been updated yet at this point.
  function resetEditor() {
    editor?.reset(get(mibEditorStore).buffer);
  }

  async function openExternal() {
    if (!(await confirmDiscard())) return;
    try {
      const s = await MibEditorOpenExternal();
      if (!s || !s.name) return; // cancelled
      mibEditorStore.openSource(s);
      resetEditor();
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

  // Every edit in the surface goes to the store, which analyses it (in Go,
  // touching no file and no gosmi state) and keeps the draft.
  function onEditorChange(text) {
    mibEditorStore.setBuffer(text);
  }

  function onEditorHistory(h) {
    canUndo = h.canUndo;
    canRedo = h.canRedo;
  }

  function undoEdit() {
    editor?.undoEdit();
  }

  function redoEdit() {
    editor?.redoEdit();
  }

  // Ctrl+S in the surface, guarded like the button: two saves of one file race
  // on a fixed staging path in mib-temp/.
  function saveFromKeyboard() {
    if (!saving && savable) save();
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
      resetEditor();
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
    resetEditor();
  }

  // The one place the editor goes from "here is the problem" to "here is the
  // repair": the diagnostic knows the symbol, the catalogue knows its module.
  async function fixImports() {
    try {
      const fix = await MibEditorFixImports(buffer);
      if (!fix.content) return;
      // Replace the whole buffer as one edit so it stays undoable.
      replaceBuffer(fix.content);
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
      replaceBuffer(fix.content);
      notificationStore.add(
        get(_)('mibEditor.repaired', { values: { count: fix.fixes.length } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // Text typed in at the caret, as one edit: a snippet, a symbol.
  function insertAtCaret(text) {
    if (editor) editor.insert(text);
    else mibEditorStore.setBuffer(buffer + text);
  }

  // Clicking a problem or an outline entry puts the caret on it.
  function jumpTo(d) {
    if (d && d.line) editor?.jumpTo(d.line, d.column || 1);
  }

  // What the buffer defines. Derived in a reactive statement rather than in the
  // markup: Svelte 5 tracks what the EXPRESSION reads, and a store read inside
  // a callee is not a dependency it can see — reactive.test.mjs exists for
  // exactly that.
  $: outline = $mibEditorStore.outline || [];

  // Muting from a diagnostic's tooltip, as from the problem table.
  async function muteRule(code) {
    await mutedRules.mute(code);
    mibEditorStore.refresh();
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

    <FileList {files} {statuses} {filter} activeName={openName} bind:onlyProblems bind:selected bind:anchor
      on:open={(e) => open(e.detail)}
      on:menu={(e) => fileActions?.openMenu(e.detail.x, e.detail.y, e.detail.names)}
      on:listmenu={(e) => fileActions?.openListMenu(e.detail.x, e.detail.y)}
      on:rename={(e) => fileActions?.run('rename', [e.detail])}
      on:delete={(e) => fileActions?.run('delete', e.detail)} />

    <!-- The outline: what the buffer defines, to jump to (mibeditor/Outline.svelte). -->
    {#if source}
      <Outline {outline} bind:open={showOutline} on:jump={(e) => jumpTo(e.detail)} />
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
      {#if source.encoding && source.encoding !== 'utf-8'}
        <p class="banner">
          <Icon name="triangle-alert" size={13} />
          {$_('mibEditor.encodingWarning', { values: { encoding: ENCODING_LABELS[source.encoding] || source.encoding } })}
        </p>
      {/if}

      <div class="surface">
        <CodeEditor bind:this={editor} doc={buffer} diagnostics={listed} symbols={catalogue.symbols}
          fixable={FIXABLE} mutable={MUTABLE}
          onChange={onEditorChange} onHistory={onEditorHistory} onSave={saveFromKeyboard}
          onFix={quickFix} onMute={muteRule} />
      </div>

      <div class="status">
        <span>{$_('mibEditor.lines', { values: { count: lineCount } })}</span>
        <span>{source.eol === 'crlf' ? 'CRLF' : 'LF'}</span>
        <select class="encoding" value={source.encoding || 'utf-8'} on:change={changeEncoding}
          title={$_('mibEditor.encodingHint')} aria-label={$_('mibEditor.encodingLabel')}>
          {#each encodings as enc (enc)}
            <option value={enc}>{ENCODING_LABELS[enc] || enc}</option>
          {/each}
        </select>
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
        <ProblemTable {diagnostics} {listed} fileName={source.name} maxHeight={problemsMaxHeight}
          on:jump={(e) => jumpTo(e.detail)}
          on:fix={(e) => quickFix(e.detail.code, e.detail.symbols)}
          on:muted={() => mibEditorStore.refresh()} />
      {/if}
    {/if}
  </section>
</div>

<FileActions bind:this={fileActions} {files} verdicts={statuses}
  onOpen={open} onReopen={reopen} onReplaceBuffer={replaceBuffer} onRestoreOpen={restoreBundled}
  onChanged={filesChanged} onRefresh={refreshList} />

<style>
  .mib-editor {
    display: grid;
    grid-template-columns: 230px minmax(0, 1fr);
    gap: var(--space-sm);
    height: 100%;
    min-height: 0;
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

  /* The editing surface: CodeMirror fills it (mibeditor/CodeEditor.svelte). */
  .surface {
    flex: 1;
    min-height: 0;
    display: flex;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    overflow: hidden;
  }

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

  .status {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 0.74em;
    color: var(--text-muted);
  }

  /* Reads as the status bar's text until it is pointed at. */
  .encoding {
    padding: 0 2px;
    border: 1px solid transparent;
    border-radius: 3px;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  .encoding:hover,
  .encoding:focus-visible {
    border-color: var(--border-color);
    color: var(--text-color);
  }

  .pill {
    padding: 1px 8px;
    border-radius: 10px;
    border: 1px solid var(--border-color);
  }

  .pill.ok { color: var(--success-color); border-color: var(--success-border); }
  .pill.warn { color: var(--warning-color); border-color: var(--warning-border); }
  .pill.err { color: var(--error-color); border-color: var(--error-border); }

</style>
