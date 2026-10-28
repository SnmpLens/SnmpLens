<script>
  import { createEventDispatcher, onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { onBackdrop } from '../utils/modal';

  // The frame of the file list's dialogs: a title, a body, actions, and the
  // two ways out every dialog here has — Escape and a click beside it.
  export let title = '';
  export let icon = '';
  export let width = 520;

  const dispatch = createEventDispatcher();
  const close = () => dispatch('close');
  let box;

  // Escape in the CAPTURE phase, stopped there: the editor has Escape
  // handlers of its own (the find bar, the completion list), and one key
  // closing this and something behind it is how work gets lost.
  function onKey(e) {
    if (e.key !== 'Escape') return;
    e.stopPropagation();
    e.preventDefault();
    close();
  }

  onMount(() => {
    const first = box?.querySelector('input, button:not(.dialog-x), [tabindex="0"]');
    (first || box)?.focus();
  });
</script>

<svelte:window on:keydown|capture={onKey} />

<div class="dialog-overlay" on:click={onBackdrop(close)} role="presentation">
  <div class="dialog" bind:this={box} role="dialog" aria-modal="true" aria-label={title} tabindex="-1"
    style="width: min({width}px, calc(100vw - 32px));">
    <header>
      {#if icon}<Icon name={icon} size={16} />{/if}
      <h3>{title}</h3>
      <button class="dialog-x" on:click={close} aria-label={$_('common.close')}><Icon name="x" size={15} /></button>
    </header>
    <div class="body"><slot /></div>
    {#if $$slots.actions}
      <footer><slot name="actions" /></footer>
    {/if}
  </div>
</div>

<style>
  .dialog-overlay {
    position: fixed;
    inset: 0;
    background-color: var(--backdrop-color-strong);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .dialog {
    display: flex;
    flex-direction: column;
    max-height: 82vh;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    box-shadow: 0 5px 20px var(--shadow-color-strong);
    outline: none;
  }

  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-color);
  }

  h3 {
    flex: 1;
    min-width: 0;
    margin: 0;
    font-size: 1em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .dialog-x {
    display: inline-flex;
    padding: 2px;
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
  }

  .dialog-x:hover {
    color: var(--text-color);
  }

  .body {
    padding: 14px 16px;
    overflow: auto;
    min-height: 0;
  }

  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 10px 16px;
    border-top: 1px solid var(--border-color);
  }
</style>
