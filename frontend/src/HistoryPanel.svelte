<script>
  import { tick, createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';

  const dispatch = createEventDispatcher();
  import { get } from 'svelte/store';
  import { historyStore } from './stores/historyStore';
  import { notificationStore } from './stores/notifications';
  import { mibStore } from './stores/mibStore';
  import DiffModal from './DiffModal.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import HistoryExportModal from './history/HistoryExportModal.svelte';
  import HistoryEntry from './history/HistoryEntry.svelte';
  import Icon from './Icon.svelte';
  import { ResolveOids } from './bridge/App';
  import { findNodeByOid, findMibNameByOid, formatValueWithEnum } from './utils/mibTree';
  import { anonMode, anonymizeIp } from './utils/anonymize';
  import { escapeCSV } from './utils/csv';

  // Entry to reveal (expand + scroll to) when opened from Recent history.
  export let highlightId = null;

  const PAGE_SIZE = 20;
  let currentPage = 1;
  let flashId = null; // entry currently flashing (local, so it doesn't persist across remounts)

  let searchTerm = '';
  let operationFilter = 'all'; // 'all', 'GET', 'SET', 'WALK'
  let statusFilter = 'all'; // 'all', 'success', 'error'
  let filteredHistory = [];
  let expandedIds = new Set();
  let showExportModal = false;
  let exportData = '';
  let diffMode = false;
  let diffSelectionA = null;
  let diffSelectionB = null;
  let showDiffModal = false;

  // Sorting (clickable sort bar). Default: newest first.
  let sortKey = 'timestamp'; // 'timestamp' | 'operation' | 'target' | 'duration' | 'status'
  let sortAsc = false;       // false = descending (newest / largest first)

  const SORT_FIELDS = [
    { key: 'timestamp', labelKey: 'history.sortDate' },
    { key: 'operation', labelKey: 'history.sortType' },
    { key: 'target', labelKey: 'history.sortTarget' },
    { key: 'duration', labelKey: 'history.sortDuration' },
    { key: 'status', labelKey: 'history.sortStatus' },
  ];

  // Multi-selection (bulk delete / export).
  let selectMode = false;
  let selectedIds = new Set();

  // Export options (scope + format), used by the export modal.
  let exportScope = 'all';   // 'all' | 'filtered' | 'selected'
  let exportFormat = 'json'; // 'json' | 'csv'

  // Pagination over the filtered history.
  $: totalPages = Math.max(1, Math.ceil(filteredHistory.length / PAGE_SIZE));
  $: pagedHistory = filteredHistory.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);
  $: if (currentPage > totalPages) currentPage = totalPages;

  // Reveal the highlighted entry: jump to its page, expand it and scroll to it.
  // Depends on filteredHistory too, so if the request arrives before history has
  // finished loading, it retries once the list populates.
  $: if (highlightId != null && filteredHistory.length > 0) revealEntry(highlightId);

  async function revealEntry(id) {
    const idx = filteredHistory.findIndex(e => e.id === id);
    // Entry not present yet (still loading, or filtered out): bail WITHOUT
    // clearing the highlight, so the reactive statement can retry.
    if (idx < 0) return;
    currentPage = Math.floor(idx / PAGE_SIZE) + 1;
    expandedIds = new Set(expandedIds).add(id);
    resolveEntryOidsById(id);
    flashId = id;
    setTimeout(() => { if (flashId === id) flashId = null; }, 2400);
    await tick();
    const esc = (window.CSS && CSS.escape) ? CSS.escape(String(id)) : String(id);
    const el = document.querySelector(`[data-entry-id="${esc}"]`);
    if (el && el.scrollIntoView) el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    // One-shot: tell the parent to clear the highlight so it isn't re-applied
    // (and re-expanded) every time this panel remounts on tab switch.
    dispatch('highlightApplied');
  }

  // Get display name (MIB name or OID)
  function getDisplayName(oid) {
    const mibName = findMibNameByOid(oid, $mibStore.tree);
    return mibName || oid;
  }

  function countEntryResults(entry) {
    let n = 0;
    for (const res of entry.results || []) {
      if (res.error) continue;
      if (Array.isArray(res.result?.value)) n += res.result.value.length;
      else if (res.result) n += 1;
    }
    return n;
  }


  // Reactive filtering
  $: {
    let history = $historyStore;
    
    // Filter by operation
    if (operationFilter !== 'all') {
      history = history.filter(entry => entry.operation === operationFilter);
    }
    
    // Filter by status
    if (statusFilter !== 'all') {
      if (statusFilter === 'success') {
        history = history.filter(entry => entry.success);
      } else if (statusFilter === 'error') {
        history = history.filter(entry => !entry.success);
      }
    }
    
    // Filter by search term
    if (searchTerm) {
      const lowerSearch = searchTerm.toLowerCase();
      history = history.filter(entry => {
        return (
          entry.oid?.toLowerCase().includes(lowerSearch) ||
          entry.targets?.some(t => t.toLowerCase().includes(lowerSearch)) ||
          entry.operation?.toLowerCase().includes(lowerSearch) ||
          entry.error?.toLowerCase().includes(lowerSearch)
        );
      });
    }
    
    // Sort (sortKey/sortAsc referenced here so Svelte re-runs on change).
    const dir = sortAsc ? 1 : -1;
    filteredHistory = [...history].sort((a, b) => dir * compareEntries(a, b, sortKey));
  }

  // Compare two entries by the given key (used for sorting).
  function compareEntries(a, b, key) {
    switch (key) {
      case 'operation':
        return (a.operation || '').localeCompare(b.operation || '');
      case 'target':
        return (a.targets?.[0] || '').localeCompare(b.targets?.[0] || '');
      case 'duration':
        return (a.duration || 0) - (b.duration || 0);
      case 'status':
        return (a.success === b.success) ? 0 : (a.success ? 1 : -1);
      case 'timestamp':
      default:
        return String(a.timestamp).localeCompare(String(b.timestamp));
    }
  }

  function toggleSort(key) {
    if (sortKey === key) {
      sortAsc = !sortAsc;
    } else {
      sortKey = key;
      // Date defaults to newest-first; the rest to ascending.
      sortAsc = key !== 'timestamp';
    }
  }

  function toggleExpand(id) {
    if (expandedIds.has(id)) {
      expandedIds.delete(id);
    } else {
      expandedIds.add(id);
      resolveEntryOidsById(id);
    }
    expandedIds = expandedIds; // Trigger reactivity
  }

  // OID name/syntax/enum cache for the ResultsDisplay shown in expanded entries,
  // resolved lazily (the cache from when the request ran isn't persisted).
  let historyOidCache = {};

  async function resolveEntryOidsById(id) {
    const entry = $historyStore.find(e => e.id === id);
    if (!entry) return;
    const oids = new Set();
    if (entry.oid) oids.add(entry.oid);
    (entry.results || []).forEach(res => {
      if (Array.isArray(res.result?.value)) {
        res.result.value.forEach(item => item?.oid && oids.add(item.oid));
      } else if (res.result?.oid) {
        oids.add(res.result.oid);
      }
    });
    const toResolve = [...oids].filter(o => !(o in historyOidCache));
    if (!toResolve.length) return;
    try {
      const resolved = await ResolveOids(toResolve);
      historyOidCache = { ...historyOidCache, ...resolved };
    } catch (e) {
      /* names just won't resolve */
    }
  }

  let showClearConfirm = false;

  function confirmClearHistory() {
    historyStore.clear();
    notificationStore.add(get(_)('history.cleared'), 'success');
    showClearConfirm = false;
  }

  function handleDeleteEntry(id) {
    const t = get(_);
    historyStore.remove(id);
    notificationStore.add(t('history.entryRemoved'), 'success');
  }

  function handleCopyExport() {
    const t = get(_);
    navigator.clipboard.writeText(exportData).then(() => {
      notificationStore.add(t('history.exportedClipboard'), 'success');
      showExportModal = false;
    }).catch(() => {
      notificationStore.add(t('clipboard.copyError'), 'error');
    });
  }

  function handleDownloadExport() {
    const t = get(_);
    const ext = exportFormat === 'csv' ? 'csv' : 'json';
    const mime = exportFormat === 'csv' ? 'text/csv;charset=utf-8' : 'application/json';
    const blob = new Blob([exportData], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `snmp-history-${new Date().toISOString().split('T')[0]}.${ext}`;
    a.click();
    URL.revokeObjectURL(url);
    notificationStore.add(t('history.downloaded'), 'success');
    showExportModal = false;
  }

  // Diff mode functions
  function isDiffEligible(entry) {
    return entry.operation === 'WALK' || entry.operation === 'GETBULK';
  }

  function toggleDiffSelection(entry) {
    if (!isDiffEligible(entry)) return;
    if (diffSelectionA?.id === entry.id) {
      diffSelectionA = null;
    } else if (diffSelectionB?.id === entry.id) {
      diffSelectionB = null;
    } else if (!diffSelectionA) {
      diffSelectionA = entry;
    } else if (!diffSelectionB) {
      diffSelectionB = entry;
    }
  }

  function isDiffSelected(entry) {
    return diffSelectionA?.id === entry.id || diffSelectionB?.id === entry.id;
  }

  function openDiff() {
    if (diffSelectionA && diffSelectionB) {
      showDiffModal = true;
    }
  }

  function enterDiffMode() {
    diffMode = true;
    // Diff and multi-select are mutually exclusive.
    selectMode = false;
    selectedIds = new Set();
  }

  function exitDiffMode() {
    diffMode = false;
    diffSelectionA = null;
    diffSelectionB = null;
  }

  // --- Multi-selection ---
  function enterSelectMode() {
    selectMode = true;
    exitDiffMode();
  }

  function exitSelectMode() {
    selectMode = false;
    selectedIds = new Set();
  }

  function toggleSelect(id) {
    if (selectedIds.has(id)) selectedIds.delete(id);
    else selectedIds.add(id);
    selectedIds = selectedIds; // trigger reactivity
  }

  function selectAllFiltered() {
    selectedIds = new Set(filteredHistory.map((e) => e.id));
  }

  function clearSelection() {
    selectedIds = new Set();
  }

  function deleteSelected() {
    const ids = [...selectedIds];
    if (ids.length === 0) return;
    historyStore.removeMany(ids);
    notificationStore.add(get(_)('history.deletedCount', { values: { count: ids.length } }), 'success');
    selectedIds = new Set();
  }

  // --- Export (scope + format) ---
  // Pure builder so the reactive statement below re-runs on any dep change.
  function entriesForScope(scope, all, filtered, selIds) {
    if (scope === 'selected') return all.filter((e) => selIds.has(e.id));
    if (scope === 'filtered') return filtered;
    return all;
  }

  function exportValueCell(entry) {
    if (entry.error) return '';
    if (entry.operation === 'GET' || entry.operation === 'SET' || entry.operation === 'GETNEXT') {
      const val = entry.results?.[0]?.result?.value;
      if (val !== undefined && !Array.isArray(val)) {
        const node = findNodeByOid(entry.oid, $mibStore.tree);
        return formatValueWithEnum(val, node);
      }
    }
    const count = entry.totalResults ?? countEntryResults(entry);
    return count > 0 ? String(count) : '';
  }

  // The formula neutralisation this function used to carry privately now lives
  // in utils/csv.js, where every other export reaches it. It was correct here
  // and absent everywhere else, including in the shared helper — which is how
  // the events export, the one carrying raw trap text, ended up without it.

  function toCsv(entries) {
    const header = ['timestamp', 'operation', 'targets', 'oid', 'name', 'value', 'version', 'durationMs', 'success', 'error'];
    const lines = entries.map((e) => [
      e.timestamp,
      e.operation,
      (e.targets || []).join('; '),
      e.oid || '',
      getDisplayName(e.oid),
      exportValueCell(e),
      e.version || '',
      e.duration ?? '',
      e.success ? 'true' : 'false',
      e.error || '',
    ].map(escapeCSV).join(','));
    return [header.join(','), ...lines].join('\r\n');
  }

  // Apply Anonymous Mode masking to an entry's targets (top-level and inside
  // per-result rows) so exports never leak real IPs the UI is hiding.
  function anonymizeEntry(e) {
    const out = { ...e, targets: (e.targets || []).map(anonymizeIp) };
    if (Array.isArray(e.results)) {
      out.results = e.results.map((r) => (r && r.target ? { ...r, target: anonymizeIp(r.target) } : r));
    }
    return out;
  }

  function buildExportData(scope, format, all, filtered, selIds, anon) {
    let entries = entriesForScope(scope, all, filtered, selIds);
    if (anon) entries = entries.map(anonymizeEntry);
    return format === 'csv' ? toCsv(entries) : historyStore.export(entries);
  }

  // Recompute the export payload whenever the modal is open and its inputs change.
  $: if (showExportModal) {
    exportData = buildExportData(exportScope, exportFormat, $historyStore, filteredHistory, selectedIds, $anonMode);
  }

  function openExport(scope) {
    exportScope = scope;
    showExportModal = true;
  }
</script>

<div class="panel">
  <div class="header">
    <h3>{$_('history.title', { values: { count: $historyStore.length } })}</h3>
    <div class="header-actions">
      <button class="btn tertiary" class:active-diff={selectMode} on:click={() => selectMode ? exitSelectMode() : enterSelectMode()}>
        <Icon name="check-square" size={14} /> {selectMode ? $_('history.exitSelect') : $_('history.selectMode')}
      </button>
      <button class="btn tertiary" class:active-diff={diffMode} on:click={() => diffMode ? exitDiffMode() : enterDiffMode()}>
        {diffMode ? $_('history.exitDiff') : $_('history.diffMode')}
      </button>
      {#if diffMode && diffSelectionA && diffSelectionB}
        <button class="btn" on:click={openDiff}>{$_('history.compare')}</button>
      {/if}
      <button class="btn tertiary" on:click={() => openExport('all')}>
        {$_('common.export')}
      </button>
      <button class="btn danger" on:click={() => showClearConfirm = true}>
        {$_('history.clearAll')}
      </button>
    </div>
  </div>

  {#if diffMode}
    <p class="diff-hint"><Icon name="square" size={13} /> {$_('history.diffHint')}</p>
  {/if}

  {#if selectMode}
    <div class="bulk-bar">
      <span class="bulk-count">{$_('history.selectedCount', { values: { count: selectedIds.size } })}</span>
      <button class="btn-link" on:click={selectAllFiltered}>{$_('history.selectAll')}</button>
      <button class="btn-link" on:click={clearSelection} disabled={selectedIds.size === 0}>{$_('history.clearSelection')}</button>
      <div class="bulk-spacer"></div>
      <button class="btn tertiary" on:click={() => openExport('selected')} disabled={selectedIds.size === 0}>
        <Icon name="download" size={14} /> {$_('history.exportSelected')}
      </button>
      <button class="btn danger" on:click={deleteSelected} disabled={selectedIds.size === 0}>
        <Icon name="trash-2" size={14} /> {$_('history.deleteSelected')}
      </button>
    </div>
  {/if}

  <div class="filters">
    <div class="filter-group">
      <input
        type="text"
        placeholder={$_('history.searchPlaceholder')}
        bind:value={searchTerm}
      />
    </div>
    <div class="filter-row">
      <div class="filter-item">
        <label for="hist-op-filter">{$_('history.operationFilter')}</label>
        <select id="hist-op-filter" bind:value={operationFilter}>
          <option value="all">{$_('history.all')}</option>
          <option value="GET">GET</option>
          <option value="SET">SET</option>
          <option value="GETNEXT">GETNEXT</option>
          <option value="GETBULK">GETBULK</option>
          <option value="WALK">WALK</option>
        </select>
      </div>
      <div class="filter-item">
        <label for="hist-status-filter">{$_('history.statusFilter')}</label>
        <select id="hist-status-filter" bind:value={statusFilter}>
          <option value="all">{$_('history.all')}</option>
          <option value="success">{$_('common.success')}</option>
          <option value="error">{$_('common.error')}</option>
        </select>
      </div>
      <div class="filter-item">
        <span class="result-count">{$_('history.nResults', { values: { count: filteredHistory.length } })}</span>
      </div>
    </div>
  </div>

  <div class="sort-bar">
    <span class="sort-label">{$_('history.sortBy')}</span>
    {#each SORT_FIELDS as field}
      <button
        class="sort-btn"
        class:active={sortKey === field.key}
        on:click={() => toggleSort(field.key)}
      >
        {$_(field.labelKey)}
        {#if sortKey === field.key}
          <Icon name={sortAsc ? 'chevron-up' : 'chevron-down'} size={13} />
        {/if}
      </button>
    {/each}
  </div>

  <div class="history-container">
    {#if filteredHistory.length === 0}
      <p class="empty-state">
        {#if searchTerm || operationFilter !== 'all' || statusFilter !== 'all'}
          {$_('history.noMatchFilter')}
        {:else}
          {$_('history.empty')}
        {/if}
      </p>
    {:else}
      {#each pagedHistory as entry (entry.id)}
        <HistoryEntry
          {entry}
          expanded={expandedIds.has(entry.id)}
          {selectMode}
          {diffMode}
          selected={selectedIds.has(entry.id)}
          diffLabel={diffSelectionA?.id === entry.id ? 'A' : (diffSelectionB?.id === entry.id ? 'B' : null)}
          diffEligible={isDiffEligible(entry)}
          flash={entry.id === flashId}
          oidInfoCache={historyOidCache}
          mibTree={$mibStore.tree}
          on:toggle={() => toggleExpand(entry.id)}
          on:select={() => toggleSelect(entry.id)}
          on:diff={() => toggleDiffSelection(entry)}
          on:delete={() => handleDeleteEntry(entry.id)}
        />
      {/each}
    {/if}
  </div>

  {#if totalPages > 1}
    <div class="history-pagination">
      <button class="btn-page" on:click={() => currentPage = Math.max(1, currentPage - 1)} disabled={currentPage <= 1} title={$_('common.previous')} aria-label={$_('common.previous')}>
        <Icon name="chevron-left" size={15} />
      </button>
      <span class="page-indicator">{currentPage} / {totalPages}</span>
      <button class="btn-page" on:click={() => currentPage = Math.min(totalPages, currentPage + 1)} disabled={currentPage >= totalPages} title={$_('common.next')} aria-label={$_('common.next')}>
        <Icon name="chevron-right" size={15} />
      </button>
    </div>
  {/if}
</div>

{#if showDiffModal && diffSelectionA && diffSelectionB}
  <DiffModal entryA={diffSelectionA} entryB={diffSelectionB} on:close={() => showDiffModal = false} />
{/if}

{#if showClearConfirm}
  <ConfirmDialog
    title={$_('history.clearAll')}
    text={$_('history.clearConfirm')}
    confirmLabel={$_('history.clearAll')}
    cancelLabel={$_('common.cancel')}
    confirmIcon="trash-2"
    danger
    on:confirm={confirmClearHistory}
    on:cancel={() => showClearConfirm = false}
  />
{/if}

{#if showExportModal}
  <HistoryExportModal
    {exportData}
    bind:scope={exportScope}
    bind:format={exportFormat}
    selectedCount={selectedIds.size}
    on:copy={handleCopyExport}
    on:download={handleDownloadExport}
    on:close={() => showExportModal = false}
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
    margin-bottom: 15px;
  }

  .header h3 {
    margin: 0;
    font-size: 1.2em;
  }

  .header-actions {
    display: flex;
    gap: 10px;
  }

  .filters {
    margin-bottom: 15px;
  }

  .filter-group {
    margin-bottom: 10px;
  }

  .filter-group input {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    box-sizing: border-box;
  }

  .filter-row {
    display: flex;
    gap: 15px;
    align-items: center;
  }

  .filter-item {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .filter-item label {
    font-size: 0.9em;
    font-weight: 500;
  }

  .filter-item select {
    padding: 6px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .result-count {
    margin-left: auto;
    color: var(--text-dimmed);
    font-size: 0.9em;
  }

  .history-container {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .history-pagination {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 12px;
    padding: 10px;
    flex-shrink: 0;
  }

  .btn-page {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    padding: 5px 9px;
    cursor: pointer;
  }

  .btn-page:hover:not(:disabled) {
    background-color: var(--hover-overlay);
    border-color: var(--border-hover);
  }

  .btn-page:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .page-indicator {
    font-size: 0.9em;
    color: var(--text-light);
    font-variant-numeric: tabular-nums;
    min-width: 52px;
    text-align: center;
  }

  /* .empty-state is defined globally in shared.css */


  /* Diff mode styles */
  .active-diff {
    border-color: var(--accent-color) !important;
    color: var(--accent-color) !important;
    background-color: var(--accent-subtle-medium) !important;
  }

  .diff-hint {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 10px;
    padding: 8px 12px;
    font-size: 0.85em;
    color: var(--accent-color);
    background-color: var(--accent-subtle);
    border: 1px solid var(--accent-border);
    border-radius: 4px;
  }

  /* --- Sort bar --- */
  .sort-bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 10px;
  }

  .sort-label {
    font-size: 0.82em;
    color: var(--text-muted);
    margin-right: 2px;
  }

  .sort-btn {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    padding: 4px 9px;
    font-size: 0.82em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-light);
    cursor: pointer;
    transition: all 0.15s;
  }

  .sort-btn:hover {
    background-color: var(--hover-overlay);
    border-color: var(--border-hover);
  }

  .sort-btn.active {
    color: var(--accent-color);
    border-color: var(--accent-border);
    background-color: var(--accent-subtle);
    font-weight: 600;
  }

  /* --- Bulk action bar (select mode) --- */
  .bulk-bar {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 0 0 12px;
    padding: 8px 12px;
    background-color: var(--accent-subtle);
    border: 1px solid var(--accent-border);
    border-radius: 4px;
  }

  .bulk-count {
    font-size: 0.88em;
    font-weight: 600;
    color: var(--accent-color);
  }

  .bulk-spacer {
    flex: 1;
  }

  .btn-link {
    background: none;
    border: none;
    color: var(--accent-color);
    cursor: pointer;
    font-size: 0.85em;
    padding: 2px 4px;
    text-decoration: underline;
  }

  .btn-link:disabled {
    color: var(--text-muted);
    cursor: not-allowed;
    text-decoration: none;
  }

  .bulk-bar .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .header-actions .btn.tertiary {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

</style>
