<script>
  import { createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import Dialog from './Dialog.svelte';
  import { treeFrom, flattenTree, MISSING, NOT_LOADED } from '../utils/mibGraph';

  // What a module imports, as the tree the MIB settings draw (utils/mibGraph.js),
  // or what imports it: directly, then through those. The second is the one
  // to read before deleting or switching a file off — everything listed stops
  // resolving with it.
  export let module = '';
  export let nodes = [];
  export let mode = 'imports';
  export let loading = false;

  const dispatch = createEventDispatcher();

  $: byModule = new Map((Array.isArray(nodes) ? nodes : []).map((n) => [n.module, n]));
  $: tree = mode === 'imports' && byModule.size ? flattenTree(treeFrom(nodes, module)).slice(1) : [];
  $: importers = mode === 'importers' ? importersOf(byModule, module) : { direct: [], indirect: [] };

  function importersOf(index, root) {
    const direct = [...(index.get(root)?.importedBy || [])].sort();
    const seen = new Set([root, ...direct]);
    const indirect = [];
    const queue = [...direct];
    while (queue.length) {
      for (const m of index.get(queue.shift())?.importedBy || []) {
        if (seen.has(m)) continue;
        seen.add(m);
        indirect.push(m);
        queue.push(m);
      }
    }
    return { direct, indirect: indirect.sort() };
  }

  function fileOf(index, m) {
    const n = index.get(m);
    return n && n.present ? n.file : '';
  }
</script>

<Dialog title={$_(mode === 'imports' ? 'mibEditor.fileActions.importsTitle' : 'mibEditor.fileActions.importersTitle', { values: { module } })}
  icon={mode === 'imports' ? 'route' : 'layers'} width={520} on:close>
  {#if loading}
    <p class="hint">{$_('common.loading')}</p>
  {:else if mode === 'imports'}
    {#if tree.length === 0}
      <p class="hint">{$_('mibEditor.fileActions.importsNone')}</p>
    {:else}
      <ul class="tree">
        {#each tree as item, i (i)}
          <li style="--depth: {item.depth - 1};">
            <button class="module" disabled={!item.file} on:click={() => dispatch('open', item.file)}
              title={item.file || ''}>{item.module}</button>
            {#if item.status === MISSING}<span class="state missing">{$_('mibEditor.fileActions.depMissing')}</span>
            {:else if item.status === NOT_LOADED}<span class="state">{$_('mibEditor.fileActions.depNotLoaded')}</span>{/if}
            {#if item.cycle}<span class="state missing">{$_('mibEditor.fileActions.depCycle')}</span>{/if}
            {#if item.repeated}<span class="state">{$_('mibEditor.fileActions.depRepeated')}</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}
  {:else}
    {#if importers.direct.length === 0}
      <p class="hint">{$_('mibEditor.fileActions.importersNone')}</p>
    {:else}
      <h4>{$_('mibEditor.fileActions.directly', { values: { count: importers.direct.length } })}</h4>
      <ul class="flat">
        {#each importers.direct as m (m)}
          <li><button class="module" disabled={!fileOf(byModule, m)} on:click={() => dispatch('open', fileOf(byModule, m))}>{m}</button></li>
        {/each}
      </ul>
      {#if importers.indirect.length}
        <h4>{$_('mibEditor.fileActions.indirectly', { values: { count: importers.indirect.length } })}</h4>
        <ul class="flat">
          {#each importers.indirect as m (m)}
            <li><button class="module" disabled={!fileOf(byModule, m)} on:click={() => dispatch('open', fileOf(byModule, m))}>{m}</button></li>
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
  <svelte:fragment slot="actions">
    <button class="btn secondary" on:click={() => dispatch('close')}>
      <Icon name="x" size={13} /> {$_('common.close')}
    </button>
  </svelte:fragment>
</Dialog>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .tree li {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 2px 0 2px calc(var(--depth) * 16px);
  }

  .flat li {
    padding: 2px 0;
  }

  h4 {
    margin: 10px 0 4px;
    font-size: 0.8em;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }

  h4:first-child {
    margin-top: 0;
  }

  .module {
    padding: 0;
    background: none;
    border: none;
    color: var(--accent-color);
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 0.9em;
    cursor: pointer;
  }

  .module:disabled {
    color: var(--text-color);
    cursor: default;
  }

  .module:not(:disabled):hover {
    text-decoration: underline;
  }

  .state {
    color: var(--text-muted);
    font-size: 0.8em;
  }

  .state.missing {
    color: var(--error-color);
  }

  .hint {
    margin: 0;
    color: var(--text-muted);
  }
</style>
