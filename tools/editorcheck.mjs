/**
 * Check, in a real browser, that the MIB editor underlines the characters a
 * problem is about.
 *
 *   node tools/editorcheck.mjs              build the bundle, then check
 *   node tools/editorcheck.mjs --no-build   check the bundle already built
 *
 * The underlines are a layer drawn over the text, and every way that layer has
 * been wrong was invisible to the other tests: it sat 8 px up and left, so the
 * line went through the middle of the word; it vanished after one screen of
 * scrolling; it was placed from a character width measured before any file was
 * open, so it drifted right by 0.6 px a column. Each of those passed every unit
 * test and was found by looking at the screen. This looks at the screen.
 *
 * It loads the editor from the screenshot bundle (the real components, the
 * bridge replaced) on the scene `check-mib-editor-geometry` — a MIB with tabs,
 * accented and CJK characters before the words in question, and a problem two
 * hundred lines down — and, for every underline, compares its box with the box
 * of the same characters in the text: same left edge, same width, same
 * baseline. Then it scrolls to the bottom and does it again for the last one.
 */
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { readFileSync, existsSync, mkdtempSync, rmSync } from 'node:fs';
import { join, dirname, extname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..');
const frontend = join(repo, 'frontend');
const dist = join(frontend, 'screenshots', 'dist');
const SCENE = 'check-mib-editor-geometry';

// Pixels. The underline and the text are laid out by the same engine in the
// same font, so a correct placement is exact; these only absorb sub-pixel
// rounding between two boxes.
const TOLERANCE = { x: 1, width: 1.5, bottom: 3 };

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

// For every underline: its box, and the box of the same characters in the
// mirror (the coloured copy of the text the textarea sits on). The line and
// column come from the underline's data attributes, the characters from the
// mirror's own text nodes, so the two are found independently.
const MEASURE = `(() => {
  // The visible text area: what is outside it is not on screen, whatever its
  // box says.
  const mirror = document.querySelector('.mirror');
  const area = document.querySelector('.surface .code');
  if (!mirror || !area) return { error: 'the editor is not on screen' };
  const view = area.getBoundingClientRect();
  const nodes = [];
  const walker = document.createTreeWalker(mirror, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) nodes.push(n);

  function at(line, column) {
    let l = 1, c = 1;
    for (const node of nodes) {
      const t = node.data;
      for (let i = 0; i < t.length; i++) {
        if (l === line && c === column) return [node, i];
        if (t[i] === '\\n') { l++; c = 1; } else { c++; }
      }
    }
    return null;
  }

  // What of a box survives every ancestor that clips: a box's own rectangle
  // ignores clipping, and an underline clipped away by its own layer — the
  // layer that moved with the scroll and cut everything below its height —
  // still reports a rectangle on screen.
  function clipped(el, rect) {
    let r = { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
    for (let p = el.parentElement; p; p = p.parentElement) {
      const o = getComputedStyle(p);
      if (o.overflowX === 'visible' && o.overflowY === 'visible') continue;
      const b = p.getBoundingClientRect();
      r = { left: Math.max(r.left, b.left), right: Math.min(r.right, b.right),
        top: Math.max(r.top, b.top), bottom: Math.min(r.bottom, b.bottom) };
    }
    return { width: Math.max(0, r.right - r.left), height: Math.max(0, r.bottom - r.top), top: r.top, bottom: r.bottom };
  }

  return [...document.querySelectorAll('.mark')].map((m) => {
    const line = Number(m.dataset.line), column = Number(m.dataset.column), length = Number(m.dataset.length);
    const mark = m.getBoundingClientRect();
    const start = at(line, column);
    const end = at(line, column + length);
    if (!start) return { line, column, rendered: false };
    const range = document.createRange();
    range.setStart(start[0], start[1]);
    if (end) range.setEnd(end[0], end[1]);
    else range.setEnd(start[0], Math.min(start[0].data.length, start[1] + length));
    const text = range.getClientRects()[0] || range.getBoundingClientRect();
    const onScreen = (r) => r.width > 0 && r.bottom > view.top && r.top < view.bottom;
    return {
      line, column, length, rendered: true,
      textOnScreen: onScreen(text), markOnScreen: onScreen(clipped(m, mark)),
      dx: mark.left - text.left, dw: mark.width - text.width, dbottom: mark.bottom - text.bottom,
    };
  });
})()`;

function judge(results, label) {
  if (!Array.isArray(results)) {
    return { measured: [], problems: [`${label}: ${results?.error || 'nothing could be measured'}`] };
  }
  const problems = [];
  const measured = results.filter((r) => r.rendered && r.textOnScreen);
  for (const r of measured) {
    if (!r.markOnScreen) {
      problems.push(`${label}: line ${r.line}: the text is on screen and its underline is not`);
      continue;
    }
    if (Math.abs(r.dx) > TOLERANCE.x || Math.abs(r.dw) > TOLERANCE.width || Math.abs(r.dbottom) > TOLERANCE.bottom) {
      problems.push(`${label}: line ${r.line}:${r.column}: off by ${r.dx.toFixed(1)} px across, `
        + `${r.dw.toFixed(1)} px in width, ${r.dbottom.toFixed(1)} px at the baseline`);
    }
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
  await cdp.until("document.querySelectorAll('.mark').length >= 3", 30000, 'the underlines');
  await sleep(300);

  const top = judge(await cdp.value(MEASURE), 'top');
  if (top.measured.length < 3) top.problems.push(`top: only ${top.measured.length} underline(s) could be measured`);

  // To the bottom, through the textarea as a person would, and wait for the
  // layers to follow it.
  await cdp.value(`(() => {
    const ta = document.querySelector('.surface textarea');
    ta.scrollTop = ta.scrollHeight;
    ta.dispatchEvent(new Event('scroll'));
    return true;
  })()`);
  await sleep(400);
  const bottom = judge(await cdp.value(MEASURE), 'scrolled');
  if (!bottom.measured.some((r) => r.line > 200)) {
    bottom.problems.push('scrolled: the last problem\'s underline was not measured at the bottom of the file');
  }

  const problems = [...top.problems, ...bottom.problems];
  console.log(`measured ${top.measured.length} underline(s) at the top and ${bottom.measured.length} after scrolling`);
  if (problems.length) {
    failed = true;
    console.error(problems.map((p) => '  ' + p).join('\n'));
  } else {
    console.log('every underline is under its characters');
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
