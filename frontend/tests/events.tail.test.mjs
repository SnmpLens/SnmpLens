// The live event tail, and the two ways one event became two rows.
//
// The list feeds a KEYED {#each}, so a duplicate id is not a cosmetic problem:
// Svelte throws `each_key_duplicate` and the whole panel disappears with an
// exception in the console. Reported from the running application, on the very
// first event.
//
// Both causes are here because they are different failures that look identical
// on screen: a second HANDLER, and a page that already contains the event the
// handler is about to prepend.
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import esbuild from 'esbuild';

const page = { items: [], total: 0, nextCursor: 0 };
const handlers = [];
let queries = 0;

globalThis.__stub = {
  EventsQuery: async () => { queries++; return JSON.parse(JSON.stringify(page)); },
  EventsCounts: async () => ({ unacked: 0, total: 0 }),
  EventsAck: async () => {},
  EventsAckAll: async () => {},
  EventsDelete: async () => {},
  EventsClear: async () => {},
  EventsOn: (name, fn) => { handlers.push({ name, fn }); },
  SettingsKeyStatus: async () => ({ backend: 'test', available: true, hasKey: false }),
  SettingsSeal: async (v) => v,
  SettingsOpen: async (v) => v,
  SettingsAdoptKey: async () => {},
  SettingsForgetKey: async () => {},
};

const stub = `const s = globalThis.__stub;
export const EventsQuery = (...a) => s.EventsQuery(...a);
export const EventsCounts = (...a) => s.EventsCounts(...a);
export const EventsAck = (...a) => s.EventsAck(...a);
export const EventsAckAll = (...a) => s.EventsAckAll(...a);
export const EventsDelete = (...a) => s.EventsDelete(...a);
export const EventsClear = (...a) => s.EventsClear(...a);
export const EventsOn = (...a) => s.EventsOn(...a);
export const ListMibFiles = async () => [];
export const SettingsKeyStatus = (...a) => s.SettingsKeyStatus(...a);
export const SettingsSeal = (...a) => s.SettingsSeal(...a);
export const SettingsOpen = (...a) => s.SettingsOpen(...a);
export const SettingsAdoptKey = (...a) => s.SettingsAdoptKey(...a);
export const SettingsForgetKey = (...a) => s.SettingsForgetKey(...a);
export const SendNotification = async () => {};
export const NotificationsAvailable = async () => false;`;

const dir = mkdtempSync(join(tmpdir(), 'snmplens-events-'));
writeFileSync(join(dir, 'stub.js'), stub);

const entry = new URL('../src/stores/eventsStore.js', import.meta.url).pathname
  .replace(/^[/]([A-Za-z]:)/, '$1');
const out = join(dir, 'bundle.mjs');
await esbuild.build({
  entryPoints: [entry],
  bundle: true, format: 'esm', outfile: out, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /bridge[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
      b.onResolve({ filter: /bridge[/]runtime$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});

const mem = new Map();
globalThis.localStorage = {
  getItem: (k) => (mem.has(k) ? mem.get(k) : null),
  setItem: (k, v) => mem.set(k, String(v)),
  removeItem: (k) => mem.delete(k),
};
globalThis.window = { addEventListener() {}, matchMedia: () => ({ matches: false, addEventListener() {} }) };
if (!globalThis.navigator) {
  Object.defineProperty(globalThis, 'navigator', { value: { language: 'en' }, configurable: true });
}

const { eventsStore } = await import(pathToFileURL(out).href);
const { get } = await import('svelte/store');

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};
const tick = () => new Promise((r) => setTimeout(r, 0));

const event = (id, seq) => ({
  id, seq, ts: '2026-09-09T10:00:00Z', category: 'system', kind: 'system.info',
  severity: 'info', source: '', summary: 'x', acked: false,
});

/* --- one handler, however many callers ------------------------------------ */
{
  eventsStore.listen();
  eventsStore.listen();
  eventsStore.listen();

  const tails = handlers.filter((h) => h.name === 'event:new');
  check('the live tail is registered once whoever asks', tails.length === 1,
    `${tails.length} handler(s)`);
}

const emit = (ev) => handlers.filter((h) => h.name === 'event:new').forEach((h) => h.fn(ev));
const ids = () => get(eventsStore).items.map((e) => e.id);
const unique = (list) => new Set(list).size === list.length;

/* --- a live event arrives once -------------------------------------------- */
{
  page.items = [];
  page.total = 0;
  page.nextCursor = 0;
  await eventsStore.load({});
  await tick();

  emit(event('e1', 10));
  check('a live event is listed', ids().includes('e1'), ids().join(','));
  check('and exactly once', unique(ids()), ids().join(','));

  // The same event delivered twice — a reconnect, a replay, or the second
  // handler this test's first section now prevents.
  emit(event('e1', 10));
  check('the same event twice is still one row', ids().filter((i) => i === 'e1').length === 1,
    ids().join(','));
}

/* --- the page that already contains it ------------------------------------ */
{
  // Go persists the row and THEN emits, so a query in flight can come back
  // holding the event the handler is about to prepend. Nothing orders those
  // two, and a keyed each does not tolerate the tie.
  page.items = [event('e2', 20), event('e1', 10)];
  page.total = 2;
  page.nextCursor = 0;

  const loading = eventsStore.load({});
  emit(event('e2', 20));      // delivered while the query is in flight
  await loading;
  await tick();

  check('a page replacing the list has no duplicate', unique(ids()), ids().join(','));
  check('and the event is still there', ids().includes('e2'), ids().join(','));

  // The other order: the emit lands after the page has been applied.
  emit(event('e2', 20));
  check('an emit after the page does not duplicate it', unique(ids()), ids().join(','));
  check('nor does one for a row further down', (() => {
    emit(event('e1', 10));
    return unique(ids());
  })(), ids().join(','));
}

/* --- and the same tie when a page is APPENDED ----------------------------- */
{
  page.items = [event('e3', 30), event('e2', 20)];
  page.total = 3;
  page.nextCursor = 25;
  await eventsStore.load({});
  await tick();

  // loadMore appends: a live event prepended in the meantime is in both lists.
  emit(event('e4', 40));
  page.items = [event('e4', 40), event('e1', 10)];
  page.nextCursor = 0;
  await eventsStore.loadMore();
  await tick();

  check('an appended page does not repeat what is already listed', unique(ids()), ids().join(','));
  check('and it did append the new rows', ids().includes('e1'), ids().join(','));
}

/* --- prove the checks can fail -------------------------------------------- */
check('the detector: a repeated id is caught', !unique(['a', 'b', 'a']));
check('the queries really ran', queries >= 3, String(queries));

process.exit(failures ? 1 : 0);
