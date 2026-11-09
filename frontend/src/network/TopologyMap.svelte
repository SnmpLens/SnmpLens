<script>
  import { onDestroy } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import { TopologyScan, TopologyCancel } from '../../wailsjs/go/app/App';
  import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
  import { settingsStore } from '../stores/settingsStore';
  import { notificationStore } from '../stores/notifications';
  import { targetLabels } from '../stores/targetLabels';
  import { getTargetsAsArray, displayTarget } from '../utils/targets';
  import { anonMode, anonymizeIp, anonymizeHost } from '../utils/anonymize';
  import { downloadFile } from '../utils/csv';
  import { buildTopologyRequest, layout, linksOf } from '../utils/topology';

  const W = 1000;
  const H = 640;
  const ICONS = { switch: 'network', router: 'route', ap: 'wifi', phone: 'radio', host: 'server' };

  let chosen = null; // null: every target
  let depth = 0;
  let drawing = false;
  let progress = null;
  let graph = null;
  let pos = {};
  let selected = null;
  let view = { x: 0, y: 0, scale: 1 };
  let svg;
  let drag = null;

  $: targets = getTargetsAsArray($settingsStore.targets);
  $: seeds = chosen === null ? targets : chosen.filter((t) => targets.includes(t));
  $: byId = Object.fromEntries((graph?.nodes || []).map((n) => [n.id, n]));

  EventsOn('topology:progress', (p) => { progress = p; });
  onDestroy(() => EventsOff('topology:progress'));

  async function draw() {
    drawing = true;
    progress = null;
    selected = null;
    try {
      graph = await TopologyScan(buildTopologyRequest(get(settingsStore), seeds, depth));
      pos = layout(graph, { width: W, height: H });
      view = { x: 0, y: 0, scale: 1 };
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      drawing = false;
      progress = null;
    }
  }

  function toggle(target, on) {
    const base = chosen === null ? [...targets] : chosen;
    chosen = on ? [...base, target] : base.filter((t) => t !== target);
  }

  function label(node, anon) {
    if (!node) return '';
    return anon ? anonymizeHost(node.name) : node.name;
  }

  function address(value, anon) {
    return anon && value ? anonymizeIp(value) : value;
  }

  function kindIcon(kind) {
    return ICONS[kind] || 'circle-dot';
  }

  // Panning and zooming the drawing, and moving a node, with the pointer.
  function svgPoint(e) {
    const r = svg.getBoundingClientRect();
    return { x: ((e.clientX - r.left) / r.width) * W, y: ((e.clientY - r.top) / r.height) * H };
  }

  function onWheel(e) {
    e.preventDefault();
    const p = svgPoint(e);
    const scale = Math.min(4, Math.max(0.3, view.scale * (e.deltaY < 0 ? 1.1 : 1 / 1.1)));
    // Zoom about the pointer: the point under it stays under it.
    view = {
      scale,
      x: p.x - ((p.x - view.x) * scale) / view.scale,
      y: p.y - ((p.y - view.y) * scale) / view.scale,
    };
  }

  function startPan(e) {
    if (e.button !== 0) return;
    drag = { kind: 'pan', from: svgPoint(e), view: { ...view } };
    selected = null;
  }

  function startNode(e, id) {
    e.stopPropagation();
    drag = { kind: 'node', id, from: svgPoint(e), at: { ...pos[id] }, moved: false };
  }

  function onMove(e) {
    if (!drag) return;
    const p = svgPoint(e);
    if (drag.kind === 'pan') {
      view = { ...drag.view, x: drag.view.x + p.x - drag.from.x, y: drag.view.y + p.y - drag.from.y };
    } else {
      const dx = (p.x - drag.from.x) / view.scale;
      const dy = (p.y - drag.from.y) / view.scale;
      if (Math.abs(dx) + Math.abs(dy) > 2) drag.moved = true;
      pos = { ...pos, [drag.id]: { x: drag.at.x + dx, y: drag.at.y + dy } };
    }
  }

  function endDrag() {
    if (drag?.kind === 'node' && !drag.moved) selected = drag.id;
    drag = null;
  }

  function exportSvg() {
    const clone = svg.cloneNode(true);
    clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    downloadFile(clone.outerHTML, 'network-map.svg', 'image/svg+xml');
  }
</script>

<svelte:window on:pointermove={onMove} on:pointerup={endDrag} />

<div class="tool-section">
  {#if targets.length === 0}
    <p class="empty">{$_('topology.noTargets')}</p>
  {:else}
    <div class="tool-form">
      <details class="seeds">
        <summary>{$_('topology.devices', { values: { count: seeds.length } })}</summary>
        <div class="seed-list">
          {#each targets as t, i (i)}
            <label class="check">
              <input type="checkbox" checked={seeds.includes(t)} disabled={drawing}
                on:change={(e) => toggle(t, e.currentTarget.checked)} />
              {displayTarget(t, $targetLabels, $anonMode)}
            </label>
          {/each}
        </div>
      </details>
      <div class="form-group">
        <label for="topology-depth">{$_('topology.depth')}</label>
        <select id="topology-depth" bind:value={depth} disabled={drawing}>
          <option value={0}>{$_('topology.depthNone')}</option>
          <option value={1}>{$_('topology.depthHops', { values: { count: 1 } })}</option>
          <option value={2}>{$_('topology.depthHops', { values: { count: 2 } })}</option>
          <option value={3}>{$_('topology.depthHops', { values: { count: 3 } })}</option>
        </select>
      </div>
      {#if drawing}
        <button class="btn secondary" on:click={() => TopologyCancel()}>{$_('topology.cancel')}</button>
        <span class="muted">
          {progress ? $_('topology.progress', { values: { walked: progress.walked, pending: progress.pending } }) : $_('topology.walking')}
        </span>
      {:else}
        <button class="btn" on:click={draw} disabled={seeds.length === 0}>
          <Icon name="network" size={14} /> {$_('topology.draw')}
        </button>
      {/if}
    </div>
    {#if depth > 0}
      <p class="hint">{$_('topology.depthHint')}</p>
    {/if}
  {/if}

  {#if graph}
    <div class="summary">
      <span>{$_('topology.summary', { values: { nodes: graph.nodes.length, links: graph.links.length } })}</span>
      <div class="spacer"></div>
      <button class="btn tertiary" on:click={() => { pos = layout(graph, { width: W, height: H }); view = { x: 0, y: 0, scale: 1 }; }}>
        <Icon name="refresh-cw" size={14} /> {$_('topology.relayout')}
      </button>
      <button class="btn tertiary" on:click={exportSvg}><Icon name="download" size={14} /> SVG</button>
    </div>
    {#each graph.warnings as w, i (i)}
      <p class="note warn"><Icon name="triangle-alert" size={14} /> {w}</p>
    {/each}

    {#if graph.nodes.length === 0}
      <p class="empty">{$_('topology.nothing')}</p>
    {:else}
      <div class="canvas">
        <!-- svelte-ignore a11y-no-static-element-interactions -->
        <svg bind:this={svg} viewBox="0 0 {W} {H}" on:wheel={onWheel} on:pointerdown={startPan}>
          <g transform="translate({view.x} {view.y}) scale({view.scale})">
            {#each graph.links as l, i (i)}
              {#if pos[l.a] && pos[l.b]}
                <line class="link" class:one-sided={!l.both} x1={pos[l.a].x} y1={pos[l.a].y} x2={pos[l.b].x} y2={pos[l.b].y}>
                  <title>{label(byId[l.a], $anonMode)} {l.aPort || ''} — {label(byId[l.b], $anonMode)} {l.bPort || ''} ({l.protocol.toUpperCase()})</title>
                </line>
              {/if}
            {/each}
            {#each graph.nodes as n (n.id)}
              {#if pos[n.id]}
                <!-- svelte-ignore a11y-no-static-element-interactions -->
                <g class="node" class:managed={n.managed} class:selected={selected === n.id}
                  transform="translate({pos[n.id].x} {pos[n.id].y})" on:pointerdown={(e) => startNode(e, n.id)}>
                  <circle r="18" />
                  <g transform="translate(-9 -9)"><Icon name={kindIcon(n.kind)} size={18} /></g>
                  <text y="32">{label(n, $anonMode)}</text>
                </g>
              {/if}
            {/each}
          </g>
        </svg>

        {#if selected && byId[selected]}
          <aside class="details">
            <h4>{label(byId[selected], $anonMode)}</h4>
            {#if byId[selected].address}<p class="mono">{address(byId[selected].address, $anonMode)}</p>{/if}
            <p class="muted">
              {byId[selected].kind ? $_(`topology.kind.${byId[selected].kind}`) : $_('topology.kind.unknown')}
              · {byId[selected].managed ? $_('topology.managed') : $_('topology.heard')}
            </p>
            {#if byId[selected].descr}<p class="descr">{byId[selected].descr}</p>{/if}
            <ul>
              {#each linksOf(graph, selected) as link, i (i)}
                <li>
                  <span class="mono">{link.port || '?'}</span> →
                  {label(byId[link.other], $anonMode)} <span class="mono">{link.remotePort || ''}</span>
                </li>
              {/each}
            </ul>
          </aside>
        {/if}
      </div>
      <p class="hint">{$_('topology.help')}</p>
    {/if}
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

  .seeds summary {
    cursor: pointer;
    font-size: 0.85em;
    color: var(--text-color);
    padding-bottom: 6px;
  }

  .seed-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
    max-width: 640px;
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

  .muted,
  .hint {
    color: var(--text-muted);
    font-size: 0.82em;
  }

  .hint {
    margin: 0;
  }

  .summary {
    display: flex;
    align-items: center;
    gap: 12px;
    font-size: 0.9em;
  }

  .spacer {
    flex: 1;
  }

  .note {
    margin: 0;
    font-size: 0.82em;
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .note.warn {
    color: var(--warning-color);
  }

  .canvas {
    position: relative;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--bg-color);
    overflow: hidden;
  }

  svg {
    display: block;
    width: 100%;
    height: auto;
    max-height: calc(100vh - 300px);
    touch-action: none;
    cursor: grab;
  }

  .link {
    stroke: var(--text-muted);
    stroke-width: 2;
  }

  .link.one-sided {
    stroke-dasharray: 5 4;
  }

  .node {
    cursor: pointer;
  }

  .node circle {
    fill: var(--bg-light-color);
    stroke: var(--text-muted);
    stroke-width: 1.5;
    stroke-dasharray: 3 3;
  }

  .node.managed circle {
    stroke: var(--accent-color);
    stroke-dasharray: none;
  }

  .node.selected circle {
    stroke-width: 3;
  }

  .node :global(svg) {
    color: var(--text-color);
  }

  .node text {
    fill: var(--text-color);
    font-size: 12px;
    text-anchor: middle;
    paint-order: stroke;
    stroke: var(--bg-color);
    stroke-width: 3px;
  }

  .details {
    position: absolute;
    top: 10px;
    right: 10px;
    width: 280px;
    max-height: calc(100% - 20px);
    overflow: auto;
    background: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 10px 12px;
    font-size: 0.82em;
  }

  .details h4 {
    margin: 0 0 4px;
  }

  .details p {
    margin: 2px 0;
  }

  .details ul {
    margin: 8px 0 0;
    padding-left: 16px;
  }

  .descr {
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }

  .mono {
    font-family: var(--font-mono, monospace);
  }

  .empty {
    color: var(--text-muted);
    font-size: 0.85em;
    padding: 10px;
  }
</style>
