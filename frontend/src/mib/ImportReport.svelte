<script>
  import { createEventDispatcher } from 'svelte';
  import { get } from 'svelte/store';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import MibDiagnosis from './MibDiagnosis.svelte';
  import { mibEditorStore } from '../stores/mibEditorStore';
  import { notificationStore } from '../stores/notifications';
  import { requestTab } from '../stores/tabRequest';
  import { shortPath, canChoose, LOAD_WARN, LOAD_ERROR, LOAD_FAIL, REPLACED, PRESENT } from '../utils/importReport';
  import { ICONS as VERDICT_ICONS, OK } from '../utils/mibVerdict';

  /**
   * What a MIB import did, one row per module (utils/importReport.js).
   *
   * Rows are collapsed: the line says what the module is, where it came from
   * and whether it is right, and the details — warnings, the versions set
   * aside — open on demand. A module that does not load opens by itself.
   */
  export let report;
  /** The version being imported in place of the one kept, while it is. */
  export let choosing = '';

  const dispatch = createEventDispatcher();

  // The codes a repair in the editor removes (pkg/mib/repair.go).
  const REPAIRABLE = new Set(['escaped-quote', 'bad-date', 'import-alias']);

  let open = {};
  let showIgnored = false;
  let seeded = null;

  // Failures open by themselves, once per report.
  $: if (report && report !== seeded) {
    seeded = report;
    open = {};
    for (const row of report.modules) {
      if (row.load?.status === LOAD_FAIL || row.load?.status === LOAD_ERROR) open[row.module] = true;
    }
    showIgnored = false;
  }

  function toggle(module) {
    open = { ...open, [module]: !open[module] };
  }

  function repairable(warnings) {
    return (warnings || []).some((w) => REPAIRABLE.has(w.code));
  }

  function statusIcon(status) {
    return VERDICT_ICONS[status] || 'circle-dot';
  }

  async function openInEditor(fileName) {
    if (mibEditorStore.dirty() && !window.confirm(get(_)('mibEditor.discardConfirm'))) return;
    try {
      await mibEditorStore.open(fileName);
    } catch (e) {
      notificationStore.add(String(e), 'error');
      return;
    }
    requestTab('mibeditor');
    dispatch('close');
  }
</script>

{#if report}
  <div class="report">
    <div class="summary">
      <span class="chip ok"><Icon name="circle-check" size={13} />
        {$_('mibImport.summary.modules', { values: { count: report.counts.modules } })}</span>
      {#if report.counts.warned > 0}
        <span class="chip warn"><Icon name="triangle-alert" size={13} />
          {$_('mibImport.summary.warned', { values: { count: report.counts.warned } })}</span>
      {/if}
      {#if report.counts.errored > 0}
        <span class="chip fail"><Icon name="circle-alert" size={13} />
          {$_('mibImport.summary.errored', { values: { count: report.counts.errored } })}</span>
      {/if}
      {#if report.counts.failed > 0}
        <span class="chip fail"><Icon name="circle-x" size={13} />
          {$_('mibImport.summary.failed', { values: { count: report.counts.failed } })}</span>
      {/if}
      {#if report.counts.errors > 0}
        <span class="chip fail"><Icon name="circle-x" size={13} />
          {$_('mibImport.summary.errors', { values: { count: report.counts.errors } })}</span>
      {/if}
      {#if report.counts.setAside > 0}
        <span class="chip muted">{$_('mibImport.summary.setAside', { values: { count: report.counts.setAside } })}</span>
      {/if}
      {#if report.counts.ignored > 0}
        <span class="chip muted">{$_('mibImport.summary.ignored', { values: { count: report.counts.ignored } })}</span>
      {/if}
    </div>

    {#if report.errors.length > 0}
      <section class="errors">
        <h4>{$_('mibImport.errorsTitle')}</h4>
        <ul>
          {#each report.errors as err}
            <li title={err.error}>
              <span class="path" title={err.fileName}>{shortPath(err.fileName)}</span>
              <span class="err-text">{err.reason ? $_('mibImport.error.' + err.reason) : err.error}</span>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if report.modules.length > 0}
      <section>
        <h4>{$_('mibImport.modulesTitle')}</h4>
        <ul class="rows">
          {#each report.modules as row (row.module)}
            <li class="row">
              <button class="row-head" aria-expanded={Boolean(open[row.module])} on:click={() => toggle(row.module)}>
                <Icon name={open[row.module] ? 'chevron-down' : 'chevron-right'} size={13} />
                <span class="status {row.load?.status || 'none'}"><Icon name={statusIcon(row.load?.status)} size={14} /></span>
                <span class="mod">{row.module}</span>
                <span class="path" title={row.source}>{shortPath(row.source)}</span>
                <span class="badges">
                  {#if row.load?.status === LOAD_FAIL}
                    <span class="badge fail">{$_('mibImport.row.fail')}</span>
                  {:else if row.load?.status === LOAD_ERROR}
                    <span class="badge fail">{$_('mibImport.row.errors', { values: { count: row.load.count } })}</span>
                  {:else if row.load?.status === LOAD_WARN}
                    <span class="badge warn">{$_('mibImport.row.warnings', { values: { count: row.load.count } })}</span>
                  {/if}
                  {#if row.outcome === REPLACED}
                    <span class="badge">{$_('mibImport.row.replaced')}</span>
                  {:else if row.outcome === PRESENT}
                    <span class="badge">{$_('mibImport.row.present')}</span>
                  {/if}
                  {#if row.setAside.length > 0}
                    <span class="badge muted">{$_('mibImport.row.setAside', { values: { count: row.setAside.length } })}</span>
                  {/if}
                </span>
              </button>

              {#if open[row.module]}
                <div class="row-body">
                  <dl>
                    <dt>{$_('mibImport.detail.storedAs')}</dt><dd class="mono">{row.storedAs}</dd>
                    <dt>{$_('mibImport.detail.source')}</dt><dd class="mono src">{row.source}</dd>
                    {#if row.date}<dt>{$_('mibImport.detail.date')}</dt><dd>{row.date}</dd>{/if}
                    {#if row.replaced}<dt>{$_('mibImport.detail.replaced')}</dt><dd><span class="mono">{row.replaced}</span> — {$_('mibImport.detail.backup')}</dd>{/if}
                  </dl>

                  {#if row.load?.status === LOAD_FAIL}
                    <MibDiagnosis diagnosis={row.load.diagnosis || { stage: '', summary: row.load.error }}
                      fileName={row.storedAs} summary={row.load.error} />
                  {/if}

                  {#if row.load?.status === LOAD_ERROR && row.load.detail}
                    <p class="verdict-detail">{row.load.detail}</p>
                    <p class="hint">{$_('mibImport.detail.analysis')}</p>
                  {/if}

                  {#if row.load?.warnings.length > 0}
                    <ul class="warnings">
                      {#each row.load.warnings as w}
                        <li>
                          <span class="w-label">{$_('mibImport.warning.' + w.code)}</span>
                          {#if w.line > 0}<span class="w-pos">{$_('mibImport.line', { values: { line: w.line } })}</span>{/if}
                          <div class="w-msg">{w.message}</div>
                        </li>
                      {/each}
                    </ul>
                    {#if repairable(row.load.warnings)}
                      <p class="hint">{$_('mibImport.tolerated')}</p>
                    {/if}
                  {/if}

                  {#if row.setAside.length > 0}
                    <p class="label">{$_('mibImport.detail.setAside')}</p>
                    <ul class="versions">
                      {#each row.setAside as v}
                        <li>
                          <span class="path" title={v.source}>{shortPath(v.source)}</span>
                          <span class="reason {v.reason}">{$_('mibImport.lost.' + v.reason)}</span>
                          {#if v.date}<span class="date">{v.date}</span>{/if}
                          <!-- The choice the import made for you, undone: this
                               version is imported in place of the one kept,
                               which goes to mib-backups. -->
                          {#if canChoose(v)}
                            <button class="btn btn-small secondary use" disabled={!!choosing}
                              title={$_('mibImport.useVersionHint')}
                              on:click={() => dispatch('choose', { module: row.module, version: v })}>
                              {choosing === v.path ? $_('mibImport.usingVersion') : $_('mibImport.useVersion')}
                            </button>
                          {/if}
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  {#if row.load && row.load.status !== OK}
                    <div class="actions">
                      <button class="btn btn-small" on:click={() => openInEditor(row.storedAs)}>
                        <Icon name="book-marked" size={13} /> {$_('settings.mibs.openInEditor')}
                      </button>
                    </div>
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if report.ignored.length > 0}
      <section class="ignored">
        <button class="section-toggle" aria-expanded={showIgnored} on:click={() => (showIgnored = !showIgnored)}>
          <Icon name={showIgnored ? 'chevron-down' : 'chevron-right'} size={13} />
          <span>{$_('mibImport.ignoredTitle', { values: { count: report.counts.ignored } })}</span>
        </button>
        <div class="kinds">
          {#each report.ignored as g}
            <span class="kind">{$_('mibImport.kind.' + g.kind)} <strong>{g.files.length}</strong></span>
          {/each}
        </div>
        {#if showIgnored}
          {#each report.ignored as g}
            <p class="label">{$_('mibImport.kind.' + g.kind)}</p>
            <ul class="files">
              {#each g.files as f}<li class="path" title={f}>{shortPath(f, 3)}</li>{/each}
            </ul>
          {/each}
        {/if}
      </section>
    {/if}
  </div>
{/if}

<style>
  .report {
    display: flex;
    flex-direction: column;
    gap: var(--space-md);
    font-size: var(--text-sm);
  }

  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 9px;
    border-radius: 999px;
    border: 1px solid var(--border-color);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .chip.ok { color: var(--success-color); border-color: var(--success-color); }
  .chip.warn { color: var(--warning-color); border-color: var(--warning-color); background: var(--warning-subtle); }
  .chip.fail { color: var(--error-color); border-color: var(--error-border); background: var(--error-subtle); }
  .chip.muted { color: var(--text-muted); font-weight: 500; }

  section {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  h4 {
    margin: 0;
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .mono, .path {
    font-family: 'Courier New', monospace;
  }

  .path {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  .errors ul {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .errors li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr);
    gap: 10px;
    padding: 6px 10px;
    border-radius: var(--radius-sm, 4px);
    background: var(--error-subtle);
  }

  .err-text {
    color: var(--error-color);
  }

  .rows {
    border: 1px solid var(--border-color);
    border-radius: var(--radius-md, 6px);
    overflow: hidden;
  }

  .row + .row {
    border-top: 1px solid var(--border-color);
  }

  .row-head {
    display: grid;
    grid-template-columns: auto auto minmax(0, max-content) minmax(0, 1fr) auto;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 10px;
    background: none;
    border: none;
    color: var(--text-color);
    text-align: left;
    cursor: pointer;
    font: inherit;
  }

  .row-head:hover {
    background: var(--bg-lighter-color);
  }

  .row-head:focus-visible {
    outline: 2px solid var(--accent-color);
    outline-offset: -2px;
  }

  .status { display: inline-flex; }
  .status.ok { color: var(--success-color); }
  .status.warning { color: var(--warning-color); }
  .status.error,
  .status.failed { color: var(--error-color); }
  .status.none { color: var(--text-muted); }

  .verdict-detail {
    margin: 0;
    color: var(--error-color);
  }

  .mod {
    font-weight: 600;
    white-space: nowrap;
  }

  .badges {
    display: inline-flex;
    gap: 4px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .badge {
    font-size: var(--text-xs);
    padding: 1px 7px;
    border-radius: 999px;
    background: var(--bg-lighter-color);
    color: var(--text-color);
    white-space: nowrap;
  }

  .badge.warn { background: var(--warning-subtle); color: var(--warning-color); }
  .badge.fail { background: var(--error-subtle); color: var(--error-color); }
  .badge.muted { color: var(--text-muted); }

  .row-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 4px 12px 12px 42px;
  }

  dl {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 2px 12px;
    margin: 0;
  }

  dt {
    color: var(--text-muted);
  }

  dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .warnings {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .w-label {
    font-weight: 600;
    color: var(--warning-color);
  }

  .w-pos {
    margin-left: 6px;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .w-msg {
    color: var(--text-muted);
    font-size: var(--text-xs);
  }

  .hint {
    margin: 0;
    color: var(--text-muted);
    font-style: italic;
  }

  .label {
    margin: 4px 0 0;
    font-weight: 600;
    color: var(--text-muted);
  }

  .versions li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto auto;
    gap: 10px;
    align-items: center;
    padding: 2px 0;
  }

  /* The button sits in the last column whether or not the version states a
     date, so the buttons of a list line up. */
  .versions .use {
    grid-column: 4;
    white-space: nowrap;
  }

  .reason {
    font-size: var(--text-xs);
    padding: 1px 7px;
    border-radius: 999px;
    background: var(--bg-lighter-color);
    white-space: nowrap;
  }

  .reason.unparsable { background: var(--error-subtle); color: var(--error-color); }

  .date {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
  }

  .section-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0;
    background: none;
    border: none;
    color: var(--text-muted);
    font: inherit;
    font-weight: 600;
    cursor: pointer;
    text-align: left;
  }

  .section-toggle:focus-visible {
    outline: 2px solid var(--accent-color);
    outline-offset: 2px;
  }

  .kinds {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
    color: var(--text-muted);
    padding-left: 19px;
  }

  .files {
    padding-left: 19px;
  }

  .files li {
    padding: 1px 0;
  }
</style>
