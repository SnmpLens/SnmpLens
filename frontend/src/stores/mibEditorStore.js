import { writable, get } from 'svelte/store';
import {
  MibEditorRead,
  MibEditorAnalyse,
  MibEditorSaveDraft,
  MibEditorReadDraft,
  MibEditorDiscardDraft,
} from '../../wailsjs/go/app/App';
import { mutedRules } from './mutedRules';

// The editor's state lives here, not in the panel component.
//
// The tab shell mounts panels with {#if activeTab === …}, which DESTROYS the
// component on every switch. With the buffer in component state, typing an
// edit and pressing Ctrl+1 lost it silently — no prompt, no trace. That is the
// one failure a text editor may not have, so the buffer outlives the panel.
//
// The buffer is also mirrored to a draft file on the Go side, which covers what
// a store cannot: closing the window, and the machine going down. Drafts are
// written OUTSIDE the MIB directory — a half-finished MIB in there would be
// loaded by the rest of the app.

const EMPTY = {
  source: null,       // the file as it was read; null when nothing is open
  buffer: '',         // what the user is editing
  diagnostics: [],
  missingImports: [],
  // What the buffer defines, from the same parse as the diagnostics.
  outline: [],
  // The buffer's verdict, as Go judged it with the analysis (pkg/mib/verdict.go):
  // the editor's file list shows it for the open file.
  verdict: null,
  checking: false,
};

// What an analysis says about the buffer, in the store's names. A file opens
// with the complete analysis (MibEditorRead), so the first thing shown is
// already what every later pause in typing will show.
function analysisOf(out) {
  return {
    diagnostics: out.diagnostics || [],
    missingImports: out.missing || [],
    outline: out.outline || [],
    verdict: out.verdict || null,
  };
}

function createMibEditorStore() {
  const { subscribe, update, set } = writable({ ...EMPTY });

  let checkTimer;
  let draftTimer;
  // Which open() call owns the store. Reading a MIB is a bridge round trip
  // whose cost is dominated by the parse — measured at 54 ms for IP-MIB and
  // 8 ms for SNMPv2-MIB — so clicking a big file and then a small one landed
  // the answers out of order, and the slow one overwrote the file the user was
  // already typing into. refresh() below has had this guard since it was
  // written; open() never did.
  let openToken = 0;

  /**
   * The name a source's draft is filed under.
   *
   * An externally opened MIB is keyed by its full path rather than its bare
   * name: `/downloads/IF-MIB` and `mibs/IF-MIB` are different files, and both
   * filed a draft called "IF-MIB" — so one silently recovered the other's
   * unsaved work. The separators are flattened because the name becomes a file
   * name in mib-drafts/.
   */
  function draftKey(source) {
    if (!source) return '';
    if (!source.external) return source.name;
    return 'ext_' + String(source.path || source.name).replace(/[^A-Za-z0-9._-]+/g, '_');
  }

  function isDirty(state) {
    return state.source !== null && state.buffer !== state.source.content;
  }

  /** Whether there is unsaved work, for the tab badge and the leave guard. */
  function dirty() {
    return isDirty(get({ subscribe }));
  }

  async function open(name) {
    // Flush, do not cancel. The pending write belongs to the file being LEFT,
    // and cancelling it was how work typed just before a switch stopped
    // existing anywhere — not in the store, not in a draft, not on disk.
    await flushDraft();

    const token = ++openToken;
    // The verdict that comes with the file leaves out what this viewer muted.
    await mutedRules.ready();
    const source = await MibEditorRead(name);
    if (token !== openToken) return { source, recovered: false, superseded: true };
    // A draft for this file means a previous session left work behind.
    let draft = '';
    try {
      draft = await MibEditorReadDraft(name);
    } catch (e) {
      draft = '';
    }
    if (token !== openToken) return { source, recovered: false, superseded: true };

    const recovered = draft && draft !== source.content;

    set({
      ...EMPTY,
      source,
      buffer: recovered ? draft : source.content,
      ...analysisOf(source),
    });
    // The analysis came with the file, of the file. A recovered draft is
    // another text, and is analysed as such.
    if (recovered) refresh();
    return { source, recovered };
  }

  /**
   * Replace the buffer with a source the caller already has — the restore of a
   * bundled MIB, or a file opened from outside the MIB directory.
   *
   * It claims the open token and drops the draft, which it used not to do:
   * restoring a bundled MIB left the broken draft on disk, so the next open
   * recovered it and handed the user back the very file they had just
   * restored away from.
   */
  function openSource(source) {
    openToken++;
    clearTimeout(draftTimer);
    set({ ...EMPTY, source, buffer: source.content, ...analysisOf(source) });
    discardDraft(draftKey(source));
  }

  function setBuffer(buffer) {
    update((s) => ({ ...s, buffer, checking: true }));
    scheduleRefresh();
    scheduleDraft();
  }

  function scheduleRefresh() {
    clearTimeout(checkTimer);
    checkTimer = setTimeout(refresh, 350);
  }

  // Validation and the import check write nothing: no file is touched and no
  // module is loaded. What they DO take is pkg/mib's exclusive gosmi mutex, to
  // read the symbol catalogue — which used to be rebuilt from scratch on every
  // one of these calls. Measured on a 135-module corpus: 122 ms a round, of
  // which 1.35 ms was the analysis, and all 122 ms held that lock, so every OID
  // translation in every other tab waited for somebody's typing to pause. The
  // catalogue is cached now and the round is 1.35 ms; this is safe on every
  // keystroke BECAUSE of that, not by nature.
  // ONE bridge call, one parse. This used to be two calls that each parsed the
  // file, on every pause in typing, over a MIB that can be 185 KB.
  // Analyses can overlap — the user keeps typing while one runs — and nothing
  // makes them answer in the order they were asked. An answer is applied only
  // if the text it describes is still the text on screen: diagnostics carry
  // line numbers, and showing the ones computed two keystrokes ago points at
  // lines that have since moved, which is worse than showing none.
  //
  // The buffer itself is the identity rather than a sequence number, because
  // that is the actual predicate: valid iff the file has not changed since.
  let inFlight = 0;

  async function refresh() {
    const { buffer: asked, source } = get({ subscribe });
    // The file in the directory the buffer is edited as, so Go can weigh what
    // the last load did with it; "" for one opened from elsewhere.
    const name = source && !source.external ? source.name : '';
    inFlight++;
    try {
      const out = (await MibEditorAnalyse(name, asked)) || {};
      update((s) => {
        if (s.buffer !== asked) return s; // superseded; leave what is shown
        return { ...s, ...analysisOf(out) };
      });
    } catch (e) {
      // Nothing usable came back; the previous diagnostics are still the best
      // description of the file we have.
    } finally {
      inFlight--;
      // Only stop saying "checking" when nothing is still running, or a
      // discarded answer would clear the indicator while work continues.
      if (inFlight === 0) update((s) => ({ ...s, checking: false }));
    }
  }

  // The draft is written for the file that was open when the timer was SET,
  // not for whatever happens to be open when it fires. Reading the current
  // state instead meant that switching files inside the delay wrote one file's
  // buffer under another file's name.
  function scheduleDraft() {
    clearTimeout(draftTimer);
    const s = get({ subscribe });
    if (!s.source) return;
    const key = draftKey(s.source);

    draftTimer = setTimeout(async () => {
      const now = get({ subscribe });
      // Still the same file? If not, that file's draft was already settled
      // when it was closed.
      if (!now.source || draftKey(now.source) !== key) return;
      try {
        if (isDirty(now)) {
          await MibEditorSaveDraft(key, now.buffer);
        } else {
          await MibEditorDiscardDraft(key);
        }
      } catch (e) {
        // A draft is a safety net, not a feature; failing to write one must
        // never interrupt typing.
      }
    }, 1200);
  }

  /**
   * Write the pending draft NOW, for the file it belongs to.
   *
   * Leaving a file used to CANCEL its timer, so anything typed in the last
   * 1200 ms existed only in a buffer that was about to be replaced — not in
   * the store, not in a draft, not on disk. Flushing costs one bridge call on
   * a file switch and is the difference between "recovered" and "gone".
   */
  async function flushDraft() {
    if (!draftTimer) return;
    clearTimeout(draftTimer);
    draftTimer = null;
    const s = get({ subscribe });
    if (!s.source) return;
    try {
      if (isDirty(s)) {
        await MibEditorSaveDraft(draftKey(s.source), s.buffer);
      } else {
        // Clean: DISCARD, do not merely skip. Typing, then undoing back to
        // the file, then switching within the debounce left an OLDER draft on
        // disk, so the next open recovered text the user had deliberately
        // undone. The scheduled writer discards for the same reason.
        await MibEditorDiscardDraft(draftKey(s.source));
      }
    } catch (e) {
      // Same reason as the scheduled write: never block a file switch.
    }
  }

  /**
   * Forget the stored buffer for a file.
   *
   * Abandoning changes has to reach the DISK, not just the screen. Without
   * this, saying yes to "discard your changes?" left the draft in place, and
   * reopening the file recovered the very edits that had just been thrown
   * away — so the prompt came back, for changes the user had already refused.
   */
  async function discardDraft(name) {
    clearTimeout(draftTimer);
    if (!name) return;
    try {
      await MibEditorDiscardDraft(name);
    } catch (e) {
      /* nothing recoverable to do */
    }
  }

  /**
   * Called after a successful save: the file on disk is now `written`. What is
   * shown stays the complete analysis it was; the panel asks again once the
   * tree is rebuilt, since the verdict depends on what loaded.
   *
   * `written` is the text that was actually sent, not the buffer as it stands
   * now. A save of a 185 KB MIB is a bridge round trip, and anything typed
   * during it used to be folded into "this is what is on disk" — so the editor
   * showed no unsaved changes, discarded the draft holding that text, and it
   * existed only in memory until the window closed.
   */
  async function markSaved(sha256, written) {
    const s = get({ subscribe });
    const content = typeof written === 'string' ? written : s.buffer;
    update((st) => ({
      ...st,
      source: { ...st.source, content, sha256, external: false },
    }));
    // The draft goes only if there is nothing left it protects. If the user
    // typed during the save, the buffer is ahead of the file and the draft is
    // the only copy of the difference.
    if (s.source && content === s.buffer) {
      try {
        await MibEditorDiscardDraft(draftKey(s.source));
      } catch (e) {
        /* nothing recoverable to do */
      }
    }
  }

  // Reverting is a decision, so the draft goes NOW rather than in a second and
  // a bit — long enough to leave the file and have it survive.
  function revert() {
    const key = draftKey(get({ subscribe }).source);
    update((s) => (s.source ? { ...s, buffer: s.source.content } : s));
    // Analysed again rather than restored from the source: the source's
    // analysis was made against the tree as it was when the file opened.
    refresh();
    discardDraft(key);
  }

  function close() {
    clearTimeout(checkTimer);
    clearTimeout(draftTimer);
    set({ ...EMPTY });
  }

  return { subscribe, open, openSource, setBuffer, refresh, markSaved, revert, close, dirty, discardDraft, flushDraft };
}

export const mibEditorStore = createMibEditorStore();

// Write the pending draft when the window goes away.
//
// The draft is written 1200 ms after you stop typing, so closing the window
// inside that window lost the last thing typed — the one moment a draft exists
// to cover. Registered on the STORE and not the panel: the panel is destroyed
// on every tab switch while the buffer deliberately survives here.
//
// Best-effort by nature. beforeunload cannot await, so the bridge call is
// started and the browser may or may not let it finish; pagehide is the more
// reliable of the two in a WebView and both are cheap. This narrows the window
// rather than closing it, which is why the debounce stays short.
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  const flush = () => {
    if (mibEditorStore.dirty()) mibEditorStore.flushDraft();
  };
  window.addEventListener('beforeunload', flush);
  window.addEventListener('pagehide', flush);
}
