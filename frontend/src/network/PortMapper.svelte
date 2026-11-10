<script>
  import { onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import { PortMapScan, PortMapCancel } from '../../wailsjs/go/app/App';
  import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
  import { settingsStore } from '../stores/settingsStore';
  import { notificationStore } from '../stores/notifications';
  import { targetLabels } from '../stores/targetLabels';
  import { getTargetsAsArray, displayTarget } from '../utils/targets';
  import { anonMode, anonymizeIp, anonymizeHost, maskMac } from '../utils/anonymize';
  import { escapeCSV, downloadFile } from '../utils/csv';
  import { buildPortMapRequest, portRows, csvRows } from '../utils/portMap';

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

  function toggleGateway(address, on) {
    gateways = on ? [...gateways, address] : gateways.filter((g) => g !== address);
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
    return mbps >= 1000 ? `${mbps / 1000} G` : `${mbps} M`;
  }

  function stageText(p, t) {
    if (!p) return t('portmap.progress.switch');
    return t(`portmap.progress.${p.stage}`, { values: { done: p.done + (p.stage === 'names' ? 0 : 1), total: p.total } });
  }
</script>

<div class="tool-section">
  {#if targets.length === 0}
    <p class="empty">{$_('portmap.noTargets')}</p>
  {:else}
    <div class="tool-form">
      <div class="form-group">
        <label for="portmap-switch">{$_('portmap.switch')}</label>
        <select id="portmap-switch" bind:value={switchTarget} disabled={scanning}>
          {#each targets as t, i (i)}
            <option value={t}>{displayTarget(t, $targetLabels, $anonMode)}</option>
          {/each}
        </select>
      </div>
      <label class="check">
        <input type="checkbox" bind:checked={resolveNames} disabled={scanning} />
        {$_('portmap.resolveNames')}
      </label>
      {#if scanning}
        <button class="btn secondary" on:click={() => PortMapCancel()}>{$_('portmap.cancel')}</button>
        <span class="progress">{stageText(progress, $_)}</span>
      {:else}
        <button class="btn" on:click={scan} disabled={!switchTarget}>
          <Icon name="network" size={14} /> {$_('portmap.scan')}
        </button>
      {/if}
    </div>

    {#if targets.length > 1}
      <details class="gateways">
        <summary>{$_('portmap.gateways', { values: { count: gateways.length } })}</summary>
        <p class="hint">{$_('portmap.gatewaysHint')}</p>
        <div class="gateway-list">
          {#each targets.filter((t) => t !== switchTarget) as t, i (i)}
            <label class="check">
              <input type="checkbox" checked={gateways.includes(t)} disabled={scanning}
                on:change={(e) => toggleGateway(t, e.currentTarget.checked)} />
              {displayTarget(t, $targetLabels, $anonMode)}
            </label>
          {/each}
        </div>
      </details>
    {/if}
  {/if}

  {#if map}
    <div class="summary">
      <span>{$_('portmap.summary', { values: { ports: map.ports.length, stations: map.stations } })}</span>
      {#if map.source}
        <span class="muted">{$_(`portmap.source.${map.source}`)}</span>
      {/if}
      <div class="spacer"></div>
      <input class="filter" type="search" bind:value={query} placeholder={$_('portmap.filter')} />
      <label class="check">
        <input type="checkbox" bind:checked={usedOnly} />
        {$_('portmap.usedOnly')}
      </label>
      <button class="btn tertiary" on:click={exportCsv}><Icon name="download" size={14} /> CSV</button>
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

    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>{$_('portmap.col.port')}</th>
            <th>{$_('portmap.col.vlan')}</th>
            <th>{$_('portmap.col.neighbour')}</th>
            <th>{$_('portmap.col.mac')}</th>
            <th>{$_('portmap.col.ip')}</th>
            <th>{$_('portmap.col.name')}</th>
          </tr>
        </thead>
        <tbody>
          {#each rows as row (row.key)}
            <tr class:first={row.first} class:down={!row.port.up}>
              <td class="port">
                {#if row.first}
                  <span class="dot" class:up={row.port.up}></span>
                  <strong>{row.port.name}</strong>
                  {#if row.port.uplink}<span class="badge">{$_('portmap.uplink')}</span>{/if}
                  {#if row.port.alias}<span class="muted">{row.port.alias}</span>{/if}
                  {#if row.port.speedMbps}<span class="muted">{speed(row.port.speedMbps)}</span>{/if}
                {/if}
              </td>
              <td>{row.station?.vlan || (row.first ? row.port.vlan || '' : '')}</td>
              <td>
                {#if row.first && row.port.neighbour}
                  <span title={row.port.neighbour.protocol.toUpperCase()}>
                    {shown('host', row.port.neighbour.name, $anonMode)}
                    {#if row.port.neighbour.port}<span class="muted">{row.port.neighbour.port}</span>{/if}
                  </span>
                {/if}
              </td>
              <td class="mono">{row.station ? shown('mac', row.station.mac, $anonMode) : ''}</td>
              <td class="mono">{row.station ? row.station.ips.map((ip) => shown('ip', ip, $anonMode)).join(', ') : ''}</td>
              <td>{row.station ? row.station.names.map((n) => shown('host', n, $anonMode)).join(', ') : ''}</td>
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
    gap: 10px;
  }

  .tool-form {
    display: flex;
    gap: 12px;
    align-items: flex-end;
    flex-wrap: wrap;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .form-group label {
    font-size: 0.8em;
    color: var(--text-muted);
  }

  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.85em;
    color: var(--text-color);
  }

  .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .progress,
  .muted,
  .hint {
    color: var(--text-muted);
    font-size: 0.82em;
  }

  .hint {
    margin: 6px 0;
  }

  .gateways summary {
    cursor: pointer;
    font-size: 0.85em;
    color: var(--text-color);
  }

  .gateway-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
  }

  .summary {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    font-size: 0.9em;
  }

  .spacer {
    flex: 1;
  }

  .filter {
    min-width: 220px;
  }

  .note {
    margin: 0;
    font-size: 0.82em;
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
    max-height: calc(100vh - 290px);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }

  th {
    position: sticky;
    top: 0;
    background: var(--bg-light-color);
    text-align: left;
    padding: 6px 10px;
    color: var(--text-muted);
    font-weight: 500;
    border-bottom: 1px solid var(--border-color);
  }

  td {
    padding: 4px 10px;
    color: var(--text-color);
    vertical-align: top;
  }

  tr.first td {
    border-top: 1px solid var(--border-color);
  }

  tr.down td {
    color: var(--text-muted);
  }

  .port {
    white-space: nowrap;
  }

  .port span {
    margin-left: 6px;
  }

  .mono {
    font-family: var(--font-mono, monospace);
  }

  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
    margin: 0 4px 0 0 !important;
  }

  .dot.up {
    background: var(--success-color);
  }

  .badge {
    font-size: 0.75em;
    padding: 1px 6px;
    border-radius: 8px;
    background: var(--accent-color);
    color: #fff;
  }

  .empty {
    color: var(--text-muted);
    font-size: 0.85em;
    padding: 10px;
  }
</style>
