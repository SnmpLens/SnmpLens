<script>
  import { createEventDispatcher } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import Dialog from './Dialog.svelte';

  // The copies kept in mib-backups/: of one file (what each save replaced, and
  // a deletion), or the files deleted from the folder. Nothing here writes on
  // its own: comparing shows a diff, restoring a copy of a file puts it in the
  // editor to be reviewed and saved, and only a deleted file is written back,
  // under the name it had, which is free by construction.
  export let title = '';
  export let copies = [];
  export let deleted = false;
  export let loading = false;

  const dispatch = createEventDispatcher();

  function when(iso, lang) {
    try {
      return new Date(iso).toLocaleString(lang || undefined, { dateStyle: 'medium', timeStyle: 'medium' });
    } catch (e) {
      return iso;
    }
  }

  function size(bytes) {
    return bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${bytes} B`;
  }
</script>

<Dialog {title} icon="history" width={560} on:close>
  {#if loading}
    <p class="hint">{$_('common.loading')}</p>
  {:else if copies.length === 0}
    <p class="hint">{deleted ? $_('mibEditor.fileActions.deletedEmpty') : $_('mibEditor.fileActions.backupsEmpty')}</p>
  {:else}
    <ul>
      {#each copies as c (c.id)}
        <li>
          <div class="what">
            {#if deleted}<span class="name">{c.name}</span>{/if}
            <span class="date">{when(c.savedAt, $locale)}</span>
            <span class="meta">{size(c.size)}{#if c.deleted && !deleted} · <span class="tag">{$_('mibEditor.fileActions.deletedTag')}</span>{/if}</span>
          </div>
          {#if !deleted}
            <button class="btn btn-small secondary" on:click={() => dispatch('compare', c)}>
              <Icon name="layers" size={13} /> {$_('mibEditor.fileActions.compare')}
            </button>
          {/if}
          <button class="btn btn-small" on:click={() => dispatch('restore', c)}>
            <Icon name="history" size={13} /> {$_('mibEditor.fileActions.restore')}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
  <svelte:fragment slot="actions">
    <button class="btn secondary" on:click={() => dispatch('close')}>{$_('common.close')}</button>
  </svelte:fragment>
</Dialog>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }

  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 0;
    border-top: 1px solid var(--border-color);
  }

  li:first-child {
    border-top: none;
  }

  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .name {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .date {
    font-variant-numeric: tabular-nums;
  }

  .meta {
    color: var(--text-muted);
    font-size: 0.85em;
  }

  .tag {
    color: var(--warning-color);
  }

  .hint {
    margin: 0;
    color: var(--text-muted);
  }
</style>
