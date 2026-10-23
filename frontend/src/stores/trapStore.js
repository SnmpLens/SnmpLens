import { writable, get } from 'svelte/store';
import { _ } from 'svelte-i18n';
import { StartTrapListener, StopTrapListener, UpdateTrapUsers, GetOidDetails } from '../../wailsjs/go/app/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { notificationStore } from './notifications';
import { settingsStore, settingsReady } from './settingsStore';
import { buildTrapListenerRequest } from '../utils/snmpParams';
import { getState } from '../utils/crypto';
import { sendNativeNotification } from '../utils/nativeNotify';
import { trapOidText } from '../utils/notifyText';
import { createBurstGate } from '../utils/burst';

// What a trap flood used to do to the window that is supposed to show it.
//
// Every received trap raised a toast when the panel was hidden, sent an OS
// notification when the window was unfocused, and — for the OS notification —
// awaited GetOidDetails, a bridge call into pkg/mib that takes the
// package-level EXCLUSIVE gosmi mutex. Nothing rate-limited any of the three,
// so an unauthenticated remote sender chose how often the renderer acquired
// the lock that every MIB operation in the product needs.
//
// Leading edge plus a trailing summary: the first trap in a window is reported
// at once, and the rest arrive as "and N more". Only the leading one performs
// the OID lookup, which is what takes the rate control down to the lock.
//
// Three seconds for the in-app toast, which is cheap and lives for five; ten
// for the OS notification, which the desktop queues and the operator dismisses
// by hand.
const trapToastGate = createBurstGate({
  windowMs: 3000,
  onSummary: (count) => {
    notificationStore.add(get(_)('traps.moreTrapsReceived', { values: { count } }), 'info');
  },
});

const nativeTrapGate = createBurstGate({
  windowMs: 10000,
  onSummary: (count) => {
    sendNativeNotification('SnmpLens', get(_)('traps.moreTrapsReceived', { values: { count } }));
  },
});

const STORAGE_KEY = 'trapHistory';
const MAX_TRAPS_DEFAULT = 1000;

// Stable, unique id per trap — used as the {#each} key so expanding a trap
// survives new traps arriving (which are prepended, shifting every index).
let trapSeq = 0;
function nextTrapId() {
  return `t${Date.now().toString(36)}-${trapSeq++}`;
}

function loadPersistedTraps() {
  const settings = get(settingsStore);
  if (settings.traps?.persist) {
    try {
      const stored = localStorage.getItem(STORAGE_KEY);
      if (stored) {
        // Ensure older persisted traps (saved before ids existed) get one.
        return JSON.parse(stored).map(t => (t && t.id ? t : { ...t, id: nextTrapId() }));
      }
    } catch (e) {
      console.warn('Failed to load persisted traps:', e);
    }
  }
  return [];
}

// Traps are now journalled in SQLite by the Go listener, BEFORE anything is
// emitted to the webview — so they survive a closed window and a restart, which
// localStorage never did. This store keeps only a live tail for the Traps
// workbench; the durable record and its search live in the Events tab.
//
// Writing up to 1000 traps back to localStorage on EVERY incoming packet was a
// real cost for a copy that is now redundant, so this is intentionally a no-op.
function persistTraps() {}

function createTrapStore() {
  const initialTraps = loadPersistedTraps();

  const { subscribe, update } = writable({
    isListening: false,
    traps: initialTraps,
    isPanelVisible: false,
    isWindowFocused: true,
    // The SNMPv3 users Go refused last time it was handed them, with its
    // reasons — the Traps tab marks them.
    refused: [],
  });

  // The SNMPv3 users last handed to Go, as JSON, so an unchanged set is not
  // sent again on every save of an unrelated setting.
  let appliedUsers = null;

  // A user Go would not take is kept for the Traps tab and, when it matters,
  // said once by name with Go's reason — never dropped in silence, since a
  // device sending as that user would look exactly like one sending nothing.
  function reportRefused(info, announce = true) {
    update((s) => ({ ...s, refused: info?.refused || [] }));
    if (!announce) return;
    const t = get(_);
    for (const r of info?.refused || []) {
      notificationStore.add(t('profiles.trapUserRefused', { values: { user: r.user, reason: r.reason } }), 'warning');
    }
  }

  async function start() {
    const currentSettings = get(settingsStore);
    const request = buildTrapListenerRequest(currentSettings);

    try {
      const info = await StartTrapListener(request);
      appliedUsers = JSON.stringify(request.users);
      update(store => ({ ...store, isListening: true }));
      const t = get(_);
      notificationStore.add(t('traps.listenerStarted', { values: { port: currentSettings.trapPort } }), 'success');
      reportRefused(info);
    } catch (err) {
      const t = get(_);
      notificationStore.add(t('traps.listenerStartFailed', { values: { error: err } }), 'error');
    }
  }

  async function stop() {
    try {
      await StopTrapListener();
      update(store => ({ ...store, isListening: false }));
      const t = get(_);
      notificationStore.add(t('traps.listenerStopped2'), 'info');
    } catch (err) {
      const t = get(_);
      notificationStore.add(t('traps.listenerStopFailed', { values: { error: err } }), 'error');
    }
  }

  function clearTraps() {
    update(s => {
      const newTraps = [];
      persistTraps(newTraps);
      return { ...s, traps: newTraps };
    });
    const t = get(_);
    notificationStore.add(t('traps.allCleared'), 'info');
  }

  // Listen for incoming traps from the backend
  EventsOn('newTrap', (trap) => {
    const storeState = get(trapStore);

    // Show internal notification if panel is hidden
    if (!storeState.isPanelVisible && trapToastGate.offer()) {
      const t = get(_);
      notificationStore.add(t('traps.newTrapReceived', { values: { type: trap.pduType || 'Trap', source: trap.source } }), 'info');
    }

    // Send native OS notification (Windows toast / macOS / Linux)
    const settings = get(settingsStore);
    // The gate is asked LAST, so a suppressed trap does not consume a window
    // it was never eligible for — and, more to the point, so the GetOidDetails
    // call below happens only for the one trap that is actually reported.
    if (settings.traps?.nativeNotifications && !storeState.isWindowFocused && nativeTrapGate.offer()) {
      (async () => {
        const trapOidVar = trap.variables?.find(v =>
          v.oid === 'snmpTrapOID.0' ||
          v.oid === '.1.3.6.1.6.3.1.1.4.1.0' ||
          v.oid === '1.3.6.1.6.3.1.1.4.1.0' ||
          (v.oid && v.oid.endsWith('.1.6.3.1.1.4.1.0'))
        );
        let body = '';
        // The sender types this varbind; only a real OID is repeated on the desktop.
        const oidValue = trapOidVar ? trapOidText(trapOidVar.value) : '';
        if (oidValue) {
          try {
            const details = await GetOidDetails(oidValue);
            body = details.name ? `${details.name} (${oidValue})` : oidValue;
          } catch {
            body = oidValue;
          }
        }
        sendNativeNotification(
          `SNMP ${trap.pduType || 'Trap'} from ${trap.source}`,
          body || 'Check application for details.'
        );
      })();
    }

    const enrichedTrap = {
      ...trap,
      id: trap.id || nextTrapId(),
      pduType: trap.pduType || 'Trap',
      timestamp: trap.timestamp || new Date().toISOString(),
    };

    update(s => {
      const settings = get(settingsStore);
      const maxCount = settings.traps?.maxCount || MAX_TRAPS_DEFAULT;
      let newTraps = [enrichedTrap, ...s.traps];
      if (newTraps.length > maxCount) {
        newTraps = newTraps.slice(0, maxCount);
      }
      persistTraps(newTraps);
      return { ...s, traps: newTraps };
    });
  });

  // Listen for backend errors
  EventsOn('trapError', (error) => {
    update(store => ({ ...store, isListening: false }));
    const t = get(_);
    notificationStore.add(t('traps.listenerError', { values: { error } }), 'error');
  });

  // The listener's SNMPv3 users follow the settings.
  //
  // Every v3 credential profile is a user the listener accepts, so saving one
  // has to reach a listener that is already running — and one that is not,
  // because Go remembers the set for a listener started at login, before any
  // window exists. Go restarts a running listener only when the set it accepts
  // actually changed.
  //
  // Only once the stored credentials are open, and only while they can be:
  // before that the store holds sealed strings, and with a locked keychain the
  // passphrases are blanked in memory. Pushing either would replace working
  // users with users that authenticate nothing.
  async function syncTrapUsers(settings) {
    if (getState() !== 'ok') return;
    const { users } = buildTrapListenerRequest(settings);
    const key = JSON.stringify(users);
    if (key === appliedUsers) return;
    appliedUsers = key;

    const t = get(_);
    try {
      const info = await UpdateTrapUsers(users);
      if (info?.restarted) {
        notificationStore.add(t('profiles.trapUsersApplied', { values: { count: info.users } }), 'info');
      }
      // Refusals matter to a listener that is running; reporting them on every
      // start of a window whose listener is off would be noise about nothing.
      reportRefused(info, !!(info?.restarted || get(trapStore).isListening));
    } catch (err) {
      update(store => ({ ...store, isListening: false }));
      notificationStore.add(t('traps.listenerError', { values: { error: err } }), 'error');
    }
  }

  settingsReady.then(() => {
    settingsStore.subscribe((s) => { syncTrapUsers(s); });
  });

  return {
    subscribe,
    start,
    stop,
    clearTraps,
    setPanelVisibility: (isVisible) => update(store => ({ ...store, isPanelVisible: isVisible })),
    setWindowFocus: (isFocused) => update(store => ({ ...store, isWindowFocused: isFocused })),
  };
}

export const trapStore = createTrapStore();
