/**
 * What the renderer offers on a node of the MIB tree, decided from what Go's
 * tree says of it (pkg/mib Node: mibType and access).
 *
 * Kept here rather than in MibPanel.svelte so that tests/mibcontract.test.mjs
 * can hold it to the tree the MIB engine really produces
 * (pkg/mib/testdata/reference.jsonl): those strings are the engine's, and an
 * engine that spells a kind or an access differently turns an action off with
 * nothing failing — a read-create column reported as "read-create" has no
 * "write" in it.
 */

/** Objects with a value: GET, GETNEXT, and SET when writable. */
export const VALUE_KINDS = ['Scalar', 'Column'];
/** Subtrees: walked, and read in bulk. */
export const WALK_KINDS = ['Table', 'Row', 'Node', 'Group'];
/** Walked as a table. */
export const TABLE_KINDS = ['Table', 'Row'];
/** Definitions with no instance to ask a device for. */
export const INERT_KINDS = ['Notification', 'Compliance', 'Capabilities'];

export function canGet(node) {
  return VALUE_KINDS.includes(node?.mibType);
}

export function canWalk(node) {
  return WALK_KINDS.includes(node?.mibType);
}

export function canWalkAsTable(node) {
  return TABLE_KINDS.includes(node?.mibType);
}

/** The access filter's bucket for a node: read-only, read-write, not-accessible. */
export function normalizeAccess(access) {
  if (!access) return 'unknown';
  const a = access.toLowerCase();
  if (a.includes('write')) return 'read-write';
  if (a === 'readonly' || a === 'read-only') return 'read-only';
  if (a === 'notaccessible' || a === 'not-accessible') return 'not-accessible';
  return a;
}

/** Whether a SET is offered, from the node's access. */
export function isWritable(node) {
  if (!node?.access) return true; // Default to writable if access is not specified
  const access = node.access.toLowerCase();
  return access.includes('write') || access === 'readwrite' || access === 'read-write';
}
