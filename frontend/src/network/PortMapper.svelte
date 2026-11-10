<script>
  import { onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import TargetChips from './TargetChips.svelte';
  import { PortMapScan, PortMapCancel } from '../../wailsjs/go/app/App';
  import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
  import { settingsStore } from '../stores/settingsStore';
  import { notificationStore } from '../stores/notifications';
  import { targetLabels } from '../stores/targetLabels';
  import { getTargetsAsArray, displayTarget } from '../utils/targets';
  import { anonMode, anonymizeIp, anonymizeHost, maskMac } from '../utils/anonymize';
  import { escapeCSV, downloadFile } from '../utils/csv';
  import { buildPortMapRequest, portRows, csvRows } from '../utils/portMap';
  import { fillHeight } from '../utils/fillHeight';

  let switchTarget = '';
  let gateways = [];
  let resolveNames = true;
  let scanning = false;
  let progress = null;
  let map = null;
  let mapped = '';
  let query = '';
  let usedOnly = true;

  $: targets = getTargetsAsArray($settingsStore.targets);
  $: if (!targets.includes(switchTarget)) switchTarget = targets[0] || '';
  $: others = targets.filter((t) => t !== switchTarget);
  $: rows = map ? portRows(map, { query, usedOnly }) : [];

  EventsOn('portmap:progress', (p) => { progress = p; });
  onDestroy(() => EventsOff('portmap:progress'));

  async function scan() {
    scanning = true;
    progress = null;
    try {
      map = await PortMapScan(buildPortMapRequest(get(settingsStore), switchTarget, gateways, resolveNames));
      mapped = switchTarget;
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      scanning = false;
      progress = null;
    }
  }

  function exportCsv() {
    const text = csvRows(map).map((r) => r.map(escapeCSV).join(',')).join('\n');
    downloadFile(text, `ports-${mapped.replace(/[^\w.-]+/g, '_')}.csv`, 'text/csv');
  }

  function shown(kind, value, anon) {
    if (!anon || !value) return value;
    if (kind === 'ip') return anonymizeIp(value);
    if (kind === 'mac') return maskMac(value);
    return anonymizeHost(value);
  }

  function speed(mbps) {
    if (!mbps) return '';
    return mbps >= 1000 ? `${mbps / 1000} Gb/s` : `${mbps} Mb/s`;
  }

  function stageText(p, t) {
    if (!p) return t('portmap.progress.switch');
    return t(`portmap.progress.${p.stage}`, { values: { done: p.done + (p.stage === 'names' ? 0 : 1), total: p.total } });
  }
</script>

<div class="tool-section">
  {#if targets.length === 0}
    <div class="empty-state"><p>{$_('portmap.noTargets')}</p></div>
  {:else}
    <div class="setup">
      <label class="row-label" for="portmap-switch">{$_('portmap.switch')}</label>
      <div class="row-field">
        <select id="portmap-switch" bind:value={switchTarget} disabled={scanning}>
          {#each targets as t, i (i)}
            <option value={t}>{displayTarget(t, $targetLabels, $anonMode)}</option>
          {/each}
        </select>
        <label class="check">
          <input type="checkbox" bind:checked={resolveNames} disabled={scanning} />
          <span>{$_('portmap.resolveNames')}</span>
        </label>
        <div class="actions">
          {#if scanning}
            <span class="progress"><span class="spin"><Icon name="loader-circle" size={14} /></span>{stageText(progress, $_)}</span>
            <button class="btn secondary" on:click={() => PortMapCancel()}>
              <Icon name="square" size={12} /> {$_('portmap.cancel')}
            </button>
          {:else}
            <button class="btn" on:click={scan} disabled={!switchTarget}>
              <Icon name="network" size={14} /> {$_('portmap.scan')}
            </button>
          {/if}
        </div>
      </div>

      {#if others.length > 0}
        <span class="row-label top" id="portmap-arp">{$_('portmap.arp')}</span>
        <div class="row-field stacked" role="group" aria-labelledby="portmap-arp">
          <TargetChips targets={others} chosen={gateways.filter((g) => others.includes(g))} disabled={scanning}
            on:change={(e) => { gateways = e.detail; }} />
          <span class="field-hint">{$_('portmap.gatewaysHint')}</span>
        </div>
      {/if}
    </div>
  {/if}

  {#if map}
    <div class="toolbar">
      <div class="counts">
        <strong>{$_('portmap.summary', { values: { ports: map.ports.length, stations: map.stations } })}</strong>
        {#if map.source}
          <span class="source">{$_(`portmap.source.${map.source}`)}</span>
        {/if}
      </div>
      <div class="tools">
        <div class="search">
          <Icon name="search" size={14} />
          <input type="search" bind:value={query} placeholder={$_('portmap.filter')} aria-label={$_('portmap.filter')} />
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={usedOnly} />
          <span>{$_('portmap.usedOnly')}</span>
        </label>
        <button class="btn tertiary" on:click={exportCsv}><Icon name="download" size={14} /> CSV</button>
      </div>
    </div>

    {#if !map.source}
      <p class="note">{$_('portmap.noStations')}</p>
    {/if}
    {#each map.warnings as w, i (i)}
      <p class="note warn"><Icon name="triangle-alert" size={14} /> {w}</p>
    {/each}
    {#if map.unplaced.length > 0}
      <p class="note">{$_('portmap.unplaced', { values: { count: map.unplaced.length } })}</p>
    {/if}

    <div class="table-wrap" use:fillHeight={{ prop: 'maxHeight', min: 240 }}>
      <table>
        <thead>
          <tr>
            <th>{$_('portmap.col.port')}</th>
            <th class="num">{$_('portmap.col.vlan')}</th>
            <th>{$_('portmap.col.neighbour')}</th>
            <th>{$_('portmap.col.mac')}</th>
            <th>{$_('portmap.col.ip')}</th>
            <th>{$_('portmap.col.name')}</th>
          </tr>
        </thead>
        <tbody>
          {#each rows as row (row.key)}
            <tr class:first={row.first} class:down={!row.port.up}>
              <td>
                {#if row.first}
                  <div class="port">
                    <span class="dot" class:up={row.port.up}></span>
                    <strong>{row.port.name}</strong>
                    {#if row.port.uplink}<span class="badge">{$_('portmap.uplink')}</span>{/if}
                    {#if row.port.alias}<span class="muted">{row.port.alias}</span>{/if}
                    {#if row.port.speedMbps}<span class="muted">{speed(row.port.speedMbps)}</span>{/if}
                  </div>
                {/if}
              </td>
              <td class="num">{row.station?.vlan || (row.first ? row.port.vlan || '' : '')}</td>
              <td>
                {#if row.first && row.port.neighbour}
                  <div class="neighbour" title={row.port.neighbour.protocol.toUpperCase()}>
                    <span>{shown('host', row.port.neighbour.name, $anonMode)}</span>
                    {#if row.port.neighbour.port}<span class="muted mono">{row.port.neighbour.port}</span>{/if}
                  </div>
                {/if}
              </td>
              <td class="mono">{row.station ? shown('mac', row.station.mac, $anonMode) : ''}</td>
              <td class="mono">{row.station ? row.station.ips.map((ip) => shown('ip', ip, $anonMode)).join(', ') : ''}</td>
              <td class="name">{row.station ? row.station.names.map((n) => shown('host', n, $anonMode)).join(', ') : ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if rows.length === 0}
        <p class="empty">{$_('portmap.nothing')}</p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .tool-section {
    padding: 0 5px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  /* The settings, as the Discovery tab lays out its own: a card, each label on
     the left of what it names, every control on a row the same height. */
  .setup {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    align-items: center;
    gap: 12px 16px;
    padding: 14px 16px;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .row-label {
    font-weight: 500;
    color: var(--text-color);
  }

  .row-label.top {
    align-self: start;
    line-height: 26px;
  }

  .row-field {
    display: flex;
    align-items: center;
    gap: 16px;
    flex-wrap: wrap;
    min-width: 0;
  }

  .row-field.stacked {
    flex-direction: column;
    align-items: stretch;
    gap: 6px;
  }

  select {
    height: 34px;
    min-width: 240px;
    padding: 0 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: inherit;
  }

  .check {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 0.9em;
    color: var(--text-color);
    cursor: pointer;
    white-space: nowrap;
  }

  .check input {
    margin: 0;
    width: 15px;
    height: 15px;
    accent-color: var(--accent-color);
    cursor: pointer;
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-left: auto;
  }

  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    height: 34px;
    padding: 0 14px;
    white-space: nowrap;
  }

  .progress {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: 0.85em;
  }

  .spin {
    display: inline-flex;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  @media (prefers-reduced-motion: reduce) {
    .spin { animation: none; }
  }

  .field-hint {
    font-size: 0.78em;
    color: var(--text-muted);
    line-height: 1.4;
  }

  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }

  .counts {
    display: flex;
    align-items: baseline;
    gap: 10px;
    font-size: 0.9em;
  }

  .source {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .tools {
    display: flex;
    align-items: center;
    gap: 16px;
  }

  .tools .btn {
    height: 32px;
  }

  .search {
    position: relative;
    display: flex;
    align-items: center;
    color: var(--text-muted);
  }

  .search :global(svg) {
    position: absolute;
    left: 9px;
    pointer-events: none;
  }

  .search input {
    width: 260px;
    height: 32px;
    padding: 0 10px 0 30px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: inherit;
    font-size: 0.9em;
  }

  .search input:focus {
    border-color: var(--accent-color);
    outline: none;
  }

  .note {
    margin: 0;
    font-size: 0.85em;
    color: var(--text-muted);
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .note.warn {
    color: var(--warning-color);
  }

  .table-wrap {
    overflow: auto;
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }

  thead {
    position: sticky;
    top: 0;
    z-index: 1;
    background-color: var(--bg-lighter-color);
  }

  th {
    text-align: left;
    padding: 8px 12px;
    font-weight: 600;
    white-space: nowrap;
    border-bottom: 2px solid var(--border-color);
  }

  td {
    height: 30px;
    padding: 4px 12px;
    color: var(--text-color);
    vertical-align: middle;
    white-space: nowrap;
  }

  /* A port's rows are one group: the line goes between ports, never between a
     port and the stations behind it. */
  tr.first td {
    border-top: 1px solid var(--border-color);
  }

  tbody tr:first-child td {
    border-top: none;
  }

  tr.down td {
    color: var(--text-muted);
  }

  .num {
    width: 1%;
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  .port,
  .neighbour {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .name {
    white-space: normal;
  }

  .muted {
    color: var(--text-muted);
    font-size: 0.92em;
  }

  /* A family named first: the generic one alone is shrunk by the browser's
     default and sits the text off the line its neighbours are on. */
  .mono {
    font-family: 'Courier New', monospace;
  }

  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
  }

  .dot.up {
    background: var(--success-color);
  }

  .badge {
    padding: 1px 7px;
    border-radius: 8px;
    font-size: 0.8em;
    font-weight: 600;
    background: var(--accent-color);
    color: #fff;
  }

  .empty {
    margin: 0;
    padding: 14px;
    color: var(--text-muted);
    font-size: 0.9em;
    text-align: center;
  }
</style>
