/**
 * The network map's renderer side: the request, and where each node is drawn.
 * Pure, so tests run it under node.
 */
import { buildSnmpRequest } from './snmpParams.js';
import { getEffectiveSettings, getTargetsAsArray } from './targets.js';

/**
 * What TopologyScan is asked. The devices chosen, each with its own identifiers;
 * every target's own, for a neighbour the walk reaches that is one; and the
 * defaults, for a neighbour that is not.
 */
export function buildTopologyRequest(settings, seeds, depth) {
  const request = (target) => buildSnmpRequest(getEffectiveSettings(settings, target), [target], '');
  return {
    seeds: (seeds || []).filter(Boolean).map(request),
    template: buildSnmpRequest(settings, [], ''),
    known: getTargetsAsArray(settings?.targets || '').map(request),
    depth: Math.max(0, Math.min(3, Number(depth) || 0)),
  };
}

/**
 * Where to draw each node: a force-directed layout (Fruchterman and Reingold),
 * linked nodes pulled together and every pair pushed apart, with a pull towards
 * the centre so that a device with no link stays on the page. Deterministic: the
 * nodes start on a circle in the order Go gave them, so the same map is drawn the
 * same way every time.
 *
 * Returns { [id]: { x, y } } inside width × height, a margin left for the labels.
 */
export function layout(graph, { width = 1000, height = 700, iterations = 300 } = {}) {
  const nodes = graph?.nodes || [];
  const n = nodes.length;
  const pos = {};
  if (n === 0) return pos;
  const margin = 60;
  const w = width - 2 * margin;
  const h = height - 2 * margin;
  const cx = width / 2;
  const cy = height / 2;
  if (n === 1) {
    pos[nodes[0].id] = { x: cx, y: cy };
    return pos;
  }
  const r = Math.min(w, h) / 2;
  nodes.forEach((node, i) => {
    const a = (2 * Math.PI * i) / n;
    pos[node.id] = { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
  });
  const links = (graph.links || []).filter((l) => pos[l.a] && pos[l.b] && l.a !== l.b);
  const k = Math.sqrt((w * h) / n) * 0.75;
  let t = Math.min(w, h) / 8;
  const cool = t / (iterations + 1);
  const ids = nodes.map((node) => node.id);
  for (let it = 0; it < iterations; it++) {
    const disp = {};
    for (const id of ids) disp[id] = { x: 0, y: 0 };
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const a = pos[ids[i]];
        const b = pos[ids[j]];
        let dx = a.x - b.x;
        let dy = a.y - b.y;
        let d = Math.hypot(dx, dy);
        if (d < 0.01) {
          // Two nodes on one spot: part them along a direction their places give.
          dx = (i - j) * 0.1;
          dy = (j - i) * 0.07;
          d = Math.hypot(dx, dy);
        }
        const f = (k * k) / d;
        disp[ids[i]].x += (dx / d) * f;
        disp[ids[i]].y += (dy / d) * f;
        disp[ids[j]].x -= (dx / d) * f;
        disp[ids[j]].y -= (dy / d) * f;
      }
    }
    for (const l of links) {
      const a = pos[l.a];
      const b = pos[l.b];
      const dx = a.x - b.x;
      const dy = a.y - b.y;
      const d = Math.max(Math.hypot(dx, dy), 0.01);
      const f = (d * d) / k;
      disp[l.a].x -= (dx / d) * f;
      disp[l.a].y -= (dy / d) * f;
      disp[l.b].x += (dx / d) * f;
      disp[l.b].y += (dy / d) * f;
    }
    for (const id of ids) {
      const p = pos[id];
      disp[id].x += (cx - p.x) * 0.02 * k / 10;
      disp[id].y += (cy - p.y) * 0.02 * k / 10;
      const d = Math.max(Math.hypot(disp[id].x, disp[id].y), 0.01);
      const step = Math.min(d, t);
      p.x = Math.min(width - margin, Math.max(margin, p.x + (disp[id].x / d) * step));
      p.y = Math.min(height - margin, Math.max(margin, p.y + (disp[id].y / d) * step));
    }
    t = Math.max(t - cool, 0.5);
  }
  return pos;
}

/** The links of one node, with the far end and the ports as seen from it. */
export function linksOf(graph, id) {
  const out = [];
  for (const l of graph?.links || []) {
    if (l.a === id) out.push({ other: l.b, port: l.aPort, remotePort: l.bPort, protocol: l.protocol });
    else if (l.b === id) out.push({ other: l.a, port: l.bPort, remotePort: l.aPort, protocol: l.protocol });
  }
  return out;
}
