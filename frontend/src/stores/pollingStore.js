import { writable, get } from 'svelte/store';
import { _ } from 'svelte-i18n';
import {
  MonitorCreateSession,
  MonitorStart,
  MonitorStop,
  MonitorRunning,
  MonitorSaveDataPoints,
  MonitorLoadSessions,
  MonitorLoadSessionData,
  MonitorDeleteSession,
  MonitorAcceptSlow,
  MonitorUpdateConnection,
  PresetBind,
} from '../bridge/App';
import { EventsOn } from '../bridge/runtime';
import { settingsStore } from './settingsStore';
import { notificationStore } from './notifications';
import { buildMonitorConnection } from '../utils/snmpParams';
import { getEffectiveSettings, groupTargets } from '../utils/targets';
import { DEFAULT_REF, changedCredentialRefs, findProfile, profileIdentity } from '../utils/credentialProfiles.js';
import { getState } from '../utils/crypto';

// In-memory scope buffer, per (target x OID) series. The chart draws a sliding
// window over this buffer and lets you travel back through it, so it has to be
// deep enough to browse — decimation keeps drawing cheap, and every point is
// also persisted to SQLite for ranges older than the buffer.
const MAX_DATA_POINTS = 5000;

// One warning per (session, OID) that returns something ungraphable: repeating
// it on every tick would bury the UI under identical toasts.
const warnedNonNumeric = new Set();

/**
 * One sample, in the shape the rest of the interface expects.
 *
 * ONE function because there are two ways in — live samples pushed by the Go
 * scheduler, and stored ones read back on startup — and they had drifted. The
 * live path wrote `value: p.value ?? null`; the restored path wrote `p.value`
 * bare, and Go's `omitempty` leaves that `undefined`. Consumers test for null:
 * `MetricTiles.svelte:106` decides the failing indicator with
 * `lastPoint.value === null`, `MonitorChart.svelte:243` counts a failed sample
 * the same way. So after any restart a restored session stopped showing which
 * of its points had failed — silently, and only for sessions that had been
 * reloaded, which is why it survived.
 *
 * `delta`, `rate` and `responseTimeMs` had drifted the same way. Two places
 * building the same shape is the defect; the fix is that there is one.
 */
export function normalisePoint(p) {
  return {
    target: p.target,
    timestamp: p.timestamp,
    value: p.value ?? null,
    delta: p.delta ?? null,
    rate: p.rate ?? null,
    responseTimeMs: p.responseTimeMs || 0,
    error: p.error || null,
    snmpType: p.snmpType || '',
    oid: p.oid || '',
  };
}

// The poll clock lives in Go.
//
// It used to be a setInterval here, which meant monitoring only ran while the
// window was open: closing it silently stopped every session, and with them the
// thresholds, the event journal and every notification route that depends on
// them. This store is now a view onto a loop that runs whether or not anyone is
// watching — it starts and stops sessions, and receives samples as they happen.
function createPollingStore() {
  const { subscribe, update, set } = writable([]);

  // Samples pushed by the Go scheduler. Arrive whenever a poll completes, at
  // whatever cadence each session was configured with.
  EventsOn('monitor:samples', (payload) => {
    if (!payload || !payload.sessionId || !Array.isArray(payload.points)) return;
    const { sessionId, points } = payload;

    update((sessions) => sessions.map((s) => {
      if (s.id !== sessionId) return s;

      const incoming = points.map(normalisePoint);

      warnAboutUngraphableValues(sessionId, incoming);

      // The buffer is capped PER SERIES, not per session: with 8 curves a
      // shared cap would leave each one only an eighth of the history.
      const cap = MAX_DATA_POINTS * Math.max(1, s.targets.length) * Math.max(1, (s.oids || [s.oid]).length);
      let results = [...s.results, ...incoming];
      if (results.length > cap) results = results.slice(-cap);
      return { ...s, results, running: true };
    }));
  });

  // The poll clock reporting that a session cannot run as fast as it says.
  //
  // Edge-triggered in Go: one message when a session stops keeping up and one
  // when it starts again, never one per tick. The decision is the operator's,
  // so this is kept ON THE SESSION and rendered as a banner rather than raised
  // as a toast — a toast is gone by the time anyone reads it, and what is being
  // asked is a choice, not a notice.
  EventsOn('monitor:overrun', (o) => {
    if (!o || !o.sessionId) return;
    update((sessions) => sessions.map((s) => (
      s.id === o.sessionId ? { ...s, overrun: o.recovered ? null : o } : s
    )));
  });

  // A point with neither a value nor an error came back as a type that cannot
  // be plotted — a string, an OID, an address. Say so once, rather than drawing
  // an empty chart and leaving the user to guess.
  function warnAboutUngraphableValues(sessionId, points) {
    for (const p of points) {
      if (p.value !== null || p.error) continue;
      const key = sessionId + '|' + p.oid;
      if (warnedNonNumeric.has(key)) continue;
      warnedNonNumeric.add(key);
      notificationStore.add(
        get(_)('monitor.nonNumericWarning', { values: { oid: p.oid, type: p.snmpType || '?' } }),
        'warning',
      );
    }
  }

  // `oid` accepts a single OID or a list: a session can watch several at once,
  // each rendered as its own small multiple (different OIDs have different
  // scales, so they must never share one plot).
  //
  // Each target is polled with ITS OWN identifiers — its credential profile,
  // its overrides, or the defaults. This used the global settings for every
  // target, so a device with a profile or a community of its own was asked with
  // the default one and every reading came back an error, looking exactly like
  // an unreachable device. A Go session holds one connection, so targets that
  // authenticate differently get a session each. `snmpVersion` is the version
  // of the targets on the default identifiers; a profile carries its own.
  //
  // Returns the ids of the sessions created: one, unless it had to split.
  async function startPolling(oid, targets, intervalMs, thresholds = null, snmpVersion = 'v2c', name = '') {
    // Deduplicate: the same OID twice would poll twice, draw two identical
    // curves and collide as a key in the channel picker.
    const oidList = [...new Set((Array.isArray(oid) ? oid : [oid]).map((o) => String(o).trim()).filter(Boolean))];
    targets = [...new Set(targets)];
    // Persisted joined so a reloaded session restores the whole list.
    const oidKey = oidList.join(',');

    // thresholds is keyed by OID. Normalise every band and drop the ones that
    // set no bound at all.
    const byOid = {};
    for (const [tOid, t] of Object.entries(thresholds || {})) {
      if (!t) continue;
      const min = t.min !== null && t.min !== undefined && t.min !== '' ? Number(t.min) : null;
      const max = t.max !== null && t.max !== undefined && t.max !== '' ? Number(t.max) : null;
      if (min === null && max === null) continue;
      byOid[tOid] = {
        min,
        max,
        forSeconds: Number(t.forSeconds) || 0,
        alertEnabled: t.alertEnabled !== false,
      };
    }
    const thresholdsPayload = Object.keys(byOid).length ? byOid : null;

    // The connection is persisted WITH the session: a background poll has no
    // renderer to ask for it, and after a restart there is no renderer at all.
    const settings = get(settingsStore);
    const groups = groupTargets({ ...settings, snmpVersion }, targets);
    const split = groups.length > 1;
    const t = get(_);
    const baseName = (name || '').trim();
    const ids = [];

    for (const group of groups) {
      const effective = group.effectiveSettings;
      const version = effective.snmpVersion || snmpVersion;
      const conn = buildMonitorConnection({ ...effective, snmpVersion: version });
      const sessionName = split
        ? [baseName, groupLabel(group, settings, t)].filter(Boolean).join(' · ')
        : baseName;

      let id;
      try {
        id = await MonitorCreateSession(oidKey, group.targets, intervalMs, version, thresholdsPayload, sessionName, conn);
      } catch (e) {
        notificationStore.add(String(e), 'error');
        throw e;
      }

      // Built through the SAME function as a session restored from Go. It used
      // to be a second object literal here, and it had already drifted: it
      // never set needsConnection, so the field consumers test was simply
      // absent on every session created in this window and present on every
      // restored one.
      update((sessions) => [...sessions, {
        ...sessionFromBackend({
          id,
          name: sessionName,
          oid: oidKey,
          targets: group.targets,
          intervalMs,
          snmpVersion: version,
          startedAt: new Date().toISOString(),
          thresholds: thresholdsPayload || {},
          conn,
        }),
        running: true,
      }]);

      try {
        await MonitorStart(id);
      } catch (e) {
        notificationStore.add(String(e), 'error');
        markRunning(id, false);
      }
      ids.push(id);
    }

    if (split) {
      notificationStore.add(t('profiles.pollingSplit', { values: { count: ids.length } }), 'info');
    }
    return ids;
  }

  // What tells the sessions of one split apart: the profile's name, the
  // default identifiers, or the targets' own.
  function groupLabel(group, settings, t) {
    const ref = group.effectiveSettings.credentialRef;
    if (ref === DEFAULT_REF) return t('profiles.defaultShort');
    const profile = findProfile(settings, ref);
    return profile ? profile.name : t('profiles.customShort');
  }

  function markRunning(sessionId, running) {
    update((sessions) => sessions.map((s) => (s.id === sessionId ? { ...s, running } : s)));
  }

  async function resumeSession(sessionId) {
    markRunning(sessionId, true);
    try {
      await MonitorStart(sessionId);
    } catch (e) {
      notificationStore.add(String(e), 'error');
      markRunning(sessionId, false);
    }
  }

  async function stopPolling(sessionId) {
    markRunning(sessionId, false);
    try {
      await MonitorStop(sessionId);
    } catch (e) {
      console.warn('Failed to stop session:', e);
    }
  }

  async function removeSession(sessionId) {
    try {
      await MonitorStop(sessionId);
    } catch (e) {
      console.warn('Failed to stop session before deleting it:', e);
    }
    update((sessions) => {
      // Keys are `sessionId|oid`, so drop every key belonging to the session.
      for (const key of [...warnedNonNumeric]) {
        if (key.startsWith(sessionId + '|')) warnedNonNumeric.delete(key);
      }
      return sessions.filter((s) => s.id !== sessionId);
    });
    MonitorDeleteSession(sessionId).catch((e) => console.warn('Failed to delete session:', e));
  }

  async function stopAll() {
    const ids = get({ subscribe }).filter((s) => s.running).map((s) => s.id);
    update((sessions) => sessions.map((s) => ({ ...s, running: false })));
    await Promise.allSettled(ids.map((id) => MonitorStop(id)));
  }

  /**
   * One session, in the shape the rest of the interface expects.
   *
   * ONE function, for the same reason normalisePoint is one: there are two ways
   * a session arrives from Go — restored at startup, and handed back by
   * PresetBind — and two builders of the same object is the defect, not the
   * symptom. Every consumer reads `oids`, `interval` and `needsConnection`, and
   * a second copy that forgets one of them produces a session that renders
   * almost correctly.
   */
  function sessionFromBackend(s, results = []) {
    const oids = (s.oid || '').split(',').map((o) => o.trim()).filter(Boolean);
    return {
      id: s.id,
      name: s.name || '',
      oid: oids[0] || '',
      oids,
      targets: s.targets || [],
      interval: s.intervalMs,
      snmpVersion: s.snmpVersion,
      results,
      running: false,
      startedAt: s.startedAt,
      thresholds: s.thresholds || {},
      // A session stored before the connection was persisted cannot be polled
      // from Go; the UI offers to re-arm it with the current settings rather
      // than failing silently.
      needsConnection: !s.conn,
      // Which identifiers the session was built from — 'default', a credential
      // profile's id, or '' — so it can follow them when they change; and how
      // it reaches its targets, which following them must not change. Read
      // from the stored connection, which holds an id and nothing secret. The
      // connection itself is NOT kept: the one startPolling passes in still
      // carries the community and the passphrases.
      credentialRef: s.conn?.profile || '',
      transport: s.conn ? { port: s.conn.port, timeoutSec: s.conn.timeoutSec, retries: s.conn.retries } : null,
      // The dashboard layout this session was bound with, or null. A SNAPSHOT:
      // Go took it when the preset was bound, so editing the file afterwards
      // changes nothing here.
      preset: s.preset || null,
      // Declared from the start rather than attached when the first report
      // arrives, so a consumer testing it sees null on a fresh session instead
      // of undefined — and so both ways in produce the same object.
      overrun: null,
    };
  }

  // Load persisted sessions, then ask Go which ones are actually polling. That
  // second question matters: with the window closed the scheduler kept running,
  // so the database's `active` flag is a record of intent while MonitorRunning
  // is the truth.
  async function initFromBackend() {
    try {
      const sessions = await MonitorLoadSessions();
      if (!sessions || sessions.length === 0) {
        await migrateLegacyData();
        return;
      }
      const loaded = [];
      for (const s of sessions) {
        let results = [];
        try {
          const points = await MonitorLoadSessionData(s.id, MAX_DATA_POINTS * (s.targets?.length || 1));
          results = (points || []).map(normalisePoint);
        } catch (e) {
          console.warn('Failed to load session data:', e);
        }
        loaded.push(sessionFromBackend(s, results));
      }
      set(loaded);
    } catch (e) {
      console.warn('Failed to load sessions from backend:', e);
      return;
    }

    await reconcileWithScheduler();
  }

  // Reflect what the Go scheduler is really doing, and resume anything the user
  // asked to have resumed.
  async function reconcileWithScheduler() {
    let running = [];
    try {
      running = (await MonitorRunning()) || [];
    } catch (e) {
      console.warn('Failed to read the running sessions:', e);
    }
    const live = new Set(running);
    update((sessions) => sessions.map((s) => ({ ...s, running: live.has(s.id) })));

    const settings = get(settingsStore);
    if (!settings.polling?.autoResume) return;
    for (const s of get({ subscribe })) {
      if (!s.running && !s.needsConnection && s.results.length > 0) {
        resumeSession(s.id);
      }
    }
  }

  // One-time migration from localStorage.
  async function migrateLegacyData() {
    const stored = localStorage.getItem('pollingHistory');
    if (!stored) return;
    try {
      const legacySessions = JSON.parse(stored);
      if (!Array.isArray(legacySessions) || legacySessions.length === 0) {
        localStorage.removeItem('pollingHistory');
        return;
      }
      const settings = get(settingsStore);
      for (const ls of legacySessions) {
        try {
          const id = await MonitorCreateSession(
            ls.oid, ls.targets, ls.interval, ls.snmpVersion || 'v2c', ls.thresholds || null, ls.name || '',
            buildMonitorConnection({ ...settings, snmpVersion: ls.snmpVersion || 'v2c' }),
          );
          if (ls.results && ls.results.length > 0) {
            await MonitorSaveDataPoints(ls.results.map((r) => ({
              sessionId: id,
              target: r.target,
              timestamp: r.timestamp,
              value: r.value,
              delta: r.delta,
              rate: r.rate,
              responseTimeMs: r.responseTimeMs || 0,
              error: r.error || '',
              snmpType: r.snmpType || '',
              oid: r.oid || '',
            })));
          }
        } catch (e) {
          console.warn('Failed to migrate session:', e);
        }
      }
      localStorage.removeItem('pollingHistory');
      await initFromBackend();
    } catch (e) {
      console.warn('Legacy migration failed:', e);
      localStorage.removeItem('pollingHistory');
    }
  }

  // Deferred initialization
  setTimeout(initFromBackend, 300);

  // The operator's answer: keep the cadence I chose. Applied optimistically so
  // the banner changes the moment it is clicked — Go has already decided, and
  // the call cannot be refused.
  async function acceptSlow(sessionId) {
    update((sessions) => sessions.map((s) => (
      s.id === sessionId && s.overrun
        ? { ...s, overrun: { ...s.overrun, accepted: true, effectiveMs: s.overrun.intervalMs } }
        : s
    )));
    try {
      await MonitorAcceptSlow(sessionId);
    } catch (e) {
      console.error('MonitorAcceptSlow failed', e);
    }
  }

  /**
   * Take a session Go has just created — PresetBind — into the store.
   *
   * It is already stored and already polling by the time this runs, so this
   * adds no session and starts nothing: it makes the window show what the poll
   * clock is doing. Idempotent, because a caller that retries after a slow
   * bridge call must not produce two rows for one monitoring.
   */
  function adoptSession(s) {
    if (!s || !s.id) return null;
    const shaped = { ...sessionFromBackend(s), running: true };
    update((sessions) => (
      sessions.some((x) => x.id === s.id)
        ? sessions.map((x) => (x.id === s.id ? { ...shaped, results: x.results } : x))
        : [...sessions, shaped]
    ));
    return shaped;
  }

  /**
   * Bind a preset to one equipment: Go creates the session, stores it and
   * starts it, and this takes the result into the store.
   *
   * The connection comes from THAT TARGET's effective settings — its profile,
   * its overrides or the defaults — never from the global ones: using the
   * global community against a device that has its own means every reading is
   * an error, reported far from the cause and looking like an unreachable
   * device.
   */
  async function bindPreset(file, address, settings) {
    const effective = getEffectiveSettings(settings, address);
    const snmpVersion = effective.snmpVersion || 'v2c';
    const conn = buildMonitorConnection({ ...effective, snmpVersion });
    const session = await PresetBind(file, address, snmpVersion, conn);
    return adoptSession(session);
  }

  /**
   * Make every session follow the identifiers it was built from, after the
   * settings changed.
   *
   * A session is a SNAPSHOT of its connection — Go keeps its credentials in the
   * OS store and polls with them windowless — so rotating a profile's
   * passphrase used to leave every session built from that profile failing
   * authentication until somebody rebound it by hand. Each session whose
   * profile, or the default identifiers, changed now gets the new credentials
   * and keeps its own transport: what changed is who it says it is, not how it
   * reaches the device. A running session is restarted by Go to take them.
   *
   * Only while the stored credentials are open: with a locked keychain the
   * passphrases are blank in memory, and this would replace working sessions'
   * credentials with nothing.
   *
   * @returns {Promise<number>} how many sessions were updated
   */
  async function followCredentials(before, after) {
    if (getState() !== 'ok') return 0;
    const refs = changedCredentialRefs(before, after);
    if (refs.size === 0) return 0;

    let updated = 0;
    for (const s of get({ subscribe })) {
      if (!s.credentialRef || !refs.has(s.credentialRef) || !s.transport) continue;

      let identity;
      if (s.credentialRef === DEFAULT_REF) {
        // The defaults carry no version of their own — it is picked when a
        // session starts — so the session keeps the one it has.
        identity = { snmpVersion: s.snmpVersion, community: after.community, v3: after.v3 };
      } else {
        const profile = findProfile(after, s.credentialRef);
        if (!profile) continue;
        identity = profileIdentity(profile);
      }

      const conn = {
        ...buildMonitorConnection({ ...after, ...identity, credentialRef: s.credentialRef }),
        port: s.transport.port,
        timeoutSec: s.transport.timeoutSec,
        retries: s.transport.retries,
      };
      try {
        await MonitorUpdateConnection(s.id, identity.snmpVersion, conn);
        updated++;
        update((sessions) => sessions.map((x) => (x.id === s.id ? { ...x, snmpVersion: identity.snmpVersion } : x)));
      } catch (e) {
        notificationStore.add(String(e), 'error');
      }
    }

    if (updated > 0) {
      notificationStore.add(get(_)('profiles.sessionsUpdated', { values: { count: updated } }), 'info');
    }
    return updated;
  }

  return {
    subscribe,
    acceptSlow,
    adoptSession,
    bindPreset,
    followCredentials,
    startPolling,
    resumeSession,
    stopPolling,
    removeSession,
    stopAll,
    reconcileWithScheduler,
  };
}

export const pollingStore = createPollingStore();
