<script>
  import { onMount } from 'svelte';
  import { onBackdrop } from './utils/modal';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from './Icon.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { eventsStore, eventCounts } from './stores/eventsStore';
  import { notificationStore } from './stores/notifications';
  import { mibStore } from './stores/mibStore';
  import { targetLabels } from './stores/targetLabels';
  import { oidName, oidTooltip } from './utils/oidDisplay';
  import { formatTimestamp } from './utils/formatting';
  import { anonMode, anonymizeIp, anonymizeText } from './utils/anonymize';
  import { escapeCSV, downloadFile } from './utils/csv';
  import { EventsPayload } from './bridge/App';
  import { displayTarget as labelled } from './utils/targets';

  const CATEGORIES = ['trap', 'threshold', 'reachability', 'system'];
  const SEVERITIES = ['critical', 'major', 'minor', 'warning', 'info'];

  let selectedCategories = new Set();
  let minSeverity = '';
  let search = '';
  let unackedOnly = false;
  let selectedIds = new Set();
  let openPayload = null; // { id, body }
  let showClearConfirm = false;

  $: filter = {
    categories: [...selectedCategories],
    minSeverity,
    search: search.trim(),
    unackedOnly,
    limit: 100,
  };

  function reload() {
    eventsStore.load(filter);
    selectedIds = new Set();
  }

  onMount(() => {
    // NOT eventsStore.listen(): App.svelte registers the live tail once at
    // startup, because the badge has to count with this tab closed. Asking
    // again here registered a second handler on every visit to the tab.
    reload();
  });

  function toggleCategory(c) {
    const next = new Set(selectedCategories);
    if (next.has(c)) next.delete(c);
    else next.add(c);
    selectedCategories = next;
    reload();
  }

  function setSeverity(s) {
    minSeverity = minSeverity === s ? '' : s;
    reload();
  }

  function toggleSelect(id) {
    const next = new Set(selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selectedIds = next;
  }

  function displaySource(source) {
    if (!source) return '—';
    return labelled(source, $targetLabels, $anonMode);
  }

  // The stored title key + params are rendered here, so an event recorded a
  // year ago still reads correctly in a locale added since. `summary` is the
  // English fallback written at insert time.
  // Anonymous Mode has to reach the params, not only the source column.
  //
  // The stored params carry the unmasked value — `{"target": "10.0.0.1"}` — and
  // every title key interpolates them: "{oid} on {target} is {kind} {bound}",
  // "{pduType} from {source} (...)", "{target} stopped responding". So the
  // column read `Device-1` while the summary beside it read the real address,
  // twice over, in a mode whose entire purpose is that it does not.
  function anonParams(params) {
    if (!$anonMode || !params) return params || {};
    const out = {};
    for (const [k, v] of Object.entries(params)) {
      out[k] = typeof v === 'string' ? anonymizeText(v) : v;
    }
    return out;
  }

  function renderTitle(ev) {
    const key = ev.titleKey;
    const translated = $_(key, { values: anonParams(ev.params), default: '' });
    if (translated && translated !== key) return translated;
    return $anonMode ? anonymizeText(ev.summary) : ev.summary;
  }

  // The tooltip is text too, and it was the raw value in both places.
  function tooltipText(text) {
    if (!text) return '';
    return $anonMode ? anonymizeText(text) : text;
  }

  async function showPayload(ev) {
    if (!ev.payloadSize) return;
    try {
      const body = await EventsPayload(ev.id);
      openPayload = { id: ev.id, body };
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function exportCsv() {
    const items = $eventsStore.items;
    if (!items.length) return;
    const rows = [['timestamp', 'severity', 'category', 'kind', 'source', 'oid', 'summary'].join(',')];
    for (const e of items) {
      rows.push([e.ts, e.severity, e.category, e.kind, e.source || '', e.oid || '', e.summary].map(escapeCSV).join(','));
    }
    downloadFile(rows.join('\n'), 'snmp-events.csv', 'text/csv');
    notificationStore.add(get(_)('events.exported'), 'success');
  }

  function confirmClear() {
    eventsStore.clear();
    showClearConfirm = false;
    notificationStore.add(get(_)('events.cleared'), 'success');
  }
</script>

<!-- Escape on the window, not on the overlay: a keydown starts at whatever
     has focus — a field inside the dialog — and the box below stops clicks,
     which used to stop keys with them. The overlay handler never ran. -->
<svelte:window on:keydown={(e) => {
  if (e.key !== 'Escape') return;
    // This panel stays mounted while its tab is open, so guard on the dialog
    // actually being up rather than assigning on every Escape.
    if (openPayload) openPayload = null;
}} />

<div class="panel">
  <div class="header">
    <h3>
      {$_('events.title')}
      {#if $eventCounts.unacked > 0}
        <span class="unacked-pill">{$_('events.unackedCount', { values: { count: $eventCounts.unacked } })}</span>
      {/if}
    </h3>
    <div class="header-actions">
      <button class="btn tertiary" on:click={() => eventsStore.ackAll(filter)} disabled={!$eventCounts.unacked}>
        <Icon name="check" size={14} /> {$_('events.ackAll')}
      </button>
      <button class="btn tertiary" on:click={exportCsv} disabled={!$eventsStore.items.length}>
        <Icon name="download" size={14} /> {$_('common.export')}
      </button>
      <button class="btn danger" on:click={() => (showClearConfirm = true)}>{$_('events.clearAll')}</button>
    </div>
  </div>

  <div class="filters">
    <input type="text" placeholder={$_('events.searchPlaceholder')} bind:value={search} on:change={reload} />
    <div class="chip-row">
      {#each CATEGORIES as c}
        <button class="chip" class:on={selectedCategories.has(c)} on:click={() => toggleCategory(c)}>
          {$_('events.category.' + c)}
          {#if $eventCounts.unackedByCategory && $eventCounts.unackedByCategory[c]}
            <span class="chip-count">{$eventCounts.unackedByCategory[c]}</span>
          {/if}
        </button>
      {/each}
    </div>
    <div class="chip-row">
      {#each SEVERITIES as s}
        <button class="chip sev sev-{s}" class:on={minSeverity === s} on:click={() => setSeverity(s)}>
          {$_('events.severity.' + s)}
        </button>
      {/each}
      <label class="toggle">
        <input type="checkbox" bind:checked={unackedOnly} on:change={reload} />
        {$_('events.unackedOnly')}
      </label>
      <span class="count">{$_('events.matching', { values: { count: $eventsStore.total } })}</span>
    </div>
  </div>

  {#if selectedIds.size > 0}
    <div class="bulk-bar">
      <span>{$_('events.selectedCount', { values: { count: selectedIds.size } })}</span>
      <span class="spacer"></span>
      <button class="btn tertiary" on:click={() => { eventsStore.ack([...selectedIds]); selectedIds = new Set(); }}>
        <Icon name="check" size={13} /> {$_('events.ack')}
      </button>
      <button class="btn danger" on:click={() => { eventsStore.remove([...selectedIds]); selectedIds = new Set(); }}>
        <Icon name="trash-2" size={13} /> {$_('common.delete')}
      </button>
    </div>
  {/if}

  <div class="events-container">
    {#if $eventsStore.items.length === 0}
      <p class="empty-state">{$eventsStore.loading ? $_('common.loading') : $_('events.empty')}</p>
    {:else}
      {#each $eventsStore.items as ev (ev.id)}
        <div class="event-row sev-{ev.severity}" class:acked={ev.acked} class:selected={selectedIds.has(ev.id)}>
          <button class="sel" class:on={selectedIds.has(ev.id)} on:click={() => toggleSelect(ev.id)} aria-label={$_('events.ack')}>
            {#if selectedIds.has(ev.id)}<Icon name="check" size={12} />{/if}
          </button>
          <span class="sev-dot" title={$_('events.severity.' + ev.severity)}></span>
          <span class="ts">{formatTimestamp(ev.ts)}</span>
          <span class="cat">{$_('events.category.' + ev.category)}</span>
          <span class="src" title={displaySource(ev.source)}>{displaySource(ev.source)}</span>
          {#if ev.oid}
            <span class="oid" title={oidTooltip(ev.oid, $mibStore.tree)}>{oidName(ev.oid, $mibStore.tree)}</span>
          {:else}
            <span></span>
          {/if}
          <span class="summary" title={tooltipText(ev.summary)}>{renderTitle(ev)}</span>
          {#if ev.payloadSize > 0}
            <button class="btn-copy-small" on:click={() => showPayload(ev)} title={$_('events.viewPayload')}>
              <Icon name="clipboard-list" size={13} />
            </button>
          {:else}
            <span></span>
          {/if}
          {#if !ev.acked}
            <button class="btn-copy-small" on:click={() => eventsStore.ack([ev.id])} title={$_('events.ack')}>
              <Icon name="check" size={13} />
            </button>
          {:else}
            <span class="acked-mark" title={$_('events.acked')}><Icon name="check" size={12} /></span>
          {/if}
        </div>
      {/each}

      {#if $eventsStore.nextCursor}
        <div class="load-more">
          <button class="btn tertiary" on:click={() => eventsStore.loadMore()} disabled={$eventsStore.loading}>
            {$eventsStore.loading ? $_('common.loading') : $_('events.loadMore')}
          </button>
        </div>
      {/if}
    {/if}
  </div>
</div>

{#if openPayload}
  <div class="modal-overlay" on:click={onBackdrop(() => (openPayload = null))} role="presentation">
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1">
      <h3>{$_('events.payloadTitle')}</h3>
      <textarea readonly>{openPayload.body}</textarea>
      <div class="modal-actions">
        <button class="btn tertiary" on:click={() => (openPayload = null)}>{$_('common.close')}</button>
      </div>
    </div>
  </div>
{/if}

{#if showClearConfirm}
  <ConfirmDialog
    title={$_('events.clearAll')}
    text={$_('events.clearConfirm')}
    confirmLabel={$_('events.clearAll')}
    cancelLabel={$_('common.cancel')}
    confirmIcon="trash-2"
    danger
    on:confirm={confirmClear}
    on:cancel={() => (showClearConfirm = false)}
  />
{/if}

<style>
  .panel {
    max-height: calc(100vh - 120px);
    display: flex;
    flex-direction: column;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
  }

  .header h3 {
    margin: 0;
    font-size: 1.2em;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .unacked-pill {
    font-size: 0.62em;
    font-weight: 700;
    padding: 2px 8px;
    border-radius: 10px;
    color: var(--text-on-accent);
    background-color: var(--error-color);
  }

  .header-actions {
    display: flex;
    gap: 8px;
  }

  .header-actions .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .filters {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 10px;
  }

  .filters input[type='text'] {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .chip-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 10px;
    font-size: 0.8em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .chip.on {
    color: var(--accent-color);
    border-color: var(--accent-border);
    background-color: var(--accent-subtle);
    font-weight: 600;
  }

  .chip-count {
    font-size: 0.85em;
    font-weight: 700;
    color: var(--error-color);
  }

  /* Severity is never carried by colour alone: every chip is labelled. */
  .chip.sev-critical.on { color: #d03b3b; border-color: #d03b3b; }
  .chip.sev-major.on { color: #ec835a; border-color: #ec835a; }
  .chip.sev-minor.on { color: #fab219; border-color: #fab219; }

  .toggle {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 0.8em;
    color: var(--text-muted);
    cursor: pointer;
  }

  .count {
    margin-left: auto;
    font-size: 0.8em;
    color: var(--text-dimmed);
  }

  .bulk-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    margin-bottom: 10px;
    font-size: 0.85em;
    background-color: var(--accent-subtle);
    border: 1px solid var(--accent-border);
    border-radius: 4px;
  }

  .bulk-bar .spacer {
    flex: 1;
  }

  .bulk-bar .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .events-container {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .event-row {
    display: grid;
    grid-template-columns: 26px 10px 140px 90px minmax(90px, 1fr) minmax(0, 1fr) minmax(140px, 2fr) 26px 26px;
    align-items: center;
    gap: 8px;
    padding: 7px 10px;
    border-bottom: 1px solid var(--border-color);
    border-left: 3px solid transparent;
    font-size: 0.85em;
  }

  .event-row:last-child {
    border-bottom: none;
  }

  .event-row.acked {
    opacity: 0.55;
  }

  .event-row.selected {
    background-color: var(--accent-subtle-medium);
  }

  .event-row.sev-critical { border-left-color: #d03b3b; }
  .event-row.sev-major { border-left-color: #ec835a; }
  .event-row.sev-minor { border-left-color: #fab219; }
  .event-row.sev-warning { border-left-color: var(--text-muted); }
  .event-row.sev-info { border-left-color: transparent; }

  .sev-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background-color: currentColor;
    opacity: 0.5;
  }

  .sel {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    box-sizing: border-box;
    border: 2px solid var(--text-muted);
    border-radius: 4px;
    background: none;
    color: var(--text-on-accent);
    cursor: pointer;
  }

  .sel.on {
    background-color: var(--accent-color);
    border-color: var(--accent-color);
  }

  .ts {
    color: var(--text-dimmed);
    font-variant-numeric: tabular-nums;
  }

  .cat {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .src {
    color: var(--text-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .oid {
    font-family: 'Courier New', monospace;
    font-size: 0.85em;
    color: var(--oid-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .summary {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .acked-mark {
    color: var(--success-color);
    display: inline-flex;
    justify-content: center;
  }

  .load-more {
    display: flex;
    justify-content: center;
    padding: 10px;
  }

  .modal-overlay {
    position: fixed;
    inset: 0;
    background-color: var(--backdrop-color-strong);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal {
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 20px;
    min-width: 600px;
    max-width: 80vw;
    max-height: 80vh;
    display: flex;
    flex-direction: column;
  }

  .modal h3 {
    margin-top: 0;
  }

  .modal textarea {
    flex: 1;
    min-height: 320px;
    margin: 14px 0;
    padding: 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: 'Courier New', monospace;
    font-size: 0.85em;
    resize: vertical;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }
</style>
