<script>
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { onMount, onDestroy } from 'svelte';
  import { trapStore } from './stores/trapStore';
  import { settingsStore } from './stores/settingsStore';
  import { notificationStore } from './stores/notifications';
  import { SendTrap, SendInform, TrapListenerEngineID } from './bridge/App';
  import Trap from './Trap.svelte';
  import { escapeCSV, downloadFile } from './utils/csv';
  import { anonMode, anonymizeIp, maskString } from './utils/anonymize';
  import Icon from './Icon.svelte';
  import { DEFAULT_REF, trapReception } from './utils/credentialProfiles.js';
  import { requestSettings } from './stores/settingsRequest';

  let searchTerm = '';
  let filterVersion = 'All';
  let filterPduType = 'All';
  let filterTimeRange = 'All'; // All, 5, 15, 30, 60 (minutes)
  let filteredTraps = [];
  let showClearConfirm = false;

  // Send Trap state
  let showSendTrap = false;
  // Trap or inform. An inform is acknowledged by the receiver, which is the
  // only reason to send one — and the reason its result is reported
  // differently: a trap that vanished and a trap that arrived look identical.
  let sendKind = 'trap';
  let sendTarget = '';
  let sendPort = 162;
  let sendCommunity = '';
  let sendVersion = 'v2c';
  let sendTrapOid = '1.3.6.1.4.1.99999.1';
  let sendVarbinds = [];
  let isSending = false;

  function addVarbind() {
    sendVarbinds = [...sendVarbinds, { oid: '', type: 'OctetString', value: '' }];
  }

  function removeVarbind(index) {
    sendVarbinds = sendVarbinds.filter((_, i) => i !== index);
  }

  async function handleSendTrap() {
    const t = get(_);
    if (!sendTarget.trim()) {
      notificationStore.add(t('traps.enterTarget'), 'error');
      return;
    }
    if (!sendTrapOid.trim()) {
      notificationStore.add(t('traps.enterTrapOid'), 'error');
      return;
    }
    isSending = true;
    try {
      const args = [
        sendTarget.trim(),
        sendPort,
        sendCommunity || $settingsStore.community,
        sendVersion,
        sendTrapOid.trim(),
        sendVarbinds.filter(v => v.oid.trim()),
      ];

      if (sendKind === 'inform') {
        const res = await SendInform(...args);
        if (res.error || !res.acknowledged) {
          // Not a notification failure to gloss over: an unacknowledged inform
          // is an inform that did NOT arrive, which is the whole distinction.
          notificationStore.add(
            t('traps.informFailed', { values: { error: res.error || t('traps.informNoAck') } }), 'error');
        } else {
          notificationStore.add(
            t('traps.informAcked', { values: { target: sendTarget, ms: res.responseTimeMs } }), 'success');
        }
      } else {
        await SendTrap(...args);
        notificationStore.add(t('traps.trapSent', { values: { target: sendTarget, port: sendPort } }), 'success');
      }
    } catch (err) {
      notificationStore.add(t('traps.trapSendFailed', { values: { error: String(err) } }), 'error');
    } finally {
      isSending = false;
    }
  }

  // Inform the store when this panel is visible/hidden
  onMount(() => trapStore.setPanelVisibility(true));
  onDestroy(() => trapStore.setPanelVisibility(false));

  // What a device sending this machine SNMPv3 INFORMs is configured with: the
  // listener cannot be discovered, so it has to be told.
  let listenerEngineId = '';
  onMount(() => {
    TrapListenerEngineID().then((id) => (listenerEngineId = id || '')).catch(() => {});
  });

  function toggleListener() {
    if ($trapStore.isListening) {
      trapStore.stop();
    } else {
      trapStore.start();
    }
  }

  // Who the listener hears: every SNMPv3 identity, and whether it is accepted.
  $: reception = trapReception($settingsStore);
  $: acceptedUsers = reception.filter((u) => u.accepted);
  $: excludedUsers = reception.filter((u) => !u.accepted);

  function refusalOf(u, refused) {
    return (refused || []).find((r) => r.user === u.user) || null;
  }

  function userTitle(u, refused, anon, t) {
    const line = t('traps.reception.userTitle', { values: { user: anon ? maskString(u.user) : u.user, level: u.secLevel } });
    const refusal = refusalOf(u, refused);
    return refusal ? `${line}\n${t('traps.reception.refused', { values: { reason: refusal.reason } })}` : line;
  }

  function excludedTitle(list, t) {
    const names = list.map((u) => (u.ref === DEFAULT_REF ? t('traps.reception.defaultUser') : u.name)).join(', ');
    return t('traps.reception.excludedTitle', { values: { names } });
  }

  function handleClear() {
    if (showClearConfirm) {
      trapStore.clearTraps();
      showClearConfirm = false;
    } else {
      showClearConfirm = true;
      setTimeout(() => { showClearConfirm = false; }, 3000);
    }
  }

  function exportCSV() {
    const lines = ['Timestamp,Source,Version,Type,Variables'];
    for (const trap of filteredTraps) {
      const vars = (trap.variables || []).map(v => `${v.oid}=${JSON.stringify(v.value)}`).join('; ');
      lines.push(`${escapeCSV(trap.timestamp)},${escapeCSV(trap.source)},${escapeCSV(trap.version)},${escapeCSV(trap.pduType)},${escapeCSV(vars)}`);
    }
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(lines.join('\n'), `traps-${ts}.csv`, 'text/csv');
  }

  function exportJSON() {
    const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(JSON.stringify(filteredTraps, null, 2), `traps-${ts}.json`, 'application/json');
  }

  // Reactive statement to filter traps
  $: {
    let traps = $trapStore.traps;

    // Filter by search term
    if (searchTerm) {
      const lowerSearch = searchTerm.toLowerCase();
      traps = traps.filter(trap => {
        if (trap.source.toLowerCase().includes(lowerSearch)) return true;
        for (const v of trap.variables) {
          if (v.oid.toLowerCase().includes(lowerSearch)) return true;
          if (JSON.stringify(v.value).toLowerCase().includes(lowerSearch)) return true;
        }
        return false;
      });
    }

    // Filter by version
    if (filterVersion !== 'All') {
      traps = traps.filter(t => t.version === filterVersion);
    }

    // Filter by pduType
    if (filterPduType !== 'All') {
      traps = traps.filter(t => (t.pduType || 'Trap') === filterPduType);
    }

    // Filter by time range
    if (filterTimeRange !== 'All') {
      const minutes = parseInt(filterTimeRange);
      const cutoff = new Date(Date.now() - minutes * 60 * 1000);
      traps = traps.filter(t => t.timestamp && new Date(t.timestamp) >= cutoff);
    }

    filteredTraps = traps;
  }
</script>

<div class="panel">
  <div class="form-group">
    <p>{$_('traps.listeningInfo', { values: { port: $settingsStore.trapPort } })}</p>
    <div class="header-buttons">
      <button class="btn" on:click={toggleListener}>
        {$trapStore.isListening ? $_('traps.stopListening') : $_('traps.startListening')}
      </button>
      <button class="btn" class:active={showSendTrap} on:click={() => showSendTrap = !showSendTrap}>
        {showSendTrap ? $_('traps.hideSender') : $_('traps.sendTrap')}
      </button>
      {#if $trapStore.traps.length > 0}
        <button class="btn btn-danger" on:click={handleClear}>
          {showClearConfirm ? $_('traps.confirmClear') : $_('traps.clearAll')}
        </button>
      {/if}
    </div>
  </div>

  <!-- Who is heard. A v3 trap is authenticated against the users listed; a v1
       or v2c trap is received whatever its community, which is not checked.
       Saying so here is the point: a device whose v3 traps never arrive
       otherwise looks exactly like one that sends nothing. -->
  <div class="reception">
    <span class="rx-title"><Icon name="radio" size={13} /> {$_('traps.reception.title')}</span>
    <span class="rx-group">
      <span class="rx-label">SNMPv3</span>
      {#each acceptedUsers as u (u.ref)}
        <span class="rx-chip" class:refused={!!refusalOf(u, $trapStore.refused)}
          title={userTitle(u, $trapStore.refused, $anonMode, $_)}>
          {#if refusalOf(u, $trapStore.refused)}<Icon name="triangle-alert" size={11} />{/if}
          {u.ref === DEFAULT_REF ? $_('traps.reception.defaultUser') : u.name}
          <span class="rx-user">{$anonMode ? maskString(u.user) : u.user}</span>
        </span>
      {:else}
        <span class="rx-quiet">{$_('traps.reception.v3None')}</span>
      {/each}
      {#if excludedUsers.length > 0}
        <span class="rx-excluded" title={excludedTitle(excludedUsers, $_)}>
          {$_('traps.reception.excluded', { values: { count: excludedUsers.length } })}
        </span>
      {/if}
    </span>
    <span class="rx-group" title={$_('traps.reception.anyCommunityTitle')}>
      <span class="rx-label">SNMPv1/v2c</span>
      <span class="rx-quiet">{$_('traps.reception.anyCommunity')}</span>
    </span>
    {#if listenerEngineId}
      <span class="rx-group" title={$_('traps.reception.engineIdTitle')}>
        <span class="rx-label">{$_('traps.reception.engineId')}</span>
        <code class="rx-engine">{$anonMode ? maskString(listenerEngineId) : listenerEngineId}</code>
      </span>
    {/if}
    <button class="rx-manage" on:click={() => requestSettings('snmp', 'credential-profiles')}>
      <Icon name="key-round" size={13} /> {$_('traps.reception.manage')}
    </button>
  </div>

  {#if showSendTrap}
    <div class="send-trap-section">
      <h4>{$_('traps.sendTrap')}</h4>
      <div class="send-form">
        <div class="send-row">
          <label for="send-target">{$_('traps.targetLabel')}</label>
          <input id="send-target" type="text" bind:value={sendTarget} placeholder="192.168.1.1" />
          <label for="send-port">{$_('traps.portLabel')}</label>
          <input id="send-port" type="number" bind:value={sendPort} style="width:80px" />
        </div>
        <div class="send-row">
          <label for="send-version">{$_('traps.versionLabel')}</label>
          <select id="send-version" bind:value={sendVersion}>
            <option value="v1" disabled={sendKind === 'inform'}>v1</option>
            <option value="v2c">v2c</option>
          </select>
          <label for="send-kind">{$_('traps.kindLabel')}</label>
          <select id="send-kind" bind:value={sendKind} on:change={() => { if (sendKind === 'inform' && sendVersion === 'v1') sendVersion = 'v2c'; }}>
            <option value="trap">{$_('traps.kindTrap')}</option>
            <option value="inform">{$_('traps.kindInform')}</option>
          </select>
          <label for="send-community">{$_('traps.communityLabel')}</label>
          <input id="send-community" type="text" bind:value={sendCommunity} placeholder={$anonMode ? maskString($settingsStore.community) : $settingsStore.community} />
        </div>
        <div class="send-row">
          <label for="send-oid">{$_('traps.trapOidLabel')}</label>
          <input id="send-oid" type="text" bind:value={sendTrapOid} placeholder="1.3.6.1.4.1.99999.1" />
        </div>

        <div class="varbinds-section">
          <div class="varbinds-header">
            <span>{$_('traps.varbinds', { values: { count: sendVarbinds.length } })}</span>
            <button class="btn btn-small" on:click={addVarbind}>{$_('traps.addVarbind')}</button>
          </div>
          {#each sendVarbinds as vb, i}
            <div class="varbind-row">
              <input type="text" bind:value={vb.oid} placeholder={$_('common.oid')} class="vb-oid" />
              <select bind:value={vb.type} class="vb-type">
                <option value="Integer">Integer</option>
                <option value="OctetString">OctetString</option>
                <option value="OID">OID</option>
                <option value="TimeTicks">TimeTicks</option>
              </select>
              <input type="text" bind:value={vb.value} placeholder={$_('common.value')} class="vb-value" />
              <button class="btn-remove" on:click={() => removeVarbind(i)}>X</button>
            </div>
          {/each}
        </div>

        <button class="btn btn-primary" on:click={handleSendTrap} disabled={isSending}>
          {isSending ? $_('traps.sending') : $_('traps.send')}
        </button>
      </div>
    </div>
  {/if}

  <div class="filter-bar">
    <input type="text" placeholder={$_('traps.filterPlaceholder')} bind:value={searchTerm} class="filter-search" />
    <div class="filter-selects">
      <select bind:value={filterVersion} title={$_('traps.allVersions')}>
        <option value="All">{$_('traps.allVersions')}</option>
        <option value="SNMPv1">v1</option>
        <option value="SNMPv2c">v2c</option>
        <option value="SNMPv3">v3</option>
      </select>
      <select bind:value={filterPduType} title={$_('traps.allTypes')}>
        <option value="All">{$_('traps.allTypes')}</option>
        <option value="Trap">Trap</option>
        <option value="Inform">Inform</option>
      </select>
      <select bind:value={filterTimeRange} title={$_('traps.allTime')}>
        <option value="All">{$_('traps.allTime')}</option>
        <option value="5">{$_('traps.last5min')}</option>
        <option value="15">{$_('traps.last15min')}</option>
        <option value="30">{$_('traps.last30min')}</option>
        <option value="60">{$_('traps.lastHour')}</option>
      </select>
    </div>
    <div class="filter-actions">
      <span class="trap-counter">{$_('traps.count', { values: { filtered: filteredTraps.length, total: $trapStore.traps.length } })}</span>
      {#if filteredTraps.length > 0}
        <button class="btn-export" on:click={exportCSV} title={$_('traps.exportCsv')}>{$_('traps.exportCsv')}</button>
        <button class="btn-export" on:click={exportJSON} title={$_('traps.exportJson')}>{$_('traps.exportJson')}</button>
      {/if}
    </div>
  </div>

  <div class="trap-container">
    {#if filteredTraps.length === 0}
      <p class="no-traps">
        {#if $trapStore.traps.length > 0}
          {$_('traps.noMatch')}
        {:else if $trapStore.isListening}
          {$_('traps.waitingForTraps')}
        {:else}
          {$_('traps.listenerStopped')}
        {/if}
      </p>
    {:else}
      <div class="trap-header">
        <span class="chevron"></span>
        <span class="timestamp">{$_('traps.tableTime')}</span>
        <span class="source">{$_('traps.tableSource')}</span>
        <span class="version">{$_('traps.tableVersion')}</span>
        <span class="pdu-type">{$_('traps.tableType')}</span>
        <span class="main-oid">{$_('traps.tableMessage')}</span>
      </div>
      {#each filteredTraps as trap (trap.id)}
        <Trap {trap} />
      {/each}
    {/if}
  </div>
</div>

<style>
  .send-trap-section {
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 15px;
    margin-bottom: 15px;
  }

  .send-trap-section h4 {
    margin: 0 0 10px 0;
    font-size: 0.95em;
  }

  .send-form {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .send-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .send-row label {
    font-size: 0.85em;
    font-weight: 500;
    min-width: 70px;
  }

  .send-row input, .send-row select {
    padding: 6px 8px;
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    flex: 1;
  }

  .varbinds-section {
    margin-top: 5px;
  }

  .varbinds-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 6px;
    font-size: 0.85em;
    font-weight: 500;
  }

  .varbind-row {
    display: flex;
    gap: 6px;
    align-items: center;
    margin-bottom: 4px;
  }

  .vb-oid { flex: 2; }
  .vb-type { flex: 0 0 110px; }
  .vb-value { flex: 2; }

  .varbind-row input, .varbind-row select {
    padding: 5px 8px;
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.85em;
  }

  .btn-remove {
    background: none;
    border: 1px solid var(--error-dark);
    color: var(--error-dark);
    border-radius: 3px;
    padding: 4px 8px;
    cursor: pointer;
    font-size: 0.8em;
  }

  .btn-remove:hover {
    background-color: var(--error-dark-subtle);
  }

  .btn-primary { flex-shrink: 0; margin-top: 5px; }

  .form-group {
    margin-bottom: 15px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .form-group p {
    margin: 0;
  }

  .header-buttons {
    display: flex;
    gap: 8px;
  }

  .reception {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 16px;
    margin-bottom: 12px;
    padding: 7px 10px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background-color: var(--bg-lighter-color);
    font-size: 0.82em;
  }

  .rx-title {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-weight: 600;
  }

  .rx-group {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 5px;
  }

  .rx-label {
    font-weight: 600;
    color: var(--text-muted);
  }

  .rx-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 1px 8px;
    border: 1px solid transparent;
    border-radius: 10px;
    background-color: var(--accent-subtle);
    color: var(--accent-color);
    font-weight: 600;
  }

  .rx-chip.refused {
    border-color: var(--warning-color);
    background-color: transparent;
    color: var(--warning-color);
  }

  .rx-engine {
    font-size: 0.92em;
    color: var(--text-muted);
    user-select: all;
  }

  .rx-user {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-weight: 400;
    opacity: 0.85;
  }

  .rx-quiet {
    color: var(--text-muted);
    font-style: italic;
  }

  .rx-excluded {
    color: var(--text-muted);
    text-decoration: underline dotted;
    cursor: help;
  }

  .rx-manage {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    margin-left: auto;
    padding: 3px 10px;
    background: transparent;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 1em;
    cursor: pointer;
  }

  .rx-manage:hover {
    border-color: var(--accent-color);
    color: var(--accent-color);
    background-color: var(--accent-subtle-medium);
  }

  .filter-bar {
    margin-bottom: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .filter-search {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    box-sizing: border-box;
  }

  .filter-selects {
    display: flex;
    gap: 8px;
  }

  .filter-selects select {
    flex: 1;
    padding: 6px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.85em;
  }

  .filter-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    justify-content: flex-end;
  }

  .trap-counter {
    font-size: 0.85em;
    color: var(--text-muted);
    background-color: var(--bg-lighter-color);
    padding: 4px 10px;
    border-radius: 12px;
  }

  .btn-export {
    padding: 4px 10px;
    font-size: 0.8em;
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-dimmed);
    border-radius: 3px;
    cursor: pointer;
    font-weight: 500;
    transition: all 0.2s;
  }

  .btn-export:hover {
    border-color: var(--accent-color);
    color: var(--accent-color);
    background-color: var(--accent-subtle-medium);
  }

  .trap-container {
    height: calc(100vh - 350px);
    overflow-y: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .trap-header {
    display: grid;
    grid-template-columns: 20px 80px 120px 80px 60px 1fr;
    align-items: center;
    gap: 10px;
    padding: 10px;
    font-weight: bold;
    color: var(--text-dimmed);
    font-size: 0.85em;
    border-bottom: 2px solid var(--border-color);
    background-color: var(--bg-lighter-color);
    position: sticky;
    top: 0;
    z-index: 1;
  }

  .trap-header span {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .no-traps {
    color: var(--text-muted);
    text-align: center;
    margin-top: 20px;
  }
</style>
