<script>
  import { _ } from 'svelte-i18n';
  import { GetOidDetails } from './bridge/App';
  import { anonMode, anonymizeIp } from './utils/anonymize';
  import { copyToClipboard } from './utils/clipboard';
  import { formatBySnmpType } from './utils/formatting';
  import Icon from './Icon.svelte';
  import { targetLabels } from './stores/targetLabels';
  import { displayTarget, targetTitle } from './utils/targets';

  export let trap;

  let isOpen = false;
  let mainTrapMessage = '';
  let translatedVars = [];

  // Reactive: re-resolve whenever the trap prop changes
  $: resolveTrap(trap);

  async function resolveTrap(t) {
    mainTrapMessage = '';
    translatedVars = [];

    // 1. Find the main trap OID to get its details
    const trapOidVar = t.variables?.find(v =>
      v.oid === 'snmpTrapOID.0' ||
      v.oid === '.1.3.6.1.6.3.1.1.4.1.0' ||
      v.oid === '1.3.6.1.6.3.1.1.4.1.0' ||
      v.oid.endsWith('.1.6.3.1.1.4.1.0')
    );
    let trapDescription = '';
    if (trapOidVar && trapOidVar.value) {
      const oidValue = String(trapOidVar.value).replace(/^\./, '');
      try {
        const details = await GetOidDetails(oidValue);
        trapDescription = details.name || oidValue;
      } catch {
        trapDescription = oidValue;
      }
    } else if (t.version === 'SNMPv1') {
      trapDescription = $_('traps.snmpv1Trap');
    } else {
      trapDescription = $_('traps.snmpv2v3Inform');
    }

    // 2. Find message string variable, if any
    const messageStringVar = t.variables?.find(v => v.oid?.endsWith('.1.4.1.8955.1.8.1.6'));
    if (messageStringVar && messageStringVar.value) {
      mainTrapMessage = `${trapDescription}: ${messageStringVar.value}`;
    } else {
      mainTrapMessage = trapDescription;
    }

    // 3. Translate all variable OIDs for the details table
    translatedVars = await Promise.all(
      (t.variables || []).map(async (variable) => {
        const cleanOid = (variable.oid || '').replace(/^\./, '');
        try {
          const details = await GetOidDetails(cleanOid);
          return { ...variable, oid: cleanOid, name: details.name || cleanOid };
        } catch {
          return { ...variable, oid: cleanOid, name: cleanOid };
        }
      })
    );
  }

  function toggle() {
    isOpen = !isOpen;
  }

  function formatTrapValue(v) {
    if (v.value == null) return '-';
    // Smart formatting by SNMP type (e.g. TimeTicks → human-readable)
    const smart = formatBySnmpType(v.value, v.type);
    if (smart !== null) return smart;
    // For strings and everything else, just display as-is (no JSON.stringify quotes)
    return typeof v.value === 'object' ? JSON.stringify(v.value) : String(v.value);
  }
</script>

<div class="trap-item" class:open={isOpen}>
  <div class="trap-summary" on:click={toggle} on:keydown={(e) => e.key === 'Enter' && toggle()} role="button" tabindex="0">
    <span class="chevron">{isOpen ? '▼' : '►'}</span>
    <span class="timestamp">{trap.timestamp ? new Date(trap.timestamp).toLocaleTimeString() : new Date().toLocaleTimeString()}</span>
    <span class="source" title={targetTitle(trap.source, $anonMode)}>{displayTarget(trap.source, $targetLabels, $anonMode)}</span>
    <button class="btn-copy-small" on:click|stopPropagation={() => copyToClipboard(trap.source, 'Source')} title="Copy source"><Icon name="copy" size={13} /></button>
    <span class="version">{trap.version}</span>
    <span class="pdu-type-badge" class:inform={trap.pduType === 'Inform'}>{trap.pduType || 'Trap'}</span>
    <span class="main-oid" title={mainTrapMessage}>{mainTrapMessage}</span>
  </div>

  {#if isOpen}
    <div class="trap-details">
      <table>
        <thead>
          <tr>
            <th>{$_('common.name')}</th>
            <th>{$_('common.oid')}</th>
            <th>{$_('common.type')}</th>
            <th>{$_('common.value')}</th>
          </tr>
        </thead>
        <tbody>
          {#each translatedVars as v}
            <tr>
              <td class="name" title={v.name}>{v.name}</td>
              <td class="oid" title={v.oid}>
                {v.oid}
                <button class="btn-copy-small" on:click={() => copyToClipboard(v.oid, $_('common.oid'))} title={$_('common.oid')}><Icon name="copy" size={13} /></button>
              </td>
              <td class="type">{v.type}</td>
              <td class="value" title={String(v.value)}>
                {formatTrapValue(v)}
                <button class="btn-copy-small" on:click={() => copyToClipboard(String(v.value), $_('common.value'))} title={$_('common.value')}><Icon name="copy" size={13} /></button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .trap-item {
    border-bottom: 1px solid var(--border-color);
  }
  .trap-summary {
    display: grid;
    grid-template-columns: 20px 80px 120px auto 80px 60px 1fr;
    align-items: center;
    gap: 10px;
    padding: 10px;
    cursor: pointer;
    transition: background-color 0.2s;
  }
  .trap-summary:hover {
    background-color: var(--bg-lighter-color);
  }
  .trap-item.open .trap-summary {
    background-color: var(--accent-color);
    color: white;
  }
  .chevron {
    font-size: 0.8em;
  }
  .pdu-type-badge {
    font-size: 0.75em;
    font-weight: 600;
    padding: 2px 6px;
    border-radius: 3px;
    text-align: center;
    background-color: var(--accent-color);
    color: white;
    text-transform: uppercase;
  }
  .pdu-type-badge.inform {
    background-color: var(--warning-color);
    color: var(--bg-color);
  }
  .timestamp, .source, .version, .main-oid {
    font-family: monospace;
    font-size: 0.9em;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .trap-details {
    padding: 15px;
    background-color: var(--bg-lighter-color);
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    text-align: left;
    padding: 8px;
    border-bottom: 1px solid var(--border-color);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 200px; /* Adjust as needed */
  }
  th {
    font-size: 0.85em;
    color: var(--text-dimmed);
  }
  td {
    font-size: 0.9em;
  }
  .name {
    font-weight: 500;
    max-width: 150px;
  }
  .oid {
    font-family: monospace;
    color: var(--text-light);
    max-width: 200px;
  }
  .type {
    max-width: 100px;
  }
  .value {
    font-family: monospace;
    color: var(--text-light);
    max-width: 250px;
  }
</style>
