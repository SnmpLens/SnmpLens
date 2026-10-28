<script>
  import { createEventDispatcher, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import {
    sortProblems, filterProblems, countBySeverity, positionOf as problemPosition, SEVERITIES,
    MUTABLE, FIXABLE, problemsText,
  } from './problems';
  import { mutedRules } from '../stores/mutedRules';
  import { copyToClipboard } from '../utils/clipboard';

  // The problems under the MIB editor, as a table (mibeditor/problems.js). It
  // shows and asks: jumping, fixing and re-analysing are the panel's, since
  // the buffer and its undo history are.

  /** Every diagnostic, muted rules included: what is hidden is counted. */
  export let diagnostics = [];
  /** The diagnostics once muted rules are taken out. */
  export let listed = [];
  /** The file, for the copied text. */
  export let fileName = '';
  /** How tall the table may grow, asked when it is dragged. */
  export let maxHeight = () => 420;

  const dispatch = createEventDispatcher();

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
    const max = Math.max(PROBLEMS_MIN, maxHeight());
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
    dispatch('resized');
  }

  async function resizeByKey(e) {
    const step = e.shiftKey ? 80 : 20;
    if (e.key === 'ArrowUp') problemsHeight = clampProblemsHeight(problemsHeight + step);
    else if (e.key === 'ArrowDown') problemsHeight = clampProblemsHeight(problemsHeight - step);
    else return;
    e.preventDefault();
    keepProblemsHeight();
    await tick();
    dispatch('resized');
  }

  // Every problem SHOWN — filtered and sorted as on screen — as text, one per
  // line, to paste into a ticket or a message to whoever wrote the MIB.
  function copyProblems() {
    const { label, text } = problemsText(fileName, shownProblems, get(_));
    copyToClipboard(text, label);
  }

  // Muting changes the verdict of every file; the list asks again by itself
  // (stores/mibStatusStore.js), the open buffer through the panel.
  async function mute(code) {
    await mutedRules.mute(code);
    dispatch('muted');
  }

  async function unmute(code) {
    await mutedRules.unmute(code);
    dispatch('muted');
  }
</script>

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
      <button class="problems-action" on:click={() => dispatch('fix', { code: f.code, symbols: [] })}
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
            <td class="col-message"><button class="jump" on:click={() => dispatch('jump', d)}>{d.message}</button></td>
            <td class="col-code problem-code">{d.code}</td>
            <td class="col-actions">
              {#if FIXABLE.has(d.code) && d.symbol}
                <button class="row-action" on:click={() => dispatch('fix', { code: d.code, symbols: [d.symbol] })}
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

<style>
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
</style>
