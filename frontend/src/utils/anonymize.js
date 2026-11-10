import { derived } from 'svelte/store';
import { settingsStore } from '../stores/settingsStore';
import { replaceAddresses } from './ipText';

/**
 * Derived store: true when anonymous mode is active.
 */
export const anonMode = derived(settingsStore, $s => !!$s.anonymousMode);

// Stable mappings (reset on page reload — by design)
const ipMap = new Map();
const hostMap = new Map();
let ipCounter = 1;
let hostCounter = 1;

/**
 * Reset all anonymous mappings (useful for testing).
 */
export function resetMappings() {
  ipMap.clear();
  hostMap.clear();
  ipCounter = 1;
  hostCounter = 1;
}

/**
 * Anonymize an IP address with a stable label.
 * Same IP always maps to the same "Device-N" within a session.
 */
export function anonymizeIp(ip) {
  if (!ip) return ip;
  if (!ipMap.has(ip)) {
    ipMap.set(ip, `Device-${ipCounter++}`);
  }
  return ipMap.get(ip);
}

/**
 * Anonymize a hostname with a stable label.
 */
export function anonymizeHost(name) {
  if (!name) return name;
  if (!hostMap.has(name)) {
    hostMap.set(name, `Host-${hostCounter++}`);
  }
  return hostMap.get(name);
}

/**
 * Replace every IP address in free text with its anonymized label.
 *
 * Both families: an IPv6 address in a trap varbind, a description or an error
 * message used to pass through untouched, which is the one thing anonymous
 * mode exists to prevent.
 */
export function anonymizeText(text) {
  return replaceAddresses(text, anonymizeIp);
}

/**
 * Mask a MAC address down to its vendor's prefix: the OUI says who made the card,
 * the rest says which card it is.
 */
export function maskMac(mac) {
  if (!mac) return mac;
  const parts = String(mac).split(':');
  return parts.length === 6 ? [...parts.slice(0, 3), '••', '••', '••'].join(':') : '••:••:••:••:••:••';
}

/**
 * Mask a sensitive string (community, username, etc.).
 */
export function maskString(str) {
  if (!str) return str;
  return '\u2022\u2022\u2022\u2022\u2022\u2022';
}

/**
 * Anonymize a CIDR notation (mask the network part, keep prefix length).
 */
export function anonymizeCidr(cidr) {
  if (!cidr) return cidr;
  const parts = cidr.split('/');
  return anonymizeIp(parts[0]) + (parts[1] ? '/' + parts[1] : '');
}

/**
 * Mask sysDescr (device description that may reveal model/vendor).
 */
export function maskSysDescr(descr) {
  if (!descr) return descr;
  return '[device description]';
}
