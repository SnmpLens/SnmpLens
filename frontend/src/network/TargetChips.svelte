<script>
  // A set of targets to pick from, drawn as the Monitor tab draws its own: one
  // chip per target, ticked when chosen, and one button for all or none.
  import { createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { targetLabels } from '../stores/targetLabels';
  import { displayTarget, targetTitle } from '../utils/targets';
  import { anonMode } from '../utils/anonymize';

  export let targets = [];
  export let chosen = [];
  export let disabled = false;

  const dispatch = createEventDispatcher();

  function toggle(target, list, all) {
    const on = !list.includes(target);
    dispatch('change', on ? all.filter((t) => t === target || list.includes(t)) : list.filter((t) => t !== target));
  }
</script>

<div class="chips">
  {#each targets as t, i (i)}
    <button type="button" class="chip" class:on={chosen.includes(t)} {disabled}
      aria-pressed={chosen.includes(t)} title={targetTitle(t, $anonMode)}
      on:click={() => toggle(t, chosen, targets)}>
      <span class="tick">{#if chosen.includes(t)}<Icon name="check" size={12} />{/if}</span>
      {displayTarget(t, $targetLabels, $anonMode)}
    </button>
  {/each}
  <button type="button" class="all" {disabled}
    on:click={() => dispatch('change', chosen.length < targets.length ? [...targets] : [])}>
    {chosen.length < targets.length ? $_('monitor.targetsAll') : $_('monitor.targetsNone')}
  </button>
</div>

<style>
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    max-height: 132px;
    overflow-y: auto;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 26px;
    padding: 0 10px 0 8px;
    font-size: 0.8em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 13px;
    color: var(--text-muted);
    cursor: pointer;
    white-space: nowrap;
  }

  .chip:hover:not(:disabled) {
    border-color: var(--border-hover);
    color: var(--text-color);
  }

  .chip.on {
    color: var(--accent-color);
    border-color: var(--accent-border);
    background-color: var(--accent-subtle-medium);
    font-weight: 600;
  }

  .tick {
    display: inline-flex;
    width: 12px;
  }

  .all {
    height: 26px;
    padding: 0 10px;
    font-size: 0.8em;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .all:hover:not(:disabled) {
    background-color: var(--hover-overlay);
    color: var(--text-color);
  }

  button:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }
</style>
