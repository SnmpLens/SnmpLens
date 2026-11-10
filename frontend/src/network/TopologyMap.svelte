<script>
  import { onDestroy, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import TargetChips from './TargetChips.svelte';
  import { TopologyScan, TopologyCancel } from '../../wailsjs/go/app/App';
  import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
  import { settingsStore } from '../stores/settingsStore';
  import { notificationStore } from '../stores/notifications';
  import { getTargetsAsArray } from '../utils/targets';
  import { anonMode, anonymizeIp, anonymizeHost } from '../utils/anonymize';
  import { downloadFile } from '../utils/csv';
  import { buildTopologyRequest, layout, linksOf } from '../utils/topology';
  import { fillHeight, afterLayout } from '../utils/fillHeight';

  // The drawing is laid out at the size of its box, one unit a pixel, so that a
  // label is the size it is written at; these are only what it starts from.
  let W = 1000;
  let H = 640;
  const ICONS = { switch: 'network', router: 'route', ap: 'wifi', phone: 'phone', host: 'server' };

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
      await tick();
      await afterLayout();
      relayout();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      drawing = false;
      progress = null;
    }
  }

  function relayout() {
    if (svg && svg.clientWidth > 0 && svg.clientHeight > 0) {
      W = svg.clientWidth;
      H = svg.clientHeight;
    }
    pos = layout(graph, { width: W, height: H });
    view = { x: 0, y: 0, scale: 1 };
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

  // Panning and zooming the drawing, and moving a node, with the pointer: read
  // through the drawing's own transform, because the box is as tall as the window
  // allows and the drawing is centred in it, so the box's edges are not the
  // viewBox's.
  function svgPoint(e) {
    const m = svg.getScreenCTM();
    if (!m) return { x: 0, y: 0 };
    const pt = svg.createSVGPoint();
    pt.x = e.clientX;
    pt.y = e.clientY;
    const p = pt.matrixTransform(m.inverse());
    return { x: p.x, y: p.y };
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
    <div class="empty-state"><p>{$_('topology.noTargets')}</p></div>
  {:else}
    <div class="setup">
      <label class="row-label" for="topology-depth">{$_('topology.depth')}</label>
      <div class="row-field">
        <select id="topology-depth" bind:value={depth} disabled={drawing}>
          <option value={0}>{$_('topology.depthNone')}</option>
          <option value={1}>{$_('topology.depthHops', { values: { count: 1 } })}</option>
          <option value={2}>{$_('topology.depthHops', { values: { count: 2 } })}</option>
          <option value={3}>{$_('topology.depthHops', { values: { count: 3 } })}</option>
        </select>
        <div class="actions">
          {#if drawing}
            <span class="progress">
              <span class="spin"><Icon name="loader-circle" size={14} /></span>
              {progress ? $_('topology.progress', { values: { walked: progress.walked, pending: progress.pending } }) : $_('topology.walking')}
            </span>
            <button class="btn secondary" on:click={() => TopologyCancel()}>
              <Icon name="square" size={12} /> {$_('topology.cancel')}
            </button>
          {:else}
            <button class="btn" on:click={draw} disabled={seeds.length === 0}>
              <Icon name="network" size={14} /> {$_('topology.draw')}
            </button>
          {/if}
        </div>
        {#if depth > 0}
          <span class="field-hint wide">{$_('topology.depthHint')}</span>
        {/if}
      </div>

      <span class="row-label top" id="topology-seeds">{$_('topology.devices')}</span>
      <div class="row-field" role="group" aria-labelledby="topology-seeds">
        <TargetChips {targets} chosen={seeds} disabled={drawing} on:change={(e) => { chosen = e.detail; }} />
      </div>
    </div>
  {/if}

  {#if graph}
    <div class="toolbar">
      <div class="counts">
        <strong>{$_('topology.summary', { values: { nodes: graph.nodes.length, links: graph.links.length } })}</strong>
        <ul class="legend">
          <li><svg viewBox="0 0 16 16" aria-hidden="true"><circle class="managed" cx="8" cy="8" r="6" /></svg>{$_('topology.managed')}</li>
          <li><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" /></svg>{$_('topology.heard')}</li>
          <li><svg viewBox="0 0 22 16" aria-hidden="true"><line x1="1" y1="8" x2="21" y2="8" /></svg>{$_('topology.oneSided')}</li>
        </ul>
      </div>
      <div class="tools">
        <button class="btn tertiary" on:click={relayout}>
          <Icon name="refresh-cw" size={14} /> {$_('topology.relayout')}
        </button>
        <button class="btn tertiary" on:click={exportSvg}><Icon name="download" size={14} /> SVG</button>
      </div>
    </div>
    {#each graph.warnings as w, i (i)}
      <p class="note warn"><Icon name="triangle-alert" size={14} /> {w}</p>
    {/each}

    {#if graph.nodes.length === 0}
      <div class="empty-state"><p>{$_('topology.nothing')}</p></div>
    {:else}
      <div class="canvas">
        <!-- svelte-ignore a11y-no-static-element-interactions -->
        <svg bind:this={svg} viewBox="0 0 {W} {H}" use:fillHeight={{ min: 360, margin: 44 }} on:wheel={onWheel} on:pointerdown={startPan}>
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
                  <text y="34">{label(n, $anonMode)}</text>
                </g>
              {/if}
            {/each}
          </g>
        </svg>

        {#if selected && byId[selected]}
          <aside class="details">
            <header>
              <span class="kind-icon"><Icon name={kindIcon(byId[selected].kind)} size={16} /></span>
              <div>
                <h4>{label(byId[selected], $anonMode)}</h4>
                <p class="muted">
                  {byId[selected].kind ? $_(`topology.kind.${byId[selected].kind}`) : $_('topology.kind.unknown')}
                  · {byId[selected].managed ? $_('topology.managed') : $_('topology.heard')}
                </p>
              </div>
            </header>
            {#if byId[selected].address}<p class="mono">{address(byId[selected].address, $anonMode)}</p>{/if}
            {#if byId[selected].descr}<p class="descr">{byId[selected].descr}</p>{/if}
            <ul>
              {#each linksOf(graph, selected) as link, i (i)}
                <li>
                  <span class="mono">{link.port || '?'}</span>
                  <Icon name="chevron-right" size={12} />
                  <span>{label(byId[link.other], $anonMode)}</span>
                  {#if link.remotePort}<span class="mono muted">{link.remotePort}</span>{/if}
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
    gap: 12px;
  }

  /* The same settings card as the port map's. */
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

  .field-hint.wide {
    flex-basis: 100%;
    margin-top: -6px;
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
    align-items: center;
    gap: 20px;
    flex-wrap: wrap;
    font-size: 0.9em;
  }

  .legend {
    display: flex;
    align-items: center;
    gap: 16px;
    margin: 0;
    padding: 0;
    list-style: none;
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .legend li {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .legend svg {
    width: 16px;
    height: 16px;
    flex: none;
  }

  .legend svg[viewBox="0 0 22 16"] {
    width: 22px;
  }

  .legend circle {
    fill: var(--bg-light-color);
    stroke: var(--text-muted);
    stroke-width: 1.5;
    stroke-dasharray: 2.5 2;
  }

  .legend circle.managed {
    stroke: var(--accent-color);
    stroke-dasharray: none;
  }

  .legend line {
    stroke: var(--text-muted);
    stroke-width: 2;
    stroke-dasharray: 4 3;
  }

  .tools {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .tools .btn {
    height: 32px;
  }

  .note {
    margin: 0;
    font-size: 0.85em;
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

  .canvas > svg {
    display: block;
    width: 100%;
    touch-action: none;
    cursor: grab;
  }

  .hint {
    margin: -4px 0 0;
    color: var(--text-muted);
    font-size: 0.8em;
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
    width: 300px;
    max-height: calc(100% - 20px);
    overflow: auto;
    background: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 12px 14px;
    font-size: 0.85em;
  }

  .details header {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    margin-bottom: 8px;
  }

  .kind-icon {
    display: inline-flex;
    padding: 6px;
    border-radius: 50%;
    border: 1px solid var(--border-color);
    color: var(--text-color);
  }

  .details h4 {
    margin: 0 0 2px;
    font-size: 1.05em;
  }

  .details p {
    margin: 4px 0;
  }

  .details ul {
    margin: 10px 0 0;
    padding: 8px 0 0;
    list-style: none;
    border-top: 1px solid var(--border-color);
  }

  .details li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 3px 0;
    color: var(--text-color);
  }

  .details li :global(svg) {
    color: var(--text-muted);
    flex: none;
  }

  .muted {
    color: var(--text-muted);
  }

  .descr {
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }

  /* A family named first: the generic one alone is shrunk by the browser. */
  .mono {
    font-family: 'Courier New', monospace;
  }
</style>
