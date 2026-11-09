/**
 * The port mapper's renderer side: the request, and the table a map is read as.
 * Pure, so tests run it under node.
 */
import { buildSnmpRequest } from './snmpParams.js';
import { getEffectiveSettings } from './targets.js';

/**
 * What PortMapScan is asked. Each device is reached as every request to it is —
 * its profile, its own overrides or the defaults — since a switch and the router
 * beside it rarely share a community.
 */
export function buildPortMapRequest(settings, switchTarget, gateways, resolveNames) {
  const request = (target) => buildSnmpRequest(getEffectiveSettings(settings, target), [target], '');
  return {
    switch: request(switchTarget),
    gateways: (gateways || []).filter((g) => g && g !== switchTarget).map(request),
    resolveNames: !!resolveNames,
  };
}

/** A MAC address as its twelve hex digits, whatever it was written with. */
export function macDigits(s) {
  return String(s || '').toLowerCase().replace(/[^0-9a-f]/g, '');
}

function matches(port, station, q, digits) {
  const hay = [port.name, port.descr, port.alias, port.neighbour?.name, port.neighbour?.port,
    station?.mac, ...(station?.ips || []), ...(station?.names || []),
    station?.vlan ? `vlan ${station.vlan}` : '']
    .filter(Boolean).join(' ').toLowerCase();
  if (hay.includes(q)) return true;
  // A MAC typed the way the switch prints it — 0011.2233.4455, 00-11-22-… — is
  // the same MAC.
  return digits.length >= 4 && !!station && macDigits(station.mac).includes(digits);
}

/**
 * The table: one row per station, and one for a port with none. `first` marks
 * a port's first row, where its own cells are drawn. A query keeps the rows it
 * matches; usedOnly keeps the ports that have a station or a neighbour.
 */
export function portRows(map, { query = '', usedOnly = false } = {}) {
  const q = query.trim().toLowerCase();
  const digits = macDigits(q);
  const rows = [];
  for (const port of map?.ports || []) {
    if (usedOnly && port.stations.length === 0 && !port.neighbour) continue;
    const stations = port.stations.length ? port.stations : [null];
    let first = true;
    for (const station of stations) {
      if (q && !matches(port, station, q, digits)) continue;
      rows.push({ key: `${port.ifIndex}/${station ? `${station.vlan}/${station.mac}` : '-'}`, port, station, first });
      first = false;
    }
  }
  return rows;
}

/** The map as CSV rows, a header first: one row per station, every port listed. */
export function csvRows(map) {
  const rows = [['port', 'ifIndex', 'alias', 'status', 'vlan', 'neighbour', 'uplink', 'mac', 'station vlan', 'ip', 'name']];
  for (const p of map?.ports || []) {
    const base = [p.name, p.ifIndex, p.alias || '', p.up ? 'up' : 'down', p.vlan || '',
      p.neighbour ? `${p.neighbour.name} ${p.neighbour.port || ''}`.trim() : '', p.uplink ? 'yes' : ''];
    if (p.stations.length === 0) rows.push([...base, '', '', '', '']);
    for (const s of p.stations) {
      rows.push([...base, s.mac, s.vlan || '', s.ips.join(' '), s.names.join(' ')]);
    }
  }
  return rows;
}
