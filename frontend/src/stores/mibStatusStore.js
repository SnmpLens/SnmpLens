import { writable } from 'svelte/store';
import { MibFileStatuses } from '../../wailsjs/go/app/App';
import { mibDiagnostics } from './mibStore';
import { mutedRules } from './mutedRules';

// Every MIB file's verdict (MibFileStatuses, pkg/mib/verdict.go): what the
// last load did with it and what the editor's analysis finds in it, joined in
// Go.
//
// One store for the three places that show a verdict — the editor's file
// list, the MIB settings, the import report — so they cannot ask at different
// moments and disagree. Asked again after every load, since a load changes the
// verdicts; the Go side answers from its cache for any file that has not
// changed. Asked again when a rule is muted or unmuted, which changes them too.

let token = 0;
let setter = null;

async function fetchInto(set) {
  const mine = ++token;
  try {
    await mutedRules.ready();
    const result = (await MibFileStatuses()) || [];
    if (mine === token) set(result);
  } catch (e) {
    // The marks stay as they were; the next load asks again.
    console.error('MibFileStatuses failed', e);
  }
}

const inner = writable([], (set) => {
  setter = set;
  // Subscribing runs it once at once, which is the first fetch.
  const stop = mibDiagnostics.subscribe(() => fetchInto(set));
  // The muted rules answer at once too; that first answer is not a change.
  let first = true;
  const stopMuted = mutedRules.subscribe(() => {
    if (first) {
      first = false;
      return;
    }
    fetchInto(set);
  });
  return () => {
    stop();
    stopMuted();
    setter = null;
  };
});

export const mibStatusStore = {
  subscribe: inner.subscribe,
  /** Ask again now — after a save, an import, a rescan. Resolves when answered. */
  refresh() {
    return setter ? fetchInto(setter) : Promise.resolve();
  },
};
