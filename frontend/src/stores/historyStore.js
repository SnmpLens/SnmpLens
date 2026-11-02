import { writable } from 'svelte/store';
import {
  SaveHistoryEntry,
  LoadHistory,
  DeleteHistoryEntry,
  DeleteHistoryEntries,
  ClearHistory as ClearHistoryBackend,
  ImportHistoryEntries,
} from '../bridge/App';

// History is persisted in SQLite (via the Go backend) — not localStorage.
// The store keeps an in-memory mirror (newest first) so every reactive
// consumer stays synchronous; writes are mirrored to SQLite fire-and-forget.
const LEGACY_KEY = 'snmpHistory';
const LEGACY_BACKUP_KEY = 'snmpHistory_migrated';
const MAX_HISTORY_ENTRIES = 2000;

// Monotonic, collision-free id (Date.now()+Math.random() loses precision at
// millisecond magnitudes and can collide within the same tick).
let idSeq = 0;
function nextId() {
  return `${Date.now()}-${idSeq++}`;
}

function createHistoryStore() {
  const { subscribe, set, update } = writable([]);

  // Keep a plain snapshot so export()/import() can read the current value
  // without an async get().
  let current = [];
  subscribe((v) => { current = v; });

  // One-time migration of any pre-SQLite history sitting in localStorage.
  async function migrateLegacy() {
    const legacy = localStorage.getItem(LEGACY_KEY);
    if (!legacy) return [];
    try {
      const parsed = JSON.parse(legacy);
      if (!Array.isArray(parsed) || parsed.length === 0) {
        localStorage.removeItem(LEGACY_KEY);
        return [];
      }
      // Normalize ids to strings (SQLite primary key is TEXT).
      const normalized = parsed.map((e) => ({ ...e, id: String(e.id) }));
      await ImportHistoryEntries(normalized);
      // Keep a backup and drop the live key so we never re-import.
      localStorage.setItem(LEGACY_BACKUP_KEY, legacy);
      localStorage.removeItem(LEGACY_KEY);
      return normalized;
    } catch (e) {
      console.error('History migration failed:', e);
      return [];
    }
  }

  // Load persisted history from the backend (called once on app startup).
  async function init() {
    try {
      let entries = await LoadHistory();
      if (!Array.isArray(entries)) entries = [];
      if (entries.length === 0) {
        entries = await migrateLegacy();
      }
      // An SNMP operation may have run during the async load: merge any
      // in-memory entries added meanwhile (newest first, deduped by id) so
      // they aren't clobbered by set().
      if (current.length > 0) {
        const loadedIds = new Set(entries.map((e) => e.id));
        const pending = current.filter((e) => !loadedIds.has(e.id));
        if (pending.length > 0) entries = [...pending, ...entries];
      }
      set(entries);
    } catch (e) {
      console.error('Failed to load history from backend:', e);
      // Fallback: keep working from localStorage this session. Depending on
      // whether migration already ran, the data is under the live key or the
      // post-migration backup key.
      try {
        const raw = localStorage.getItem(LEGACY_KEY) || localStorage.getItem(LEGACY_BACKUP_KEY);
        set(raw ? JSON.parse(raw) : []);
      } catch {
        set([]);
      }
    }
  }

  return {
    subscribe,
    init,

    // Add a new history entry (in-memory immediately, persisted async).
    add: (entry) => {
      const newEntry = {
        id: nextId(),
        timestamp: new Date().toISOString(),
        ...entry,
      };
      update((history) => [newEntry, ...history].slice(0, MAX_HISTORY_ENTRIES));
      SaveHistoryEntry(newEntry).catch((e) => console.error('History save failed:', e));
    },

    // Clear all history.
    clear: () => {
      set([]);
      ClearHistoryBackend().catch((e) => console.error('History clear failed:', e));
    },

    // Remove a single entry by id.
    remove: (id) => {
      update((history) => history.filter((entry) => entry.id !== id));
      DeleteHistoryEntry(id).catch((e) => console.error('History delete failed:', e));
    },

    // Remove several entries by id in one batch.
    removeMany: (ids) => {
      const idSet = new Set(ids);
      update((history) => history.filter((entry) => !idSet.has(entry.id)));
      DeleteHistoryEntries(ids).catch((e) => console.error('History batch delete failed:', e));
    },

    // Export history as JSON. Pass a subset (e.g. filtered/selected) to export
    // only those entries; defaults to the whole history.
    export: (entries) => JSON.stringify(entries ?? current, null, 2),

    // Import history from a JSON string; replaces the current history with
    // exactly the imported entries (matching the previous localStorage behavior).
    import: async (jsonString) => {
      try {
        const imported = JSON.parse(jsonString);
        if (!Array.isArray(imported)) return false;
        const normalized = imported.map((e) => ({
          ...e,
          id: e.id != null ? String(e.id) : nextId(),
        }));
        await ClearHistoryBackend();
        await ImportHistoryEntries(normalized);
        const entries = await LoadHistory();
        set(Array.isArray(entries) ? entries : normalized);
        return true;
      } catch (error) {
        console.error('Failed to import history:', error);
        return false;
      }
    },
  };
}

export const historyStore = createHistoryStore();
