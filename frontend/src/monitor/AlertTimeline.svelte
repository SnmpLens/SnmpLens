<script>
  import { onMount, onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { formatTimestamp } from '../utils/formatting';
  import { anonMode, anonymizeIp, anonymizeText } from '../utils/anonymize';
  import { targetLabels } from '../stores/targetLabels';
  import { mibStore } from '../stores/mibStore';
  import { oidName, oidTooltip } from '../utils/oidDisplay';
  import { EventsQuery } from '../bridge/App';
  import { EventsOn } from '../bridge/runtime';
  import { capNewestFirst } from '../utils/burst';

  // Far more incidents than anyone reads on a timeline, and a ceiling all
  // the same: 'event:new' arrives at whatever rate the network chooses.
  const MAX_INCIDENTS = 500;
  import { displayTarget as labelled } from '../utils/targets';

  export let session;

  const MAX_LISTED = 12;

  // Incidents come from the event journal, not from an in-memory list on the
  // session: detection lives in Go now, the journal is the single record, and
  // it survives a restart and a webview reload.
  let incidents = [];
  let unlisten = null;

  async function load(sessionId) {
    if (!sessionId) return;
    try {
      const page = await EventsQuery({
        sessionId,
        categories: ['threshold', 'reachability'],
        limit: 50,
      });
      incidents = page.items || [];
    } catch (e) {
      console.warn('Failed to load incidents:', e);
    }
  }

  $: load(session?.id);

  onMount(() => {
    unlisten = EventsOn('event:new', (ev) => {
      if (!ev || ev.sessionId !== session?.id) return;
      if (ev.category !== 'threshold' && ev.category !== 'reachability') return;
      // Capped: this list is fed by 'event:new', which a trap flood drives at
      // the rate the network chooses. It has no paging, so dropping the tail
      // costs nothing a reload does not restore.
      incidents = capNewestFirst([ev, ...incidents], MAX_INCIDENTS);
    });
  });

  onDestroy(() => {
    if (typeof unlisten === 'function') unlisten();
  });

  // Only openings get a marker: a resolution ends an incident, it is not a
  // second one.
  $: openings = incidents.filter((e) => e.state !== 'resolved');

  function strip(list, s) {
    const points = s?.results || [];
    if (!points.length || !list.length) return [];
    const first = Date.parse(points[0].timestamp);
    const last = Date.parse(points[points.length - 1].timestamp);
    const span = last - first || 1;
    return list.map((e) => ({
      ...e,
      left: Math.max(0, Math.min(100, ((Date.parse(e.ts) - first) / span) * 100)),
    }));
  }

  $: marks = strip(openings, session);

  function displayTarget(source) {
    if (!source) return '—';
    return labelled(source, $targetLabels, $anonMode);
  }

  // The stored params carry the unmasked address and every title key
  // interpolates them, so the target column read `Device-1` while the sentence
  // beside it read the real address. Same defect as EventsPanel.
  function anonParams(params) {
    if (!$anonMode || !params) return params || {};
    const out = {};
    for (const [k, v] of Object.entries(params)) {
      out[k] = typeof v === 'string' ? anonymizeText(v) : v;
    }
    return out;
  }

  function title(ev) {
    const translated = $_(ev.titleKey, { values: anonParams(ev.params), default: '' });
    if (translated && translated !== ev.titleKey) return translated;
    return $anonMode ? anonymizeText(ev.summary) : ev.summary;
  }
</script>

{#if incidents.length}
  <div class="alerts">
    <div class="alerts-head">
      <Icon name="triangle-alert" class="icon-warning" size={14} />
      <strong>{$_('monitor.alertsTitle', { values: { count: incidents.length } })}</strong>
    </div>

    <div class="strip" aria-hidden="true">
      {#each marks as m (m.id)}
        <span class="mark" style="left: {m.left}%" title={formatTimestamp(m.ts)}></span>
      {/each}
    </div>

    <ul class="alert-list">
      {#each incidents.slice(0, MAX_LISTED) as ev (ev.id)}
        <li class:resolved={ev.state === 'resolved'}>
          <span class="when">{formatTimestamp(ev.ts)}</span>
          <span class="who" title={displayTarget(ev.source)}>{displayTarget(ev.source)}</span>
          {#if ev.oid}
            <span class="what-oid" title={oidTooltip(ev.oid, $mibStore.tree)}>{oidName(ev.oid, $mibStore.tree)}</span>
          {:else}
            <span></span>
          {/if}
          <span class="what">
            <Icon name={ev.state === 'resolved' ? 'check' : 'triangle-alert'} size={12} />
            {title(ev)}
          </span>
        </li>
      {/each}
    </ul>

    {#if incidents.length > MAX_LISTED}
      <p class="more">{$_('monitor.alertsMore', { values: { count: incidents.length - MAX_LISTED } })}</p>
    {/if}
  </div>
{/if}

<style>
  .alerts {
    margin: 10px 15px 0;
    padding: 10px 12px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background-color: var(--bg-color);
  }

  .alerts-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 0.85em;
    margin-bottom: 8px;
  }

  .strip {
    position: relative;
    height: 10px;
    border-radius: 3px;
    background-color: var(--bg-lighter-color);
    margin-bottom: 8px;
    overflow: hidden;
  }

  .mark {
    position: absolute;
    top: 0;
    width: 3px;
    height: 100%;
    border-radius: 1px;
    background-color: var(--error-color);
  }

  .alert-list {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 0.78em;
  }

  .alert-list li {
    display: grid;
    grid-template-columns: 130px minmax(80px, 1fr) minmax(0, 1fr) minmax(140px, 2fr);
    gap: 10px;
    align-items: center;
    padding: 3px 0;
    border-top: 1px solid var(--border-color);
  }

  .alert-list li:first-child {
    border-top: none;
  }

  /* A resolution is good news: it must not read as another alarm. */
  .alert-list li.resolved {
    opacity: 0.7;
  }

  .alert-list li.resolved .what {
    color: var(--success-color);
  }

  .when {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .who {
    color: var(--text-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .what-oid {
    font-family: 'Courier New', monospace;
    font-size: 0.9em;
    color: var(--oid-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .what {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--error-color);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .more {
    margin: 6px 0 0;
    font-size: 0.75em;
    color: var(--text-muted);
  }
</style>
