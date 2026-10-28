// The MIB editor store, against a stubbed bridge.
//
// The thing being pinned here is ordering. Analysis is asynchronous and the
// user keeps typing while it runs, so two answers can be in flight at once and
// arrive in the wrong order. Diagnostics carry line numbers; showing the ones
// computed two keystrokes ago points at lines that have since moved, which is
// worse than showing none.
import * as esbuild from 'esbuild';
import { writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const calls = [];
let pending = [];
let drafts = {};
const names = [];

globalThis.__stub = {
  // Reading a MIB is a bridge round trip dominated by the parse — measured
  // at 54 ms for IP-MIB and 8 ms for a small one — so two clicks can land
  // out of order. readDelay lets a test choose that order.
  readDelay: {},
  MibEditorRead: async function (name) {
    const wait = this.readDelay[name] || 0;
    if (wait) await new Promise((r) => setTimeout(r, wait));
    // The complete analysis comes with the file, as MibEditorRead gives it.
    return {
      name,
      content: name + ' DEFINITIONS ::= BEGIN\nEND\n',
      sha256: 'h-' + name,
      diagnostics: [],
      missing: [],
      outline: [{ name: name + 'Uptime', kind: 'object', line: 2, column: 1, syntax: 'Counter32' }],
      verdict: { level: 'ok', count: 0 },
    };
  },
  MibEditorReadDraft: async (name) => drafts[name] || '',
  MibEditorSaveDraft: async (name, content) => { drafts[name] = content; },
  MibEditorDiscardDraft: async (name) => { delete drafts[name]; },
  MibEditorSetMutedRules: async (codes) => codes,
  // Each analysis parks itself; the test decides when and in what order each
  // one answers.
  MibEditorAnalyse: (name, content) => {
    calls.push(content);
    names.push(name);
    return new Promise((resolve) => {
      pending.push({ content, resolve });
    });
  },
};

const stub = `const s = globalThis.__stub;
export const MibEditorRead = (...a) => s.MibEditorRead(...a);
export const MibEditorReadDraft = (...a) => s.MibEditorReadDraft(...a);
export const MibEditorSaveDraft = (...a) => s.MibEditorSaveDraft(...a);
export const MibEditorDiscardDraft = (...a) => s.MibEditorDiscardDraft(...a);
export const MibEditorAnalyse = (...a) => s.MibEditorAnalyse(...a);
export const MibEditorSetMutedRules = (...a) => s.MibEditorSetMutedRules(...a);`;

const dir = mkdtempSync(join(tmpdir(), 'snmplens-mibed-'));
writeFileSync(join(dir, 'stub.js'), stub);

const entry = new URL('../src/stores/mibEditorStore.js', import.meta.url).pathname.replace(/^[/]([A-Za-z]:)/, '$1');
const out = join(dir, 'bundle.mjs');
await esbuild.build({
  entryPoints: [entry],
  bundle: true, format: 'esm', outfile: out, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /wailsjs[/]go[/]app[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});

const { mibEditorStore } = await import(pathToFileURL(out).href);
const { get } = await import('svelte/store');

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};
const tick = () => new Promise((r) => setTimeout(r, 0));

await mibEditorStore.open('A-MIB');
// Anything an open dispatched has to be answered before anything below
// measures `checking`: an unresolved one keeps inFlight above zero for the
// rest of the file, and the indicator never clears.
for (const p of pending.splice(0)) p.resolve({ diagnostics: [], missing: [], outline: [] });
await tick();
pending = [];
calls.length = 0;

// Two edits, two analyses in flight. The FIRST one answers last.
mibEditorStore.setBuffer('first');
mibEditorStore.refresh();
await tick();
mibEditorStore.setBuffer('second');
mibEditorStore.refresh();
await tick();

check('both analyses were dispatched', pending.length === 2, `pending=${pending.length}`);

const [older, newer] = pending;
newer.resolve({ diagnostics: [{ line: 2, message: 'from the newer buffer' }], missing: [] });
await tick();
older.resolve({ diagnostics: [{ line: 999, message: 'from the older buffer' }], missing: [] });
await tick();

const shown = get(mibEditorStore).diagnostics;
check('the stale answer does not overwrite the fresh one',
  shown.length === 1 && shown[0].message === 'from the newer buffer',
  shown.map((d) => d.message).join(' | ') || 'none');

// An answer computed for a buffer the user has since changed must not be shown
// either: its line numbers describe text that no longer exists.
pending = [];
mibEditorStore.setBuffer('third');
mibEditorStore.refresh();
await tick();
const inflight = pending[0];
mibEditorStore.setBuffer('fourth');          // the user types again
inflight.resolve({ diagnostics: [{ line: 5, message: 'about the third buffer' }], missing: [] });
await tick();

const after = get(mibEditorStore).diagnostics;
check('an answer about a superseded buffer is discarded',
  !after.some((d) => d.message === 'about the third buffer'),
  after.map((d) => d.message).join(' | ') || 'none');

// And the ordinary case still works.
pending = [];
mibEditorStore.setBuffer('fifth');
mibEditorStore.refresh();
await tick();
pending[0].resolve({ diagnostics: [{ line: 1, message: 'current' }], missing: [] });
await tick();
check('a current answer is applied',
  get(mibEditorStore).diagnostics.some((d) => d.message === 'current'));
check('checking is cleared when the answer lands',
  get(mibEditorStore).checking === false);


// --- drafts ---
//
// Abandoning changes has to reach the disk. It did not: saying yes to
// "discard your changes?" left the draft in place, so reopening the file
// recovered exactly the edits that had just been refused — and asked again.
drafts = { 'B-MIB': 'work in progress' };
await mibEditorStore.discardDraft('B-MIB');
check('discarding removes the stored buffer', drafts['B-MIB'] === undefined);

// Reverting is a decision, so the draft goes immediately rather than in a
// second and a bit — long enough to leave the file and have it survive.
drafts = {};
await mibEditorStore.open('C-MIB');
mibEditorStore.setBuffer('edited');
await tick();
drafts['C-MIB'] = 'edited';           // as the debounced writer would have
mibEditorStore.revert();
await tick();
check('reverting removes the draft at once', drafts['C-MIB'] === undefined);
check('reverting restores the saved content',
  get(mibEditorStore).buffer === get(mibEditorStore).source.content);

// A pending draft write belongs to the file it was scheduled for. Reading the
// current file instead wrote one file's buffer under another file's name.
drafts = {};
await mibEditorStore.open('D-MIB');
mibEditorStore.setBuffer('belongs to D');
await mibEditorStore.open('E-MIB');    // switch before the debounce fires
await new Promise((r) => setTimeout(r, 1400));
check('a pending draft is not written under the newly opened file',
  drafts['E-MIB'] === undefined, JSON.stringify(drafts));


// --- a slow open must not land on top of a faster one ---
//
// open() had no supersede guard, although refresh() twenty lines below has an
// elaborate one. Click a 185 KB MIB, then a small one 5 ms later: the small
// one opened, the user typed into it, and the big one's answer arrived later
// and replaced the buffer. The typing was then in no store, no draft and no
// file.
{
  drafts = {};
  globalThis.__stub.readDelay = { 'BIG-MIB': 60 };

  const slow = mibEditorStore.open('BIG-MIB');
  await new Promise((r) => setTimeout(r, 5));
  await mibEditorStore.open('SMALL-MIB');

  mibEditorStore.setBuffer('SMALL-MIB typed by hand');
  const typed = get(mibEditorStore).buffer;

  await slow;
  await new Promise((r) => setTimeout(r, 20));

  const now = get(mibEditorStore);
  check('the slower open does not replace the file being edited',
    now.source.name === 'SMALL-MIB', now.source.name);
  check('what was typed survives the late answer',
    now.buffer === typed, JSON.stringify(now.buffer.slice(0, 40)));

  globalThis.__stub.readDelay = {};
}

// --- leaving a file FLUSHES its draft rather than cancelling it ---
{
  drafts = {};
  await mibEditorStore.open('F-MIB');
  mibEditorStore.setBuffer('F-MIB important unsaved work');

  await mibEditorStore.open('G-MIB');   // well inside the 1200 ms debounce
  await new Promise((r) => setTimeout(r, 30));

  check('the work typed just before a switch is kept as a draft',
    (drafts['F-MIB'] || '').includes('important unsaved work'), JSON.stringify(drafts));
  check('and the new file is the one on screen',
    get(mibEditorStore).source.name === 'G-MIB');
}

// --- restoring a bundled MIB must drop its draft ---
//
// openSource neither cleared the timer nor discarded the draft, so the next
// open handed the broken text straight back: the escape hatch out of a broken
// MIB returned the broken MIB.
{
  drafts = { 'H-MIB': 'H-MIB BROKEN LEFTOVER' };
  await mibEditorStore.open('H-MIB');
  check('the broken draft is recovered first',
    get(mibEditorStore).buffer.includes('BROKEN LEFTOVER'));

  mibEditorStore.openSource({
    name: 'H-MIB', content: 'H-MIB restored', sha256: 'h', diagnostics: [],
  });
  await new Promise((r) => setTimeout(r, 30));
  check('restoring discards the draft', !drafts['H-MIB'], JSON.stringify(drafts));

  const again = await mibEditorStore.open('H-MIB');
  check('and the next open does not hand the broken text back',
    !again.recovered && !get(mibEditorStore).buffer.includes('BROKEN LEFTOVER'),
    get(mibEditorStore).buffer.slice(0, 40));
}

// --- a save that overlaps typing must not claim the typed text is on disk ---
{
  drafts = {};
  await mibEditorStore.open('I-MIB');
  const written = 'I-MIB version sent to disk';
  mibEditorStore.setBuffer(written);
  mibEditorStore.setBuffer(written + ' plus typed during the save');

  // As the debounced writer would have left it, so the test can see whether
  // markSaved takes it away.
  drafts['I-MIB'] = get(mibEditorStore).buffer;

  await mibEditorStore.markSaved('h2', written);
  await new Promise((r) => setTimeout(r, 30));

  const st = get(mibEditorStore);
  check('the file on disk is what was actually written',
    st.source.content === written, JSON.stringify(st.source.content));
  check('the editor still shows unsaved changes', mibEditorStore.dirty());
  check('the draft holding the extra text is NOT discarded',
    (drafts['I-MIB'] || '').includes('typed during the save'), JSON.stringify(drafts));

  // And when nothing diverged, the draft IS cleaned up — otherwise every save
  // would leave one behind and every reopen would offer a phantom recovery.
  mibEditorStore.setBuffer(written);
  drafts['I-MIB'] = written;
  await mibEditorStore.markSaved('h3', written);
  await new Promise((r) => setTimeout(r, 30));
  check('a save with nothing typed after it does discard the draft',
    drafts['I-MIB'] === undefined, JSON.stringify(drafts));
}


// --- undoing back to the file must not leave an older draft behind ---
//
// Type (a draft is written), undo everything so the buffer IS the file, then
// switch inside the debounce. The flush skipped the write because nothing was
// dirty — and left the EARLIER draft on disk, so the next open recovered text
// the user had deliberately undone.
{
  drafts = {};
  await mibEditorStore.open('J-MIB');
  const original = get(mibEditorStore).source.content;

  mibEditorStore.setBuffer(original + 'typed then undone');
  await new Promise((r) => setTimeout(r, 1300));   // let the debounce write it
  check('the draft was written while dirty', !!drafts['J-MIB'], JSON.stringify(drafts));

  mibEditorStore.setBuffer(original);              // undo back to the file
  await mibEditorStore.open('K-MIB');              // switch inside the debounce
  await new Promise((r) => setTimeout(r, 30));

  check('the stale draft is discarded when the buffer is back to the file',
    !drafts['J-MIB'], JSON.stringify(drafts));

  const again = await mibEditorStore.open('J-MIB');
  check('and reopening does not resurrect the undone text',
    !again.recovered && get(mibEditorStore).buffer === original,
    get(mibEditorStore).buffer.slice(0, 40));
}

// --- an external file does not share a draft with a MIB of the same name ---
//
// Both were filed under the bare name, so /downloads/L-MIB and mibs/L-MIB
// wrote the same draft and one recovered the other's unsaved work.
{
  drafts = {};
  await mibEditorStore.open('L-MIB');
  const inside = get(mibEditorStore).source.content;
  mibEditorStore.setBuffer(inside + 'work on the one in mibs/');
  await new Promise((r) => setTimeout(r, 1300));
  const insideKey = Object.keys(drafts)[0];
  check('the file in the MIB directory is keyed by its name',
    insideKey === 'L-MIB', JSON.stringify(drafts));

  mibEditorStore.openSource({
    name: 'L-MIB', path: 'C:/downloads/L-MIB', external: true,
    content: 'L-MIB from outside', sha256: 'h', diagnostics: [],
  });
  mibEditorStore.setBuffer('L-MIB from outside, edited');
  await new Promise((r) => setTimeout(r, 1300));

  const keys = Object.keys(drafts);
  check('the external file gets a key of its own', keys.length === 2, JSON.stringify(keys));
  check('and it is not the bare name', keys.filter((k) => k === 'L-MIB').length === 1,
    JSON.stringify(keys));
}

// A file opens with its COMPLETE analysis — diagnostics, outline and verdict —
// because MibEditorRead gives it. It used to open with the syntax check alone:
// the outline read "nothing defined yet" over a MIB defining forty things, and
// the file's mark turned from warning to error when the full analysis answered.
{
  pending = [];
  calls.length = 0;
  await mibEditorStore.open('N-MIB');
  await tick();

  const st = get(mibEditorStore);
  check('the outline is there before the first keystroke',
    st.outline?.length === 1 && st.outline[0].name === 'N-MIBUptime', JSON.stringify(st.outline));
  check('and the verdict', st.verdict?.level === 'ok', JSON.stringify(st.verdict));
  check('with no second round trip for the same text', pending.length === 0, `${pending.length} dispatched`);

  // A recovered draft is another text: it is analysed, as the file it is.
  drafts['Q-MIB'] = 'Q-MIB edited in a previous session';
  names.length = 0;
  await mibEditorStore.open('Q-MIB');
  await tick();
  check('a recovered draft is analysed', pending.length === 1 && pending[0].content === drafts['Q-MIB'],
    `${pending.length} dispatched`);
  check('as the directory file it is edited as', names[0] === 'Q-MIB', JSON.stringify(names));
  pending.splice(0).forEach((p) => p.resolve({ diagnostics: [], missing: [], outline: [] }));
  await tick();

  // A file from elsewhere has no load to weigh: it is analysed under no name.
  mibEditorStore.openSource({
    name: 'R-MIB', path: 'C:/downloads/R-MIB', external: true,
    content: 'R-MIB from outside', sha256: 'h', diagnostics: [],
  });
  names.length = 0;
  mibEditorStore.setBuffer('R-MIB from outside, edited');
  mibEditorStore.refresh();
  await tick();
  check('an external file is analysed under no name', names[names.length - 1] === '', JSON.stringify(names));
  pending.splice(0).forEach((p) => p.resolve({ diagnostics: [], missing: [], outline: [] }));
  await tick();
}

// The outline rides on the analysis, and it must not outlive the file it
// describes.
//
// A stale outline is worse than none: it lists definitions that are not in the
// buffer, and clicking one puts the caret on a line that now holds something
// else entirely.
{
  await mibEditorStore.open('O-MIB');
  pending = [];
  mibEditorStore.setBuffer('anything');
  mibEditorStore.refresh();
  await tick();
  pending[0].resolve({
    diagnostics: [],
    missing: [],
    outline: [{ name: 'oUptime', kind: 'object', line: 12, column: 1, syntax: 'Counter32' }],
  });
  await tick();

  check('the outline reaches the store', get(mibEditorStore).outline?.length === 1,
    JSON.stringify(get(mibEditorStore).outline));

  // Opening another file replaces it with that file's own.
  pending = [];
  await mibEditorStore.open('P-MIB');
  check('opening another file replaces the previous outline',
    (get(mibEditorStore).outline || []).every((o) => o.name === 'P-MIBUptime'),
    JSON.stringify(get(mibEditorStore).outline));

  // An answer with no outline at all — an older backend, or a failure — leaves
  // an array rather than undefined, because the pane maps over it.
  mibEditorStore.setBuffer('x');
  mibEditorStore.refresh();
  await tick();
  pending[pending.length - 1].resolve({ diagnostics: [], missing: [] });
  await tick();
  check('a response with no outline still leaves an array',
    Array.isArray(get(mibEditorStore).outline), JSON.stringify(get(mibEditorStore).outline));
}

process.exit(failures ? 1 : 0);
