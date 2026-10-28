import { writable, get } from 'svelte/store';
import { MibEditorSetMutedRules } from '../../wailsjs/go/app/App';

// The MIB rules this viewer has muted (pkg/mib/mute.go): their findings leave
// the editor's problem table, its underlines and every verdict, until unmuted.
//
// Kept per viewer in localStorage, like the problem table's height, and given
// to Go at every start: Go judges the verdicts, so it has to know before the
// first one is asked for — which is why the verdicts and the editor wait for
// `ready()`. A store that refuses to keep it only costs the choice next time.

const KEY = 'snmplens.mibEditor.mutedRules';

function stored() {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || '[]');
    return Array.isArray(v) ? v.filter((c) => typeof c === 'string') : [];
  } catch (e) {
    return [];
  }
}

function keep(list) {
  try {
    localStorage.setItem(KEY, JSON.stringify(list));
  } catch (e) {
    /* muted for this session only */
  }
}

const inner = writable(new Set());
let started = null;

// Go answers with the rules it took — only advice may be muted — and that is
// what is kept. The browser demo answers nothing, and the list stands as asked.
async function push(list) {
  let taken = list;
  try {
    const answer = await MibEditorSetMutedRules(list);
    if (Array.isArray(answer)) taken = answer;
  } catch (e) {
    console.error('MibEditorSetMutedRules failed', e);
  }
  inner.set(new Set(taken));
  keep(taken);
}

export const mutedRules = {
  subscribe: inner.subscribe,
  /** Resolves once Go has the stored choice. */
  ready() {
    if (!started) started = push(stored());
    return started;
  },
  async mute(code) {
    await this.ready();
    const now = get(inner);
    if (!now.has(code)) await push([...now, code]);
  },
  async unmute(code) {
    await this.ready();
    const now = get(inner);
    if (now.has(code)) await push([...now].filter((c) => c !== code));
  },
};
