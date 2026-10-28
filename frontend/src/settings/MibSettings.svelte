<script>
  import { onMount } from 'svelte';
  import { mibEditorStore } from '../stores/mibEditorStore';
  import { requestTab } from '../stores/tabRequest';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { mibStore, mibDiagnostics } from '../stores/mibStore';
  import { mibPathsStore } from '../stores/mibPathsStore';
  import { notificationStore } from '../stores/notifications';
  import Icon from '../Icon.svelte';
  import MibDiagnosis from '../mib/MibDiagnosis.svelte';
  import { MibDiagnose, BrowseDialog, ListMibFiles, MibDependencyGraph, OpenFolder } from '../../wailsjs/go/app/App';
  import { dependencyRoll, symbolsWanted, ABSENT, FAILED } from '../utils/mibDependencies';
  import { byVerdict, countVerdicts, LEVELS, ICONS as VERDICT_ICONS } from '../utils/mibVerdict';
  import { mibStatusStore } from '../stores/mibStatusStore';
  import {
    rootsOf, treeFrom, flattenTree, graphSummary, MISSING, NOT_LOADED,
  } from '../utils/mibGraph';

  export let defaultMibPath;

  // Rolled the other way round: one row per MISSING MODULE instead of one per
  // broken file. A single absent VENDOR-SMI fails twelve files and reads as
  // twelve unrelated problems in the per-file list below.
  //
  // Derived here rather than in the markup: reactive.test.mjs exists because
  // Svelte 5 tracks what the EXPRESSION reads, and a function reaching for a
  // store inside itself is not a dependency it can see.
  $: roll = dependencyRoll($mibDiagnostics);
  // Every loaded file with the verdict the editor's list and the import report
  // show too, decided in Go (pkg/mib/verdict.go): the load says whether the
  // file is in the tree, the editor's analysis whether what is there is right.
  // Counting the load alone said "26 loaded, 0 failed" beside a list marking
  // files in error. Worst first: the files to look at were somewhere among
  // two hundred that were fine.
  $: loadResults = verdictRows($mibStatusStore);
  $: verdictCounts = countVerdicts(loadResults.map((r) => r.v));

  function verdictRows(statuses) {
    return (Array.isArray(statuses) ? statuses : [])
      .filter((s) => s && s.loaded && s.verdict)
      .map((s) => ({ name: s.name, v: s.verdict }))
      .sort((a, b) => byVerdict(a.v, b.v) || a.name.localeCompare(b.name));
  }

  let newMibPath = '';

  // The dependency graph: what imports what, rather than what failed.
  //
  // The roll above answers "which modules are missing and who wants them",
  // which is the question after a load goes wrong. This one answers the
  // question somebody has in front of a folder from a vendor — what does THIS
  // file need, and what does that need in turn — and it is cheap enough to
  // offer: the Go side reads the head of each file as text and stops at the
  // semicolon ending the IMPORTS clause. No parse, no gosmi, no exclusive lock
  // held across the directory.
  let graph = [];
  let graphOpen = false;
  let graphRoot = '';
  let graphLoading = false;

  async function loadGraph() {
    graphLoading = true;
    try {
      graph = (await MibDependencyGraph()) || [];
      if (!graph.some((n) => n.module === graphRoot)) {
        graphRoot = rootsOf(graph)[0]?.module || '';
      }
    } catch (e) {
      console.error('MibDependencyGraph failed', e);
      graph = [];
    } finally {
      graphLoading = false;
    }
  }

  function toggleGraph() {
    graphOpen = !graphOpen;
    if (graphOpen && graph.length === 0) loadGraph();
  }

  // Derived rather than computed in the markup: Svelte 5 tracks what the
  // EXPRESSION reads, and a call reaching for `graph` inside itself is not a
  // dependency it can see. reactive.test.mjs exists for exactly that.
  $: graphRoots = rootsOf(graph);
  $: graphStats = graphSummary(graph);
  $: graphRows = graphRoot ? flattenTree(treeFrom(graph, graphRoot)) : [];

  onMount(() => {
    mibPathsStore.load();
    if (defaultMibPath) {
      scanPathForMibs(defaultMibPath);
    }
  });

  async function handleBrowseFolder() {
    try {
      const selectedPath = await BrowseDialog();
      if (selectedPath && selectedPath.trim()) {
        newMibPath = selectedPath.trim();
      }
    } catch (e) {
      console.error('Failed to open directory picker:', e);
      const path = prompt('Enter the full path to your MIB directory:');
      if (path && path.trim()) {
        newMibPath = path.trim();
      }
    }
  }

  async function handleAddPath() {
    if (newMibPath && newMibPath.trim()) {
      mibPathsStore.addPath(newMibPath.trim());
      await scanPathForMibs(newMibPath.trim());
      newMibPath = '';
    }
  }

  async function scanPathForMibs(path) {
    try {
      const mibs = await ListMibFiles(path);
      mibPathsStore.setDetectedMibs(path, mibs);
    } catch (e) {
      console.error('Failed to scan path for MIBs:', e);
      mibPathsStore.setDetectedMibs(path, []);
    }
  }

  // Shown in the system's file manager: the quickest way to drop a vendor MIB
  // in, take a backup, or see what the list above is reading.
  async function openFolder(path) {
    if (!path) return;
    try {
      await OpenFolder(path);
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function handleRemovePath(path) {
    const t = get(_);
    if (confirm(t('settings.mibs.removeConfirm', { values: { path } }))) {
      mibPathsStore.removePath(path);
    }
  }

  // Jump straight from "this MIB failed to load" to the editor showing where.
  async function openInEditor(fileName) {
    // Ask, like the editor's own file list does. This path went straight to
    // the store, so unsaved work was replaced with no prompt. It is now also
    // flushed to a draft rather than dropped, but replacing someone's buffer
    // without asking is still a surprise.
    if (mibEditorStore.dirty() && !window.confirm(get(_)('mibEditor.discardConfirm'))) {
      return;
    }
    try {
      await mibEditorStore.open(fileName);
    } catch (e) {
      notificationStore.add(String(e), 'error');
      return;
    }
    requestTab('mibeditor');
  }
  // A diagnosis on demand. Not run for every file on every load: it re-reads
  // and re-parses, and a directory of two hundred vendor MIBs would pay that
  // cost for the one someone is actually looking at.
  let diagnosis = null;
  let diagnosisFor = '';
  let diagnosing = '';
  // Which request the state on screen belongs to. Only one file's Diagnose
  // button is disabled at a time, so two can overlap — and without this the
  // slower one lands last and replaces the answer the user asked for second.
  let diagnosisToken = 0;

  async function runDiagnosis(fileName) {
    const token = ++diagnosisToken;
    diagnosing = fileName;
    try {
      const result = await MibDiagnose(fileName);
      if (token !== diagnosisToken) return;
      diagnosis = result;
      diagnosisFor = fileName;
    } catch (e) {
      if (token !== diagnosisToken) return;
      diagnosis = { stage: '', summary: String(e?.message || e), fileName };
      diagnosisFor = fileName;
    } finally {
      if (token === diagnosisToken) diagnosing = '';
    }
  }

</script>

<fieldset>
  <legend><Icon name="folder" size={15} /> {$_('settings.mibs.directoriesTitle')}</legend>

  <!-- Default MIB Path -->
  <div class="mib-path-item default">
    <div class="path-header">
      <span class="path-badge">{$_('settings.mibs.default')}</span>
      <span class="path-text" title={defaultMibPath}>{defaultMibPath || $_('common.loading')}</span>
      <button class="btn-small btn-folder" on:click={() => openFolder(defaultMibPath)} disabled={!defaultMibPath}
        title={$_('settings.mibs.openFolderHint')}>
        <Icon name="folder-open" size={14} /> {$_('settings.mibs.openFolder')}
      </button>
    </div>
    {#if $mibPathsStore.detectedMibs[defaultMibPath]}
      <div class="mib-list">
        <div class="mib-list-header">
          <span>{$_('settings.mibs.mibsDetected', { values: { count: $mibPathsStore.detectedMibs[defaultMibPath].length } })}</span>
          <div class="mib-actions">
            <button class="btn-small" on:click={() => mibPathsStore.enableAllInPath(defaultMibPath)}>{$_('common.enableAll')}</button>
            <button class="btn-small" on:click={() => mibPathsStore.disableAllInPath(defaultMibPath)}>{$_('common.disableAll')}</button>
          </div>
        </div>
        <div class="mib-items">
          {#each $mibPathsStore.detectedMibs[defaultMibPath] as mib}
            <label class="mib-item">
              <input
                type="checkbox"
                checked={$mibPathsStore.enabledMibs[defaultMibPath]?.[mib] !== false}
                on:change={() => mibPathsStore.toggleMib(defaultMibPath, mib)}
              />
              <span>{mib}</span>
            </label>
          {/each}
        </div>
      </div>
    {/if}
  </div>

  <!-- Custom MIB Paths -->
  {#each $mibPathsStore.customPaths as customPath}
    <div class="mib-path-item">
      <div class="path-header">
        <span class="path-badge custom">{$_('settings.mibs.custom')}</span>
        <span class="path-text" title={customPath}>{customPath}</span>
        <button class="btn-small btn-folder" on:click={() => openFolder(customPath)} title={$_('settings.mibs.openFolderHint')}>
          <Icon name="folder-open" size={14} /> {$_('settings.mibs.openFolder')}
        </button>
        <button class="btn-remove" on:click={() => handleRemovePath(customPath)} title={$_('common.remove')}><Icon name="x" size={14} /></button>
      </div>
      {#if $mibPathsStore.detectedMibs[customPath]}
        <div class="mib-list">
          <div class="mib-list-header">
            <span>{$_('settings.mibs.mibsDetected', { values: { count: $mibPathsStore.detectedMibs[customPath].length } })}</span>
            <div class="mib-actions">
              <button class="btn-small" on:click={() => mibPathsStore.enableAllInPath(customPath)}>{$_('common.enableAll')}</button>
              <button class="btn-small" on:click={() => mibPathsStore.disableAllInPath(customPath)}>{$_('common.disableAll')}</button>
            </div>
          </div>
          <div class="mib-items">
            {#each $mibPathsStore.detectedMibs[customPath] as mib}
              <label class="mib-item">
                <input
                  type="checkbox"
                  checked={$mibPathsStore.enabledMibs[customPath]?.[mib] !== false}
                  on:change={() => mibPathsStore.toggleMib(customPath, mib)}
                />
                <span>{mib}</span>
              </label>
            {/each}
          </div>
        </div>
      {/if}
    </div>
  {/each}

  <!-- Add New Path -->
  <div class="add-path-section">
    <h4>{$_('settings.mibs.addCustomTitle')}</h4>
    <div class="add-path-form">
      <input
        type="text"
        placeholder={$_('settings.mibs.addPlaceholder')}
        bind:value={newMibPath}
        on:keydown={(e) => e.key === 'Enter' && handleAddPath()}
      />
      <button class="btn secondary" on:click={handleBrowseFolder}><Icon name="folder-open" size={15} /> {$_('common.browse')}</button>
      <button class="btn" on:click={handleAddPath} disabled={!newMibPath.trim()}><Icon name="plus" size={15} /> {$_('common.add')}</button>
    </div>
  </div>

  <!-- Reload All MIBs -->
  <div class="reload-section">
    <button
      class="btn"
      on:click={() => mibStore.load()}
      disabled={$mibStore.isLoading}
      style="width: 100%;"
    >
      {#if $mibStore.isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('settings.mibs.reloadingAll')}{:else}<Icon name="refresh-cw" /> {$_('settings.mibs.reloadAll')}{/if}
    </button>
    <small class="mib-empty-text">
      {$_('settings.mibs.reloadHint')}
    </small>
  </div>

  <!-- What is missing across the whole directory, before the per-file list -->
  {#if roll.missing.length > 0}
    <div class="diagnostics-section">
      <h4>{$_('settings.mibs.dependenciesTitle')}</h4>

      {#each roll.missing as m (m.module)}
        <div class="dep-row" class:dep-absent={m.reason === ABSENT}>
          <div class="dep-head">
            <span class="dep-module">{m.module}</span>
            <span class="dep-reason">
              {#if m.reason === ABSENT}{$_('settings.mibs.depAbsent')}
              {:else if m.reason === FAILED}{$_('settings.mibs.depFailed')}
              {:else}{$_('settings.mibs.depNotLoaded')}{/if}
            </span>
            <span class="dep-count">{$_('settings.mibs.depNeededBy', { values: { count: m.neededBy.length } })}</span>
          </div>
          {#if symbolsWanted(m).length > 0}
            <div class="dep-symbols">{$_('settings.mibs.depSymbols', { values: { symbols: symbolsWanted(m).join(', ') } })}</div>
          {/if}
          {#if m.cause}
            <div class="dep-cause">{m.cause}</div>
          {/if}
          <div class="dep-files">{m.neededBy.map((n) => n.file).join(', ')}</div>
        </div>
      {/each}

    </div>
  {/if}

  <!-- What imports what. Closed by default: it is the question you ask
       deliberately, not the one the panel should answer unprompted. -->
  <div class="graph-section">
    <button class="graph-head" on:click={toggleGraph} aria-expanded={graphOpen}>
      <Icon name={graphOpen ? 'chevron-down' : 'chevron-right'} size={13} />
      {$_('settings.mibs.graphTitle')}
    </button>

    {#if graphOpen}
      {#if graphLoading}
        <p class="hint">{$_('common.loading')}</p>
      {:else if graph.length === 0}
        <p class="hint">{$_('settings.mibs.graphEmpty')}</p>
      {:else}
        <div class="graph-controls">
          <select bind:value={graphRoot}>
            {#each graphRoots as r (r.module)}
              <option value={r.module}>{r.module}</option>
            {/each}
          </select>
          <button class="btn-copy-small" on:click={loadGraph} title={$_('mibEditor.refresh')}>
            <Icon name="refresh-cw" size={13} />
          </button>
          <span class="hint">
            {$_('settings.mibs.graphSummary', {
              values: { modules: graphStats.modules, roots: graphStats.roots, missing: graphStats.missing },
            })}
          </span>
        </div>

        <ul class="graph-tree">
          {#each graphRows as row, i (row.module + ':' + i)}
            <li class="graph-row status-{row.status}" style="padding-left:{row.depth * 1.1}rem">
              <span class="graph-module">{row.module}</span>
              {#if row.status === MISSING}
                <span class="graph-tag missing">{$_('settings.mibs.graphMissing')}</span>
              {:else if row.status === NOT_LOADED}
                <span class="graph-tag">{$_('settings.mibs.graphNotLoaded')}</span>
              {/if}
              {#if row.cycle}
                <span class="graph-tag cycle">{$_('settings.mibs.graphCycle')}</span>
              {:else if row.repeated}
                <span class="graph-tag">{$_('settings.mibs.graphRepeated')}</span>
              {/if}
              {#if row.file && row.file !== row.module}
                <span class="graph-file">{row.file}</span>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>

  <!-- MIB Diagnostics -->
  {#if $mibDiagnostics.length > 0}
    <div class="diagnostics-section">
      <h4>{$_('settings.mibs.diagnosticsTitle')}</h4>
      <div class="diagnostics-summary">
        {#each LEVELS as level}
          {#if verdictCounts[level] > 0 || level === 'ok'}
            <span class="diag-count {level}">
              <Icon name={VERDICT_ICONS[level]} size={13} />
              {$_('settings.mibs.verdict.' + level, { values: { count: verdictCounts[level] } })}
            </span>
          {/if}
        {/each}
      </div>
      <p class="diag-legend">{$_('settings.mibs.verdictHint')}</p>
      <div class="diagnostics-list">
        {#each loadResults as row (row.name)}
          <div class="diag-item {row.v.level}">
            <span class="diag-icon {row.v.level}"><Icon name={VERDICT_ICONS[row.v.level]} size={14} /></span>
            <span class="diag-filename" title={row.name}>{row.name}</span>
            {#if row.v.level !== 'ok'}
              <!-- What is wrong, and the way to it: the editor is where the
                   line number lives. -->
              <span class="diag-msg {row.v.level}" title={row.v.detail}>
                <strong>{$_('mibVerdict.' + row.v.level, { values: { count: row.v.count } })}</strong>{#if row.v.detail} — {row.v.detail}{/if}
              </span>
              <button class="diag-open" on:click={() => openInEditor(row.name)}>
                {$_('settings.mibs.openInEditor')}
              </button>
            {/if}
            <!-- Offered on SUCCESSES too: a MIB whose IMPORTS cannot be
                 satisfied loads with no error at all and resolves to nothing,
                 so "it loaded" is exactly when this is worth asking. -->
            <button class="diag-open" on:click={() => runDiagnosis(row.name)} disabled={diagnosing === row.name}>
              {diagnosing === row.name ? $_('settings.mibs.diagnosing') : $_('settings.mibs.diagnose')}
            </button>
          </div>
          {#if diagnosisFor === row.name && diagnosis}
            <MibDiagnosis {diagnosis} fileName={row.name} />
          {/if}
        {/each}
      </div>
    </div>
  {/if}
</fieldset>

<style>
  .graph-section {
    margin-top: 1rem;
    border-top: 1px solid var(--border-color);
    padding-top: 0.6rem;
  }

  .graph-head {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.2rem 0;
    background: none;
    border: none;
    color: var(--text-muted);
    font: inherit;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    cursor: pointer;
  }

  .graph-controls {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.4rem 0;
    flex-wrap: wrap;
  }

  .graph-tree {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 320px;
    overflow: auto;
    font-size: 0.78rem;
  }

  .graph-row {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
    padding: 0.1rem 0.3rem;
    /* The indent IS the edge: a MIB has no other structure to show, and a line
       from parent to child would be a drawing where a margin says the same. */
    border-left: 2px solid transparent;
    white-space: nowrap;
  }

  .graph-row.status-missing {
    border-left-color: var(--error-color, #f85149);
  }

  .graph-row.status-notloaded {
    border-left-color: var(--warning-color, #d29922);
  }

  .graph-module {
    font-family: var(--font-mono, monospace);
  }

  .graph-row.status-missing .graph-module {
    color: var(--error-color, #f85149);
  }

  .graph-tag {
    padding: 0 0.3rem;
    border-radius: 3px;
    background-color: var(--bg-lighter-color);
    color: var(--text-muted);
    font-size: 0.68rem;
  }

  .graph-tag.missing {
    background-color: var(--error-subtle, var(--bg-lighter-color));
    color: var(--error-color, var(--text-muted));
  }

  .graph-tag.cycle {
    background-color: var(--warning-subtle, var(--bg-lighter-color));
    color: var(--warning-color, var(--text-muted));
  }

  .graph-file {
    color: var(--text-muted);
    font-size: 0.7rem;
  }

  .hint {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .dep-row {
    border-left: 2px solid var(--border-color);
    padding: 0.4rem 0 0.4rem 0.6rem;
    margin-bottom: 0.5rem;
  }
  .dep-row.dep-absent { border-left-color: var(--error-color, #d2544f); }
  .dep-head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.5rem;
  }
  .dep-module { font-family: var(--font-mono, monospace); font-weight: 600; }
  .dep-reason { font-size: 0.78rem; opacity: 0.85; }
  .dep-count { font-size: 0.78rem; opacity: 0.7; margin-left: auto; }
  .dep-symbols, .dep-cause, .dep-files {
    font-size: 0.78rem;
    opacity: 0.75;
    margin-top: 0.2rem;
    word-break: break-word;
  }
  .dep-files { font-family: var(--font-mono, monospace); opacity: 0.6; }

  .diag-open {
    background: none;
    border: 1px solid var(--border-color);
    border-radius: 3px;
    color: var(--accent-color);
    font-size: 0.9em;
    padding: 1px 6px;
    cursor: pointer;
    white-space: nowrap;
  }

  .diag-open:hover {
    border-color: var(--accent-color);
  }

  fieldset {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 20px;
    margin-bottom: 20px;
    /* A fieldset is min-width: min-content by default, in every browser: one
       long load error that does not wrap made the whole settings page as wide
       as the sentence, and the dialog — which does not grow — scrolled
       sideways. */
    min-width: 0;
  }

  legend {
    padding: 0 10px;
    color: var(--text-color);
    font-weight: 500;
    font-size: 1.1em;
  }

  .mib-path-item {
    margin-bottom: 20px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    overflow: hidden;
  }

  .mib-path-item.default {
    border-color: var(--favorites-border);
  }

  .path-header {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px;
    background-color: var(--hover-overlay);
    border-bottom: 1px solid var(--border-color);
  }

  .path-badge {
    padding: 4px 10px;
    border-radius: 4px;
    font-size: 0.8em;
    font-weight: 600;
    text-transform: uppercase;
  }

  .path-badge {
    background-color: var(--favorites-subtle-strong);
    color: var(--favorites-color);
  }

  .path-badge.custom {
    background-color: var(--accent-subtle-strong);
    color: var(--oid-color);
  }

  .path-text {
    flex-grow: 1;
    min-width: 0;
    font-family: 'Courier New', monospace;
    font-size: 0.9em;
    color: var(--text-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .btn-folder {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex-shrink: 0;
    white-space: nowrap;
  }

  .btn-remove {
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: 1.2em;
    cursor: pointer;
    padding: 4px 8px;
    border-radius: 4px;
    transition: all 0.2s;
  }

  .btn-remove:hover {
    background-color: var(--error-subtle-strong);
    color: var(--error-color);
  }

  .mib-list {
    padding: 12px;
  }

  .mib-list-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 10px;
    font-size: 0.9em;
    color: var(--text-dimmed);
  }

  .mib-empty-text {
    color: var(--text-dimmed);
    margin-top: 5px;
    display: block;
    text-align: center;
  }

  .mib-actions {
    display: flex;
    gap: 8px;
  }

  .btn-small {
    padding: 4px 8px;
    font-size: 0.8em;
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-color);
    border-radius: 3px;
    cursor: pointer;
    transition: all 0.2s;
  }

  .btn-small:hover {
    background-color: var(--hover-overlay-medium);
    border-color: var(--accent-color);
  }

  .mib-items {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 8px;
    max-height: 150px;
    overflow-y: auto;
    padding: 8px;
    background-color: var(--bg-color);
    border-radius: 4px;
  }

  .mib-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-radius: 4px;
    cursor: pointer;
    transition: background-color 0.2s;
    font-size: 0.9em;
  }

  .mib-item:hover {
    background-color: var(--hover-overlay);
  }

  .mib-item input[type="checkbox"] {
    width: auto;
    cursor: pointer;
  }

  .add-path-section {
    margin-top: 20px;
    padding: 15px;
    background-color: var(--hover-overlay);
    border-radius: 6px;
  }

  .add-path-section h4 {
    margin: 0 0 12px 0;
    font-size: 1em;
    color: var(--text-color);
  }

  .add-path-form {
    display: flex;
    gap: 10px;
  }

  .add-path-form input {
    flex-grow: 1;
  }

  .reload-section {
    margin-top: 20px;
    padding: 15px;
    background-color: var(--favorites-subtle);
    border: 1px solid var(--favorites-subtle-strong);
    border-radius: 6px;
  }

  .diagnostics-section {
    margin-top: 20px;
    padding: 15px;
    background-color: var(--hover-overlay);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .diagnostics-section h4 {
    margin: 0 0 10px 0;
    font-size: 1em;
  }

  .diagnostics-summary {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 15px;
    margin-bottom: 10px;
    font-size: 0.9em;
  }

  .diag-count {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .diag-count.ok { color: var(--success-color); }
  .diag-count.warning { color: var(--warning-color); }
  .diag-count.error,
  .diag-count.failed { color: var(--error-color); }

  .diag-legend {
    margin: 0 0 10px;
    font-size: 0.8em;
    color: var(--text-muted);
  }

  .diagnostics-list {
    max-height: 200px;
    overflow-y: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-color);
  }

  .diag-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-color);
    font-size: 0.85em;
  }

  .diag-item:last-child {
    border-bottom: none;
  }

  .diag-item.failed,
  .diag-item.error {
    background-color: var(--error-subtle);
  }

  .diag-item.warning {
    background-color: var(--warning-subtle);
  }

  .diag-icon.ok { color: var(--success-color); }
  .diag-icon.warning { color: var(--warning-color); }
  .diag-icon.error,
  .diag-icon.failed { color: var(--error-color); }

  .diag-icon {
    flex-shrink: 0;
  }

  /* Every text in a row may shrink: a flex item will not go below its content
     unless told it may, and a nowrap sentence is a very wide content. */
  .diag-filename {
    font-family: 'Courier New', monospace;
    font-weight: 500;
    flex: 0 1 auto;
    min-width: 0;
    max-width: 40%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Two lines of the message, then the tooltip: one line cut the part that
     says what is wrong, and no limit at all is how the row grew. */
  .diag-msg {
    flex: 1 1 0;
    min-width: 0;
    font-size: 0.85em;
    overflow: hidden;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    cursor: help;
  }

  .diag-msg.warning {
    color: var(--warning-color);
  }

  .diag-msg.error,
  .diag-msg.failed {
    color: var(--error-color);
  }
</style>
