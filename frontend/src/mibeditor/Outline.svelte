<script>
  import { createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';

  // The outline. A MIB has no indentation to navigate by and no folding to
  // collapse, so scrolling is the only tool and a 185 KB file is a lot of
  // scrolling. It costs nothing: every field comes off the parse the analysis
  // already makes on every pause in typing.

  /** What the buffer defines (pkg/mib/outline.go). */
  export let outline = [];
  /** Whether it is unfolded; kept by the panel across files. */
  export let open = true;

  const dispatch = createEventDispatcher();
  let outlineFilter = '';

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
</script>

<div class="outline">
  <button class="outline-head" on:click={() => (open = !open)}
    aria-expanded={open}>
    <Icon name={open ? 'chevron-down' : 'chevron-right'} size={12} />
    {$_('mibEditor.outline')}
    <span class="count">{outline.length}</span>
  </button>

  {#if open}
    <input type="search" class="outline-filter" bind:value={outlineFilter}
      placeholder={$_('mibEditor.outlineFilter')} />

    <ul class="outline-list">
      {#each shownOutline as item (item.kind + item.name + item.line)}
        <li>
          <button class="outline-item" on:click={() => dispatch('jump', item)}
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

<style>
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


  .empty {
    padding: 8px 6px;
    font-size: 0.78em;
    color: var(--text-muted);
  }
</style>
