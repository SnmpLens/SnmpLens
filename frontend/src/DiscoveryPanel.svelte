<script>
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { SnmpDiscover, NetworkPing, NetworkTraceroute } from '../wailsjs/go/app/App';
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime';
  import { settingsStore } from './stores/settingsStore';
  import { notificationStore } from './stores/notifications';
  import { escapeCSV, downloadFile } from './utils/csv';
  import { buildDiscoverRequest } from './utils/snmpParams';
  import { anonMode, anonymizeIp, anonymizeHost, maskSysDescr, anonymizeCidr } from './utils/anonymize';
  import { targetLabels } from './stores/targetLabels';
  import { displayTarget, targetTitle } from './utils/targets';
  import { assignProfile, findProfile, withProfile } from './utils/credentialProfiles.js';
  import PortMapper from './network/PortMapper.svelte';

  let activeSubTab = 'discovery'; // 'discovery' | 'ports' | 'ping' | 'traceroute'

  let cidr = '192.168.1.0/24';
  let scanTimeout = 2;
  let isScanning = false;
  let results = [];
  let progress = { current: 0, total: 0, ip: '' };
  let showOnlyReachable = false;

  // The identifiers the scan asks with: '' for the defaults, or a credential
  // profile's id. A device found this way is added WITH that profile — it
  // answered to those identifiers, and to nothing else we know of.
  let scanProfile = '';
  $: if (scanProfile && !findProfile($settingsStore, scanProfile)) scanProfile = '';

  function withScanProfile(overrides, addresses) {
    let next = overrides || {};
    if (!scanProfile) return next;
    for (const address of addresses) next = assignProfile(next, address, scanProfile);
    return next;
  }

  // Ping state
  let pingTarget = '';
  let pingCount = 4;
  let isPinging = false;
  let pingResult = null;

  // Traceroute state
  let traceTarget = '';
  let isTracing = false;
  let traceHops = [];
  let traceComplete = false;

  $: filteredResults = showOnlyReachable ? results.filter(r => r.reachable) : results;
  $: reachableCount = results.filter(r => r.reachable).length;

  async function handleScan() {
    const t = get(_);
    if (!cidr.trim()) {
      notificationStore.add(t('discovery.cidrPlaceholder'), 'error');
      return;
    }
    isScanning = true;
    results = [];
    progress = { current: 0, total: 0, ip: '' };

    const unsub = EventsOn('discoveryProgress', (data) => {
      progress = data;
    });

    try {
      results = await SnmpDiscover(buildDiscoverRequest(withProfile($settingsStore, scanProfile), cidr.trim(), scanTimeout));
      notificationStore.add(t('discovery.scanComplete', { values: { count: reachableCount, total: results.length } }), 'success');
    } catch (err) {
      notificationStore.add(t('discovery.scanFailed', { values: { error: err } }), 'error');
    } finally {
      isScanning = false;
      EventsOff('discoveryProgress');
    }
  }

  function addToTargets(ip) {
    const t = get(_);
    const current = $settingsStore.targets || '';
    const lines = current.split('\n').map(l => l.trim()).filter(l => l);
    if (lines.includes(ip)) {
      notificationStore.add(t('discovery.alreadyInTargets', { values: { ip } }), 'info');
      return;
    }
    lines.push(ip);
    settingsStore.save({
      ...$settingsStore,
      targets: lines.join('\n'),
      targetOverrides: withScanProfile($settingsStore.targetOverrides, [ip]),
    });
    notificationStore.add(t('discovery.addedToTargets', { values: { ip } }), 'success');
  }

  function addAllReachable() {
    const t = get(_);
    const current = $settingsStore.targets || '';
    const lines = current.split('\n').map(l => l.trim()).filter(l => l);
    const added = [];
    for (const r of results) {
      if (r.reachable && !lines.includes(r.ip)) {
        lines.push(r.ip);
        added.push(r.ip);
      }
    }
    if (added.length > 0) {
      settingsStore.save({
        ...$settingsStore,
        targets: lines.join('\n'),
        targetOverrides: withScanProfile($settingsStore.targetOverrides, added),
      });
      notificationStore.add(t('discovery.addedMultiple', { values: { count: added } }), 'success');
    } else {
      notificationStore.add(t('discovery.allAlreadyInTargets'), 'info');
    }
  }

  function exportCSV() {
    const lines = ['IP,SysName,SysDescr,SysUpTime,ResponseTime(ms),Reachable'];
    for (const r of filteredResults) {
      lines.push(`${escapeCSV(r.ip)},${escapeCSV(r.sysName)},${escapeCSV(r.sysDescr)},${escapeCSV(r.sysUpTime)},${r.responseTime},${r.reachable}`);
    }
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(lines.join('\n'), `discovery-${ts}.csv`, 'text/csv');
  }

  async function handlePing() {
    if (!pingTarget.trim() || isPinging) return;
    isPinging = true;
    pingResult = null;
    try {
      pingResult = await NetworkPing(pingTarget.trim(), pingCount);
    } catch (e) {
      const t = get(_);
      notificationStore.add(t('network.pingFailed'), 'error');
    } finally {
      isPinging = false;
    }
  }

  async function handleTraceroute() {
    if (!traceTarget.trim() || isTracing) return;
    isTracing = true;
    traceHops = [];
    traceComplete = false;

    const unsub = EventsOn('tracerouteProgress', (hop) => {
      traceHops = [...traceHops, hop];
    });

    try {
      const result = await NetworkTraceroute(traceTarget.trim());
      traceHops = result || traceHops;
      traceComplete = true;
    } catch (e) {
      const t = get(_);
      notificationStore.add(t('network.tracerouteFailed'), 'error');
    } finally {
      isTracing = false;
      EventsOff('tracerouteProgress');
    }
  }
</script>

<div class="network-panel">
  <div class="sub-tabs">
    <button class="sub-tab" class:active={activeSubTab === 'discovery'} on:click={() => activeSubTab = 'discovery'}>
      {$_('network.discovery')}
    </button>
    <button class="sub-tab" class:active={activeSubTab === 'ports'} on:click={() => activeSubTab = 'ports'}>
      {$_('network.ports')}
    </button>
    <button class="sub-tab" class:active={activeSubTab === 'ping'} on:click={() => activeSubTab = 'ping'}>
      {$_('network.ping')}
    </button>
    <button class="sub-tab" class:active={activeSubTab === 'traceroute'} on:click={() => activeSubTab = 'traceroute'}>
      {$_('network.traceroute')}
    </button>
  </div>

  {#if activeSubTab === 'discovery'}
    <div class="panel">
      <div class="setup-form">
        <div class="form-group">
          <label for="discovery-cidr">{$_('discovery.cidrLabel')}</label>
          <input id="discovery-cidr" type="text" bind:value={cidr} placeholder={$_('discovery.cidrPlaceholder')} />
        </div>
        <div class="form-row">
          <div class="form-group compact">
            <label for="discovery-profile">{$_('profiles.identifiers')}</label>
            <select id="discovery-profile" bind:value={scanProfile}>
              <option value="">{$_('profiles.useDefault')}</option>
              {#each $settingsStore.credentialProfiles || [] as p (p.id)}
                <option value={p.id}>{p.name} · {p.version}</option>
              {/each}
            </select>
          </div>
          <div class="form-group compact">
            <label for="discovery-timeout">{$_('discovery.timeoutLabel')}</label>
            <select id="discovery-timeout" bind:value={scanTimeout}>
              <option value={1}>1s</option>
              <option value={2}>2s</option>
              <option value={5}>5s</option>
            </select>
          </div>
          <button class="btn btn-primary" on:click={handleScan} disabled={isScanning}>
            {isScanning ? $_('discovery.scanning') : $_('discovery.scan')}
          </button>
        </div>
      </div>

      {#if isScanning && progress.total > 0}
        <div class="progress-section">
          <div class="progress-bar-bg">
            <div class="progress-bar-fill" style="width: {(progress.current / progress.total) * 100}%"></div>
          </div>
          <span class="progress-text">{progress.current}/{progress.total} — {$anonMode ? anonymizeIp(progress.ip) : progress.ip}</span>
        </div>
      {/if}

      {#if results.length > 0}
        <div class="results-header">
          <div class="results-info">
            <span>{$_('discovery.reachableCount', { values: { reachable: reachableCount, total: results.length } })}</span>
            <label class="toggle-label">
              <input type="checkbox" bind:checked={showOnlyReachable} />
              {$_('discovery.showReachableOnly')}
            </label>
          </div>
          <div class="results-actions">
            <button class="btn btn-small" on:click={addAllReachable}>{$_('discovery.addAllReachable')}</button>
            <button class="btn-export" on:click={exportCSV}>{$_('results.csv')}</button>
          </div>
        </div>

        <div class="discovery-table">
          <table>
            <thead>
              <tr>
                <th>{$_('discovery.tableIp')}</th>
                <th>{$_('discovery.tableSysName')}</th>
                <th>{$_('discovery.tableSysDescr')}</th>
                <th>{$_('discovery.tableSysUpTime')}</th>
                <th>{$_('discovery.tableTime')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {#each filteredResults as r}
                <tr class:reachable={r.reachable} class:unreachable={!r.reachable}>
                  <td class="ip-cell" title={targetTitle(r.ip, $anonMode)}>{displayTarget(r.ip, $targetLabels, $anonMode)}</td>
                  <td title={$anonMode ? anonymizeHost(r.sysName) : r.sysName}>{$anonMode ? (r.sysName ? anonymizeHost(r.sysName) : '-') : (r.sysName || '-')}</td>
                  <td class="descr-cell" title={$anonMode ? maskSysDescr(r.sysDescr) : r.sysDescr}>{$anonMode ? (r.sysDescr ? maskSysDescr(r.sysDescr) : (r.error ? r.error : '-')) : (r.sysDescr || (r.error ? r.error : '-'))}</td>
                  <td>{r.sysUpTime || '-'}</td>
                  <td class="time-cell">{r.responseTime}ms</td>
                  <td>
                    {#if r.reachable}
                      <button class="btn btn-small" on:click={() => addToTargets(r.ip)}>{$_('discovery.addToTargets')}</button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else if !isScanning}
        <div class="empty-state">
          <p>{$_('discovery.empty')}</p>
          <p class="hint">{$_('discovery.emptyHint')}</p>
        </div>
      {/if}
    </div>
  {/if}

  {#if activeSubTab === 'ports'}
    <PortMapper />
  {/if}

  {#if activeSubTab === 'ping'}
    <div class="tool-section">
      <div class="tool-form">
        <div class="form-group">
          <label for="ping-target">{$_('network.pingTarget')}</label>
          <input id="ping-target" type="text" bind:value={pingTarget} placeholder="192.168.1.1" on:keydown={(e) => e.key === 'Enter' && handlePing()} />
        </div>
        <div class="form-group">
          <label for="ping-count">{$_('network.pingCount')}</label>
          <select id="ping-count" bind:value={pingCount}>
            <option value={4}>4</option>
            <option value={8}>8</option>
            <option value={16}>16</option>
          </select>
        </div>
        <button class="btn" on:click={handlePing} disabled={isPinging || !pingTarget.trim()}>
          {isPinging ? $_('network.pingRunning') : $_('network.pingRun')}
        </button>
      </div>

      {#if pingResult}
        <div class="ping-results">
          <div class="ping-stats">
            <div class="stat">
              <span class="stat-label">{$_('network.pingSent')}</span>
              <span class="stat-value">{pingResult.sent}</span>
            </div>
            <div class="stat">
              <span class="stat-label">{$_('network.pingReceived')}</span>
              <span class="stat-value">{pingResult.received}</span>
            </div>
            <div class="stat">
              <span class="stat-label">{$_('network.pingLoss')}</span>
              <span class="stat-value" class:error={pingResult.lossPercent > 0}>{pingResult.lossPercent}%</span>
            </div>
            <div class="stat">
              <span class="stat-label">{$_('network.pingMin')}</span>
              <span class="stat-value">{pingResult.minMs} ms</span>
            </div>
            <div class="stat">
              <span class="stat-label">{$_('network.pingAvg')}</span>
              <span class="stat-value">{pingResult.avgMs} ms</span>
            </div>
            <div class="stat">
              <span class="stat-label">{$_('network.pingMax')}</span>
              <span class="stat-value">{pingResult.maxMs} ms</span>
            </div>
          </div>
          <!-- The per-probe round trips, which pro-bing collects and nothing showed.
             `pingResult.output` was a field the Go PingResult never had, so this
             box rendered permanently empty under every ping. -->
        {#if pingResult.replies?.length}
          <pre class="raw-output">{pingResult.replies.map((ms, i) => `reply ${i + 1}: ${ms.toFixed(2)} ms`).join('\n')}</pre>
        {/if}
        </div>
      {/if}
    </div>
  {/if}

  {#if activeSubTab === 'traceroute'}
    <div class="tool-section">
      <div class="tool-form">
        <div class="form-group">
          <label for="trace-target">{$_('network.tracerouteTarget')}</label>
          <input id="trace-target" type="text" bind:value={traceTarget} placeholder="8.8.8.8" on:keydown={(e) => e.key === 'Enter' && handleTraceroute()} />
        </div>
        <button class="btn" on:click={handleTraceroute} disabled={isTracing || !traceTarget.trim()}>
          {isTracing ? $_('network.tracerouteRunning') : $_('network.tracerouteRun')}
        </button>
      </div>

      {#if traceHops.length > 0}
        <table class="trace-table">
          <thead>
            <tr>
              <th>{$_('network.hopNumber')}</th>
              <th>{$_('network.hopRtt1')}</th>
              <th>{$_('network.hopRtt2')}</th>
              <th>{$_('network.hopRtt3')}</th>
              <th>{$_('network.hopIp')}</th>
            </tr>
          </thead>
          <tbody>
            {#each traceHops as hop}
              <tr class:timeout={hop.timeout}>
                <td>{hop.hop}</td>
                <td>{hop.rtt1}</td>
                <td>{hop.rtt2}</td>
                <td>{hop.rtt3}</td>
                <td class="ip-cell">{$anonMode ? (hop.ip ? anonymizeIp(hop.ip) : '*') : (hop.ip || '*')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if isTracing}
          <div class="trace-progress">{$_('network.tracerouteRunning')}...</div>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .network-panel {
    padding: 0;
  }

  .sub-tabs {
    display: flex;
    border-bottom: 1px solid var(--border-color);
    margin-bottom: 15px;
  }

  .sub-tab {
    padding: 8px 16px;
    border: none;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    font-size: 0.9em;
    font-weight: 500;
  }

  .sub-tab:hover {
    color: var(--text-color);
  }

  .sub-tab.active {
    color: var(--accent-color);
    border-bottom-color: var(--accent-color);
  }

  .tool-section {
    padding: 0 5px;
  }

  .tool-form {
    display: flex;
    gap: 10px;
    align-items: flex-end;
    margin-bottom: 15px;
    flex-wrap: wrap;
  }

  .tool-form .form-group {
    margin-bottom: 0;
  }

  .tool-form input[type="text"] {
    min-width: 200px;
  }

  .ping-results {
    margin-top: 10px;
  }

  .ping-stats {
    display: flex;
    gap: 20px;
    flex-wrap: wrap;
    margin-bottom: 15px;
    padding: 12px;
    background-color: var(--bg-light-color);
    border-radius: 6px;
    border: 1px solid var(--border-color);
  }

  .stat {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .stat-label {
    font-size: 0.75em;
    color: var(--text-muted);
    font-weight: 500;
  }

  .stat-value {
    font-size: 1.1em;
    font-weight: 600;
    font-family: 'Courier New', monospace;
    color: var(--text-color);
  }

  .stat-value.error {
    color: var(--error-color);
  }

  .raw-output {
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding: 12px;
    font-family: 'Courier New', monospace;
    font-size: 0.8em;
    overflow-x: auto;
    white-space: pre-wrap;
    color: var(--text-dimmed);
    max-height: 300px;
    overflow-y: auto;
    margin: 0;
  }

  .trace-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }

  .trace-table th {
    background-color: var(--bg-lighter-color);
    padding: 6px 10px;
    text-align: left;
    border-bottom: 2px solid var(--border-color);
    font-weight: 600;
  }

  .trace-table td {
    padding: 5px 10px;
    border-bottom: 1px solid var(--border-color);
    font-family: 'Courier New', monospace;
  }

  .trace-table tr.timeout td {
    color: var(--text-muted);
  }

  .trace-progress {
    padding: 10px;
    color: var(--text-muted);
    font-style: italic;
  }

  .setup-form {
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 15px;
    margin-bottom: 15px;
  }

  .form-row {
    display: flex;
    gap: 10px;
    align-items: center;
  }

  .form-group.compact {
    margin-bottom: 0;
    flex: 0 0 auto;
  }

  .form-group.compact select { width: 80px; }

  .progress-section {
    margin-bottom: 15px;
  }

  .progress-bar-bg {
    height: 6px;
    background-color: var(--bg-lighter-color);
    border-radius: 3px;
    overflow: hidden;
    margin-bottom: 5px;
  }

  .progress-bar-fill {
    height: 100%;
    background-color: var(--accent-color);
    transition: width 0.1s;
  }

  .progress-text {
    font-size: 0.85em;
    color: var(--text-muted);
  }

  .results-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 10px;
    flex-wrap: wrap;
    gap: 8px;
  }

  .results-info {
    display: flex;
    align-items: center;
    gap: 15px;
    font-size: 0.9em;
  }

  .results-actions {
    display: flex;
    gap: 8px;
    align-items: center;
  }

  .toggle-label {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    font-size: 0.85em;
  }

  .btn-export {
    padding: 4px 10px;
    font-size: 0.8em;
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-dimmed);
    border-radius: 3px;
    cursor: pointer;
  }

  .btn-export:hover {
    border-color: var(--accent-color);
    color: var(--accent-color);
  }

  .discovery-table {
    max-height: 500px;
    overflow: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }

  thead {
    position: sticky;
    top: 0;
    background-color: var(--bg-lighter-color);
    z-index: 1;
  }

  th {
    text-align: left;
    padding: 8px 10px;
    border-bottom: 2px solid var(--border-color);
    font-weight: 600;
  }

  td {
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-color);
  }

  .ip-cell {
    font-family: 'Courier New', monospace;
    font-weight: 600;
    color: var(--oid-color);
    white-space: nowrap;
  }

  .descr-cell {
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .time-cell {
    text-align: right;
    white-space: nowrap;
  }

  tr.reachable { background-color: var(--success-subtle); }
  tr.unreachable { opacity: 0.5; }

  /* .empty-state is defined globally in shared.css */

  .hint { font-size: 0.9em; font-style: italic; }
</style>
