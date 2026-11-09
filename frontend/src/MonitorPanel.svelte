<script>
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { MonitorGetStats, MonitorLoadHistoricalData, MonitorLoadBuckets } from './bridge/App';
  import { pollingStore } from './stores/pollingStore';
  import { settingsStore } from './stores/settingsStore';
  import { notificationStore } from './stores/notifications';
  import { getTargetsAsArray, usesOwnIdentifiers } from './utils/targets';
  import { formatTimeShort, formatCadence } from './utils/formatting';
  import { anonMode, anonymizeIp } from './utils/anonymize';
  import { targetLabels } from './stores/targetLabels';
  import { mibStore } from './stores/mibStore';
  import { oidName, oidTooltip } from './utils/oidDisplay';
  import { copyToClipboard } from './utils/clipboard';
  import Icon from './Icon.svelte';
  import MonitorChart from './monitor/MonitorChart.svelte';
  import MetricTiles from './monitor/MetricTiles.svelte';
  import AlertTimeline from './monitor/AlertTimeline.svelte';
  import ChannelsModal from './monitor/ChannelsModal.svelte';
  import { monitorViewStore } from './stores/monitorViewStore';
  import { DEFAULT_STATS } from './utils/seriesStats';

  // Read once. This panel lives behind {#if activeTab === 'monitor'}, so it is
  // destroyed whenever the user leaves the tab — every preference below has to
  // come back from the store rather than from a fresh default.
  // NOTE: never read $monitorViewStore reactively here; this component writes
  // to it, and subscribing would loop.
  const savedMonitorView = monitorViewStore.snapshot();
  const savedForm = savedMonitorView.form || {};
  const savedView = savedMonitorView.view || {};

  // Resolved theme ('dark' | 'light') handed to the chart so it re-themes when
  // the setting changes — including 'system', which follows the OS.
  $: resolvedTheme =
    $settingsStore.theme === 'light' || $settingsStore.theme === 'dark'
      ? $settingsStore.theme
      : (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');

  // One entry per OID to watch. Capped at the validated palette size so no two
  // curves ever end up sharing a colour.
  const MAX_OIDS = 8;
  let pollName = savedForm.pollName ?? '';
  let pollOids = savedForm.pollOids && savedForm.pollOids.length ? [...savedForm.pollOids] : [''];
  // Alert band per OID row, aligned with pollOids by index. Bounds belong to a
  // metric, not to a session: a link rate and an uptime share no scale.
  let pollThresholds = savedForm.pollThresholds && savedForm.pollThresholds.length
    ? [...savedForm.pollThresholds]
    : [];
  let openThreshold = null; // index of the OID row whose band editor is open

  function thresholdAt(list, i) {
    return list[i] || { min: '', max: '', forSeconds: 0, enabled: true };
  }

  function patchThreshold(i, changes) {
    const next = [...pollThresholds];
    next[i] = { ...thresholdAt(next, i), ...changes };
    pollThresholds = next;
  }

  function hasThreshold(list, i) {
    const t = list[i];
    return !!(t && t.enabled !== false && ((t.min !== '' && t.min != null) || (t.max !== '' && t.max != null)));
  }

  // Map the row-indexed bands onto their OIDs for the backend.
  function buildThresholdMap(oidValues, list) {
    const map = {};
    oidValues.forEach((raw, i) => {
      const oid = String(raw || '').trim();
      if (!oid || !hasThreshold(list, i)) return;
      const t = thresholdAt(list, i);
      map[oid] = {
        min: t.min === '' || t.min == null ? null : Number(t.min),
        max: t.max === '' || t.max == null ? null : Number(t.max),
        forSeconds: Number(t.forSeconds) || 0,
        alertEnabled: true,
      };
    });
    return map;
  }
  let layoutModes = savedView.layoutModes || {};   // sessionId -> 'separate' | 'stacked'
  let hiddenSeries = savedView.hiddenSeries || {};  // sessionId -> ["target|oid", ...] hidden from the plot
  let channelsFor = null; // sessionId whose channel picker is open (transient)
  let chartOpts = savedView.chartOpts || {}; // sessionId -> {windowMs, yScaleType, zeroBased, height}
  let statsSel = savedView.statsSel || {};   // sessionId -> ['last','avg',...]
  let statsScope = savedView.statsScope || {}; // sessionId -> 'window' | 'all'
  let collapsed = savedView.collapsed || {}; // sessionId -> boolean (card folded)

  // Folding a card unmounts its charts, which is also what keeps a page full of
  // sessions cheap. Their options are persisted, so they come back untouched.
  function toggleCollapsed(id) {
    collapsed[id] = !collapsed[id];
    collapsed = collapsed;
  }
  let visibleRanges = {}; // sessionId -> {min,max} reported by its chart (transient)

  const statsFor = (sel, id) => sel[id] || DEFAULT_STATS;
  const scopeFor = (sc, id) => sc[id] || 'window';

  function setStats(id, list) {
    statsSel[id] = list;
    statsSel = statsSel;
  }

  function setScope(id, value) {
    statsScope[id] = value;
    statsScope = statsScope;
  }

  function noteRange(id, range) {
    const prev = visibleRanges[id];
    if (prev && prev.min === range.min && prev.max === range.max) return;
    visibleRanges[id] = range;
    visibleRanges = visibleRanges;
  }

  function saveChartOpts(sessionId, opts) {
    const prev = chartOpts[sessionId];
    // Guard against a pointless write loop: the chart re-announces its options
    // on every reactive pass, not only when they actually change.
    if (prev && JSON.stringify(prev) === JSON.stringify(opts)) return;
    chartOpts[sessionId] = opts;
    chartOpts = chartOpts;
  }

  // Persist every preference whenever any of them changes. All of them are
  // referenced here so Svelte actually tracks them.
  $: monitorViewStore.saveView({
    viewModes,
    displayModes,
    layoutModes,
    hiddenSeries,
    showLatency,
    showStats,
    showHistoryTable,
    historyFrom,
    historyTo,
    autoModeApplied,
    chartOpts,
    statsSel,
    statsScope,
    collapsed,
  });

  $: monitorViewStore.patchForm({
    pollName,
    pollOids,
    pollThresholds,
    pollInterval,
    pollVersion,
    excludedTargets: [...excludedTargets],
  });

  // Targets available from the settings, minus the ones deselected here. The
  // exclusion set is keyed by address so it survives edits to the target list.
  let excludedTargets = new Set(savedForm.excludedTargets || []);
  $: availableTargets = getTargetsAsArray($settingsStore.targets);
  $: selectedTargets = availableTargets.filter((t) => !excludedTargets.has(t));

  function toggleTarget(t) {
    const next = new Set(excludedTargets);
    if (next.has(t)) next.delete(t);
    else next.add(t);
    excludedTargets = next;
  }

  function setAllTargets(on) {
    excludedTargets = on ? new Set() : new Set(availableTargets);
  }

  function setHidden(sessionId, keys) {
    hiddenSeries[sessionId] = keys;
    hiddenSeries = hiddenSeries;
  }

  function addOid() {
    if (pollOids.length < MAX_OIDS) pollOids = [...pollOids, ''];
  }

  function removeOid(i) {
    pollOids = pollOids.filter((_, idx) => idx !== i);
    pollThresholds = pollThresholds.filter((_, idx) => idx !== i);
    if (pollOids.length === 0) pollOids = [''];
    if (openThreshold === i) openThreshold = null;
  }

  // Tooltip for the session's threshold badge: one line per OID band.
  function describeThresholds(session, tree) {
    return Object.entries(session.thresholds || {})
      .map(([o, t]) => {
        const parts = [];
        if (t.min !== null && t.min !== undefined) parts.push('min ' + t.min);
        if (t.max !== null && t.max !== undefined) parts.push('max ' + t.max);
        if (t.forSeconds) parts.push(t.forSeconds + 's');
        return oidName(o, tree) + ' : ' + (parts.join(' · ') || '—');
      })
      .join('\n');
  }

  function setLayout(sessionId, layout) {
    layoutModes[sessionId] = layout;
    layoutModes = layoutModes;
  }
  let pollInterval = savedForm.pollInterval ?? 5000;
  let pollVersion = savedForm.pollVersion || 'v2c'; // 'v1', 'v2c', 'v3'
  let viewModes = savedView.viewModes || {}; // sessionId -> 'raw' | 'delta' | 'rate' | 'latency'
  let displayModes = savedView.displayModes || {}; // sessionId -> 'graph' | 'table'
  let sessionStats = {};    // sessionId -> stats object
  let showStats = savedView.showStats || {};       // sessionId -> boolean
  let historyFrom = savedView.historyFrom || {};     // sessionId -> datetime string
  let historyTo = savedView.historyTo || {};       // sessionId -> datetime string
  let loadingHistory = {};  // sessionId -> boolean
  let historicalResults = {}; // sessionId -> DataPoint[] (raw, short ranges)
  let historicalBuckets = {}; // sessionId -> Bucket[] (aggregated, long ranges)
  let historicalInfo = {};    // sessionId -> { aggregated, bucketSec, count }
  let showHistoryTable = savedView.showHistoryTable || {};  // sessionId -> boolean
  let showLatency = savedView.showLatency || {};       // sessionId -> boolean (latency companion chart)
  let autoModeApplied = savedView.autoModeApplied || {};   // sessionId -> boolean (counter -> rate, once)

  // A counter (ifInOctets and friends) is meaningless as a raw total: what the
  // operator wants is its rate. Switch once, when the SNMP type first arrives,
  // and never fight a manual choice afterwards.
  $: for (const s of $pollingStore) {
    if (!autoModeApplied[s.id] && (s.results || []).some((r) => r.snmpType)) {
      autoModeApplied[s.id] = true;
      autoModeApplied = autoModeApplied;
      const type = (s.results.find((r) => r.snmpType) || {}).snmpType;
      if (/counter/i.test(type || '') && (viewModes[s.id] || 'raw') === 'raw') {
        viewModes[s.id] = 'rate';
        viewModes = viewModes;
      }
    }
  }

  async function handleStartPolling() {
    const t = get(_);
    const oids = pollOids.map((o) => o.trim()).filter(Boolean);
    if (oids.length === 0) {
      notificationStore.add(t('monitor.enterOid'), 'error');
      return;
    }
    const targets = selectedTargets;
    if (availableTargets.length === 0) {
      notificationStore.add(t('monitor.configureTarget'), 'error');
      return;
    }
    if (targets.length === 0) {
      notificationStore.add(t('monitor.noTargetSelected'), 'error');
      return;
    }
    const thresholdMap = buildThresholdMap(pollOids, pollThresholds);
    // startPolling is async: without the await, `id` is a Promise and every
    // per-session display default below would be filed under "[object Promise]"
    // instead of the session.
    //
    // Usually one session; one per set of identifiers when the targets do not
    // all authenticate the same way.
    const ids = await pollingStore.startPolling(oids, targets, pollInterval, thresholdMap, pollVersion, pollName.trim());
    for (const id of ids) {
      viewModes[id] = 'raw';
      displayModes[id] = 'graph';
      layoutModes[id] = 'separate';
    }
    notificationStore.add(t('monitor.pollingStarted', { values: { oid: oids.join(', '), version: pollVersion, interval: pollInterval/1000, thresholds: Object.keys(thresholdMap).length ? t('monitor.withThresholds') : '' } }), 'success');
  }

  function handleStop(id) {
    pollingStore.stopPolling(id);
  }

  function handleResume(id) {
    pollingStore.resumeSession(id);
  }

  function handleRemove(id) {
    monitorViewStore.dropSession(id);
    delete chartOpts[id];
    delete collapsed[id];
    delete statsSel[id];
    delete statsScope[id];
    delete visibleRanges[id];
    delete hiddenSeries[id];
    delete layoutModes[id];
    delete viewModes[id];
    delete displayModes[id];
    pollingStore.removeSession(id);
  }

  function handleStopAll() {
    const t = get(_);
    pollingStore.stopAll();
    notificationStore.add(t('monitor.allStopped'), 'info');
  }

  function setViewMode(sessionId, mode) {
    viewModes[sessionId] = mode;
    viewModes = viewModes; // trigger reactivity
  }

  function setDisplayMode(sessionId, mode) {
    displayModes[sessionId] = mode;
    displayModes = displayModes; // trigger reactivity
  }

  async function loadStats(sessionId) {
    try {
      sessionStats[sessionId] = await MonitorGetStats(sessionId);
      sessionStats = sessionStats;
    } catch (e) {
      console.warn('Failed to load stats:', e);
    }
  }

  async function toggleStats(sessionId) {
    showStats[sessionId] = !showStats[sessionId];
    showStats = showStats;
    if (showStats[sessionId] && !sessionStats[sessionId]) {
      await loadStats(sessionId);
    }
  }

  const TWO_HOURS_MS = 2 * 3600 * 1000;
  const TARGET_BUCKETS = 500;

  async function loadHistorical(sessionId) {
    const from = historyFrom[sessionId];
    const to = historyTo[sessionId];
    if (!from || !to) return;
    const t = get(_);
    const spanMs = new Date(to) - new Date(from);
    if (!(spanMs > 0)) {
      notificationStore.add(t('monitor.invalidRange'), 'error');
      return;
    }

    loadingHistory[sessionId] = true;
    loadingHistory = loadingHistory;
    historicalResults[sessionId] = null;
    historicalBuckets[sessionId] = null;

    try {
      const fromIso = new Date(from).toISOString();
      const toIso = new Date(to).toISOString();

      if (spanMs > TWO_HOURS_MS) {
        // Long range: aggregate in SQL. Shipping every raw sample would be slow
        // and unreadable — buckets keep the shape with a bounded point count.
        const bucketSec = Math.max(60, Math.round(spanMs / 1000 / TARGET_BUCKETS));
        const buckets = await MonitorLoadBuckets(sessionId, fromIso, toIso, bucketSec);
        if (!buckets || buckets.length === 0) {
          notificationStore.add(t('monitor.noHistoricalData'), 'info');
          return;
        }
        historicalBuckets[sessionId] = buckets;
        historicalInfo[sessionId] = { aggregated: true, bucketSec, count: buckets.length };
        notificationStore.add(
          t('monitor.historicalAggregated', { values: { count: buckets.length, bucket: bucketSec } }),
          'success'
        );
      } else {
        const points = await MonitorLoadHistoricalData(sessionId, fromIso, toIso);
        if (!points || points.length === 0) {
          notificationStore.add(t('monitor.noHistoricalData'), 'info');
          return;
        }
        historicalResults[sessionId] = points;
        historicalInfo[sessionId] = { aggregated: false, bucketSec: 0, count: points.length };
        notificationStore.add(t('monitor.historicalLoaded', { values: { count: points.length } }), 'success');
      }
    } catch (e) {
      console.warn('Failed to load historical data:', e);
      notificationStore.add(t('monitor.historyFailed'), 'error');
    } finally {
      historicalResults = historicalResults;
      historicalBuckets = historicalBuckets;
      historicalInfo = historicalInfo;
      loadingHistory[sessionId] = false;
      loadingHistory = loadingHistory;
    }
  }

  function clearHistorical(sessionId) {
    historicalResults[sessionId] = null;
    historicalBuckets[sessionId] = null;
    historicalInfo[sessionId] = null;
    historicalResults = historicalResults;
    historicalBuckets = historicalBuckets;
    historicalInfo = historicalInfo;
  }

  function toggleLatency(sessionId) {
    showLatency[sessionId] = !showLatency[sessionId];
    showLatency = showLatency;
  }

  function toggleHistoryTable(sessionId) {
    showHistoryTable[sessionId] = !showHistoryTable[sessionId];
    showHistoryTable = showHistoryTable;
  }

</script>

<div class="panel">
  <div class="setup-form">
    <div class="form-group">
      <label for="poll-name">{$_('monitor.nameLabel')}</label>
      <input id="poll-name" type="text" bind:value={pollName} placeholder={$_('monitor.namePlaceholder')} />
    </div>
    <div class="form-group">
      <label for="poll-oid">{$_('monitor.oidLabel')}</label>
      <div class="oid-rows">
        {#each pollOids as oidEntry, i (i)}
          <div class="oid-row">
            <input
              id={i === 0 ? 'poll-oid' : undefined}
              type="text"
              bind:value={pollOids[i]}
              placeholder={$_('monitor.oidPlaceholder')}
            />
            <button
              class="btn-mode threshold-btn"
              class:on={hasThreshold(pollThresholds, i)}
              on:click={() => (openThreshold = openThreshold === i ? null : i)}
              title={$_('monitor.thresholdsHint')}
            >
              <Icon name="triangle-alert" size={12} /> {$_('monitor.thresholds')}
            </button>
            {#if pollOids.length > 1}
              <button class="btn-icon" on:click={() => removeOid(i)} title={$_('common.remove')} aria-label={$_('common.remove')}>
                <Icon name="trash-2" size={14} />
              </button>
            {/if}
          </div>

          {#if openThreshold === i}
            {@const th = thresholdAt(pollThresholds, i)}
            <div class="threshold-editor">
              <div class="th-field">
                <label for={'th-min-' + i}>{$_('monitor.minLabel')}</label>
                <input id={'th-min-' + i} type="number" value={th.min} on:input={(e) => patchThreshold(i, { min: e.target.value })} />
              </div>
              <div class="th-field">
                <label for={'th-max-' + i}>{$_('monitor.maxLabel')}</label>
                <input id={'th-max-' + i} type="number" value={th.max} on:input={(e) => patchThreshold(i, { max: e.target.value })} />
              </div>
              <div class="th-field">
                <label for={'th-for-' + i}>{$_('monitor.thresholdFor')}</label>
                <input id={'th-for-' + i} type="number" min="0" step="5" value={th.forSeconds ?? 0} on:input={(e) => patchThreshold(i, { forSeconds: e.target.value })} />
                <span class="th-unit">s</span>
              </div>
              <button class="btn-mode" on:click={() => patchThreshold(i, { min: '', max: '', forSeconds: 0 })}>{$_('common.clear')}</button>
              <span class="th-explain">{$_('monitor.thresholdExplain')}</span>
            </div>
          {/if}
        {/each}
        <button class="btn-mode add-oid" on:click={addOid} disabled={pollOids.length >= MAX_OIDS}>
          <Icon name="plus" size={13} /> {$_('monitor.addOid')}
        </button>
      </div>
      <span class="field-hint">{$_('monitor.multiOidHint')}</span>
    </div>
    <div class="form-group targets-picker" role="group" aria-labelledby="targets-to-watch">
      <span class="group-label" id="targets-to-watch">{$_('monitor.targetsToWatch')}</span>
      <div class="target-chips">
        {#each availableTargets as t}
          <button
            class="target-chip"
            class:on={!excludedTargets.has(t)}
            on:click={() => toggleTarget(t)}
            title={t}
          >
            {#if !excludedTargets.has(t)}<Icon name="check" size={12} />{/if}
            {$anonMode ? anonymizeIp(t) : $targetLabels[t] || t}
          </button>
        {/each}
        {#if availableTargets.length === 0}
          <span class="field-hint">{$_('monitor.configureTarget')}</span>
        {:else}
          <button class="btn-mode" on:click={() => setAllTargets(excludedTargets.size > 0)}>
            {excludedTargets.size > 0 ? $_('monitor.targetsAll') : $_('monitor.targetsNone')}
          </button>
        {/if}
      </div>
      {#if usesOwnIdentifiers($settingsStore, selectedTargets)}
        <span class="field-hint"><Icon name="key-round" size={12} /> {$_('profiles.monitorOwnIdentity')}</span>
      {/if}
    </div>
    <div class="form-row">
      <div class="form-group compact">
        <label for="poll-version">{$_('monitor.versionLabel')}</label>
        <select id="poll-version" bind:value={pollVersion} title={$_('profiles.monitorVersionHint')}>
          <option value="v1">v1</option>
          <option value="v2c">v2c</option>
          <option value="v3">v3</option>
        </select>
      </div>
      <div class="form-group compact">
        <label for="poll-interval">{$_('monitor.intervalLabel')}</label>
        <select id="poll-interval" bind:value={pollInterval}>
          <option value={1000}>1s</option>
          <option value={5000}>5s</option>
          <option value={10000}>10s</option>
          <option value={30000}>30s</option>
          <option value={60000}>60s</option>
        </select>
      </div>
      <button class="btn btn-primary" on:click={handleStartPolling}>{$_('monitor.startPolling')}</button>
      {#if $pollingStore.some(s => s.running)}
        <button class="btn btn-danger" on:click={handleStopAll}>{$_('monitor.stopAll')}</button>
      {/if}
    </div>
    <div class="threshold-section">
      <div class="notification-options">
          <label class="toggle-label">
            <input
              type="checkbox"
              checked={$settingsStore.monitor?.systemNotifications}
              on:change={(e) => {
                const enabled = e.target.checked;
                settingsStore.save({
                  ...$settingsStore,
                  monitor: { ...$settingsStore.monitor, systemNotifications: enabled }
                });
              }}
            />
            <span>{$_('monitor.systemNotifications')}</span>
          </label>
          <label class="toggle-label">
            <input
              type="checkbox"
              checked={$settingsStore.monitor?.alertSound}
              on:change={(e) => settingsStore.save({
                ...$settingsStore,
                monitor: { ...$settingsStore.monitor, alertSound: e.target.checked }
              })}
            />
            <span>{$_('monitor.alertSound')}</span>
          </label>
      </div>
    </div>
  </div>

  {#if $pollingStore.length === 0}
    <div class="empty-state">
      <p>{$_('monitor.empty')}</p>
      <p class="hint">{$_('monitor.emptyHint')}</p>
    </div>
  {:else}
    <div class="sessions">
      {#each $pollingStore as session (session.id)}
        <div class="session-card">
          <div class="session-header">
            <div class="session-info">
              <button
                class="collapse-btn"
                on:click={() => toggleCollapsed(session.id)}
                title={collapsed[session.id] ? $_('monitor.expandSession') : $_('monitor.collapseSession')}
                aria-expanded={!collapsed[session.id]}
              >
                <Icon name={collapsed[session.id] ? 'chevron-right' : 'chevron-down'} size={14} />
              </button>
              {#if session.name}
                <span class="session-name">{session.name}</span>
                <span class="session-oid subtle" title={(session.oids || [session.oid]).join('\n')}>
                  {(session.oids && session.oids.length ? session.oids : [session.oid])
                    .map((o) => oidName(o, $mibStore.tree))
                    .join(', ')}
                </span>
              {:else}
                <span class="session-oid" title={(session.oids || [session.oid]).join('\n')}>
                  {(session.oids && session.oids.length ? session.oids : [session.oid])
                    .map((o) => oidName(o, $mibStore.tree))
                    .join(', ')}
                </span>
              {/if}
              <button class="btn-copy-small" on:click|stopPropagation={() => copyToClipboard(session.oid, 'OID')} title="Copy OID"><Icon name="copy" size={13} /></button>
              <span class="session-status" class:running={session.running}>
                {session.running ? $_('monitor.running') : $_('monitor.stopped')}
              </span>
              <span class="session-meta">{session.snmpVersion} / {session.targets.length} target(s) / {session.interval/1000}s</span>
              <span class="session-meta">{session.results.length} data point(s)</span>
              {#if Object.keys(session.thresholds || {}).length}
                <span class="threshold-badge" title={describeThresholds(session, $mibStore.tree)}>
                  {$_('monitor.thresholdsBadge')} ({Object.keys(session.thresholds).length})
                </span>
              {/if}
            </div>
            <div class="session-actions">
              <div class="display-mode-toggle" class:hidden-controls={collapsed[session.id]}>
                <button class="btn-mode" class:active={displayModes[session.id] !== 'table'} on:click={() => setDisplayMode(session.id, 'graph')}>{$_('monitor.graph')}</button>
                <button class="btn-mode" class:active={displayModes[session.id] === 'table'} on:click={() => setDisplayMode(session.id, 'table')}>{$_('monitor.table')}</button>
              </div>
              {#if displayModes[session.id] !== 'table' && !collapsed[session.id]}
                <div class="view-mode-toggle">
                  <button class="btn-mode" class:active={viewModes[session.id] === 'raw'} on:click={() => setViewMode(session.id, 'raw')}>{$_('monitor.raw')}</button>
                  <button class="btn-mode" class:active={viewModes[session.id] === 'delta'} on:click={() => setViewMode(session.id, 'delta')}>{$_('monitor.delta')}</button>
                  <button class="btn-mode" class:active={viewModes[session.id] === 'rate'} on:click={() => setViewMode(session.id, 'rate')}>{$_('monitor.rate')}</button>
                  <button class="btn-mode" class:active={viewModes[session.id] === 'latency'} on:click={() => setViewMode(session.id, 'latency')}>{$_('monitor.latency')}</button>
                </div>
                {#if (session.oids || []).length > 1}
                  <div class="mode-toggle">
                    <button class="btn-mode" class:active={(layoutModes[session.id] || 'separate') === 'separate'} on:click={() => setLayout(session.id, 'separate')}>{$_('monitor.layoutSeparate')}</button>
                    <button class="btn-mode" class:active={layoutModes[session.id] === 'stacked'} on:click={() => setLayout(session.id, 'stacked')} title={$_('monitor.layoutStackedHint')}>{$_('monitor.layoutStacked')}</button>
                  </div>
                {/if}
                <div class="mode-toggle">
                  <button class="btn-mode" on:click={() => (channelsFor = session.id)} title={$_('monitor.channelsHintShort')}>
                    <Icon name="activity" size={12} /> {$_('monitor.channels')}
                  </button>
                </div>
                <div class="mode-toggle">
                  <button class="btn-mode" class:active={showLatency[session.id]} on:click={() => toggleLatency(session.id)} title={$_('monitor.latencyCompanionHint')}>+ {$_('monitor.latency')}</button>
                </div>
              {/if}
              {#if session.running}
                <button class="btn btn-small" on:click={() => handleStop(session.id)}>{$_('common.stop')}</button>
              {:else}
                <button class="btn btn-small" on:click={() => handleResume(session.id)}>{$_('monitor.resume')}</button>
              {/if}
              <button class="btn btn-small btn-danger" on:click={() => handleRemove(session.id)}>{$_('monitor.remove')}</button>
              <button class="btn btn-small" on:click={() => toggleStats(session.id)}>
                {$_('monitor.stats')}
              </button>
            </div>
          </div>

          {#if session.overrun}
            <div class="overrun-banner" role="status">
              <Icon name="triangle-alert" size={14} />
              <div class="overrun-text">
                <strong>{$_('monitor.overrunTitle')}</strong>
                {#if session.overrun.accepted}
                  {$_('monitor.overrunAccepted', { values: {
                    interval: formatCadence(session.overrun.intervalMs),
                    cycle: formatCadence(session.overrun.cycleMs),
                  } })}
                {:else}
                  {$_('monitor.overrunBody', { values: {
                    interval: formatCadence(session.overrun.intervalMs),
                    cycle: formatCadence(session.overrun.cycleMs),
                    effective: formatCadence(session.overrun.effectiveMs),
                  } })}
                {/if}
              </div>
              {#if !session.overrun.accepted}
                <button
                  class="btn btn-small"
                  title={$_('monitor.overrunAcceptHint')}
                  on:click={() => pollingStore.acceptSlow(session.id)}
                >{$_('monitor.overrunAccept')}</button>
              {/if}
            </div>
          {/if}

          {#if !collapsed[session.id]}
          {#if showStats[session.id] && sessionStats[session.id]}
            <div class="stats-panel">
              <div class="stats-grid">
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsTotalPoints')}</span>
                  <span class="stat-value">{sessionStats[session.id].totalPoints}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsDateRange')}</span>
                  <span class="stat-value">{sessionStats[session.id].firstTimestamp?.slice(0,16)} — {sessionStats[session.id].lastTimestamp?.slice(0,16)}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsMinValue')}</span>
                  <span class="stat-value">{sessionStats[session.id].minValue ?? '—'}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsMaxValue')}</span>
                  <span class="stat-value">{sessionStats[session.id].maxValue ?? '—'}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsAvgValue')}</span>
                  <span class="stat-value">{sessionStats[session.id].avgValue != null ? sessionStats[session.id].avgValue.toFixed(2) : '—'}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsAvgLatency')}</span>
                  <span class="stat-value">{sessionStats[session.id].avgLatency != null ? sessionStats[session.id].avgLatency.toFixed(1) + ' ms' : '—'}</span>
                </div>
                <div class="stat-item">
                  <span class="stat-label">{$_('monitor.statsErrorCount')}</span>
                  <span class="stat-value">{sessionStats[session.id].errorCount}</span>
                </div>
              </div>
            </div>
          {/if}
          <div class="history-controls">
            <input type="datetime-local" bind:value={historyFrom[session.id]} />
            <span>→</span>
            <input type="datetime-local" bind:value={historyTo[session.id]} />
            <button class="btn btn-small" on:click={() => loadHistorical(session.id)} disabled={loadingHistory[session.id]}>
              {loadingHistory[session.id] ? '...' : $_('monitor.loadHistory')}
            </button>
          </div>
          {#if historicalInfo[session.id]}
            <div class="historical-block">
              <div class="historical-header">
                <span>
                  {$_('monitor.historicalData')}
                  {#if historicalInfo[session.id].aggregated}
                    — {$_('monitor.aggregatedBy', { values: { bucket: historicalInfo[session.id].bucketSec } })}
                  {/if}
                  ({historicalInfo[session.id].count})
                </span>
                <span class="hist-actions">
                  <button class="btn-mode" on:click={() => toggleHistoryTable(session.id)}>
                    {showHistoryTable[session.id] ? $_('monitor.hideTable') : $_('monitor.showTable')}
                  </button>
                  <button class="btn-mode" on:click={() => clearHistorical(session.id)}>{$_('common.clear')}</button>
                </span>
              </div>

              {#each (session.oids && session.oids.length ? session.oids : [session.oid]) as sessionOid}
                {#if (session.oids || []).length > 1}
                  <div class="oid-facet-label" title={oidTooltip(sessionOid, $mibStore.tree)}>
                    <Icon name="route" size={12} />
                    {oidName(sessionOid, $mibStore.tree)}
                    <span class="oid-raw">{sessionOid}</span>
                  </div>
                {/if}
                <MonitorChart
                  {session}
                  oid={sessionOid}
                  mode={viewModes[session.id] || 'raw'}
                  points={historicalResults[session.id] || null}
                  buckets={historicalBuckets[session.id] || null}
                  theme={resolvedTheme}
                  syncGroup={'hist-' + session.id}
                  options={chartOpts['hist-' + session.id] || {}}
                  on:options={(e) => saveChartOpts('hist-' + session.id, e.detail)}
                />
              {/each}

              {#if showHistoryTable[session.id] && historicalResults[session.id]}
                <div class="table-container">
                  <table class="data-table">
                    <thead>
                      <tr>
                        <th>{$_('monitor.tableTime')}</th>
                        <th>{$_('monitor.tableTarget')}</th>
                        <th>{$_('monitor.tableValue')}</th>
                        <th>{$_('monitor.tableDelta')}</th>
                        <th>{$_('monitor.tableRate')}</th>
                        <th>{$_('monitor.tableLatency')}</th>
                        <th>{$_('monitor.tableError')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each historicalResults[session.id] as point}
                        <tr class:error-row={point.error}>
                          <td class="mono">{formatTimeShort(point.timestamp)}</td>
                          <td title={$anonMode ? anonymizeIp(point.target) : point.target}>{$anonMode ? anonymizeIp(point.target) : $targetLabels[point.target] || point.target}</td>
                          <td class="mono">
                            {point.value !== null ? point.value : '-'}
                            {#if point.value !== null}
                              <button class="btn-copy-small" on:click={() => copyToClipboard(String(point.value), $_('monitor.tableValue'))} title={$_('monitor.tableValue')}><Icon name="copy" size={13} /></button>
                            {/if}
                          </td>
                          <td class="mono">{point.delta !== null ? point.delta : '-'}</td>
                          <td class="mono">{point.rate !== null ? point.rate.toFixed(2) : '-'}</td>
                          <td class="mono">{point.responseTimeMs}</td>
                          <td class="error-cell">{point.error || ''}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
            </div>
          {/if}
          {#if displayModes[session.id] === 'table'}
            <div class="table-container">
              <table class="data-table">
                <thead>
                  <tr>
                    <th>{$_('monitor.tableTime')}</th>
                    <th>{$_('monitor.tableTarget')}</th>
                    <th>{$_('monitor.tableValue')}</th>
                    <th>{$_('monitor.tableDelta')}</th>
                    <th>{$_('monitor.tableRate')}</th>
                    <th>{$_('monitor.tableLatency')}</th>
                    <th>{$_('monitor.tableError')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each [...session.results].reverse().slice(0, 100) as point}
                    <tr class:error-row={point.error}>
                      <td class="mono">{formatTimeShort(point.timestamp)}</td>
                      <td title={$anonMode ? anonymizeIp(point.target) : point.target}>{$anonMode ? anonymizeIp(point.target) : $targetLabels[point.target] || point.target}</td>
                      <td class="mono">
                        {point.value !== null ? point.value : '-'}
                        {#if point.value !== null}
                          <button class="btn-copy-small" on:click={() => copyToClipboard(String(point.value), $_('monitor.tableValue'))} title={$_('monitor.tableValue')}><Icon name="copy" size={13} /></button>
                        {/if}
                      </td>
                      <td class="mono">{point.delta !== null ? point.delta : '-'}</td>
                      <td class="mono">{point.rate !== null ? point.rate.toFixed(2) : '-'}</td>
                      <td class="mono">{point.responseTimeMs}</td>
                      <td class="error-cell">{point.error || ''}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              {#if session.results.length > 100}
                <div class="table-info">{$_('monitor.showingLast', { values: { count: session.results.length } })}</div>
              {/if}
            </div>
          {:else}
            {#if (session.oids || []).length > 1 && layoutModes[session.id] === 'stacked'}
              <MetricTiles
                {session}
                oids={session.oids}
                layout="stacked"
                mode={viewModes[session.id] || 'raw'}
                theme={resolvedTheme}
                hidden={hiddenSeries[session.id] || []}
                stats={statsFor(statsSel, session.id)}
                scope={scopeFor(statsScope, session.id)}
                range={visibleRanges[session.id] || null}
              />
              <MonitorChart
                {session}
                oids={session.oids}
                mode={viewModes[session.id] || 'raw'}
                theme={resolvedTheme}
                hidden={hiddenSeries[session.id] || []}
                syncGroup={'live-' + session.id}
                options={chartOpts[session.id] || {}}
                on:options={(e) => saveChartOpts(session.id, e.detail)}
                on:range={(e) => noteRange(session.id, e.detail)}
              />
            {:else}
              <MetricTiles
                {session}
                oids={session.oids && session.oids.length ? session.oids : [session.oid]}
                layout="separate"
                mode={viewModes[session.id] || 'raw'}
                theme={resolvedTheme}
                hidden={hiddenSeries[session.id] || []}
                stats={statsFor(statsSel, session.id)}
                scope={scopeFor(statsScope, session.id)}
                range={visibleRanges[session.id] || null}
              />
              {#each (session.oids && session.oids.length ? session.oids : [session.oid]) as sessionOid}
                {#if (session.oids || []).length > 1}
                  <div class="oid-facet-label" title={oidTooltip(sessionOid, $mibStore.tree)}>
                    <Icon name="route" size={12} />
                    {oidName(sessionOid, $mibStore.tree)}
                    <span class="oid-raw">{sessionOid}</span>
                  </div>
                {/if}
                <MonitorChart
                  {session}
                  oid={sessionOid}
                  mode={viewModes[session.id] || 'raw'}
                  theme={resolvedTheme}
                  hidden={hiddenSeries[session.id] || []}
                  syncGroup={'live-' + session.id}
                  options={chartOpts[session.id + '|' + sessionOid] || chartOpts[session.id] || {}}
                  on:options={(e) => saveChartOpts(session.id + '|' + sessionOid, e.detail)}
                  on:range={(e) => noteRange(session.id, e.detail)}
                />
              {/each}
            {/if}
            {#if showLatency[session.id] && (viewModes[session.id] || 'raw') !== 'latency'}
              <div class="companion-label">{$_('monitor.latencyCompanion')}</div>
              <MonitorChart
                {session}
                mode="latency"
                theme={resolvedTheme}
                syncGroup={'live-' + session.id}
              />
            {/if}
            <AlertTimeline {session} />
          {/if}
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

{#if channelsFor}
  {@const cs = $pollingStore.find((s) => s.id === channelsFor)}
  {#if cs}
    <ChannelsModal
      session={cs}
      hidden={hiddenSeries[cs.id] || []}
      layout={layoutModes[cs.id] || 'separate'}
      theme={resolvedTheme}
      stats={statsFor(statsSel, cs.id)}
      scope={scopeFor(statsScope, cs.id)}
      on:change={(e) => setHidden(cs.id, e.detail)}
      on:stats={(e) => setStats(cs.id, e.detail)}
      on:scope={(e) => setScope(cs.id, e.detail)}
      on:close={() => (channelsFor = null)}
    />
  {/if}
{/if}

<style>
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

  .form-group.compact select {
    width: 80px;
  }

  /* .empty-state is defined globally in shared.css */

  .hint { font-size: 0.9em; font-style: italic; }

  .sessions {
    display: flex;
    flex-direction: column;
    gap: 15px;
  }

  .session-card {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    overflow: hidden;
    background-color: var(--bg-lighter-color);
  }

  /* The one thing on this card that asks a question rather than showing a
     number, so it is the one thing that is not the card's own colour. */
  .overrun-banner {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.5rem 0.75rem;
    border-top: 1px solid var(--border-color);
    background-color: var(--warning-subtle);
    color: var(--warning-color);
    font-size: 0.82rem;
  }

  .overrun-text {
    flex: 1;
    min-width: 0;
  }

  .session-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 15px;
    background-color: var(--shadow-color);
    border-bottom: 1px solid var(--border-color);
    flex-wrap: wrap;
    gap: 8px;
  }

  .session-info {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .session-oid {
    font-family: 'Courier New', monospace;
    font-weight: 600;
    color: var(--oid-color);
  }

  .session-status {
    padding: 2px 8px;
    border-radius: 3px;
    font-size: 0.8em;
    font-weight: 600;
    background-color: var(--error-subtle-strong);
    color: var(--error-color);
  }

  .session-status.running {
    background-color: var(--success-subtle-strong);
    color: var(--success-color);
    animation: pulse 2s infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.6; }
  }

  .session-meta {
    font-size: 0.85em;
    color: var(--text-muted);
  }

  .session-actions {
    display: flex;
    gap: 8px;
    align-items: center;
  }

  .view-mode-toggle {
    display: flex;
    gap: 2px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    overflow: hidden;
  }

  .btn-mode {
    padding: 4px 10px;
    font-size: 0.8em;
    background-color: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.2s;
  }

  .btn-mode:hover { background-color: var(--hover-overlay); color: var(--text-color); }
  .btn-mode.active { background-color: var(--accent-color); color: white; }

  /* Chart styles now live in monitor/MonitorChart.svelte */

  /* Threshold UI */
  .threshold-section {
    margin-top: 10px;
    padding-top: 10px;
    border-top: 1px solid var(--border-color);
  }

  .toggle-label {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: 0.9em;
    user-select: none;
  }

  .toggle-label input[type="checkbox"] {
    width: 16px;
    height: 16px;
    accent-color: var(--accent-color);
    cursor: pointer;
  }

  .threshold-badge {
    padding: 2px 8px;
    border-radius: 3px;
    font-size: 0.75em;
    font-weight: 600;
    background-color: var(--warning-subtle);
    color: var(--warning-color);
  }

  .notification-options {
    margin-top: 8px;
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
  }

  .toggle-label {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.85em;
    color: var(--text-dimmed);
    cursor: pointer;
    user-select: none;
  }

  .toggle-label input[type="checkbox"] {
    accent-color: var(--accent-color);
    cursor: pointer;
  }

  .display-mode-toggle {
    display: flex;
    gap: 2px;
    border: 1px solid var(--accent-color);
    border-radius: 4px;
    overflow: hidden;
  }

  .table-container {
    max-height: 300px;
    overflow-y: auto;
    padding: 10px 15px;
  }

  .data-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85em;
  }

  .data-table th,
  .data-table td {
    padding: 6px 10px;
    text-align: left;
    border-bottom: 1px solid var(--border-color);
  }

  .data-table th {
    background-color: var(--shadow-color-strong);
    font-weight: 600;
    position: sticky;
    top: 0;
    z-index: 1;
  }

  .data-table tbody tr:hover {
    background-color: var(--hover-overlay);
  }

  .data-table .mono {
    font-family: 'Courier New', monospace;
  }

  .data-table .error-row {
    background-color: var(--error-subtle-medium);
  }

  .data-table .error-cell {
    color: var(--error-color);
    font-size: 0.85em;
  }

  .table-info {
    text-align: center;
    padding: 8px;
    font-size: 0.85em;
    color: var(--text-muted);
    font-style: italic;
  }

  .stats-panel {
    padding: 10px 15px;
    background-color: var(--bg-color);
    border-radius: 4px;
    margin-top: 8px;
  }

  .stats-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 8px;
  }

  .stat-item {
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
    font-size: 0.9em;
    font-weight: 600;
    color: var(--text-color);
    font-family: 'Courier New', monospace;
  }

  .history-controls {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 15px;
    font-size: 0.85em;
  }

  .history-controls input[type="datetime-local"] {
    padding: 4px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.9em;
  }

  .history-controls span {
    color: var(--text-muted);
  }

  .historical-block {
    margin: 10px 15px 0;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    overflow: hidden;
  }

  .hist-actions {
    display: inline-flex;
    gap: 6px;
  }

  .targets-picker {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }

  .target-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .target-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 10px;
    font-size: 0.8em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .target-chip.on {
    color: var(--accent-color);
    border-color: var(--accent-border);
    background-color: var(--accent-subtle);
    font-weight: 600;
  }

  .threshold-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    white-space: nowrap;
  }

  .threshold-btn.on {
    color: var(--error-color);
    border-color: var(--error-color);
  }

  .threshold-editor {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin: 2px 0 6px 12px;
    padding: 8px 12px;
    border-left: 2px solid var(--error-color);
    background-color: var(--bg-color);
    border-radius: 0 4px 4px 0;
  }

  .th-field {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .th-field label {
    font-size: 0.78em;
    color: var(--text-muted);
    min-width: auto;
  }

  .th-field input {
    width: 90px;
    padding: 3px 6px;
  }

  .th-unit {
    font-size: 0.78em;
    color: var(--text-muted);
  }

  .th-explain {
    font-size: 0.72em;
    color: var(--text-muted);
    flex-basis: 100%;
  }

  .oid-rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
    flex: 1;
  }

  .oid-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .oid-row input {
    flex: 1;
  }

  .add-oid {
    align-self: flex-start;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .field-hint {
    display: block;
    margin-top: 4px;
    font-size: 0.75em;
    color: var(--text-muted);
  }

  .collapse-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: none;
    border: none;
    padding: 2px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .collapse-btn:hover {
    color: var(--text-color);
  }

  .hidden-controls {
    display: none;
  }

  .session-name {
    font-weight: 600;
    color: var(--text-color);
  }

  .session-oid.subtle {
    font-weight: 400;
    opacity: 0.75;
    font-size: 0.85em;
  }

  .oid-facet-label .oid-raw {
    color: var(--text-muted);
    font-weight: 400;
    font-size: 0.92em;
  }

  .oid-facet-label {
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 10px 15px 0;
    font-family: 'Courier New', monospace;
    font-size: 0.78em;
    color: var(--oid-color);
    font-weight: 600;
  }

  .companion-label {
    padding: 8px 15px 0;
    font-size: 0.78em;
    color: var(--text-muted);
  }

  .historical-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    font-size: 0.85em;
    font-weight: 600;
    color: var(--text-muted);
    padding: 4px 0 8px;
    border-bottom: 1px solid var(--border-color);
    margin-bottom: 4px;
  }
</style>
