import { writable } from 'svelte/store';

// A request to show a node in the MIB tree, from anywhere — the MIB editor's
// "show in the MIB tree". The sibling of tabRequest. Each request carries its
// own stamp and is never cleared: two readers act on it (the tree reveals the
// node, the shell opens the panel if it was folded away), and one clearing it
// could hide it from the other.
export const treeRequest = writable(null);

let seq = 0;

/** Ask the tree to select, expand to and scroll to the node at oid. */
export function requestTreeNode(oid, name = '') {
  treeRequest.set({ oid, name, seq: ++seq });
}
