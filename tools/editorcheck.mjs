/**
 * Check, in a real browser, that the MIB editor underlines the characters a
 * problem is about.
 *
 *   node tools/editorcheck.mjs              build the bundle, then check
 *   node tools/editorcheck.mjs --no-build   check the bundle already built
 *
 * The editing surface is CodeMirror, which draws a problem's underline as a
 * span around the very characters; what can still be wrong is WHICH
 * characters — the line and column Go gives, turned into an offset in a text
 * with tabs, accented and wide characters before the word. The hand-made
 * surface before it got that wrong three ways no unit test saw (8 px off, gone
 * after a screen of scrolling, drifting by a mismeasured character width).
 *
 * It loads the editor from the screenshot bundle (the real components, the
 * bridge replaced) on the scene `check-mib-editor-geometry` — a MIB with tabs,
 * accented and CJK characters before the words in question, and a problem two
 * hundred lines down — and, for every problem, finds its underline and checks
 * it holds exactly the word, at the line the gutter numbers and the column the
 * line's own text gives, on screen. Then it scrolls to the bottom and does it
 * again for the last one.
 */
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { readFileSync, existsSync, mkdtempSync, rmSync } from 'node:fs';
import { join, dirname, extname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { tmpdir } from 'node:os';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..');
const frontend = join(repo, 'frontend');
const dist = join(frontend, 'screenshots', 'dist');
const SCENE = 'check-mib-editor-geometry';

// What the scene's MIB holds, and where each problem is (frontend/screenshots/scenes.js).
const { geometryMib } = await import(pathToFileURL(join(frontend, 'screenshots', 'scenes.js')).href);
const EXPECTED = geometryMib().diagnostics.filter((d) => d.severity !== 'info');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const CHROME_CANDIDATES = [
  process.env.CHROME_PATH,
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  join(process.env.LOCALAPPDATA || '', 'Google/Chrome/Application/chrome.exe'),
  '/usr/bin/google-chrome',
  '/usr/bin/google-chrome-stable',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
];

function findChrome() {
  for (const p of CHROME_CANDIDATES) {
    try {
      if (p && existsSync(p)) return p;
    } catch { /* keep looking */ }
  }
  return null;
}

/* --- build and serve ----------------------------------------------------- */

if (!existsSync(join(frontend, 'screenshots', 'bridge', 'seeds.json'))
  || !existsSync(join(frontend, 'screenshots', 'bridge', 'App.js'))) {
  console.error('No bridge yet. Run: node tools/genbridge.mjs tools/screenshot-spec.json');
  process.exit(1);
}

if (!process.argv.includes('--no-build')) {
  const viteBin = join(frontend, 'node_modules', 'vite', 'bin', 'vite.js');
  const build = spawnSync(
    process.execPath,
    [viteBin, 'build', '--config', 'vite.screenshots.config.js', '--logLevel', 'error'],
    { cwd: frontend, stdio: 'inherit' },
  );
  if (build.status !== 0) {
    console.error('The bundle did not build.');
    process.exit(1);
  }
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.woff2': 'font/woff2',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
};

const server = createServer((req, res) => {
  const path = decodeURIComponent(req.url.split('?')[0]);
  const file = join(dist, path === '/' ? 'index.html' : path);
  if (!file.startsWith(dist)) {
    res.writeHead(404).end('not found');
    return;
  }
  try {
    const body = readFileSync(file);
    res.writeHead(200, { 'Content-Type': MIME[extname(file)] || 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404).end('not found');
  }
});
const port = await new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));

/* --- the DevTools protocol, as tools/record.mjs speaks it ---------------- */

class CDP {
  constructor(url) {
    this.url = url;
    this.next = 1;
    this.pending = new Map();
  }

  open() {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(this.url);
      this.ws = ws;
      ws.addEventListener('open', () => resolve(this));
      ws.addEventListener('error', () => reject(new Error(`cannot reach ${this.url}`)));
      ws.addEventListener('message', (ev) => {
        let msg;
        try {
          msg = JSON.parse(ev.data);
        } catch {
          return;
        }
        const p = msg.id !== undefined && this.pending.get(msg.id);
        if (!p) return;
        this.pending.delete(msg.id);
        if (msg.error) p.reject(new Error(`${p.method}: ${msg.error.message}`));
        else p.resolve(msg.result);
      });
    });
  }

  send(method, params = {}) {
    const id = this.next++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async value(expression) {
    const r = await this.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (r?.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || 'evaluation failed');
    return r?.result?.value;
  }

  async until(expression, timeoutMs, label) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      try {
        if (await this.value(expression)) return;
      } catch { /* not there yet */ }
      await sleep(100);
    }
    throw new Error(`timed out waiting for ${label}`);
  }

  close() {
    try {
      this.ws.close();
    } catch { /* already gone */ }
  }
}

async function debuggerUrl(profileDir, timeoutMs = 20000) {
  const portFile = join(profileDir, 'DevToolsActivePort');
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(portFile)) {
      const p = parseInt(readFileSync(portFile, 'utf8').split('\n')[0], 10);
      if (p > 0) {
        try {
          const list = await fetch(`http://127.0.0.1:${p}/json/list`).then((r) => r.json());
          const page = list.find((t) => t.type === 'page' && t.webSocketDebuggerUrl);
          if (page) return page.webSocketDebuggerUrl;
        } catch { /* listening, not yet serving */ }
      }
    }
    await sleep(120);
  }
  throw new Error('Chrome never reported a debugging port');
}

function killTree(child) {
  if (process.platform === 'win32') {
    spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' });
    return;
  }
  try {
    child.kill('SIGKILL');
  } catch { /* already gone */ }
}

/* --- the measurement, run in the page ------------------------------------ */

// For every underline: the characters it holds, its line as the gutter numbers
// it, its column from the line's own text before it, and whether it is on
// screen. The line is matched to the gutter by position, so it is found
// independently of the underline.
const MEASURE = `(() => {
  const scroller = document.querySelector('.cm-scroller');
  if (!scroller) return { error: 'the editor is not on screen' };
  const view = scroller.getBoundingClientRect();
  const gutters = [...document.querySelectorAll('.cm-lineNumbers .cm-gutterElement')];
  const lineNumber = (lineEl) => {
    const top = lineEl.getBoundingClientRect().top;
    const g = gutters.find((e) => Math.abs(e.getBoundingClientRect().top - top) < 2);
    return g ? Number(g.textContent) : 0;
  };
  const out = [];
  for (const el of document.querySelectorAll('.cm-lintRange')) {
    const lineEl = el.closest('.cm-line');
    if (!lineEl) continue;
    const before = document.createRange();
    before.setStart(lineEl, 0);
    before.setEndBefore(el);
    const r = el.getBoundingClientRect();
    const l = lineEl.getBoundingClientRect();
    out.push({
      text: el.textContent,
      line: lineNumber(lineEl),
      column: before.toString().length + 1,
      onScreen: r.width > 0 && r.bottom > view.top && r.top < view.bottom,
      inLine: r.top >= l.top - 1 && r.bottom <= l.bottom + 1,
    });
  }
  // A range the highlighter splits is several spans: put them back together.
  out.sort((a, b) => a.line - b.line || a.column - b.column);
  const merged = [];
  for (const m of out) {
    const last = merged[merged.length - 1];
    if (last && last.line === m.line && last.column + last.text.length === m.column) {
      last.text += m.text;
      last.onScreen = last.onScreen && m.onScreen;
      last.inLine = last.inLine && m.inLine;
    } else {
      merged.push({ ...m });
    }
  }
  return merged;
})()`;

// Every problem expected on screen has its underline, holding exactly its
// word, where Go put it.
function judge(results, label, expected) {
  if (!Array.isArray(results)) {
    return { measured: [], problems: [`${label}: ${results?.error || 'nothing could be measured'}`] };
  }
  const problems = [];
  const measured = [];
  for (const d of expected) {
    const u = results.find((r) => r.line === d.line && r.column === d.column);
    if (!u) {
      problems.push(`${label}: line ${d.line}:${d.column}: no underline for ${d.symbol}`);
      continue;
    }
    measured.push(u);
    if (u.text !== d.symbol) problems.push(`${label}: line ${d.line}:${d.column}: underlines "${u.text}", not "${d.symbol}"`);
    if (!u.onScreen) problems.push(`${label}: line ${d.line}: the underline is not on screen`);
    if (!u.inLine) problems.push(`${label}: line ${d.line}: the underline leaves its line`);
  }
  return { measured, problems };
}

/* --- run ------------------------------------------------------------------ */

const chrome = findChrome();
if (!chrome) {
  console.error('Chrome was not found. Set CHROME_PATH, or looked in:\n  ' + CHROME_CANDIDATES.filter(Boolean).join('\n  '));
  server.close();
  process.exit(1);
}

const profile = mkdtempSync(join(tmpdir(), 'snmplens-check-'));
const child = spawn(chrome, [
  '--headless=new',
  '--disable-gpu',
  '--no-first-run',
  '--no-default-browser-check',
  `--user-data-dir=${profile}`,
  '--remote-debugging-port=0',
  '--hide-scrollbars',
  '--window-size=1400,900',
  'about:blank',
], { stdio: 'ignore' });

let failed = false;
let cdp = null;
try {
  cdp = await new CDP(await debuggerUrl(profile)).open();
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');
  await cdp.send('Page.navigate', { url: `http://127.0.0.1:${port}/index.html?scene=${SCENE}` });
  await cdp.until("document.querySelectorAll('.cm-lintRange').length >= 3", 30000, 'the underlines');
  await sleep(300);

  const near = EXPECTED.filter((d) => d.line < 30);
  const far = EXPECTED.filter((d) => d.line > 200);
  const top = judge(await cdp.value(MEASURE), 'top', near);

  // To the bottom, as a person would scroll, and wait for the editor to draw
  // what is there.
  await cdp.value(`(() => {
    const s = document.querySelector('.cm-scroller');
    s.scrollTop = s.scrollHeight;
    s.dispatchEvent(new Event('scroll'));
    return true;
  })()`);
  await sleep(500);
  const bottom = judge(await cdp.value(MEASURE), 'scrolled', far);
  if (!far.length) bottom.problems.push('scrolled: the scene has no problem far down to check');

  const problems = [...top.problems, ...bottom.problems];
  console.log(`checked ${top.measured.length} underline(s) at the top and ${bottom.measured.length} after scrolling`);
  if (problems.length) {
    failed = true;
    console.error(problems.map((p) => '  ' + p).join('\n'));
  } else {
    console.log('every underline holds its word, where Go put it');
  }
} catch (e) {
  failed = true;
  console.error(String(e?.stack || e));
} finally {
  cdp?.close();
  killTree(child);
  server.close();
  try {
    rmSync(profile, { recursive: true, force: true });
  } catch { /* Chrome may still hold it on Windows */ }
}
process.exit(failed ? 1 : 0);
