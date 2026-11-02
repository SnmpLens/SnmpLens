import { writable, get } from 'svelte/store';
import {
  EventsQuery,
  EventsCounts,
  EventsAck,
  EventsAckAll,
  EventsDelete,
  EventsClear,
} from '../bridge/App';
import { EventsOn } from '../bridge/runtime';
import { _ } from 'svelte-i18n';
import { notificationStore } from './notifications';
import { settingsStore } from './settingsStore';
import { sendNativeNotification } from '../utils/nativeNotify';

/**
 * The event journal: what the system tells the operator.
 *
 * Deliberately NOT the SNMP query history — that records what the operator
 * asked a device to do. This records what happened to them: traps arriving,
 * thresholds breached, targets going dark. Only this stream is worth
 * forwarding to syslog or email, and only this stream has to keep working
 * with no window open.
 *
 * Unlike historyStore, nothing is mirrored in memory wholesale: the journal is
 * server-paginated with a keyset cursor, because it is written to while it is
 * being read and can hold tens of thousands of rows.
 */
const EMPTY_COUNTS = { unacked: 0, unackedBySeverity: {}, unackedByCategory: {} };

// Severities worth interrupting the operator for. Everything else is recorded
// and visible in the tab, but does not raise a toast.
const NOTIFY_FROM = new Set(['major', 'critical']);
const NOTIFY_COOLDOWN_MS = 30000;
let lastNotifiedAt = 0;

// Alert sound, kept here because this is now the single place an incident is
// announced — Go detects, the journal records, and this notifies.
function playAlertSound() {
  try {
    const ctx = new (window.AudioContext || window.webkitAudioContext)();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.frequency.value = 880;
    osc.type = 'sine';
    gain.gain.value = 0.3;
    osc.start();
    osc.stop(ctx.currentTime + 0.2);
  } catch (e) {
    /* audio is a nicety, never a failure */
  }
}

function announce(ev) {
  if (!ev || ev.acked || !NOTIFY_FROM.has(ev.severity)) return;

  const t = get(_);
  const message = t(ev.titleKey, { values: ev.params || {}, default: '' }) || ev.summary;
  notificationStore.add(message, 'error');

  // The OS notification and the sound are rate-limited so a flapping target
  // cannot spam the desktop; the journal still records every occurrence.
  const now = Date.now();
  if (now - lastNotifiedAt < NOTIFY_COOLDOWN_MS) return;
  lastNotifiedAt = now;

  const settings = get(settingsStore);
  if (settings.monitor?.systemNotifications) {
    sendNativeNotification(t('events.title'), message);
    if (settings.monitor?.alertSound) playAlertSound();
  }
}

function createEventsStore() {
  const { subscribe, update, set } = writable({
    items: [],
    total: 0,
    nextCursor: 0,
    loading: false,
    error: null,
    filter: {},
  });

  const counts = writable(EMPTY_COUNTS);

  async function refreshCounts() {
    try {
      const c = await EventsCounts();
      counts.set(c || EMPTY_COUNTS);
    } catch (e) {
      console.warn('Failed to load event counts:', e);
    }
  }

  /** Load the newest page for a filter, replacing what is displayed. */
  async function load(filter = {}) {
    update((s) => ({ ...s, loading: true, error: null, filter }));
    try {
      const page = await EventsQuery({ ...filter, beforeSeq: 0 });
      set({
        items: page.items || [],
        total: page.total || 0,
        nextCursor: page.nextCursor || 0,
        loading: false,
        error: null,
        filter,
      });
    } catch (e) {
      console.error('Failed to query events:', e);
      update((s) => ({ ...s, loading: false, error: String(e) }));
    }
    refreshCounts();
  }

  /**
   * One row per event id, first occurrence winning.
   *
   * The list feeds a KEYED each, and Svelte throws on a duplicate key rather
   * than rendering something odd — so a tie here is not a cosmetic problem, it
   * is the panel disappearing with an exception in the console.
   */
  function dedupeById(items) {
    const seen = new Set();
    const out = [];
    for (const e of items) {
      if (!e || seen.has(e.id)) continue;
      seen.add(e.id);
      out.push(e);
    }
    return out;
  }

  /** Append the next page. */
  async function loadMore() {
    const state = get({ subscribe });
    if (!state.nextCursor || state.loading) return;
    update((s) => ({ ...s, loading: true }));
    try {
      const page = await EventsQuery({ ...state.filter, beforeSeq: state.nextCursor });
      update((s) => ({
        ...s,
        // The same tie, from the other side: a live event prepended while this
        // page was in flight is in both lists.
        items: dedupeById([...s.items, ...(page.items || [])]),
        total: page.total || s.total,
        nextCursor: page.nextCursor || 0,
        loading: false,
      }));
    } catch (e) {
      console.error('Failed to load more events:', e);
      update((s) => ({ ...s, loading: false, error: String(e) }));
    }
  }

  async function ack(ids) {
    if (!ids || !ids.length) return;
    // Optimistic: the badge should drop the moment the operator clicks.
    update((s) => ({
      ...s,
      items: s.items.map((e) => (ids.includes(e.id) ? { ...e, acked: true } : e)),
    }));
    try {
      await EventsAck(ids);
    } catch (e) {
      console.error('Failed to acknowledge events:', e);
    }
    refreshCounts();
  }

  async function ackAll(filter) {
    try {
      await EventsAckAll(filter || {});
    } catch (e) {
      console.error('Failed to acknowledge all events:', e);
    }
    await load(get({ subscribe }).filter);
  }

  async function remove(ids) {
    if (!ids || !ids.length) return;
    update((s) => ({ ...s, items: s.items.filter((e) => !ids.includes(e.id)) }));
    try {
      await EventsDelete(ids);
    } catch (e) {
      console.error('Failed to delete events:', e);
    }
    refreshCounts();
  }

  async function clear() {
    try {
      await EventsClear();
    } catch (e) {
      console.error('Failed to clear the journal:', e);
    }
    await load(get({ subscribe }).filter);
  }

  // How many events the LIVE TAIL may hold. loadMore is a deliberate user
  // action and is not bounded by this; what is bounded is growth driven from
  // the network.
  const MAX_LIVE_ITEMS = 2000;

  /**
   * Live tail. The Go side persists an event and THEN emits this — the row
   * exists whether or not a window was listening, so a missed emit costs a
   * refresh, never a record.
   */
  // Registered ONCE, whoever asks.
  //
  // Two callers asked: App.svelte at startup, because the badge has to count
  // with the tab closed, and EventsPanel's onMount. The panel is mounted with
  // {#if activeTab === …}, so onMount ran again on every visit to the tab —
  // each registering ANOTHER handler on the same runtime event. Every live
  // event was then prepended once per handler, and Svelte's keyed each threw
  // `each_key_duplicate` and took the panel down. Visit the tab five times and
  // an event arrived six times.
  //
  // This is the accumulating-listener shape lifecycle.test.mjs exists for, one
  // level down: the leak is not in the component, it is in what the component
  // asks the store to do on every mount.
  let listening = false;

  function listen() {
    if (listening) return;
    listening = true;
    EventsOn('event:new', (ev) => {
      announce(ev);
      counts.update((c) => ({
        ...c,
        unacked: (c.unacked || 0) + (ev && ev.acked ? 0 : 1),
      }));
      update((s) => {
        // Only prepend when looking at the newest page of a matching filter;
        // otherwise the list would silently disagree with its own filter.
        if (s.nextCursor && s.items.length === 0) return s;
        if (!matchesFilter(ev, s.filter)) return s;

        // Capped, and the CURSOR IS REPAIRED with it. The live tail had no
        // ceiling, and it is fed at whatever rate the network sends traps —
        // the Go side absorbs 2000 a second by design, and every one of them
        // is emitted here.
        //
        // Truncating the tail alone would open a hole: nextCursor is a
        // beforeSeq, so paging would resume below the events just dropped and
        // silently skip them. Setting it to the seq of the last kept item
        // means "load more" continues exactly where the visible list ends,
        // which is what the cursor already meant.
        // Deduplicated, because one handler is not the only way the same
        // event can arrive twice: load() awaits EventsQuery, and the row was
        // persisted BEFORE the emit — so a page fetched while an emit is in
        // flight can already contain the event the handler is about to
        // prepend. Nothing orders those two, and a keyed each does not
        // tolerate the tie.
        if (s.items.some((e) => e.id === ev.id)) return s;

        const items = [ev, ...s.items];
        if (items.length <= MAX_LIVE_ITEMS) {
          return { ...s, items, total: s.total + 1 };
        }
        const kept = items.slice(0, MAX_LIVE_ITEMS);
        return {
          ...s,
          items: kept,
          total: s.total + 1,
          nextCursor: kept[kept.length - 1].seq || s.nextCursor,
        };
      });
    });
  }

  return { subscribe, counts, load, loadMore, ack, ackAll, remove, clear, refreshCounts, listen };
}

// Mirror of the SQL filter, for the live-tail path only. Kept intentionally
// small: the authoritative filtering happens in SQL.
function matchesFilter(ev, filter) {
  if (!ev || !filter) return true;
  if (filter.categories && filter.categories.length && !filter.categories.includes(ev.category)) return false;
  if (filter.unackedOnly && ev.acked) return false;
  if (filter.sessionId && ev.sessionId !== filter.sessionId) return false;
  return true;
}

export const eventsStore = createEventsStore();
export const eventCounts = eventsStore.counts;
