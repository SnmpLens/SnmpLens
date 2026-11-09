<script>
  import { _ } from 'svelte-i18n';
  import { onDestroy } from 'svelte';
  import { SnmpSetDebug, SnmpGetDebugLog, SnmpClearDebugLog } from './bridge/App';
  import { anonMode, anonymizeText } from './utils/anonymize';

  let entries = [];
  let debugEnabled = false;
  let logContainer;
  let pollTimer = null;

  async function toggleDebug() {
    debugEnabled = !debugEnabled;
    await SnmpSetDebug(debugEnabled);
    if (debugEnabled) {
      startPolling();
    } else {
      stopPolling();
    }
  }

  function startPolling() {
    stopPolling();
    refreshLog();
    pollTimer = setInterval(refreshLog, 1000);
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  async function refreshLog() {
    try {
      const newEntries = await SnmpGetDebugLog() || [];
      if (newEntries.length !== entries.length) {
        const wasAtBottom = logContainer && (logContainer.scrollHeight - logContainer.scrollTop - logContainer.clientHeight < 30);
        entries = newEntries;
        if (wasAtBottom) {
          setTimeout(() => {
            if (logContainer) logContainer.scrollTop = logContainer.scrollHeight;
          }, 20);
        }
      }
    } catch (e) {
      console.warn('Failed to get debug log:', e);
    }
  }

  async function clearLog() {
    await SnmpClearDebugLog();
    entries = [];
  }

  onDestroy(() => {
    stopPolling();
  });
</script>

<div class="debug-panel">
  <div class="debug-header">
    <span class="debug-title">{$_('debug.title')}</span>
    <div class="debug-actions">
      <button class="btn btn-small" class:active={debugEnabled} on:click={toggleDebug}>
        {debugEnabled ? $_('debug.disable') : $_('debug.enable')}
      </button>
      <button class="btn btn-small" on:click={refreshLog} disabled={!debugEnabled}>
        {$_('debug.refresh')}
      </button>
      <button class="btn btn-small" on:click={clearLog}>
        {$_('debug.clear')}
      </button>
      <span class="debug-count">{entries.length} {$_('debug.entries')}</span>
    </div>
  </div>
  <div class="debug-log" bind:this={logContainer}>
    {#if entries.length === 0}
      <div class="debug-empty">{$_('debug.empty')}</div>
    {:else}
      {#each entries as entry}
        <div class="debug-entry">
          <span class="debug-ts">{entry.timestamp}</span>
          <span class="debug-msg">{$anonMode ? anonymizeText(entry.message) : entry.message}</span>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .debug-panel {
    border-top: 2px solid var(--accent-color);
    background-color: var(--bg-color);
    display: flex;
    flex-direction: column;
    height: 250px;
    min-height: 100px;
  }

  .debug-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 12px;
    background-color: var(--bg-lighter-color);
    border-bottom: 1px solid var(--border-color);
    flex-shrink: 0;
  }

  .debug-title {
    font-weight: 600;
    font-size: 0.85em;
    color: var(--text-color);
  }

  .debug-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .debug-actions .btn-small {
    padding: 3px 8px;
    font-size: 0.8em;
  }

  .debug-actions .btn-small.active {
    background-color: var(--success-color);
  }

  .debug-count {
    font-size: 0.8em;
    color: var(--text-muted);
    margin-left: 8px;
  }

  .debug-log {
    flex: 1;
    overflow-y: auto;
    padding: 8px 12px;
    font-family: 'Courier New', monospace;
    font-size: 0.8em;
    line-height: 1.5;
  }

  .debug-empty {
    color: var(--text-muted);
    text-align: center;
    padding: 20px;
    font-style: italic;
    font-family: inherit;
  }

  .debug-entry {
    display: flex;
    gap: 10px;
    padding: 1px 0;
    border-bottom: 1px solid var(--hover-overlay);
  }

  .debug-entry:hover {
    background-color: var(--hover-overlay);
  }

  .debug-ts {
    color: var(--text-muted);
    flex-shrink: 0;
    min-width: 85px;
  }

  .debug-msg {
    color: var(--text-color);
    word-break: break-all;
  }
</style>
