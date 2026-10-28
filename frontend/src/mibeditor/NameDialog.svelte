<script>
  import { createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Dialog from './Dialog.svelte';

  // A file name to type: renaming, duplicating. Go checks it (a name, not a
  // path; not taken; not a standard MIB's) and the refusal is shown here, in
  // the dialog it answers, rather than in a notification behind it.
  export let title = '';
  export let value = '';
  export let confirmLabel = '';
  export let error = '';
  export let busy = false;

  const dispatch = createEventDispatcher();

  function submit() {
    const name = value.trim();
    if (!name || busy) return;
    dispatch('confirm', { name });
  }

  // The stem selected, as a file manager does, so typing replaces the name
  // and keeps the extension.
  function selectStem(node) {
    const dot = value.lastIndexOf('.');
    const end = dot > 0 && value.length - dot <= 5 ? dot : value.length;
    requestAnimationFrame(() => node.setSelectionRange(0, end));
  }
</script>

<Dialog {title} icon="pencil" width={440} on:close>
  <form on:submit|preventDefault={submit}>
    <label for="mib-file-name">{$_('mibEditor.fileActions.newName')}</label>
    <input id="mib-file-name" bind:value use:selectStem spellcheck="false" autocomplete="off"
      aria-invalid={error ? 'true' : undefined} aria-describedby={error ? 'mib-file-name-error' : undefined} />
    {#if error}<p class="error" id="mib-file-name-error">{error}</p>{/if}
  </form>
  <svelte:fragment slot="actions">
    <button class="btn secondary" on:click={() => dispatch('close')}>{$_('common.cancel')}</button>
    <button class="btn" on:click={submit} disabled={!value.trim() || busy}>{confirmLabel}</button>
  </svelte:fragment>
</Dialog>

<style>
  form {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  label {
    font-size: 0.85em;
    color: var(--text-muted);
  }

  input {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }

  .error {
    margin: 2px 0 0;
    color: var(--error-color);
    font-size: 0.85em;
  }
</style>
