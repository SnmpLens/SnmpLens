<script>
  import { _ } from 'svelte-i18n';
  import Dialog from './Dialog.svelte';
  import { diffLines, hunks } from './diff.js';

  // Two texts of one MIB side by side in one column: the lines removed, the
  // lines added, three lines of context around each change and the rest
  // folded (mibeditor/diff.js).
  export let title = '';
  export let leftLabel = '';
  export let rightLabel = '';
  export let before = '';
  export let after = '';

  $: result = diffLines(before, after);
  $: rows = hunks(result.rows);
</script>

<Dialog {title} icon="layers" width={980} on:close>
  <div class="legend">
    <span class="del">− {leftLabel}</span>
    <span class="add">+ {rightLabel}</span>
    {#if !result.tooDifferent}
      <span class="counts"><span class="del">−{result.removed}</span> <span class="add">+{result.added}</span></span>
    {/if}
  </div>
  {#if result.tooDifferent}
    <p class="hint">{$_('mibEditor.fileActions.tooDifferent')}</p>
  {:else if rows.length === 0}
    <p class="hint">{$_('mibEditor.fileActions.identical')}</p>
  {:else}
    <div class="diff" role="table" aria-label={title}>
      {#each rows as r, i (i)}
        {#if r.type === 'skip'}
          <div class="row skip" role="row">
            <span role="cell">{$_('mibEditor.fileActions.unchanged', { values: { count: r.count } })}</span>
          </div>
        {:else}
          <div class="row {r.type}" role="row">
            <span class="n" role="cell">{r.a ?? ''}</span>
            <span class="n" role="cell">{r.b ?? ''}</span>
            <span class="mark" role="cell">{r.type === 'del' ? '−' : r.type === 'add' ? '+' : ''}</span>
            <span class="text" role="cell">{r.text}</span>
          </div>
        {/if}
      {/each}
    </div>
  {/if}
</Dialog>

<style>
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
    margin-bottom: 8px;
    font-size: 0.85em;
  }

  .counts {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }

  .legend .del,
  .counts .del { color: var(--error-color); }
  .legend .add,
  .counts .add { color: var(--success-color); }

  .diff {
    border: 1px solid var(--border-color);
    border-radius: 4px;
    overflow: auto;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 18px;
    tab-size: 4;
  }

  .row {
    display: flex;
    min-width: max-content;
  }

  .row.del { background: var(--error-subtle); }
  .row.add { background: var(--success-subtle-medium); }

  .row.skip {
    padding: 2px 8px;
    color: var(--text-muted);
    background: var(--hover-overlay);
    font-style: italic;
  }

  .n {
    flex: 0 0 44px;
    padding-right: 6px;
    text-align: right;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    user-select: none;
  }

  .mark {
    flex: 0 0 16px;
    text-align: center;
    user-select: none;
  }

  .row.del .mark { color: var(--error-color); }
  .row.add .mark { color: var(--success-color); }

  .text {
    white-space: pre;
    padding-right: 12px;
  }

  .hint {
    margin: 0;
    color: var(--text-muted);
  }
</style>
