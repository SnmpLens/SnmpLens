<script>
  import { createEventDispatcher, onDestroy, onMount, tick } from 'svelte';
  import Icon from './Icon.svelte';

  export let x;
  export let y;
  // [{ label: 'Copy OID', icon: 'copy', action: 'copyOid', disabled: false, hint: 'F2' }]
  export let items;

  const dispatch = createEventDispatcher();

  let menuEl;
  // Where the menu is drawn: where it was asked for, moved back inside the
  // window when it would overflow — a right-click near the bottom of the file
  // list opened a menu whose last items were off screen.
  let left = x;
  let top = y;

  function handleClick(item) {
    if (item.disabled) return;
    dispatch('action', { action: item.action });
    dispatch('close');
  }

  function close() {
    dispatch('close');
  }

  function entries() {
    return menuEl ? [...menuEl.querySelectorAll('[role="menuitem"]:not([aria-disabled="true"])')] : [];
  }

  // The keyboard drives it as a menu does: the arrows move between the items
  // that can be chosen, Home and End go to either end, Enter or Space chooses,
  // Escape and Tab leave. A menu opened from the keyboard (Shift+F10, the menu
  // key) was otherwise a menu nobody could use without the mouse.
  function move(step) {
    const list = entries();
    if (!list.length) return;
    const at = list.indexOf(document.activeElement);
    const next = at < 0 ? (step > 0 ? 0 : list.length - 1) : (at + step + list.length) % list.length;
    list[next].focus();
  }

  function handleKeydown(e) {
    switch (e.key) {
      case 'Escape':
      case 'Tab':
        e.preventDefault();
        close();
        break;
      case 'ArrowDown':
        e.preventDefault();
        move(1);
        break;
      case 'ArrowUp':
        e.preventDefault();
        move(-1);
        break;
      case 'Home':
        e.preventDefault();
        entries()[0]?.focus();
        break;
      case 'End':
        e.preventDefault();
        entries().at(-1)?.focus();
        break;
      default:
        return;
    }
    e.stopPropagation();
  }

  function itemKeydown(e, item) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      e.stopPropagation();
      handleClick(item);
    }
  }

  onMount(async () => {
    await tick();
    if (menuEl) {
      const r = menuEl.getBoundingClientRect();
      const margin = 4;
      left = Math.max(margin, Math.min(x, window.innerWidth - r.width - margin));
      top = Math.max(margin, Math.min(y, window.innerHeight - r.height - margin));
    }
    entries()[0]?.focus();
  });

  // Add listeners to close the menu when clicking outside
  window.addEventListener('click', close);
  window.addEventListener('contextmenu', close, { capture: true });

  onDestroy(() => {
    window.removeEventListener('click', close);
    window.removeEventListener('contextmenu', close, { capture: true });
  });
</script>

<!-- Escape from wherever the focus is, as before the menu took the focus. -->
<svelte:window on:keydown={(e) => { if (e.key === 'Escape') close(); }} />

<div class="context-menu" bind:this={menuEl} style="left: {left}px; top: {top}px;" on:click|stopPropagation
  on:keydown={handleKeydown} role="menu" tabindex="-1">
  <!-- The `ul` carries `role="none"`: a `menu` must contain `menuitem`s, and an
       announced list in between is not what this is. The items are the widgets. -->
  <ul role="none">
    {#each items as item}
      {#if item.label === '---'}
        <li class="separator" role="separator"></li>
      {:else}
        <li
          class:disabled={item.disabled}
          role="menuitem"
          tabindex="-1"
          aria-disabled={item.disabled || undefined}
          on:click={() => handleClick(item)}
          on:keydown={(e) => itemKeydown(e, item)}
          title={item.disabled ? item.disabledReason || 'This action is not available' : ''}
        >
          {#if item.icon}<Icon name={item.icon} size={14} filled={item.iconFilled} class="menu-icon" />{/if}
          <span class="label">{item.label}</span>
          {#if item.hint}<span class="hint">{item.hint}</span>{/if}
        </li>
      {/if}
    {/each}
  </ul>
</div>

<style>
  .context-menu {
    position: fixed;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    box-shadow: 0 5px 15px var(--shadow-color);
    z-index: 200;
    padding: 5px 0;
    max-height: calc(100vh - 8px);
    overflow-y: auto;
  }
  .context-menu:focus {
    outline: none;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li {
    padding: 8px 15px;
    cursor: pointer;
    font-size: 0.9em;
    white-space: nowrap;
    display: flex;
    align-items: center;
    gap: 9px;
  }
  li :global(.menu-icon) {
    opacity: 0.75;
  }
  .label {
    flex: 1;
  }
  .hint {
    margin-left: 18px;
    color: var(--text-muted);
    font-size: 0.85em;
  }
  li:hover,
  li:focus {
    background-color: var(--accent-color);
    color: white;
    outline: none;
  }
  li:hover .hint,
  li:focus .hint {
    color: inherit;
    opacity: 0.8;
  }
  li.disabled {
    opacity: 0.4;
    cursor: not-allowed;
    color: var(--text-muted);
  }
  li.disabled:hover {
    background-color: transparent;
    color: var(--text-muted);
  }
  li.separator {
    padding: 0;
    margin: 5px 0;
    height: 1px;
    background-color: var(--border-color);
    cursor: default;
  }
  li.separator:hover {
    background-color: var(--border-color);
  }
</style>
